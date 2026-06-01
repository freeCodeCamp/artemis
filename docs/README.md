# Artemis — reference

Audience: artemis contributors + maintainers. Architecture, API contract, configuration, observability, and the integration suite. Project overview lives in the [root README](../README.md); the release flow in [`RELEASING.md`](RELEASING.md).

## API

```
POST   /api/deploy/init                   { site, sha, files? } → { deployId, jwt, expiresAt }
PUT    /api/deploy/{deployId}/upload      multipart stream      → { received }
POST   /api/deploy/{deployId}/finalize    { mode }              → { url }
POST   /api/site/{site}/promote                                 → { url }
POST   /api/site/{site}/rollback          { to }                → { url }
GET    /api/site/{site}/deploys                                 → [{ deployId, ts, sha, size }]
GET    /api/whoami                                              → { login, authorizedSites }
GET    /healthz                                                 → { ok: true }
```

Auth headers (`/api/*` except `/healthz`):

| Endpoint                                                                    | Bearer                                                                       |
| --------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `POST /api/deploy/init`, `POST /api/site/*`, `GET /api/*`                   | GitHub token (PAT / OIDC)                                                    |
| `PUT /api/deploy/{deployId}/upload`, `POST /api/deploy/{deployId}/finalize` | Deploy-session JWT (HS256, ≤15 min, scoped to one `(login, site, deployId)`) |

## Configuration (env-driven)

| Variable                      | Default                      | Description                                    |
| ----------------------------- | ---------------------------- | ---------------------------------------------- |
| `PORT`                        | `8080`                       | HTTP listen port                               |
| `R2_ENDPOINT`                 | _(required)_                 | `https://<account>.r2.cloudflarestorage.com`   |
| `R2_ACCESS_KEY_ID`            | _(required)_                 | Admin S3 key                                   |
| `R2_SECRET_ACCESS_KEY`        | _(required)_                 | Admin S3 secret                                |
| `R2_BUCKET`                   | `universe-static-apps-01`    | Single shared bucket (prefix-scoped per site)  |
| `GH_CLIENT_ID`                | _(required)_                 | GitHub OAuth app client ID (CLI device flow)   |
| `GH_ORG`                      | `freeCodeCamp`               | GitHub org for team probes                     |
| `GH_API_BASE`                 | `https://api.github.com`     | GitHub REST API base                           |
| `VALKEY_ADDR`                 | _(required)_                 | Valkey `host:port` for the sites registry      |
| `VALKEY_PASSWORD`             | _(required)_                 | Valkey AUTH password                           |
| `REGISTRY_AUTHZ_TEAM`         | `staff`                      | GH team allowed to mutate the sites registry   |
| `JWT_SIGNING_KEY`             | _(required)_                 | 32-byte random; mounted from k8s Secret        |
| `JWT_TTL_SECONDS`             | `900`                        | Deploy-session JWT TTL (15 min)                |
| `GH_MEMBERSHIP_CACHE_TTL`     | `300`                        | GH `/user` + team membership cache TTL (5 min) |
| `GH_APP_ID`                   | _(empty → repo feature off)_ | Apollo-11 GitHub App id (numeric string)       |
| `GH_APP_INSTALLATION_ID`      | _(empty)_                    | App installation id (numeric string)           |
| `GH_APP_PRIVATE_KEY`          | _(empty)_                    | App private key PEM (PKCS#1 or PKCS#8)         |
| `ALIAS_PRODUCTION_KEY_FORMAT` | `<site>/production`          | R2 alias key for production env                |
| `ALIAS_PREVIEW_KEY_FORMAT`    | `<site>/preview`             | R2 alias key for preview env                   |
| `DEPLOY_PREFIX_FORMAT`        | `<site>/deploys/<ts>-<sha>/` | R2 prefix per immutable deploy                 |
| `LOG_LEVEL`                   | `info`                       | `debug`, `info`, `warn`, `error`               |
| `SENTRY_DSN`                  | _(empty → off)_              | Sentry DSN; empty disables the SDK entirely    |
| `ENVIRONMENT`                 | _(empty)_                    | Sentry environment tag (`production`, …)       |
| `SENTRY_TRACES_SAMPLE_RATE`   | `0.2`                        | Tracing sample rate `[0,1]`; probes dropped    |
| `SENTRY_DEBUG`                | `false`                      | Log SDK internals to stderr (`1`/`true`)       |

`GH_APP_ID` / `GH_APP_INSTALLATION_ID` / `GH_APP_PRIVATE_KEY` are all-or-none: set all three to enable the `/api/repo*` self-service repo-creation feature, or none. The two ids must be digit-only strings — `validate()` rejects a malformed value at boot (a YAML int sealed in sops renders as scientific notation through Helm `quote`; seal them as strings).

## Observability

Three signals, all optional and independently degradable:

- **Structured logs** — JSON to stdout via `log/slog` (`LOG_LEVEL`). Source of truth; scraped by Loki. Probe paths (`/healthz`, `/readyz`, `/metrics`) are silenced.
- **Prometheus** — `/metrics` exposes deploy/promote counters, `artemis_upstream_error_total{op}`, `artemis_alias_drift_total`, and `artemis_registry_refresh_failures_total`.
- **Sentry** — errors, panics, performance traces, and a slog→Sentry Logs tee. **Off unless `SENTRY_DSN` is set**, so dev/test runs send nothing.

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
