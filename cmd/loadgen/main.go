//go:build load

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

type config struct {
	dsn            string
	sites          int
	deploysPerSite int
	concurrency    int
	relayBatch     int
	keep           bool
}

type stageResult struct {
	Stage     string  `json:"stage"`
	Ops       int     `json:"ops"`
	Millis    float64 `json:"millis"`
	OpsPerSec float64 `json:"ops_per_sec"`
	P50Micros float64 `json:"p50_micros"`
	P95Micros float64 `json:"p95_micros"`
	P99Micros float64 `json:"p99_micros"`
	MaxMicros float64 `json:"max_micros"`
	Errors    int     `json:"errors"`
}

type report struct {
	StartedAt      string        `json:"started_at"`
	Sites          int           `json:"sites"`
	DeploysPerSite int           `json:"deploys_per_site"`
	Concurrency    int           `json:"concurrency"`
	RelayBatch     int           `json:"relay_batch"`
	PoolMaxConns   int32         `json:"pool_max_conns"`
	TotalDeploys   int           `json:"total_deploys"`
	Stages         []stageResult `json:"stages"`
}

func main() {
	cfg := parseFlags()

	ctx := context.Background()
	db, err := pg.New(ctx, pg.Config{DatabaseURL: cfg.dsn})
	if err != nil {
		fatal("connect: %v", err)
	}
	defer db.Close()

	if err := pg.Migrate(ctx, db.Pool); err != nil {
		fatal("migrate: %v", err)
	}
	if !cfg.keep {
		if err := truncate(ctx, db); err != nil {
			fatal("truncate: %v", err)
		}
	}

	rep := report{
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		Sites:          cfg.sites,
		DeploysPerSite: cfg.deploysPerSite,
		Concurrency:    cfg.concurrency,
		RelayBatch:     cfg.relayBatch,
		PoolMaxConns:   db.Pool.Config().MaxConns,
		TotalDeploys:   cfg.sites * cfg.deploysPerSite,
	}

	reg := pg.NewRegistryStore(db)
	repo := pg.NewRepo(db)

	rep.Stages = append(rep.Stages, runRegister(ctx, cfg, reg))
	rep.Stages = append(rep.Stages, runDeploys(ctx, cfg, repo))
	rep.Stages = append(rep.Stages, runOutboxEnqueue(ctx, cfg, repo))
	rep.Stages = append(rep.Stages, runRelay(ctx, cfg, repo))
	rep.Stages = append(rep.Stages, runGCPlan(ctx, cfg, repo))

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		fatal("encode: %v", err)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.dsn, "dsn", envOr("LOADGEN_DATABASE_URL", "postgres://artemis:artemis@localhost:55433/artemis?sslmode=disable"), "postgres DSN")
	flag.IntVar(&cfg.sites, "sites", 500, "number of sites to register")
	flag.IntVar(&cfg.deploysPerSite, "deploys-per-site", 40, "deploys upserted per site")
	flag.IntVar(&cfg.concurrency, "concurrency", 16, "worker goroutines driving PG")
	flag.IntVar(&cfg.relayBatch, "relay-batch", 100, "outbox relay batch size")
	flag.BoolVar(&cfg.keep, "keep", false, "skip TRUNCATE before the run")
	flag.Parse()
	return cfg
}

func runRegister(ctx context.Context, cfg config, reg *pg.RegistryStore) stageResult {
	teams := []string{"staff"}
	return drive("register", cfg.sites, cfg.concurrency, func(i int) error {
		_, err := reg.Register(ctx, siteSlug(i), teams, "loadgen")
		return err
	})
}

func runDeploys(ctx context.Context, cfg config, repo *pg.Repo) stageResult {
	total := cfg.sites * cfg.deploysPerSite
	base := time.Now().Add(-90 * 24 * time.Hour)
	return drive("deploy_upsert", total, cfg.concurrency, func(i int) error {
		site := siteSlug(i % cfg.sites)
		seq := i / cfg.sites
		id := fmt.Sprintf("%d-%08x", base.Add(time.Duration(seq)*time.Hour).Unix(), i)
		return repo.UpsertDeploy(ctx, site, id, base.Add(time.Duration(seq)*time.Hour), 1<<20, true, "active")
	})
}

func runOutboxEnqueue(ctx context.Context, cfg config, repo *pg.Repo) stageResult {
	return drive("outbox_enqueue", cfg.sites, cfg.concurrency, func(i int) error {
		return repo.EnqueueSiteChanged(ctx, siteSlug(i))
	})
}

func runRelay(ctx context.Context, cfg config, repo *pg.Repo) stageResult {
	relay := &worker.Relay{Source: repo, Publisher: nopPublisher{}, Batch: cfg.relayBatch, Now: time.Now}
	start := time.Now()
	published := 0
	var samples []time.Duration
	errs := 0
	for {
		t0 := time.Now()
		n, err := relay.RunOnce(ctx)
		samples = append(samples, time.Since(t0))
		if err != nil {
			errs++
			break
		}
		published += n
		if n == 0 {
			break
		}
	}
	return summarize("relay_drain", published, errs, time.Since(start), samples)
}

func runGCPlan(ctx context.Context, cfg config, repo *pg.Repo) stageResult {
	g := &gc.SiteGC{
		Store:        repo,
		Mover:        nopMover{},
		Policy:       gc.Policy{RecentKeep: 10, Grace: 24 * time.Hour, Retention: 30 * 24 * time.Hour, ServeCacheTTL: time.Hour},
		BlastCap:     1000,
		DeployPrefix: func(site, id string) string { return site + "/deploys/" + id + "/" },
		TrashPrefix:  func(site, id string) string { return "_trash/" + site + "/" + id + "/" },
		Now:          time.Now,
	}
	return drive("gc_plan_dryrun", cfg.sites, cfg.concurrency, func(i int) error {
		_, err := g.Run(ctx, siteSlug(i), true)
		return err
	})
}

func drive(stage string, n, concurrency int, op func(i int) error) stageResult {
	if concurrency < 1 {
		concurrency = 1
	}
	samples := make([]time.Duration, n)
	var errs atomic.Int64
	var next atomic.Int64
	next.Store(-1)

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1))
				if i >= n {
					return
				}
				t0 := time.Now()
				if err := op(i); err != nil {
					errs.Add(1)
				}
				samples[i] = time.Since(t0)
			}
		}()
	}
	wg.Wait()
	return summarize(stage, n, int(errs.Load()), time.Since(start), samples)
}

func summarize(stage string, ops, errs int, wall time.Duration, samples []time.Duration) stageResult {
	millis := float64(wall.Nanoseconds()) / 1e6
	r := stageResult{Stage: stage, Ops: ops, Millis: round2(millis), Errors: errs}
	if millis > 0 {
		r.OpsPerSec = round2(float64(ops) / (millis / 1000))
	}
	if len(samples) > 0 {
		sorted := append([]time.Duration(nil), samples...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		r.P50Micros = micros(pct(sorted, 0.50))
		r.P95Micros = micros(pct(sorted, 0.95))
		r.P99Micros = micros(pct(sorted, 0.99))
		r.MaxMicros = micros(sorted[len(sorted)-1])
	}
	return r
}

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func micros(d time.Duration) float64 { return round2(float64(d.Nanoseconds()) / 1e3) }

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func truncate(ctx context.Context, db *pg.DB) error {
	_, err := db.Pool.Exec(ctx, `TRUNCATE sites, deploys, aliases, tombstones, outbox RESTART IDENTITY CASCADE`)
	return err
}

func siteSlug(i int) string { return fmt.Sprintf("loadgen-site-%06d.freecode.camp", i) }

type nopPublisher struct{}

func (nopPublisher) Publish(context.Context, string, []byte) error { return nil }

type nopMover struct{}

func (nopMover) MovePrefix(context.Context, string, string) (int, error) { return 0, nil }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
