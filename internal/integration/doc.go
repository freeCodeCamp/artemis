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
//	make integration
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
//	PROD_SLO      Production-alias serve SLO (Go duration). Default: `2m`
//	              (matches D38 from ADR-016).
//	PREVIEW_SLO   Preview-alias serve SLO (Go duration). Default: `90s`.
//
// The suite is **safe to run against production** because:
//
//  1. It writes only under SITE (default `test`), which is reserved as a
//     staff-only smoke target in the artemis registry. Real customer
//     sites are untouched.
//  2. Each run uploads a tiny HTML payload tagged with a unique marker
//     and verifies the marker round-trips through Caddy + R2.
//  3. It does not delete deploys; cleanup is handled by the cleanup cron
//     (T22, 7-day retention) so prior deploys remain available for
//     rollback testing.
package integration
