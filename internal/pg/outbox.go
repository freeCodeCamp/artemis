package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	TopicSiteChanged       = "site.changed"
	TopicSiteLifecycle     = "site.lifecycle"
	LifecycleActionReclaim = "reclaim"
)

type SiteLifecycleEvent struct {
	Action string `json:"action"`
	Slug   string `json:"slug"`
	Site   string `json:"site"`
}

type OutboxEvent struct {
	ID      int64
	Topic   string
	Payload []byte
}

func (r *Repo) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func Enqueue(ctx context.Context, tx pgx.Tx, topic string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pg outbox marshal %s: %w", topic, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox (topic, payload) VALUES ($1, $2)`, topic, b); err != nil {
		return fmt.Errorf("pg outbox enqueue %s: %w", topic, err)
	}
	return nil
}

func (r *Repo) EnqueueSiteChanged(ctx context.Context, site sitekey.Dirname) error {
	return r.WithTx(ctx, func(tx pgx.Tx) error {
		return Enqueue(ctx, tx, TopicSiteChanged, map[string]string{"site": string(site)})
	})
}

func (r *Repo) EnqueueSiteLifecycle(ctx context.Context, events []SiteLifecycleEvent) error {
	if len(events) == 0 {
		return nil
	}
	return r.WithTx(ctx, func(tx pgx.Tx) error {
		for _, e := range events {
			if err := Enqueue(ctx, tx, TopicSiteLifecycle, e); err != nil {
				return err
			}
		}
		return nil
	})
}

const (
	claimTTL          = 5 * time.Minute
	relayRetryBackoff = time.Minute
)

func (r *Repo) claimBatch(ctx context.Context, limit int) ([]OutboxEvent, error) {
	var events []OutboxEvent
	err := r.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`UPDATE outbox
			 SET claimed_at = now(), claim_expires_at = now() + make_interval(secs => $2)
			 WHERE id IN (
			   SELECT id FROM outbox
			   WHERE published_at IS NULL
			     AND (claim_expires_at IS NULL OR claim_expires_at < now())
			   ORDER BY id
			   LIMIT $1
			   FOR UPDATE SKIP LOCKED)
			 RETURNING id, topic, payload`, limit, claimTTL.Seconds())
		if err != nil {
			return fmt.Errorf("pg outbox claim: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e OutboxEvent
			if err := rows.Scan(&e.ID, &e.Topic, &e.Payload); err != nil {
				return fmt.Errorf("pg outbox claim scan: %w", err)
			}
			events = append(events, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pg outbox claim rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(events, func(a, b OutboxEvent) int { return int(a.ID - b.ID) })
	return events, nil
}

func (r *Repo) RelayBatch(ctx context.Context, limit int, publish func(OutboxEvent) error, at time.Time) (int, error) {
	events, err := r.claimBatch(ctx, limit)
	if err != nil {
		return 0, err
	}

	var doneIDs []int64
	var pubErr error
	for i, e := range events {
		if err := publish(e); err != nil {
			held := make([]int64, 0, len(events)-i)
			for _, rest := range events[i:] {
				held = append(held, rest.ID)
			}
			pubErr = fmt.Errorf("relay: publish id=%d topic=%s (%d claims retry in %s): %w", e.ID, e.Topic, len(held), relayRetryBackoff, err)
			pubErr = errors.Join(pubErr, r.releaseClaims(ctx, held))
			break
		}
		doneIDs = append(doneIDs, e.ID)
	}
	if len(doneIDs) == 0 {
		return 0, pubErr
	}
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.MarkPublished(markCtx, doneIDs, at); err != nil {
		return len(doneIDs), errors.Join(pubErr, err)
	}
	return len(doneIDs), pubErr
}

func (r *Repo) releaseClaims(ctx context.Context, ids []int64) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := r.pool.Exec(releaseCtx,
		`UPDATE outbox SET claimed_at = NULL, claim_expires_at = now() + make_interval(secs => $2)
		 WHERE id = ANY($1) AND published_at IS NULL`, ids, relayRetryBackoff.Seconds()); err != nil {
		return fmt.Errorf("pg outbox release claims: %w", err)
	}
	return nil
}

func (r *Repo) MarkPublished(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := r.pool.Exec(ctx,
		`UPDATE outbox SET published_at = $1 WHERE id = ANY($2)`, at, ids); err != nil {
		return fmt.Errorf("pg outbox mark published: %w", err)
	}
	return nil
}

// PurgeOutbox deletes rows the relay already published and that are
// older than before, at most limit per call, and returns how many.
// Unpublished rows are never eligible: they are undelivered work, and
// only the relay retires one. A non-positive limit is a refusal, not
// "unbounded". Under dryRun it counts the same set and deletes none,
// so a planning run reports the delete set the live run would take.
func (r *Repo) PurgeOutbox(ctx context.Context, before time.Time, limit int, dryRun bool) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	if dryRun {
		var n int
		err := r.pool.QueryRow(ctx,
			`SELECT count(*) FROM (
			   SELECT 1 FROM outbox
			   WHERE published_at IS NOT NULL AND published_at < $1
			   ORDER BY id
			   LIMIT $2) t`, before, limit).Scan(&n)
		if err != nil {
			return 0, fmt.Errorf("pg outbox purge count: %w", err)
		}
		return n, nil
	}
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM outbox WHERE id IN (
		   SELECT id FROM outbox
		   WHERE published_at IS NOT NULL AND published_at < $1
		   ORDER BY id
		   LIMIT $2)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("pg outbox purge: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *Repo) OutboxBacklog(ctx context.Context) (int, time.Duration, error) {
	var count int
	var oldestSeconds float64
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*), coalesce(extract(epoch FROM now() - min(created_at)), 0)
		   FROM outbox
		  WHERE published_at IS NULL`).Scan(&count, &oldestSeconds); err != nil {
		return 0, 0, fmt.Errorf("pg outbox backlog: %w", err)
	}
	return count, time.Duration(oldestSeconds * float64(time.Second)), nil
}
