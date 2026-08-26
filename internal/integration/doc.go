// Package integration contains end-to-end integration tests for the
// artemis deploy proxy. Build-tagged behind `integration` so they are
// excluded from the default `go test ./...` run.
//
// Usage:
//
//	ARTEMIS_URL=https://uploads.freecode.camp \
//	GH_TOKEN=$(gh auth token) \
//	SITE=test \
//	ROOT_DOMAIN=freecode.camp \
//	go test -v -tags=integration ./internal/integration/...
//
// or:
//
//	just integration
//
// Required environment:
//
//	ARTEMIS_URL   Base URL of a live artemis deployment (no trailing slash).
//	GH_TOKEN      A GitHub bearer the target site authorizes (the caller's
//	              team must appear under the site's `teams` list in the
//	              artemis registry — `universe sites ls | grep "<site>"`
//	              to inspect). `gh auth token` is the easiest source on a
//	              dev laptop. CI can pass any PAT or a workflow token.
//
// Optional environment:
//
//	SITE          Target site slug registered with artemis. Default: `test`.
//	ROOT_DOMAIN   Public root domain. Default: `freecode.camp`. Combined
//	              with SITE to derive preview/production URLs as
//	              `<site>.preview.<root>` and `<site>.<root>`.
//	HTTP_TIMEOUT  Per-request HTTP timeout (Go duration). Default: `30s`.
//	PROD_SLO      Production-alias serve SLO (Go duration). Default: `2m`.
//	PREVIEW_SLO   Preview-alias serve SLO (Go duration). Default: `90s`.
//
// The default suite is **safe to run against production** because:
//
//  1. It writes only under SITE (default `test`), which is reserved as a
//     staff-only smoke target in the artemis registry. Real customer
//     sites are untouched.
//  2. Each run uploads a tiny HTML payload tagged with a unique marker
//     and verifies the marker round-trips through Caddy + R2.
//  3. It does not delete deploys; cleanup is handled by the cleanup cron
//     (7-day retention) so prior deploys remain available for rollback
//     testing.
//
// The site-lifecycle suite is the exception to 1 and 3, and is opt-in for
// that reason. It registers its own throwaway slugs (`it-lc-<nanos>` and
// `it-lc-<nanos>r`), never SITE, publishes them, deletes, undeletes and
// releases them — and a release moves the whole site prefix into `_trash/`.
// It runs only when
// ARTEMIS_LIFECYCLE_OK=1 and GH_APPROVER_TOKEN are both set, and it skips
// itself when `go test -timeout` leaves too little budget to reach its own
// cleanup. TestMain also skips its SITE baseline capture and restore under
// this suite. Combined with the `-run TestSiteLifecycle` filter that
// `just integration-lifecycle` supplies, nothing it runs touches SITE. The
// filter is load-bearing: the skip is keyed on the environment variable, so
// exporting it by hand and running the whole suite would disable the SITE
// baseline restore the default tests rely on. Run it via
// `just integration-lifecycle`. It requires artemis 1.10.0 or newer:
// undelete and release do not exist before that.
//
// Site-lifecycle environment:
//
//	ARTEMIS_LIFECYCLE_OK  Set to 1 to enable the suite. Off by default.
//	GH_APPROVER_TOKEN     Bearer on REPO_APPROVE_AUTHZ_TEAM. Required: the
//	                      cleanup releases the throwaway slug, and without
//	                      it every run leaks a live site.
//	REGISTRY_TEAM         Team to register the throwaway slug under.
//	                      Default: `staff`.
package integration
