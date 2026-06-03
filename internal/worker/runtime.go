package worker

import (
	"context"
	"errors"
	"fmt"
)

const ConcurrencyKeySite = "site"

const (
	WorkflowGCSite         = "gc-site"
	WorkflowManualDelete   = "manual-delete"
	WorkflowSitePurge      = "site-purge"
	WorkflowTombstonePurge = "tombstone-purge"
)

type Handler func(ctx context.Context, input map[string]any) error

type WorkflowDef struct {
	Name           string
	ConcurrencyKey string
	EventTriggers  []string
	Cron           []string
	Handler        Handler
}

type Engine interface {
	Register(def WorkflowDef) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type Runtime struct {
	engine Engine
	defs   []WorkflowDef
}

func NewRuntime(engine Engine) *Runtime {
	return &Runtime{engine: engine}
}

func (rt *Runtime) Register(def WorkflowDef) error {
	if def.Name == "" {
		return errors.New("worker: workflow name required")
	}
	if def.Handler == nil {
		return fmt.Errorf("worker: workflow %s has nil handler", def.Name)
	}
	for _, existing := range rt.defs {
		if existing.Name == def.Name {
			return fmt.Errorf("worker: workflow %s already registered", def.Name)
		}
	}
	if err := rt.engine.Register(def); err != nil {
		return fmt.Errorf("worker: register %s: %w", def.Name, err)
	}
	rt.defs = append(rt.defs, def)
	return nil
}

func (rt *Runtime) Registered() []WorkflowDef {
	out := make([]WorkflowDef, len(rt.defs))
	copy(out, rt.defs)
	return out
}

func (rt *Runtime) Start(ctx context.Context) error { return rt.engine.Start(ctx) }

func (rt *Runtime) Stop(ctx context.Context) error { return rt.engine.Stop(ctx) }
