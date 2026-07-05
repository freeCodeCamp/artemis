# Artemis — reference

Audience: artemis contributors + maintainers. Architecture, API contract, configuration, observability, and the integration suite. Project overview lives in the [root README](../README.md); the release flow in [`RELEASING.md`](RELEASING.md).

## API

Full route table, cross-checked against `internal/server/server.go` (`chi` wiring — source of truth):

```
GET    /healthz                                               → { ok: true }
GET    /readyz                                                → readiness (probes Valkey + R2)
GET    /metrics                                                → prometheus exposition

GET    /api/whoami                                             → { login, authorizedSites }
POST   /api/deploy/init                   { site, sha, files? } → { deployId, jwt, expiresAt }
GET    /api/sites                         [?slug=…]             → { count, sites: [SiteRow] }
POST   /api/site/register                 { slug, teams? }      → 201 SiteRow
PATCH  /api/site/{slug}                   { teams }             → 200 SiteRow
DELETE /api/site/{slug}                   [?purge=true]         → 204 · or 200 { slug, status: "purged", moved } when purging
GET    /api/site/{site}/deploys                                 → [{ deployId, ts, sha, size }]
DELETE /api/site/{site}/deploys/{deployId}                      → 200 { site, deployId, status: "tombstoned", moved } · 409 deploy_aliased
GET    /api/site/{site}/alias/{mode}                            → { site, mode, deployId, url }
POST   /api/site/{site}/promote                                 → { url }
POST   /api/site/{site}/rollback          { to }                → { url }

POST   /api/repo                          { name, visibility?, description?, template? } → 201 RepoRow  (feature-gated)
GET    /api/repos                         [?status=&mine=]      → [RepoRow]                              (feature-gated)
GET    /api/repo/templates                                      → { templates: string[] }                (feature-gated)
GET    /api/repo/{id}                                           → RepoRow                                 (feature-gated)
POST   /api/repo/{id}/approve                                   → { outcome, request: RepoRow }           (feature-gated)
POST   /api/repo/{id}/reject              { reason? }           → RepoRow                                 (feature-gated)
DELETE /api/repo/{id}                                           → 204                                     (feature-gated)

PUT    /api/deploy/{deployId}/upload      multipart stream      → { received }
POST   /api/deploy/{deployId}/finalize    { mode }              → { url }
```

`/api/repo*` is mounted only when `RepoEnabled()` is true (Apollo-11 App credentials configured — see Configuration). `DELETE /api/site/{slug}?purge=true` additionally moves the site's R2 prefix to `_trash/` and records a tombstone (gated the same as the plain delete); the bare `DELETE` only removes the registry row.

Auth headers (`/api/*` except `/healthz`, `/readyz`, `/metrics`):

| Endpoint                                                                                    | Bearer                                                                       |
| ------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `GET /api/*`, `POST /api/deploy/init`, `POST /api/site/*`, `POST`/`GET`/`DELETE /api/repo*` | GitHub token (PAT / OIDC)                                                    |
| `PUT /api/deploy/{deployId}/upload`, `POST /api/deploy/{deployId}/finalize`                 | Deploy-session JWT (HS256, ≤15 min, scoped to one `(login, site, deployId)`) |

Team-gated beyond the base GitHub-bearer check: `POST /api/site/register`, `PATCH /api/site/{slug}`, `DELETE /api/site/{slug}` (`REGISTRY_AUTHZ_TEAM`); `POST /api/repo` (`REPO_CREATE_AUTHZ_TEAM`); `POST /api/repo/{id}/approve`, `POST /api/repo/{id}/reject`, `DELETE /api/repo/{id}` (`REPO_APPROVE_AUTHZ_TEAM`). All other `/api/*` reads are open to any authenticated GitHub bearer.

## Configuration (env-driven)

Loaded + validated in `internal/config/config.go` (`Load()` — fails fast on the first bad var).

**Core / R2 / server**

| Variable               | Default                   | Description                                            |
| ---------------------- | -------------------------- | -------------------------------------------------------- |
| `PORT`                 | `8080`                     | HTTP listen port                                          |
| `R2_ENDPOINT`          | _(required)_               | `https://<account>.r2.cloudflarestorage.com`             |
| `R2_ACCESS_KEY_ID`     | _(required)_               | Admin S3 key                                              |
| `R2_SECRET_ACCESS_KEY` | _(required)_               | Admin S3 secret                                           |
| `R2_BUCKET`            | `universe-static-apps-01` | Single shared bucket (prefix-scoped per site)             |
| `UPLOAD_MAX_BYTES`     | `104857600` (100 MiB)      | Body-size cap on `PUT /api/deploy/{deployId}/upload`     |
| `LOG_LEVEL`            | `info`                     | `debug`, `info`, `warn`, `error`                          |

**GitHub identity + site registry**

| Variable                 | Default                  | Description                                              |
| ------------------------- | -------------------------- | ------------------------------------------------------------ |
| `GH_CLIENT_ID`            | _(required)_              | GitHub OAuth app client ID (CLI device flow)              |
| `GH_ORG`                  | `freeCodeCamp`            | GitHub org for site-registry team probes                  |
| `GH_API_BASE`             | `https://api.github.com` | GitHub REST API base                                       |
| `GH_MEMBERSHIP_CACHE_TTL` | `300`                     | GH `/user` + team membership cache TTL, seconds (5 min)   |
| `VALKEY_ADDR`             | _(required)_              | Valkey `host:port` for the sites registry                  |
| `VALKEY_PASSWORD`         | _(empty)_                 | Valkey AUTH password; empty for unauthenticated dev        |
| `REGISTRY_AUTHZ_TEAM`     | `staff`                  | GH team allowed to mutate the sites registry                |

**Deploy-session JWT + R2 key layout**

| Variable                     | Default                     | Description                                                          |
| ----------------------------- | ------------------------------ | ------------------------------------------------------------------------ |
| `JWT_SIGNING_KEY`             | _(required)_                 | ≥32-byte random; mounted from k8s Secret                              |
| `JWT_TTL_SECONDS`             | `900`                        | Deploy-session JWT TTL, seconds (15 min)                              |
| `ALIAS_PRODUCTION_KEY_FORMAT` | `<site>/production`          | R2 alias key for production env                                       |
| `ALIAS_PREVIEW_KEY_FORMAT`    | `<site>/preview`             | R2 alias key for preview env                                           |
| `DEPLOY_PREFIX_FORMAT`        | `<site>/deploys/<ts>-<sha>/` | R2 prefix per immutable deploy; must contain `<site>` and `<ts>-<sha>` |

**Repo-creation (Apollo-11, feature-gated)**

| Variable                 | Default                     | Description                                                                       |
| ------------------------- | ------------------------------ | -------------------------------------------------------------------------------------- |
| `GH_REPO_ORG`             | `freeCodeCamp-Universe`      | Org repos are created in + whose teams gate repo authz (distinct from `GH_ORG`)        |
| `REPO_CREATE_AUTHZ_TEAM`  | `staff`                     | GH team gating `POST /api/repo`                                                        |
| `REPO_APPROVE_AUTHZ_TEAM` | `none`                       | GH team gating approve/reject/delete; placeholder — production must override           |
| `GH_APP_ID`               | _(empty → repo feature off)_ | Apollo-11 GitHub App id (numeric string)                                              |
| `GH_APP_INSTALLATION_ID`  | _(empty)_                    | App installation id (numeric string)                                                   |
| `GH_APP_PRIVATE_KEY`      | _(empty)_                    | App private key PEM (PKCS#1 or PKCS#8)                                                 |

`GH_APP_ID` / `GH_APP_INSTALLATION_ID` / `GH_APP_PRIVATE_KEY` are all-or-none: set all three to enable the `/api/repo*` self-service repo-creation feature, or none. The two ids must be digit-only strings — `validate()` rejects a malformed value at boot (a YAML int sealed in sops renders as scientific notation through Helm `quote`; seal them as strings).

**Sentry**

| Variable                   | Default          | Description                                    |
| --------------------------- | ------------------ | -------------------------------------------------- |
| `SENTRY_DSN`                | _(empty → off)_  | Sentry DSN; empty disables the SDK entirely       |
| `ENVIRONMENT`               | _(empty)_         | Sentry environment tag (`production`, …)          |
| `SENTRY_TRACES_SAMPLE_RATE` | `0.2`             | Tracing sample rate `[0,1]`; probes dropped       |
| `SENTRY_DEBUG`              | `false`           | Log SDK internals to stderr (`1`/`true`)          |

**Postgres + retention GC + Hatchet** (feature-gated on `DATABASE_URL`; see [local ADR 0001](design/0001-durable-execution-model.md))

| Variable                 | Default                    | Description                                                                                |
| ------------------------- | ----------------------------- | ------------------------------------------------------------------------------------------------ |
| `DATABASE_URL`            | _(empty → GC off)_           | artemis-owned Postgres DSN; empty runs deploy-only mode (no GC, no repo-creation queue)          |
| `PG_CONNECT_RETRY_WINDOW` | `45s`                         | Boot-time retry window for the initial Postgres connect (Go duration; `0` disables retry)       |
| `BACKFILL_ON_BOOT`        | `false`                       | One-shot: scan R2, backfill the Postgres deploy index, then exit (requires `DATABASE_URL`)       |
| `HATCHET_CLIENT_TOKEN`    | _(empty)_                     | Hatchet engine auth token                                                                         |
| `HATCHET_ADDR`            | _(empty → workflows off)_     | Hatchet gRPC address; empty leaves GC wired but workflow scheduling + outbox relay unstarted     |
| `CLEANUP_RETENTION_DAYS`  | `7`                           | Days before a superseded deploy becomes GC-eligible                                               |
| `CLEANUP_RECENT_KEEP`     | `3`                           | Newest N deploys per site kept regardless of age (rollback floor)                                 |
| `CLEANUP_GRACE`           | `72h`                         | Minimum deploy age before GC; must be ≥ `JWT_TTL_SECONDS` and ≥ the 15s serve-cache TTL           |
| `CLEANUP_BLAST_CAP`       | `0` (disabled)                | Max deploys reclaimed per sweep; an over-cap sweep reaps only the oldest N this run               |
| `CLEANUP_TRASH_PREFIX`    | `_trash/`                     | R2 prefix soft-deleted (tombstoned) objects move to before hard purge                             |
| `CLEANUP_RECOVERY_DAYS`   | `7`                           | Days a tombstone survives before the purge pass hard-deletes it                                   |
| `CLEANUP_DRY_RUN`         | `false`                       | Plan-only GC: compute + log the delete set, execute nothing                                       |

## Observability

Three signals, all optional and independently degradable:

- **Structured logs** — JSON to stdout via `log/slog` (`LOG_LEVEL`). Source of truth; scraped by Loki. Probe paths (`/healthz`, `/readyz`, `/metrics`) are silenced.
- **Prometheus** — `/metrics` exposes the standard Go + process collectors plus 15 artemis-specific metrics (full inventory below).
- **Sentry** — errors, panics, performance traces, and a slog→Sentry Logs tee. **Off unless `SENTRY_DSN` is set**, so dev/test runs send nothing.

### Metric inventory

Registered in `internal/handler/metrics.go`, `internal/gc/metrics.go`, `internal/worker/metrics.go`; wired onto one `prometheus.Registry` in `cmd/artemis/main.go`.

**Handler** (`handler.NewMetrics`)

| Metric                                     | Type       | Meaning                                                                                     |
| -------------------------------------------- | ------------ | -------------------------------------------------------------------------------------------------- |
| `artemis_registry_refresh_failures_total`    | Counter    | Full-snapshot registry/valkey refresh that errored; stale snapshot stays served                    |
| `artemis_alias_drift_total`                  | Counter    | 409 `alias_drift` responses from `SitePromote` / `SiteRollback` (CAS body-pin mismatch)             |
| `artemis_promote_legacy_bare_total`          | Counter    | Empty-body `POST /api/site/{site}/promote` (no `expectedCurrent` CAS pin)                          |
| `artemis_upstream_error_total{op}`           | CounterVec | `writeUpstreamError` invocations, labelled by `op` (e.g. `r2.put.alias`, `valkey.register`)         |

**GC** (`gc.NewMetrics`)

| Metric                                     | Type       | Meaning                                                                                          |
| -------------------------------------------- | ------------ | -------------------------------------------------------------------------------------------------------- |
| `artemis_gc_deploys_tombstoned_total`        | Counter    | Deploys soft-deleted (moved to `_trash`) by retention GC, manual delete, or site purge                   |
| `artemis_gc_bytes_reclaimed_total`           | Counter    | Bytes hard-reclaimed from `_trash` by the tombstone-purge pass past the recovery window                  |
| `artemis_gc_runs_total{workflow,outcome}`    | CounterVec | GC workflow runs, labelled by workflow (`gc-site`, `manual-delete`, `site-purge`, `tombstone-purge`, `reconcile`) and outcome |
| `artemis_gc_drift_total{kind}`               | CounterVec | Reconcile drift events, labelled by kind (`reindexed`, `orphan`, `pruned`, `aliased_missing`)             |

**Worker / Hatchet** (`worker.NewMetrics`)

| Metric                                              | Type       | Meaning                                                                     |
| ------------------------------------------------------ | ------------ | -------------------------------------------------------------------------------- |
| `artemis_worker_queue_depth{workflow}`                 | GaugeVec   | Pending tasks per workflow queue (sampled from the engine)                       |
| `artemis_worker_dlq_depth`                             | Gauge      | Dead-lettered workflow runs awaiting operator attention                          |
| `artemis_worker_workflow_runs_total{workflow,outcome}` | CounterVec | Workflow runs, labelled by workflow and outcome                                  |
| `artemis_worker_workflow_failures_total{workflow}`     | CounterVec | Workflow run failures, labelled by workflow                                       |
| `artemis_worker_dead_lettered_total{workflow}`         | CounterVec | Workflow runs that exhausted retries and dead-lettered, labelled by workflow      |
| `artemis_relay_published_total`                        | Counter    | Outbox rows published to the engine by the relay loop (at-least-once)            |
| `artemis_relay_failures_total`                         | Counter    | Relay `RunOnce` passes that errored before draining the batch                    |

When enabled, Sentry captures:

| Signal              | Source                                                             |
| ------------------- | ------------------------------------------------------------------ |
| Issues (errors)     | `writeUpstreamError` (tagged + fingerprinted by `op`), repo create |
| Issues (panics)     | the `Recoverer` middleware, with stacktrace                        |
| Issues (background) | registry refresh failures; boot/fatal errors                       |
| Performance traces  | per request (`SENTRY_TRACES_SAMPLE_RATE`; probes always dropped)   |
| Logs                | every slog record (`>= LOG_LEVEL`), teed alongside stdout          |

Each event carries `release = artemis@<version>+<commit>`, the GitHub `login` as user, and the `request_id` tag — the same value returned in the `X-Request-ID` response header, so a Sentry issue joins directly to the stdout log line and the caller's request.

**Secrets never leave the process.** `SendDefaultPII` is off, and each of the three egress channels has its own scrubber (sharing one secret-aware core so they cannot diverge). Issues + transactions (`BeforeSend` / `BeforeSendTransaction`) strip the `Authorization`, `Cookie`, `Proxy-Authorization`, and `X-Forwarded-For` headers, the request body, the query string, and breadcrumbs, and redact secret-shaped substrings from exception values and messages. Logs (`BeforeSendLog` — the SDK does **not** run `BeforeSend` on log envelopes) redact the body and drop attributes keyed as secret or client IP. So GitHub bearer tokens, deploy-session JWTs, and upload bytes never ship on any channel. The R2 admin key, JWT signing key, and GitHub App private key are never attached (the SDK does not send the process env); the redaction pass is defense in depth over already-audited error wrapping.

## R2 layout

```
<bucket>/
└── <site>/
    ├── deploys/
    │   ├── 20260420-141522-abc1234/   # immutable
    │   │   ├── index.html
    │   │   └── ...
    │   └── 20260421-091807-def5678/
    ├── preview                          # alias → "deploys/20260421-091807-def5678"
    └── production                       # alias → "deploys/20260420-141522-abc1234"
```

Atomic alias semantics: `PutObject` is atomic per-key in R2. Old deploy keeps serving until the alias `PUT` lands. Verify-then-PUT order means a partial deploy never becomes live.

## Sites registry

Authoritative store: Valkey (`VALKEY_ADDR`, namespace `valkey`). Each entry maps a site slug to the list of GitHub teams whose members may deploy to that site. Mutations go through the registry endpoints:

```
POST   /api/site/register      { slug, teams? }      → 201 SiteRow
GET    /api/sites              [?slug=…]             → { count, sites: [SiteRow] }
PATCH  /api/site/{slug}        { teams }             → 200 SiteRow
DELETE /api/site/{slug}                              → 204
```

Write endpoints are gated on `REGISTRY_AUTHZ_TEAM` (default `staff`). The read endpoint is open to any GitHub bearer.

Operator-facing CLI surface (universe-cli ≥ 0.5.0):

```sh
universe sites register <slug> --team <team>[,<team>...]
universe sites update   <slug> --team <team>[,<team>...]
universe sites rm       <slug>
universe sites ls       [--mine]
```

Mutations propagate to every artemis replica via the `registry.changed` pub-sub channel within seconds, or ≤ 60 s on the TTL fallback.

See `config/sites.yaml.example` for the on-disk schema shape. The live registry is Valkey; the on-disk YAML form is not consumed at runtime.

## Local development

```sh
cp .env.example .env  # then fill values
just run              # boots HTTP server on $PORT
just test             # go test ./... -cover (unit only)
just image            # docker build
just                  # list all recipes
```

## Local stack (docker-compose)

A fully offline stack — no real GitHub, no real R2, no secrets — for exercising the repo command surface end to end. `docker-compose.yml` wires four services:

| Service      | Image / build            | Role                                                               |
| ------------ | ------------------------ | ------------------------------------------------------------------ |
| `valkey`     | `valkey/valkey:8-alpine` | Registry + name-claim store                                        |
| `minio`      | `minio/minio`            | S3-compatible R2 stand-in (path-style; `minio-setup` seeds bucket) |
| `fakegithub` | `Dockerfile.fakegithub`  | In-memory GitHub API double (`cmd/fakegithub`)                     |
| `artemis`    | `Dockerfile`             | The service under test, pointed at the three fakes via env         |

`cmd/fakegithub` validates the App JWT (RS256 signature + `iss` + ≤600s `exp` cap, like real GitHub) and serves the identity (`/user`, `/user/teams`, team membership) and App (`access_tokens`, repo create/generate/get/list/contents) endpoints artemis calls. One staff user (`smoke-bot`) is a member of `staff` + `apollo-11-approvers`.

```sh
just smoke         # mint ephemeral App keypair, boot stack, run E2E, tear down
just compose-up    # boot the stack and leave it running
just compose-logs  # tail artemis logs
just compose-down  # tear down + drop volumes
```

`just smoke` mints a throwaway RSA keypair (private → artemis `GH_APP_PRIVATE_KEY`, public → `fakegithub`), then asserts `readyz → whoami → templates → repo create (pending) → approve (App creates repo → active) → list`. Set `KEEP_STACK=1` to leave the stack up after the run for inspection.

## Integration testing

End-to-end suite under `internal/integration/`. Build-tagged behind `integration` so it stays out of `just test`. Hits a live, deployed artemis over HTTPS and exercises the full deploy lifecycle:

```
healthz → whoami → init → upload → finalize(preview) → curl preview
       → promote → curl production → list deploys → rollback
```

Plus negative-path coverage (bad token → 401, missing token → 401, unknown site → 403, missing required field → 400).

```sh
ARTEMIS_URL=https://uploads.freecode.camp \
  GH_TOKEN=$(gh auth token) \
  SITE=test ROOT_DOMAIN=freecode.camp \
  just integration
```

`just integration-help` prints the full env-var reference. The suite is **safe to run against production** — it writes only under the `test` site (a staff-only smoke target registered in the artemis registry) and relies on the cleanup cron (7-day retention) for prefix GC.

### Setup / teardown

Suite-level (`TestMain` in `setup_teardown_test.go`):

| Phase    | Action                                                                               |
| -------- | ------------------------------------------------------------------------------------ |
| Setup    | Pre-flight `GET /healthz` — abort with exit 2 if artemis unreachable                 |
| Setup    | Capture **baseline production deploy id** for `SITE` from `/api/site/{site}/deploys` |
| Run      | `m.Run()` — execute every test in the package                                        |
| Teardown | Restore production alias to the captured baseline via `/rollback`                    |

Per-test (`t.Cleanup` in tests that mint deploys):

| Test             | Cleanup                                                                                                                                                                                                                                         |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestDeployFlow` | Logs the new deploy id at end (success or failure) so the artifact is visible in test output. R2 prefix sweep is owned by the cleanup cron — the suite intentionally does not call a delete API (none exists; deploys are immutable by design). |
| `TestRollback`   | None per-test — suite teardown handles prod alias restore                                                                                                                                                                                       |

If teardown's restore call fails, `TestMain` logs the manual fix:

```
[teardown] WARN: restore prod alias failed: ...
[teardown]      manual fix: POST /api/site/test/rollback {"to":"<baselineDeployID>"}
```

Edge cases:

- **Fresh site (no deploys):** baseline capture returns empty; teardown is a no-op.
- **Env unset:** `TestMain` skips capture/teardown; tests `Skip` themselves.
- **Healthz down:** `TestMain` aborts before any test runs (exit 2).

| Variable       | Default         | Purpose                                       |
| -------------- | --------------- | --------------------------------------------- |
| `ARTEMIS_URL`  | _(required)_    | Live artemis base URL, no trailing slash      |
| `GH_TOKEN`     | _(required)_    | GitHub bearer authorized for `SITE`           |
| `SITE`         | `test`          | Registered site slug                          |
| `ROOT_DOMAIN`  | `freecode.camp` | Root domain for preview/production URL derive |
| `PROD_SLO`     | `2m`            | Production-alias serve SLO                    |
| `PREVIEW_SLO`  | `90s`           | Preview-alias serve SLO                       |
| `HTTP_TIMEOUT` | `30s`           | Per-request HTTP timeout                      |

### Apollo-11 App preflight

`just preflight` mints an App JWT from the live `GH_APP_*` env via artemis's own signer and exercises the App-JWT → installation-token path against GitHub (non-mutating). Use it to confirm the Apollo-11 credentials before a deploy that enables `/api/repo*`.

## curl examples

```sh
# init a deploy
curl -X POST https://uploads.freecode.camp/api/deploy/init \
  -H "Authorization: Bearer $GITHUB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"site":"www","sha":"abc1234"}'
# → { "deployId": "20260420-141522-abc1234", "jwt": "<deploy-session-jwt>", "expiresAt": "..." }

# upload a file (deploy-session JWT)
curl -X PUT "https://uploads.freecode.camp/api/deploy/20260420-141522-abc1234/upload?path=index.html" \
  -H "Authorization: Bearer $DEPLOY_JWT" \
  --data-binary @index.html

# finalize → atomic alias
curl -X POST https://uploads.freecode.camp/api/deploy/20260420-141522-abc1234/finalize \
  -H "Authorization: Bearer $DEPLOY_JWT" \
  -H "Content-Type: application/json" \
  -d '{"mode":"preview"}'
# → { "url": "https://www.preview.freecode.camp" }

# promote preview → production
curl -X POST https://uploads.freecode.camp/api/site/www/promote \
  -H "Authorization: Bearer $GITHUB_TOKEN"

# rollback production
curl -X POST https://uploads.freecode.camp/api/site/www/rollback \
  -H "Authorization: Bearer $GITHUB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"20260419-110000-old1234"}'

# whoami
curl https://uploads.freecode.camp/api/whoami -H "Authorization: Bearer $GITHUB_TOKEN"
# → { "login": "octocat", "authorizedSites": ["www","learn"] }
```
