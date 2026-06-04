package pg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freeCodeCamp/artemis/internal/reporequest"
)

type RepoQueue struct {
	pool  *pgxpool.Pool
	now   func() time.Time
	newID func() string
}

func NewRepoQueue(db *DB) *RepoQueue {
	return &RepoQueue{pool: db.Pool, now: time.Now, newID: defaultRepoRequestID}
}

func (q *RepoQueue) WithClock(now func() time.Time) *RepoQueue { q.now = now; return q }
func (q *RepoQueue) WithIDGen(fn func() string) *RepoQueue     { q.newID = fn; return q }

func defaultRepoRequestID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "req_" + hex.EncodeToString(b[:])
}

const repoRequestCols = `id, name, owner, visibility, description, template, status, url, error, requested_by, approver, reject_reason, created_at, updated_at`

func scanRequest(row pgx.Row) (reporequest.Request, error) {
	var r reporequest.Request
	err := row.Scan(&r.ID, &r.Name, &r.Owner, &r.Visibility, &r.Description, &r.Template,
		&r.Status, &r.URL, &r.Error, &r.RequestedBy, &r.Approver, &r.RejectReason, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (q *RepoQueue) Create(ctx context.Context, req reporequest.Request) (reporequest.Request, error) {
	if req.Name == "" {
		return reporequest.Request{}, errors.New("reporequest/pg: empty name")
	}
	now := q.now().UTC()
	req.ID = q.newID()
	req.Status = reporequest.StatusPending
	req.CreatedAt = now
	req.UpdatedAt = now

	_, err := q.pool.Exec(ctx,
		`INSERT INTO repo_requests (`+repoRequestCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		req.ID, req.Name, req.Owner, req.Visibility, req.Description, req.Template,
		req.Status, req.URL, req.Error, req.RequestedBy, req.Approver, req.RejectReason, req.CreatedAt, req.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return reporequest.Request{}, reporequest.ErrAlreadyExists
		}
		return reporequest.Request{}, fmt.Errorf("pg repoqueue create: %w", err)
	}
	return req, nil
}

func (q *RepoQueue) Get(ctx context.Context, id string) (reporequest.Request, error) {
	r, err := scanRequest(q.pool.QueryRow(ctx, `SELECT `+repoRequestCols+` FROM repo_requests WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return reporequest.Request{}, reporequest.ErrNotFound
	}
	if err != nil {
		return reporequest.Request{}, fmt.Errorf("pg repoqueue get %s: %w", id, err)
	}
	return r, nil
}

func (q *RepoQueue) List(ctx context.Context) ([]reporequest.Request, error) {
	rows, err := q.pool.Query(ctx, `SELECT `+repoRequestCols+` FROM repo_requests ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("pg repoqueue list: %w", err)
	}
	defer rows.Close()

	var out []reporequest.Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("pg repoqueue scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *RepoQueue) Delete(ctx context.Context, id string) error {
	tag, err := q.pool.Exec(ctx, `DELETE FROM repo_requests WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("pg repoqueue delete %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return reporequest.ErrNotFound
	}
	return nil
}

func (q *RepoQueue) Approve(ctx context.Context, id, approver string) (reporequest.Request, error) {
	return q.transition(ctx, id, reporequest.StatusPending, reporequest.ErrNotPending, func(r *reporequest.Request) {
		r.Status = reporequest.StatusApproved
		r.Approver = approver
	})
}

func (q *RepoQueue) Reject(ctx context.Context, id, approver, reason string) (reporequest.Request, error) {
	return q.transition(ctx, id, reporequest.StatusPending, reporequest.ErrNotPending, func(r *reporequest.Request) {
		r.Status = reporequest.StatusRejected
		r.Approver = approver
		r.RejectReason = reason
	})
}

func (q *RepoQueue) MarkActive(ctx context.Context, id, url string) (reporequest.Request, error) {
	return q.transition(ctx, id, reporequest.StatusApproved, reporequest.ErrNotPending, func(r *reporequest.Request) {
		r.Status = reporequest.StatusActive
		r.URL = url
	})
}

func (q *RepoQueue) MarkFailed(ctx context.Context, id, errMsg string) (reporequest.Request, error) {
	return q.transition(ctx, id, reporequest.StatusApproved, reporequest.ErrNotPending, func(r *reporequest.Request) {
		r.Status = reporequest.StatusFailed
		r.Error = errMsg
	})
}

func (q *RepoQueue) MarkStale(ctx context.Context, id, reason string) (reporequest.Request, error) {
	return q.transition(ctx, id, reporequest.StatusActive, reporequest.ErrNotActive, func(r *reporequest.Request) {
		r.Status = reporequest.StatusFailed
		r.Error = reason
	})
}

func (q *RepoQueue) transition(ctx context.Context, id string, want reporequest.Status, mismatch error, apply func(*reporequest.Request)) (reporequest.Request, error) {
	var out reporequest.Request
	err := pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		cur, err := scanRequest(tx.QueryRow(ctx,
			`SELECT `+repoRequestCols+` FROM repo_requests WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return reporequest.ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Status != want {
			return mismatch
		}
		apply(&cur)
		cur.UpdatedAt = q.now().UTC()
		_, err = tx.Exec(ctx,
			`UPDATE repo_requests SET status=$2, url=$3, error=$4, approver=$5, reject_reason=$6, updated_at=$7 WHERE id=$1`,
			id, cur.Status, cur.URL, cur.Error, cur.Approver, cur.RejectReason, cur.UpdatedAt)
		if err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		if errors.Is(err, reporequest.ErrNotFound) || errors.Is(err, mismatch) {
			return reporequest.Request{}, err
		}
		return reporequest.Request{}, fmt.Errorf("pg repoqueue transition %s: %w", id, err)
	}
	return out, nil
}
