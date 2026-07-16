package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const TopicSiteChanged = "site.changed"

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

func (r *Repo) EnqueueSiteChanged(ctx context.Context, site string) error {
	return r.WithTx(ctx, func(tx pgx.Tx) error {
		return Enqueue(ctx, tx, TopicSiteChanged, map[string]string{"site": site})
	})
}

func (r *Repo) FetchUnpublished(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, topic, payload FROM outbox
		 WHERE published_at IS NULL
		 ORDER BY id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("pg outbox fetch: %w", err)
	}
	defer rows.Close()

	var out []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		if err := rows.Scan(&e.ID, &e.Topic, &e.Payload); err != nil {
			return nil, fmt.Errorf("pg outbox scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repo) claimBatch(ctx context.Context, limit int) ([]OutboxEvent, error) {
	var events []OutboxEvent
	err := r.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, topic, payload FROM outbox
			 WHERE published_at IS NULL
			 ORDER BY id
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED`, limit)
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
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

func (r *Repo) RelayBatch(ctx context.Context, limit int, publish func(OutboxEvent) error, at time.Time) (int, error) {
	events, err := r.claimBatch(ctx, limit)
	if err != nil {
		return 0, err
	}

	var doneIDs []int64
	var pubErr error
	for _, e := range events {
		if err := publish(e); err != nil {
			pubErr = fmt.Errorf("relay: publish id=%d topic=%s: %w", e.ID, e.Topic, err)
			break
		}
		doneIDs = append(doneIDs, e.ID)
	}
	if len(doneIDs) == 0 {
		return 0, pubErr
	}
	if err := r.MarkPublished(ctx, doneIDs, at); err != nil {
		return 0, err
	}
	return len(doneIDs), pubErr
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
