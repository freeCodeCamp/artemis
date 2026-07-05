//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"testing"
)

func TestGCSite_LiveTombstone_DeepAssert(t *testing.T) {
	e := requireStack(t)
	r2c := e.r2Client(t)
	pool := e.pgPool(t)
	ctx := context.Background()

	slug := uniqueSlug("gcs")
	registerSite(t, e, slug)
	t.Cleanup(func() { _ = e.call(t, http.MethodDelete, "/api/site/"+slug, e.GHToken, nil, nil) })

	old := mintDeploy(t, e, slug, "preview")
	mintDeploy(t, e, slug, "preview")
	mintDeploy(t, e, slug, "preview")
	mintDeploy(t, e, slug, "preview")

	site := siteDir(slug)
	oldPrefix := site + "/deploys/" + old + "/"
	trashPrefix := "_trash/" + site + "/" + old + "/"

	if !hasPrefix(t, r2c, oldPrefix) {
		t.Fatalf("R2 deploy prefix %q absent before GC", oldPrefix)
	}

	tag, err := pool.Exec(ctx,
		`UPDATE deploys SET mtime = now() - interval '30 days', alias_released_at = NULL
		 WHERE site=$1 AND id=$2 AND state='active'`, site, old)
	if err != nil {
		t.Fatalf("backdate deploy mtime: %v", err)
	}
	if n := tag.RowsAffected(); n != 1 {
		t.Fatalf("backdate affected %d rows want 1 (site=%q id=%q)", n, site, old)
	}

	newest := mintDeploy(t, e, slug, "preview")

	waitTrash(t, r2c, trashPrefix, oldPrefix)

	if hasPrefix(t, r2c, oldPrefix) {
		t.Fatalf("R2 deploy prefix %q still present after gc-site tombstone-move", oldPrefix)
	}
	if !hasPrefix(t, r2c, trashPrefix) {
		t.Fatalf("R2 trash prefix %q absent after gc-site", trashPrefix)
	}

	var tombN int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM tombstones WHERE site=$1 AND id=$2`, site, old).Scan(&tombN); err != nil {
		t.Fatalf("pg tombstone query: %v", err)
	}
	if tombN != 1 {
		t.Fatalf("pg tombstone rows=%d want 1 for gc'd deploy %q", tombN, old)
	}

	var activeN int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM deploys WHERE site=$1 AND id=$2 AND state='active'`, site, old).Scan(&activeN); err != nil {
		t.Fatalf("pg active deploy query: %v", err)
	}
	if activeN != 0 {
		t.Fatalf("pg active deploy rows=%d want 0 after tombstone for %q", activeN, old)
	}

	newestPrefix := site + "/deploys/" + newest + "/"
	if !hasPrefix(t, r2c, newestPrefix) {
		t.Fatalf("R2 prefix %q for current preview deploy missing; gc-site over-collected", newestPrefix)
	}
}
