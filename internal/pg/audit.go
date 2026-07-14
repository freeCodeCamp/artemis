package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type AuditEvent struct {
	Actor     string
	Action    string
	Site      string
	DeployID  string
	Outcome   string
	RequestID string
	Detail    map[string]any
}

type AuditFilter struct {
	Site   string
	Actor  string
	Action string
	Since  time.Time
	Limit  int
	Offset int
}

type AuditRecord struct {
	ID         int64
	OccurredAt time.Time
	Actor      string
	Action     string
	Site       string
	DeployID   string
	Outcome    string
	RequestID  string
	Detail     map[string]any
}

const auditListDefaultLimit = 100
const auditListMaxLimit = 500

func (r *Repo) ListAudit(ctx context.Context, f AuditFilter) ([]AuditRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = auditListDefaultLimit
	}
	if limit > auditListMaxLimit {
		limit = auditListMaxLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, occurred_at, actor, action, site, deploy_id, outcome, request_id, detail
		 FROM audit_log
		 WHERE ($1 = '' OR site = $1)
		   AND ($2 = '' OR actor = $2)
		   AND ($3 = '' OR action = $3)
		   AND occurred_at >= $4
		 ORDER BY occurred_at DESC, id DESC
		 LIMIT $5 OFFSET $6`,
		f.Site, f.Actor, f.Action, f.Since, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("pg audit list: %w", err)
	}
	defer rows.Close()

	out := make([]AuditRecord, 0)
	for rows.Next() {
		var rec AuditRecord
		var detail []byte
		if err := rows.Scan(&rec.ID, &rec.OccurredAt, &rec.Actor, &rec.Action, &rec.Site, &rec.DeployID, &rec.Outcome, &rec.RequestID, &detail); err != nil {
			return nil, fmt.Errorf("pg audit list scan: %w", err)
		}
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &rec.Detail); err != nil {
				return nil, fmt.Errorf("pg audit list detail %d: %w", rec.ID, err)
			}
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg audit list rows: %w", err)
	}
	return out, nil
}

func (r *Repo) DeployActors(ctx context.Context, site string) (map[string]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT deploy_id, actor FROM audit_log
		 WHERE site = $1 AND action = 'deploy.finalize' AND outcome = 'success' AND deploy_id <> ''
		 ORDER BY occurred_at ASC`,
		site)
	if err != nil {
		return nil, fmt.Errorf("pg deploy actors: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var id, actor string
		if err := rows.Scan(&id, &actor); err != nil {
			return nil, fmt.Errorf("pg deploy actors scan: %w", err)
		}
		out[id] = actor
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg deploy actors rows: %w", err)
	}
	return out, nil
}

func (r *Repo) RecordAudit(ctx context.Context, e AuditEvent) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("pg audit marshal %s: %w", e.Action, err)
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO audit_log (actor, action, site, deploy_id, outcome, request_id, detail)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.Actor, e.Action, e.Site, e.DeployID, e.Outcome, e.RequestID, b); err != nil {
		return fmt.Errorf("pg audit insert %s: %w", e.Action, err)
	}
	return nil
}
