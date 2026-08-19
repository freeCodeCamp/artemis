package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const reconcileCommand = "reconcile"

func parseReconcileArgs(args []string) (string, bool, error) {
	fs := flag.NewFlagSet(reconcileCommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	apply := fs.Bool("apply", false, "repair the drift instead of reporting it")

	var sites []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return "", false, fmt.Errorf("reconcile: %w", err)
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		sites = append(sites, rest[0])
		rest = rest[1:]
	}

	if len(sites) == 0 {
		return "", false, errors.New("reconcile: name exactly one site; there is no fleet-wide repair")
	}
	if len(sites) > 1 {
		return "", false, fmt.Errorf("reconcile: name exactly one site, got %d: %v", len(sites), sites)
	}
	return sites[0], *apply, nil
}

func resolveSweepSite(known []sitekey.Dirname, tmpl handler.DeployPrefixTemplate, arg string) (sitekey.Dirname, error) {
	if arg == "" {
		return "", errors.New("reconcile: site must not be empty")
	}
	if slices.Contains(known, sitekey.Dirname(arg)) {
		return sitekey.Dirname(arg), nil
	}
	dirname := tmpl.SiteDirname(sitekey.Slug(arg))
	if slices.Contains(known, dirname) {
		return dirname, nil
	}
	return "", fmt.Errorf("reconcile: neither %q nor %q is a site the index or registry knows: "+
		"refusing to touch a prefix no store has heard of", arg, dirname)
}

func runReconcileCLI(ctx context.Context, out io.Writer, args []string) error {
	site, apply, err := parseReconcileArgs(args)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required for a reconcile")
	}

	db, err := pg.New(ctx, pg.Config{DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer db.Close()

	r2Client, err := r2.New(ctx, r2.Config{
		Endpoint:        cfg.R2.Endpoint,
		AccessKeyID:     cfg.R2.AccessKeyID,
		SecretAccessKey: cfg.R2.SecretAccessKey,
		Bucket:          cfg.R2.Bucket,
		Region:          "auto",
	})
	if err != nil {
		return fmt.Errorf("connect r2: %w", err)
	}

	repo := pg.NewRepo(db)
	wiring, err := newGCWiring(cfg, repo, r2Client)
	if err != nil {
		return fmt.Errorf("wire gc: %w", err)
	}
	tmpl, err := handler.NewDeployPrefixTemplate(cfg.DeployPrefixFormat)
	if err != nil {
		return fmt.Errorf("deploy prefix format: %w", err)
	}

	known, err := driftReportSites(ctx, repo, pg.NewRegistryStore(db), tmpl)
	if err != nil {
		return fmt.Errorf("enumerate sites: %w", err)
	}
	resolved, err := resolveSweepSite(known, tmpl, site)
	if err != nil {
		return err
	}

	if !apply {
		res, err := newReadOnlySweeper(wiring.Reconciler, r2Client, repo, pg.NewRegistryStore(db), tmpl).
			runSite(ctx, resolved)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "dry run: pass --apply to repair\n")
		return finishReport(out, res.Reports, cfg.Cleanup, res.Stats)
	}

	report, err := wiring.Reconciler.ReconcileSite(ctx, resolved, false)
	fmt.Fprintf(out, "repaired %s: reindex=%d tombstone=%d prune=%d aliased-missing=%d\n",
		resolved, len(report.Reindexed), len(report.OrphanTombstoned),
		len(report.PGPruned), len(report.AliasedMissing))
	if err != nil {
		return fmt.Errorf("reconcile %s: %w", resolved, err)
	}
	return nil
}
