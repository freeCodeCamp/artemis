package hatchet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	v0Client "github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/client/types"
	hsdk "github.com/hatchet-dev/hatchet/sdks/go"

	"github.com/freeCodeCamp/artemis/internal/worker"
)

const defaultWorkerName = "artemis"

type Config struct {
	Token      string
	Addr       string
	WorkerName string
}

type Adapter struct {
	cfg    Config
	defs   []worker.WorkflowDef
	client *hsdk.Client
	worker *hsdk.Worker
}

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Register(def worker.WorkflowDef) error {
	if def.Name == "" {
		return fmt.Errorf("hatchet: workflow name required")
	}
	if def.Handler == nil {
		return fmt.Errorf("hatchet: workflow %s has nil handler", def.Name)
	}
	a.defs = append(a.defs, def)
	return nil
}

func (a *Adapter) Registered() []worker.WorkflowDef {
	out := make([]worker.WorkflowDef, len(a.defs))
	copy(out, a.defs)
	return out
}

func (a *Adapter) Start(ctx context.Context) error {
	client, err := a.connect()
	if err != nil {
		return fmt.Errorf("hatchet: connect: %w", err)
	}
	a.client = client

	workflows := make([]hsdk.WorkflowBase, 0, len(a.defs))
	for _, def := range a.defs {
		workflows = append(workflows, a.buildWorkflow(client, def))
	}

	w, err := client.NewWorker(a.workerName(), hsdk.WithWorkflows(workflows...))
	if err != nil {
		return fmt.Errorf("hatchet: new worker: %w", err)
	}
	a.worker = w
	return w.StartBlocking(ctx)
}

func (a *Adapter) Stop(context.Context) error {
	return nil
}

func (a *Adapter) Publish(ctx context.Context, topic string, payload []byte) error {
	if a.client == nil {
		return fmt.Errorf("hatchet: publish %s before start", topic)
	}
	var data any
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &data); err != nil {
			return fmt.Errorf("hatchet: publish %s: decode payload: %w", topic, err)
		}
	}
	if err := a.client.Events().Push(ctx, topic, data); err != nil {
		return fmt.Errorf("hatchet: publish %s: %w", topic, err)
	}
	return nil
}

func (a *Adapter) buildWorkflow(client *hsdk.Client, def worker.WorkflowDef) *hsdk.Workflow {
	var opts []hsdk.WorkflowOption
	if def.ConcurrencyKey != "" {
		maxRuns := int32(1)
		strategy := types.GroupRoundRobin
		opts = append(opts, hsdk.WithWorkflowConcurrency(types.Concurrency{
			Expression:    "input." + def.ConcurrencyKey,
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}))
	}
	wf := client.NewWorkflow(def.Name, opts...)
	handler := def.Handler
	wf.NewTask(def.Name, func(ctx hsdk.Context, input map[string]any) (any, error) {
		return nil, handler(ctx, input)
	})
	return wf
}

func (a *Adapter) connect() (*hsdk.Client, error) {
	var opts []v0Client.ClientOpt
	if a.cfg.Token != "" {
		opts = append(opts, v0Client.WithToken(a.cfg.Token))
	}
	if a.cfg.Addr != "" {
		host, portStr, err := net.SplitHostPort(a.cfg.Addr)
		if err != nil {
			return nil, fmt.Errorf("parse addr %q: %w", a.cfg.Addr, err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("parse addr %q port: %w", a.cfg.Addr, err)
		}
		opts = append(opts, v0Client.WithHostPort(host, port))
	}
	return hsdk.NewClient(opts...)
}

func (a *Adapter) workerName() string {
	if a.cfg.WorkerName != "" {
		return a.cfg.WorkerName
	}
	return defaultWorkerName
}

var (
	_ worker.Engine    = (*Adapter)(nil)
	_ worker.Publisher = (*Adapter)(nil)
)
