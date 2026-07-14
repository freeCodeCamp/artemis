package pg

import (
	"context"
	"encoding/json"
	"fmt"
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
