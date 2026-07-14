# Artemis — reference

Audience: artemis contributors + maintainers. Architecture, API contract, configuration, observability, and the integration suite. Project overview lives in the [root README](../README.md); the release flow in [`RELEASING.md`](RELEASING.md).

## API

Full route table, cross-checked against `internal/server/server.go` (`chi` wiring — source of truth):

```
GET    /healthz                                               → { ok: true }
GET    /readyz                                                → readiness (probes Valkey + R2)

GET    /api/whoami                                             → { login, authorizedSites }
POST   /api/deploy/init                   { site, sha, files? } → { deployId, jwt, expiresAt }
GET    /api/sites                         [?slug=…]             → { count, sites: [SiteRow] }
POST   /api/site/register                 { slug, teams? }      → 201 SiteRow
PATCH  /api/site/{slug}                   { teams }             → 200 SiteRow
DELETE /api/site/{slug}                   [?purge=true]         → 204 · or 200 { slug, status: "purged", moved } when purging
GET    /api/site/{site}/deploys                                 → [{ deployId, actor, ts, sha, size }]
DELETE /api/site/{site}/deploys/{deployId}                      → 200 { site, deployId, status: "tombstoned", moved } · 409 deploy_aliased
POST   /api/site/{site}/deploys/{deployId}/restore              → 200 { site, deployId, status: "restored", moved, bytes } · 410 site_gone/already_purged
GET    /api/site/{site}/trash                                   → [{ deployId, trashedAt, expiresAt, bytes }]
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

GET    /api/audit                         [?site=&actor=&action=&since=&limit=&offset=] → [AuditRow]  (durable trail, newest-first)

PUT    /api/deploy/{deployId}/upload      multipart stream      → { received }
POST   /api/deploy/{deployId}/finalize    { mode }              → { url }
```

`/api/repo*` is mounted only when `RepoEnabled()` is true (Apollo-11 App credentials configured — see Configuration). `DELETE /api/site/{slug}?purge=true` additionally moves the site's R2 prefix to `_trash/` and records a tombstone (gated the same as the plain delete); the bare `DELETE` only removes the registry row. `POST /api/site/{site}/deploys/{deployId}/restore` reverses a `DELETE .../deploys/{deployId}` tombstone, moving the bytes back from `_trash/` and re-marking the deploy active; `GET /api/site/{site}/trash` lists the site's tombstoned deploys with their purge-eligibility `expiresAt` (`CLEANUP_RECOVERY_DAYS` out from `trashedAt`).

`GET /api/audit` reads the durable, append-only `audit_log` — every privileged action (deploy, site, repo lifecycle, GC tombstone/reconcile) attributed to an actor. Filter by `site` / `actor` / `action` / `since` (RFC3339), paginated (`limit` default 100, max 500; `offset`), newest-first. It replaces the raw-`psql`-on-prod path for reading the trail. From the CLI: `universe audit ls [--actor --action --site --since --limit] [--json]` (universe-cli release follows artemis v1.5.0, since it depends on the deployed endpoint).

Auth headers (`/api/*` except `/healthz`, `/readyz`):

| Endpoint                                                                                    | Bearer                                                                       |
| ------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `GET /api/*`, `POST /api/deploy/init`, `POST /api/site/*`, `POST`/`GET`/`DELETE /api/repo*` | GitHub token (PAT / OIDC)                                                    |
| `PUT /api/deploy/{deployId}/upload`, `POST /api/deploy/{deployId}/finalize`                 | Deploy-session JWT (HS256, ≤15 min, scoped to one `(login, site, deployId)`) |

Team-gated beyond the base GitHub-bearer check: `POST /api/site/register`, `PATCH /api/site/{slug}`, `DELETE /api/site/{slug}` (`REGISTRY_AUTHZ_TEAM`); `POST /api/repo` (`REPO_CREATE_AUTHZ_TEAM`); `POST /api/repo/{id}/approve`, `POST /api/repo/{id}/reject`, `DELETE /api/repo/{id}` (`REPO_APPROVE_AUTHZ_TEAM`). All other `/api/*` reads are open to any authenticated GitHub bearer.

## Configuration (env-driven)

Loaded + validated in `internal/config/config.go` (`Load()` — fails fast on the first bad var).

**Core / R2 / server**

| Variable               | Default                   | Description                                          |
| ---------------------- | ------------------------- | ---------------------------------------------------- |
| `PORT`                 | `8080`                    | HTTP listen port                                     |
| `R2_ENDPOINT`          | _(required)_              | `https://<account>.r2.cloudflarestorage.com`         |
| `R2_ACCESS_KEY_ID`     | _(required)_              | Admin S3 key                                         |
| `R2_SECRET_ACCESS_KEY` | _(required)_              | Admin S3 secret                                      |
| `R2_BUCKET`            | `universe-static-apps-01` | Single shared bucket (prefix-scoped per site)        |
| `UPLOAD_MAX_BYTES`     | `104857600` (100 MiB)     | Body-size cap on `PUT /api/deploy/{deployId}/upload` |
| `LOG_LEVEL`            | `info`                    | `debug`, `info`, `warn`, `error`                     |

**GitHub identity + site registry**

| Variable                  | Default                  | Description                                             |
| ------------------------- | ------------------------ | ------------------------------------------------------- |
| `GH_CLIENT_ID`            | _(required)_             | GitHub OAuth app client ID (CLI device flow)            |
| `GH_ORG`                  | `freeCodeCamp`           | GitHub org for site-registry team probes                |
| `GH_API_BASE`             | `https://api.github.com` | GitHub REST API base                                    |
| `GH_MEMBERSHIP_CACHE_TTL` | `300`                    | GH `/user` + team membership cache TTL, seconds (5 min) |
| `VALKEY_ADDR`             | _(required)_             | Valkey `host:port` for the sites registry               |
| `VALKEY_PASSWORD`         | _(empty)_                | Valkey AUTH password; empty for unauthenticated dev     |
| `REGISTRY_AUTHZ_TEAM`     | `staff`                  | GH team allowed to mutate the sites registry            |

**Deploy-session JWT + R2 key layout**

| Variable                      | Default                      | Description                                                            |
| ----------------------------- | ---------------------------- | ---------------------------------------------------------------------- |
| `JWT_SIGNING_KEY`             | _(required)_                 | ≥32-byte random; mounted from k8s Secret                               |
| `JWT_TTL_SECONDS`             | `900`                        | Deploy-session JWT TTL, seconds (15 min)                               |
| `ALIAS_PRODUCTION_KEY_FORMAT` | `<site>/production`          | R2 alias key for production env                                        |
| `ALIAS_PREVIEW_KEY_FORMAT`    | `<site>/preview`             | R2 alias key for preview env                                           |
| `DEPLOY_PREFIX_FORMAT`        | `<site>/deploys/<ts>-<sha>/` | R2 prefix per immutable deploy; must contain `<site>` and `<ts>-<sha>` |

**Repo-creation (Apollo-11, feature-gated)**

| Variable                  | Default                      | Description                                                                     |
| ------------------------- | ---------------------------- | ------------------------------------------------------------------------------- |
| `GH_REPO_ORG`             | `freeCodeCamp-Universe`      | Org repos are created in + whose teams gate repo authz (distinct from `GH_ORG`) |
| `REPO_CREATE_AUTHZ_TEAM`  | `staff`                      | GH team gating `POST /api/repo`                                                 |
| `REPO_APPROVE_AUTHZ_TEAM` | `none`                       | GH team gating approve/reject/delete; placeholder — production must override    |
| `GH_APP_ID`               | _(empty → repo feature off)_ | Apollo-11 GitHub App id (numeric string)                                        |
| `GH_APP_INSTALLATION_ID`  | _(empty)_                    | App installation id (numeric string)                                            |
| `GH_APP_PRIVATE_KEY`      | _(empty)_                    | App private key PEM (PKCS#1 or PKCS#8)                                          |

`GH_APP_ID` / `GH_APP_INSTALLATION_ID` / `GH_APP_PRIVATE_KEY` are all-or-none: set all three to enable the `/api/repo*` self-service repo-creation feature, or none. The two ids must be digit-only strings — `validate()` rejects a malformed value at boot (a YAML int sealed in sops renders as scientific notation through Helm `quote`; seal them as strings).

**Sentry**

| Variable                    | Default         | Description                                 |
| --------------------------- | --------------- | ------------------------------------------- |
| `SENTRY_DSN`                | _(empty → off)_ | Sentry DSN; empty disables the SDK entirely |
| `ENVIRONMENT`               | _(empty)_       | Sentry environment tag (`production`, …)    |
| `SENTRY_TRACES_SAMPLE_RATE` | `0.2`           | Tracing sample rate `[0,1]`; probes dropped |
| `SENTRY_DEBUG`              | `false`         | Log SDK internals to stderr (`1`/`true`)    |

**Postgres + retention GC + Hatchet** (feature-gated on `DATABASE_URL`; see [local ADR 0001](design/0001-durable-execution-model.md))

| Variable                  | Default                   | Description                                                                                  |
| ------------------------- | ------------------------- | -------------------------------------------------------------------------------------------- |
| `DATABASE_URL`            | _(empty → GC off)_        | artemis-owned Postgres DSN; empty runs deploy-only mode (no GC, no repo-creation queue)      |
| `PG_CONNECT_RETRY_WINDOW` | `45s`                     | Boot-time retry window for the initial Postgres connect (Go duration; `0` disables retry)    |
| `BACKFILL_ON_BOOT`        | `false`                   | One-shot: scan R2, backfill the Postgres deploy index, then exit (requires `DATABASE_URL`)   |
| `HATCHET_CLIENT_TOKEN`    | _(empty)_                 | Hatchet engine auth token                                                                    |
| `HATCHET_ADDR`            | _(empty → workflows off)_ | Hatchet gRPC address; empty leaves GC wired but workflow scheduling + outbox relay unstarted |
| `CLEANUP_RETENTION_DAYS`  | `7`                       | Days before a superseded deploy becomes GC-eligible                                          |
| `CLEANUP_RECENT_KEEP`     | `3`                       | Newest N deploys per site kept regardless of age (rollback floor)                            |
| `CLEANUP_GRACE`           | `72h`                     | Minimum deploy age before GC; must be ≥ `JWT_TTL_SECONDS` and ≥ the 15s serve-cache TTL      |
| `CLEANUP_BLAST_CAP`       | `0` (disabled)            | Max deploys reclaimed per sweep; an over-cap sweep reaps only the oldest N this run          |
| `CLEANUP_TRASH_PREFIX`    | `_trash/`                 | R2 prefix soft-deleted (tombstoned) objects move to before hard purge                        |
| `CLEANUP_RECOVERY_DAYS`   | `7`                       | Days a tombstone survives before the purge pass hard-deletes it                              |
| `CLEANUP_DRY_RUN`         | `false`                   | Plan-only GC: compute + log the delete set, execute nothing                                  |

## Observability

Observability is **Sentry-only** and independent. artemis is platform infra, so it does NOT route its own telemetry through the platform o11y stack (GlitchTip / VictoriaMetrics / ClickHouse) it deploys — that would be circular. `SENTRY_DSN` MUST point at an **external** Sentry project (`ingest.sentry.io`), never the self-hosted GlitchTip. Everything is off unless `SENTRY_DSN` is set, so dev/test runs send nothing.

- **Issues** — errors, panics, and background-job failures via explicit `CaptureException` / `CaptureBackground` (op-tagged, fingerprinted). `slog.Error` does NOT create issues; the slog tee emits logs only.
- **Performance (traces)** — inbound HTTP transactions `<METHOD> <route>` + outbound spans (GitHub/R2). Probes sampled at 0; destructive routes at 100%; base `SENTRY_TRACES_SAMPLE_RATE` otherwise.
- **Logs** — a `slog`→Sentry Logs tee (`EnableLogs`), scrubbed via `BeforeSendLog`, trace-correlated; numeric attributes preserved as typed values.
- **Crons** — check-ins on `tombstone-purge` (`0 3 * * *`) and `reconcile-scheduler` (`0 4 * * *`).
- **Stdout logs** — JSON via `log/slog` (`LOG_LEVEL`, default `info`) for `kubectl logs`; probe paths (`/healthz`, `/readyz`) silenced. Keep `LOG_LEVEL=info` in prod — several Sentry-Logs-covered signals are Info-level.
- **Durable audit trail** — a Postgres append-only `audit_log` (indefinite retention) records every privileged action attributed to an actor. This is the forensic system-of-record, distinct from Sentry Logs (a ~90-day glance stream): Sentry answers "what is happening / trending now"; `audit_log` answers "who did X, provably, months later". Read via `GET /api/audit` or `universe audit ls` (see API). `request_id` correlates a durable row back to its Sentry trace / stdout access-log line.

There is **no Prometheus `/metrics` endpoint** (removed in v1.4.0). Signals that were counters are covered by the mechanisms above; [ADR-016](../../fCC-U/Architecture/decisions/016-deploy-proxy.md) holds the design rationale + the full signal→Sentry map.

### Sentry Monitors + Alerts (operator setup)

Sentry's 2026 model splits **Monitors** (what to watch) from **Alerts** (who to notify) — both must exist to page. Create a Monitor (dataset → query → threshold) plus an Alert route (Slack / PagerDuty) for each:

| Signal                    | Monitor dataset | Condition                                                                                |
| ------------------------- | --------------- | ---------------------------------------------------------------------------------------- |
| upstream faults           | Issues          | new issue where `op` in (`r2.*`, `valkey.*`, `github.*`)                                 |
| workflow / relay failures | Issues          | new issue where `op` in (`gc.site.run`, `reconcile.run`, `tombstone.purge`, `relay.run`) |
| audit write failure       | Issues          | new issue `op=audit.record`                                                              |
| reconcile dangerous drift | Issues          | new issue `op=reconcile.aliased_missing`                                                 |
| cron missed / failed      | Crons           | `tombstone-purge` / `reconcile-scheduler` missed or errored                              |
| HTTP error rate / latency | Spans           | 5xx rate or p99 on `POST /api/*` transactions                                            |

Deferred: a DLQ-depth gauge — Hatchet v0.88.6 exposes queue depth only via a deprecated API; dead-letter events are already covered by the per-failure Issues above.

### Who-did-what dashboard (operator setup)

The at-a-glance "who did what" view is a **Sentry Logs** dashboard — the slog stream carries `actor` on every request-scoped line (auto-injected by the context log handler), so no code change is needed to query it. Add these widgets to the **Artemis** dashboard in the Sentry UI (the MCP has no dashboard-write API). This is the ~90-day glance; the durable/forensic answer is Postgres `audit_log` via `GET /api/audit` / `universe audit ls`.

Three widgets, dataset **Logs** for each:

| Widget                 | Type  | Fields / group-by                                       | Filter                                    |
| ---------------------- | ----- | ------------------------------------------------------- | ----------------------------------------- |
| Who did what (24h)     | Table | `actor`, `message`, `count(message)`; sort `-timestamp` | `message:[<privileged set>]`              |
| Actor leaderboard (7d) | Bar   | `count(message)` grouped by `actor`                     | `message:[<privileged set>]`              |
| Unattributed actions   | Table | `actor`, `message`                                      | `message:[<privileged set>]` `!has:actor` |

The `<privileged set>` of terminal-success slog messages: `deploy.finalize`, `site.register`, `site.update`, `site.delete`, `site.purge`, `site.promote`, `site.rollback`, `repo.create.queued`, `repo.approve.created`, `repo.reject.recorded`, `repo.delete.removed`. The "Unattributed actions" widget is a regression tripwire — it should stay empty; anything in it means an action reached Sentry with no actor. GC tombstone/reconcile are system-driven and land in `audit_log` (`actor=system:gc` / `system:reconcile`) + Issues, not this human-activity view.

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
