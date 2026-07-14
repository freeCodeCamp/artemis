package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/freeCodeCamp/artemis/internal/pg"
)

type AuditRow struct {
	ID         int64          `json:"id"`
	OccurredAt time.Time      `json:"occurredAt"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	Site       string         `json:"site,omitempty"`
	DeployID   string         `json:"deployId,omitempty"`
	Outcome    string         `json:"outcome"`
	RequestID  string         `json:"requestId,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

func toAuditRow(r pg.AuditRecord) AuditRow {
	return AuditRow{
		ID:         r.ID,
		OccurredAt: r.OccurredAt,
		Actor:      r.Actor,
		Action:     r.Action,
		Site:       r.Site,
		DeployID:   r.DeployID,
		Outcome:    r.Outcome,
		RequestID:  r.RequestID,
		Detail:     r.Detail,
	}
}

func (h *Handlers) AuditList(w http.ResponseWriter, r *http.Request) {
	if h.Audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit log is not configured")
		return
	}
	q := r.URL.Query()
	f := pg.AuditFilter{
		Site:   q.Get("site"),
		Actor:  q.Get("actor"),
		Action: q.Get("action"),
	}
	if s := q.Get("since"); s != "" {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_since", "since must be an RFC3339 timestamp")
			return
		}
		f.Since = ts
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a non-negative integer")
			return
		}
		f.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_offset", "offset must be a non-negative integer")
			return
		}
		f.Offset = n
	}

	records, err := h.Audit.ListAudit(r.Context(), f)
	if err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "audit_read_failed", "pg.audit.list", err)
		return
	}
	rows := make([]AuditRow, 0, len(records))
	for _, rec := range records {
		rows = append(rows, toAuditRow(rec))
	}
	writeJSON(w, http.StatusOK, rows)
}
