package telemetry

import (
	"context"
	"log/slog"
	"sync"
)

type Scope struct {
	ReqID string

	mu       sync.Mutex
	actor    string
	action   string
	site     string
	deployID string
	outcome  string
	route    string
}

func New(reqID string) *Scope {
	return &Scope{ReqID: reqID}
}

type scopeKey struct{}

func NewContext(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

func FromContext(ctx context.Context) *Scope {
	if s, ok := ctx.Value(scopeKey{}).(*Scope); ok && s != nil {
		return s
	}
	return &Scope{}
}

func (s *Scope) SetActor(actor string) {
	s.mu.Lock()
	s.actor = actor
	s.mu.Unlock()
}

func (s *Scope) SetAction(action string) {
	s.mu.Lock()
	s.action = action
	s.mu.Unlock()
}

func (s *Scope) SetResource(site, deployID string) {
	s.mu.Lock()
	s.site = site
	s.deployID = deployID
	s.mu.Unlock()
}

func (s *Scope) SetOutcome(outcome string) {
	s.mu.Lock()
	s.outcome = outcome
	s.mu.Unlock()
}

func (s *Scope) SetRoute(route string) {
	s.mu.Lock()
	s.route = route
	s.mu.Unlock()
}

func (s *Scope) Actor() string    { return s.read(&s.actor) }
func (s *Scope) Action() string   { return s.read(&s.action) }
func (s *Scope) Site() string     { return s.read(&s.site) }
func (s *Scope) DeployID() string { return s.read(&s.deployID) }
func (s *Scope) Outcome() string  { return s.read(&s.outcome) }
func (s *Scope) Route() string    { return s.read(&s.route) }

func (s *Scope) read(field *string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *field
}

func (s *Scope) LogAttrs() []slog.Attr {
	s.mu.Lock()
	defer s.mu.Unlock()

	attrs := make([]slog.Attr, 0, 7)
	attrs = append(attrs, slog.String("reqID", s.ReqID))
	for _, kv := range []struct{ k, v string }{
		{"actor", s.actor},
		{"action", s.action},
		{"site", s.site},
		{"deployId", s.deployID},
		{"outcome", s.outcome},
		{"route", s.route},
	} {
		if kv.v != "" {
			attrs = append(attrs, slog.String(kv.k, kv.v))
		}
	}
	return attrs
}
