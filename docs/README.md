# Artemis — reference

Audience: artemis contributors and maintainers. This reference covers the API contract, configuration, observability, the R2 layout, the sites registry, and the test suites. The [root README](../README.md) gives the project overview. [`ORIENTATION.md`](ORIENTATION.md) gives the read sequence for a new contributor. [`ARCHITECTURE.md`](ARCHITECTURE.md) describes how the service is built. [`RELEASING.md`](RELEASING.md) gives the release flow. This file documents the surface as it stands today; [`COMPATIBILITY.md`](COMPATIBILITY.md) documents what changed under callers between releases.

## API

Full route table, cross-checked against `internal/server/server.go` (`chi` wiring — source of truth):

```
GET    /healthz                                               → { ok: true }
GET    /readyz                                                → readiness (probes Valkey + R2 + Postgres)

GET    /api/whoami                                             → { login, authorizedSites }
POST   /api/deploy/init                   { site, sha, files? } → { deployId, jwt, expiresAt }
GET    /api/sites                         [?slug=…]             → { count, sites: [SiteRow] }
POST   /api/site/register                 { slug, teams? }      → 201 SiteRow
PATCH  /api/site/{slug}                   { teams }             → 200 SiteRow
DELETE /api/site/{slug}                   [?purge=true ignored] → 204 · or 200 { slug, status: "unpublished", reserved: false }
POST   /api/site/{slug}/undelete                                → 200 { slug, prevProduction, prevPreview } · 404 not_found
POST   /api/site/{slug}/release                                 → 200 { slug, status: "released", moved } · 403 · 404 not_found
GET    /api/site/{site}/deploys                                 → [{ deployId, actor? }]
DELETE /api/site/{site}/deploys/{deployId}                      → 200 { site, deployId, status: "tombstoned", moved } · 409 deploy_aliased
POST   /api/site/{site}/deploys/{deployId}/restore              → 200 { site, deployId, status: "restored", moved, bytes } · 410 site_gone/already_purged
GET    /api/site/{site}/trash                                   → [{ deployId, trashedAt, expiresAt, bytes }]
GET    /api/site/{site}/alias/{mode}                            → { site, mode, deployId, url }
POST   /api/site/{site}/promote                                 → { url } · 422 missing_index
POST   /api/site/{site}/rollback          { to }                → { url } · 422 missing_index

POST   /api/repo                          { name, visibility?, description?, template? } → 201 RepoRow  (feature-gated)
GET    /api/repos                         [?status=&mine=]      → [RepoRow]                              (feature-gated)
GET    /api/repo/templates                                      → { templates: string[] }                (feature-gated)
GET    /api/repo/{id}                                           → RepoRow                                 (feature-gated)
POST   /api/repo/{id}/approve                                   → { outcome, request: RepoRow }           (feature-gated)
POST   /api/repo/{id}/reject              { reason? }           → RepoRow                                 (feature-gated)
DELETE /api/repo/{id}                                           → 204                                     (feature-gated)

GET    /api/audit                         [?site=&actor=&action=&since=&limit=&offset=] → [AuditRow]  (durable trail, newest-first)

PUT    /api/deploy/{deployId}/upload      multipart stream      → { received }
POST   /api/deploy/{deployId}/finalize    { mode }              → { url } · 422 missing_index
```

`/readyz` probes the three upstreams concurrently (5 s each, `internal/handler/readyz.go`). The grades differ by upstream. Valkey is the only hard failure: unreachable, it returns `503 valkey_unreachable` and the pod leaves the Service. An unreachable R2 or Postgres returns `200 {"ready":true,"degraded":true}` and the pod stays in rotation, logging `readyz.r2.degraded` or `readyz.postgres.degraded` at Warn. R2 degrades rather than drains because every replica shares one bucket, so an R2 fault hits all of them at once and a `503` would empty the Service; the endpoints that need R2 still answer their own `502` per request. A failing Valkey or R2 probe reaches Sentry only after `readyzPageThreshold` (3) consecutive failures, and then once per outage until the probe recovers — R2 still pages on that threshold even though the pod stays ready. The R2 probe is one `ListObjectsV2` with retries disabled and its own 3 s HTTP ceiling (`internal/r2/r2.go`), so a probe failure measures R2, not the SDK's retry budget.

`/api/repo*` is mounted only when `RepoEnabled()` is true (Apollo-11 App credentials configured — see Configuration). `DELETE /api/site/{slug}` removes both R2 alias objects and then reserves the name for `SITE_RESERVATION_GRACE` (default 72h); see ADR 0006 and `docs/COMPATIBILITY.md` entries 19-20. The HTML stops serving within the 15s serve-cache TTL; Cloudflare caches non-HTML assets for up to 4h (`max-age=14400`), so an asset URL can still answer from an edge after the site is dark. That window self-heals and is accepted — delete is not instant for assets. `?purge=true` is retired: it is accepted and ignored, so a caller that sends it gets the same reserving delete. `POST /api/site/{slug}/undelete` returns a reserved name to service inside the grace window; past the deadline it answers `404`, because the reclaim sweep owns the row from that moment. `POST /api/site/{slug}/release` ends the reservation early and reclaims the bytes at once; it is gated on `REPO_APPROVE_AUTHZ_TEAM`, not `REGISTRY_AUTHZ_TEAM`, because delete is reversible and release is not. A `DELETE` on a name the serve plane answers but the registry does not know — an orphaned alias, the state `drift-detect` reports as `drift.orphan_aliases` — removes the aliases and answers `200 { slug, status: "unpublished", reserved: false }`. Nothing is reserved there, so `undelete` cannot bring it back. A name that nothing served and nothing registered still answers `404`. Every delete writes one `audit_log` row — `outcome=success`, or `outcome=failure` with `detail.stage` of `unpublish` or `reserve` naming how far it got. `POST /api/site/{site}/deploys/{deployId}/restore` reverses a `DELETE .../deploys/{deployId}` tombstone. It moves the bytes back from `_trash/` and marks the deploy active again. `GET /api/site/{site}/trash` lists the site's tombstoned deploys with their purge-eligibility `expiresAt` (`CLEANUP_RECOVERY_DAYS` after `trashedAt`).

A deploy becomes live only if the serve plane can serve it at `/`. When the target deploy has no root `index.html` — the one object the serve plane requires for `/` — `finalize`, `promote`, and `rollback` reject with `422 missing_index`. The alias does not change, and the previous deploy continues to serve. On `finalize` the `422` body also carries an advisory `hint` when the upload looks like a framework build directory (for example a raw `.next` server build) and not a static export. See ADR-016 §2026-07-26.

`GET /api/audit` reads the durable, append-only `audit_log`. The log holds every privileged action attributed to an actor: staff/CI lifecycle actions (deploy, site, repo) plus system-driven GC rows (`gc.purge` under `actor=system:gc`). A reconcile repair writes `gc.reconcile` rows under `actor=system:reconcile`, and only when an operator starts one. Filter by `site` / `actor` / `action` / `since` (RFC3339). Results are paginated newest-first (`limit` default 100, max 500; `offset`). `limit=0` clamps to the default 100 — it does not return zero rows. This endpoint replaces raw `psql` against production as the read path for the trail. The trail is cross-tenant, so the endpoint is team-gated: the caller must be on the Universe-org staff team (`AUDIT_READ_AUTHZ_TEAM`, default `staff`), not merely any authenticated GitHub bearer. From the CLI: `universe audit ls [--actor --action --site --since --limit] [--json]`. The endpoint shipped in artemis v1.5.0; universe-cli gained the verb in a later release, because the verb depends on the deployed endpoint.

Auth headers (`/api/*` except `/healthz`, `/readyz`):

| Endpoint                                                                                    | Bearer                                                                       |
| ------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `GET /api/*`, `POST /api/deploy/init`, `POST /api/site/*`, `POST`/`GET`/`DELETE /api/repo*` | GitHub token (PAT / OIDC)                                                    |
| `PUT /api/deploy/{deployId}/upload`, `POST /api/deploy/{deployId}/finalize`                 | Deploy-session JWT (HS256, ≤15 min, scoped to one `(login, site, deployId)`) |

These routes have a team gate beyond the base GitHub-bearer check: `POST /api/site/register`, `PATCH /api/site/{slug}`, `DELETE /api/site/{slug}` (`REGISTRY_AUTHZ_TEAM`); `POST /api/repo` (`REPO_CREATE_AUTHZ_TEAM`); `POST /api/repo/{id}/approve`, `POST /api/repo/{id}/reject`, `DELETE /api/repo/{id}`, `POST /api/site/{slug}/release` (`REPO_APPROVE_AUTHZ_TEAM`); `GET /api/audit` (`AUDIT_READ_AUTHZ_TEAM` — the only team-gated read, because the trail is cross-tenant). All other `/api/*` reads are open to any authenticated GitHub bearer.

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

| Variable                      | Default                  | Description                                                                          |
| ----------------------------- | ------------------------ | ------------------------------------------------------------------------------------ |
| `GH_CLIENT_ID`                | _(required)_             | GitHub OAuth app client ID (CLI device flow)                                         |
| `GH_ORG`                      | `freeCodeCamp`           | GitHub org for site-registry team probes                                             |
| `GH_API_BASE`                 | `https://api.github.com` | GitHub REST API base                                                                 |
| `GH_MEMBERSHIP_CACHE_TTL`     | `300`                    | GH `/user` + team membership cache TTL, seconds (5 min)                              |
| `VALKEY_ADDR`                 | _(required)_             | Valkey `host:port` for the sites registry                                            |
| `VALKEY_PASSWORD`             | _(empty)_                | Valkey AUTH password; empty for unauthenticated dev                                  |
| `VALKEY_CONNECT_RETRY_WINDOW` | `5s`                     | Boot-time retry window for the initial Valkey dial (Go duration; `0` disables retry) |
| `REGISTRY_AUTHZ_TEAM`         | `staff`                  | GH team allowed to mutate the sites registry                                         |

**Deploy-session JWT + R2 key layout**

| Variable                      | Default                      | Description                                                            |
| ----------------------------- | ---------------------------- | ---------------------------------------------------------------------- |
| `JWT_SIGNING_KEY`             | _(required)_                 | ≥32-byte random; mounted from k8s Secret                               |
| `JWT_TTL_SECONDS`             | `900`                        | Deploy-session JWT TTL, seconds (15 min)                               |
| `ALIAS_PRODUCTION_KEY_FORMAT` | `<site>/production`          | R2 alias key for production env                                        |
| `ALIAS_PREVIEW_KEY_FORMAT`    | `<site>/preview`             | R2 alias key for preview env                                           |
| `DEPLOY_PREFIX_FORMAT`        | `<site>/deploys/<ts>-<sha>/` | R2 prefix per immutable deploy; must contain `<site>` and `<ts>-<sha>` |
| `PUBLIC_URL_PRODUCTION_FORMAT` | `https://<site>.freecode.camp` | URL returned to the CLI on a production finalize; must contain `<site>` or boot fails |
| `PUBLIC_URL_PREVIEW_FORMAT`   | `https://<site>.preview.freecode.camp` | URL returned to the CLI on a preview finalize; must contain `<site>` or boot fails |

**Repo-creation (Apollo-11, feature-gated)**

| Variable                  | Default                      | Description                                                                            |
| ------------------------- | ---------------------------- | -------------------------------------------------------------------------------------- |
| `GH_REPO_ORG`             | `freeCodeCamp-Universe`      | Org repos are created in + whose teams gate repo authz (distinct from `GH_ORG`)        |
| `REPO_CREATE_AUTHZ_TEAM`  | `staff`                      | GH team gating `POST /api/repo`                                                        |
| `REPO_APPROVE_AUTHZ_TEAM` | `none`                       | GH team gating repo approve/reject/delete and site release; placeholder — production must override |
| `AUDIT_READ_AUTHZ_TEAM`   | `staff`                      | GH team (in `GH_REPO_ORG`) gating `GET /api/audit`; probed via the Universe-org client |
| `GH_APP_ID`               | _(empty → repo feature off)_ | Apollo-11 GitHub App id (numeric string)                                               |
| `GH_APP_INSTALLATION_ID`  | _(empty)_                    | App installation id (numeric string)                                                   |
| `GH_APP_PRIVATE_KEY`      | _(empty)_                    | App private key PEM (PKCS#1 or PKCS#8)                                                 |

`GH_APP_ID` / `GH_APP_INSTALLATION_ID` / `GH_APP_PRIVATE_KEY` are all-or-none: set all three to enable the `/api/repo*` self-service repo-creation feature, or set none. The two ids must be digit-only strings — `validate()` rejects a malformed value at boot. A YAML int sealed in sops renders as scientific notation through Helm `quote`, so seal both ids as strings.

**Sentry**

| Variable                    | Default         | Description                                 |
| --------------------------- | --------------- | ------------------------------------------- |
| `SENTRY_DSN`                | _(empty → off)_ | Sentry DSN; empty disables the SDK entirely |
| `ENVIRONMENT`               | _(empty)_       | Sentry environment tag (`production`, …)    |
| `SENTRY_TRACES_SAMPLE_RATE` | `0.2`           | Tracing sample rate `[0,1]`; probes dropped |
| `SENTRY_DEBUG`              | `false`         | Log SDK internals to stderr                 |

**Postgres + retention GC + Hatchet** (feature-gated on `DATABASE_URL`; see [local ADR 0001](design/0001-durable-execution-model.md))

| Variable                  | Default                   | Description                                                                                  |
| ------------------------- | ------------------------- | -------------------------------------------------------------------------------------------- |
| `DATABASE_URL`            | _(empty → GC off)_        | artemis-owned Postgres DSN; empty runs deploy-only mode (no GC, no repo-creation queue)      |
| `PG_CONNECT_RETRY_WINDOW` | `45s`                     | Boot-time retry window for the initial Postgres connect (Go duration; `0` disables retry)    |
| `BACKFILL_ON_BOOT`        | `false`                   | One-shot: scan R2, backfill the Postgres deploy index, then exit (requires `DATABASE_URL`)   |
| `HATCHET_CLIENT_TOKEN`    | _(empty)_                 | Hatchet engine auth token                                                                    |
| `HATCHET_ADDR`            | _(empty → workflows off)_ | Hatchet gRPC address; empty leaves GC wired but workflow scheduling + outbox relay unstarted |
| `SITE_RESERVATION_GRACE`  | `72h`                     | How long a deleted site's name is held before the nightly sweep frees it (positive duration) |
| `CLEANUP_RETENTION_DAYS`  | `7`                       | Days before a superseded deploy becomes GC-eligible                                          |
| `CLEANUP_RECENT_KEEP`     | `3`                       | Newest N deploys per site kept regardless of age (rollback floor)                            |
| `CLEANUP_GRACE`           | `72h`                     | Minimum deploy age before GC; must be ≥ `JWT_TTL_SECONDS` and ≥ the 15s serve-cache TTL      |
| `CLEANUP_BLAST_CAP`       | `10`                      | Max deploys reclaimed per sweep, oldest first; `0` refuses every destructive repair          |
| `CLEANUP_TRASH_PREFIX`    | `_trash/`                 | R2 prefix soft-deleted (tombstoned) objects move to before hard purge                        |
| `CLEANUP_RECOVERY_DAYS`   | `7`                       | Days a tombstone survives before the purge pass hard-deletes it                              |
| `CLEANUP_OUTBOX_RETENTION_DAYS` | `30`                | Days a **published** outbox row is kept; unpublished rows are never purged, at any age       |
| `CLEANUP_DRY_RUN`         | `false`                   | Plan-only GC: compute + log the delete set, execute nothing                                  |

The three boolean variables above — `SENTRY_DEBUG`, `BACKFILL_ON_BOOT` and `CLEANUP_DRY_RUN` — are parsed with `strconv.ParseBool`. Accepted: `1`, `t`, `T`, `TRUE`, `true`, `True`, `0`, `f`, `F`, `FALSE`, `false`, `False`. Any other non-empty value **refuses the boot** with a named error. It is not silently read as false, because a `CLEANUP_DRY_RUN=yes` typed for safety would otherwise arm a destructive sweep.

## Observability

Observability is **Sentry-only** and independent. artemis is platform infra, so it does NOT route its own telemetry through the platform observability stack (GlitchTip / VictoriaMetrics / ClickHouse) that it deploys — that path would be circular. `SENTRY_DSN` MUST point at an **external** Sentry project (`ingest.sentry.io`), never the self-hosted GlitchTip. All telemetry is off unless `SENTRY_DSN` is set, so dev and test runs send nothing.

- **Issues** — errors, panics, and background-job failures via explicit `CaptureException` / `CaptureBackground` (op-tagged, fingerprinted). `slog.Error` does NOT create issues; the slog tee emits logs only.
- **Performance (traces)** — inbound HTTP transactions `<METHOD> <route>` + outbound spans (GitHub/R2). Probes sampled at 0; destructive routes at 100%; base `SENTRY_TRACES_SAMPLE_RATE` otherwise.
- **Logs** — a `slog`→Sentry Logs tee (`EnableLogs`), scrubbed via `BeforeSendLog`, trace-correlated; numeric attributes preserved as typed values.
- **Crons** — check-ins on `tombstone-purge` (`0 3 * * *`) and `drift-detect` (`0 4 * * *`).
- **Stdout logs** — JSON via `log/slog` (`LOG_LEVEL`, default `info`) for `kubectl logs`; probe paths (`/healthz`, `/readyz`) silenced. Keep `LOG_LEVEL=info` in prod — several Sentry-Logs-covered signals are Info-level.
- **Durable audit trail** — a Postgres append-only `audit_log` (indefinite retention) records every privileged action attributed to an actor. This is the forensic system-of-record, distinct from Sentry Logs (a stream with ~90-day retention). Sentry answers "what happens now, and what is trending"; `audit_log` answers "who did X, provably, months later". Read it via `GET /api/audit` or `universe audit ls` (see API). `request_id` correlates a durable row back to its Sentry trace and its stdout access-log line.

There is **no Prometheus `/metrics` endpoint** (removed in v1.4.0). Signals that were counters are covered by the mechanisms above; [ADR-016](../../fCC-U/Architecture/decisions/016-deploy-proxy.md) holds the design rationale + the full signal→Sentry map.

### Sentry Monitors + Alerts

#### Configured today

Live Sentry holds exactly two issue alert rules, and no metric alert rules. No Monitor/Alert pair from the target-state table below exists yet.

| Rule                                            | Conditions                                                              | Action                                                      |
| ----------------------------------------------- | ----------------------------------------------------------------------- | ----------------------------------------------------------- |
| `Notify Errors via Google Chat` (id `3504127`)  | Regression event · first seen event · issue resolved · reappeared event | Webhook `sentry-google-chat-4b39c5`                         |
| `Notify High Priority via Email` (id `3507165`) | New high-priority issue · existing high-priority issue                  | Email, target type `issue_owners`, fallthrough `AllMembers` |

Both rules fire only on an issue state change, per the conditions listed above — never on a steady, unchanged failure state. One incident on record shows the effect: a cron failure repeated for 33 consecutive nights, and Sentry sent exactly one notification, on the first night. The issue reached its first-seen state that night, then stayed in that same state on every later night, so neither rule fired again. A cron whose failure state never changes can keep failing indefinitely while alerting only once. An operator who watches only these two rules can miss a long-running failure entirely.

Cron check-ins exist for `drift-detect` (`0 4 * * *`) and `tombstone-purge` (`0 3 * * *`); the check-in code path (the `withCheckIn` wrapper in `cmd/artemis/gcworkflows.go`) runs independent of alert coverage. No alert rule is scoped to a missed or failed check-in today — a missed cron reaches Sentry as a Crons signal, but no rule above watches the Crons dataset.

#### Target state (operator setup — recommended, not yet created)

Sentry's 2026 model splits **Monitors** (what to watch) from **Alerts** (who to notify) — both must exist to page. The table below is a recommendation: create a Monitor (dataset → query → threshold) plus an Alert route (Slack / PagerDuty) for each row. None of it exists in the live project today; read every row as a to-do, not as a description of current state.

| Signal                    | Monitor dataset | Condition                                                                                |
| ------------------------- | --------------- | ---------------------------------------------------------------------------------------- |
| upstream faults           | Issues          | new issue where `op` in (`r2.*`, `valkey.*`, `github.*`)                                 |
| workflow / relay failures | Issues          | new issue where `op` in (`gc.site.run`, `tombstone.purge`, `relay.run`, `drift.sweep`, `outbox.backlog`) |
| audit write failure       | Issues          | new issue `op=audit.record`                                                              |
| dangerous drift           | Issues          | new issue where `op` in (`drift.aliased_missing`, `drift.unreadable`, `drift.selfcheck`) |
| cron missed / failed      | Crons           | `tombstone-purge` / `drift-detect` missed or errored                                     |
| HTTP error rate / latency | Spans           | 5xx rate or p99 on `POST /api/*` transactions                                            |

`outbox.backlog` belongs on the relay row rather than a row of its own: it fires when the relay reports success while draining nothing (`cmd/artemis/gcworkflows.go:129-141`), which is a relay failure the `relay.run` row cannot see. `r2.ping` needs no new row — the `r2.*` glob on the upstream-faults row already matches it — but it does need weighting. Since a readyz R2 fault returns `200` degraded and keeps the pod in the Service (`internal/handler/readyz.go:66-75`), `op:r2.ping` is now the only signal that R2 is unreachable; it must not be deprioritised as a duplicate of a probe failure.

The `drift-detect` cron stays silent when it finds only reclaimable drift. It sends an event only when a person must act. A green check-in with no event therefore means "the job ran and found nothing to do", and a missed check-in is the only signal that the job stopped running. Both rows above are needed: the Crons row proves the job runs, and the drift row carries what it found.

The two configured rules above fire on state transitions only. A Monitor built from this table still needs its own re-notification policy for any signal that can stay in one failed state across many runs, such as a cron that fails silently every night. The exact policy is an operator decision; the fact to carry forward is that "an alert rule exists" does not by itself mean "a repeating failure keeps notifying."

Deferred: a DLQ-depth gauge — Hatchet v0.88.6 exposes queue depth only via a deprecated API; dead-letter events are already covered by the per-failure Issues above.

### Who-did-what dashboard (operator setup)

The quick "who did what" view is a **Sentry Logs** dashboard. The slog stream carries `actor` on every request-scoped line (injected by the context log handler), so no code change is needed to query it. Add these widgets to the **Artemis** dashboard in the Sentry UI (the MCP has no dashboard-write API). This view has ~90-day retention; the durable, forensic answer is the Postgres `audit_log` via `GET /api/audit` / `universe audit ls`. Neither the configured rules nor the target-state table above has a Logs-dataset row, even though this dashboard is a Logs view — no alert rule watches the Logs dataset today.

Three widgets, dataset **Logs** for each:

| Widget                 | Type  | Fields / group-by                                       | Filter                                    |
| ---------------------- | ----- | ------------------------------------------------------- | ----------------------------------------- |
| Who did what (24h)     | Table | `actor`, `message`, `count(message)`; sort `-timestamp` | `message:[<privileged set>]`              |
| Actor leaderboard (7d) | Bar   | `count(message)` grouped by `actor`                     | `message:[<privileged set>]`              |
| Unattributed actions   | Table | `actor`, `message`                                      | `message:[<privileged set>]` `!has:actor` |

The `<privileged set>` of terminal-success slog messages: `deploy.finalize`, `site.register`, `site.update`, `site.delete`, `site.purge`, `site.promote`, `site.rollback`, `repo.create.queued`, `repo.approve.created`, `repo.reject.recorded`, `repo.delete.removed`. The "Unattributed actions" widget is a regression alarm. It stays empty in normal operation; a row in it means an action reached Sentry with no actor. GC tombstone actions are system-driven. They land in `audit_log` (`actor=system:gc`) and in Issues, not in this human-activity view. A reconcile repair is an operator action, started by hand, and it writes under `actor=system:reconcile`.

When enabled, Sentry captures:

| Signal              | Source                                                             |
| ------------------- | ------------------------------------------------------------------ |
| Issues (errors)     | `writeUpstreamError` (tagged + fingerprinted by `op`), repo create |
| Issues (panics)     | the `Recoverer` middleware, with stacktrace                        |
| Issues (background) | registry refresh failures; boot/fatal errors                       |
| Performance traces  | per request (`SENTRY_TRACES_SAMPLE_RATE`; probes always dropped)   |
| Logs                | every slog record (`>= LOG_LEVEL`), teed alongside stdout          |

Each event carries `release = artemis@<version>+<commit>`, the GitHub `login` as user, and the `request_id` tag — the same value returned in the `X-Request-ID` response header, so a Sentry issue joins directly to the stdout log line and the caller's request.

**Secrets never leave the process.** `SendDefaultPII` is off. Each of the three egress channels has its own scrubber, and the three share one secret-aware core so they cannot diverge. Issues and transactions (`BeforeSend` / `BeforeSendTransaction`) strip the `Authorization`, `Cookie`, `Proxy-Authorization`, and `X-Forwarded-For` headers, the request body, the query string, and breadcrumbs. They also redact secret-shaped substrings from exception values and messages. Logs (`BeforeSendLog` — the SDK does **not** run `BeforeSend` on log envelopes) redact the body and drop attributes keyed as secret or client IP. GitHub bearer tokens, deploy-session JWTs, and upload bytes therefore never ship on any channel. The R2 admin key, JWT signing key, and GitHub App private key are never attached (the SDK does not send the process env). The redaction pass is defense in depth over already-audited error wrapping.

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

`<site>` here is the storage dirname, not always the registered slug. Artemis derives it by rendering `DEPLOY_PREFIX_FORMAT` with the slug and taking the text up to the first `/`. With the default `DEPLOY_PREFIX_FORMAT` (see the Configuration table above), the dirname equals the slug. See [`ARCHITECTURE.md`](ARCHITECTURE.md) section 9 for the full rule.

Alias writes are atomic: R2 makes each `PutObject` atomic for one key. The old deploy serves until the alias `PUT` completes. Artemis verifies the deploy before the `PUT`, so a partial deploy never becomes live.

## Sites registry

Authoritative store: Postgres when `DATABASE_URL` is set, else Valkey (`VALKEY_ADDR`, namespace `valkey`). See [`ARCHITECTURE.md`](ARCHITECTURE.md) section 9 for the promotion detail. Each entry maps a site slug to the list of GitHub teams whose members may deploy to that site. Mutations go through the registry endpoints:

```
POST   /api/site/register      { slug, teams? }      → 201 SiteRow
GET    /api/sites              [?slug=…]             → { count, sites: [SiteRow] }
PATCH  /api/site/{slug}        { teams }             → 200 SiteRow
DELETE /api/site/{slug}                              → 204 · 200 when the name was an orphan
POST   /api/site/{slug}/undelete                     → 200 · 404 past the grace deadline
POST   /api/site/{slug}/release                      → 200 · approver-gated early reclaim
```

Write endpoints are gated on `REGISTRY_AUTHZ_TEAM` (default `staff`). The read endpoint is open to any GitHub bearer.

Operator-facing CLI surface (universe-cli; the verb table it ships is authoritative — see its `docs/reference.md`):

```sh
universe sites register <slug> --team <team>[,<team>...]
universe sites update   <slug> --team <team>[,<team>...]
universe sites rm       <slug>
universe sites ls       [--mine]
```

Mutations propagate to every artemis replica via the `registry.changed` pub-sub channel within seconds, or ≤ 60 s on the TTL fallback.

See `config/sites.yaml.example` for the on-disk schema shape. The live registry is Postgres or Valkey, per the rule above; the on-disk YAML form is not consumed at runtime.

## Local development

```sh
cp .env.example .env      # then fill values
just run                  # boots HTTP server on $PORT
just test                 # go test -race -cover (unit only — integration excluded by build tag)
just cover                # same, plus coverage.out + coverage.html
just lint                 # go vet
just tidy                 # go mod tidy
just image                # docker build — multi-stage distroless
just clean                # remove build artifacts
just                      # list all recipes
```

### Operator subcommands

The `artemis` binary carries two subcommands beside the server. Both read the same environment as the server, and both need `DATABASE_URL`.

```sh
artemis driftreport               # read the whole fleet, print the drift, write nothing
artemis reconcile <site>          # read one site, print its drift, write nothing
artemis reconcile <site> --apply  # repair that one site
```

`driftreport` is the same sweep that the `drift-detect` cron runs. It cannot write: its store and its mover are read-only types, and its locker is `nil`. Read-only R2 credentials are enough for it.

`reconcile` without `--apply` does the same read for one site. `--apply` does the repair, so it needs R2 credentials that can write, and it takes the per-site lock for each repair. It accepts a registry slug or a storage dirname, and it refuses a name that neither the index nor the registry knows. There is no fleet-wide `--apply`: name one site.

Heavier suites, each with its own stack (see the recipe body for what it boots):

```sh
just e2e-local            # artemis + pg + valkey + minio + hatchet, runs test/e2e
just hatchet-integration  # real hatchet-lite via compose; R2/R3/R4/R5 workflow cases
just loadgen              # scalability harness: ephemeral pg, registry/outbox/gc throughput (R14)
just smoke                # repo create → approve → list against the local stack
just integration          # live-deployment E2E (see Integration testing below)
```

## Local stack (docker-compose)

A fully offline stack — no real GitHub, no real R2, no secrets — that exercises the repo command surface end to end. `docker-compose.yml` wires six services:

| Service       | Image / build             | Role                                                    |
| ------------- | ------------------------- | ------------------------------------------------------- |
| `postgres`    | `postgres:<major>-alpine` | Deploy index, outbox, audit log, tombstones, repo queue |
| `valkey`      | `valkey/valkey:8-alpine`  | Registry + name-claim store                             |
| `minio`       | `minio/minio:latest`      | S3-compatible R2 stand-in (path-style)                  |
| `minio-setup` | `minio/mc:latest`         | One-shot: seeds the bucket, then exits                  |
| `fakegithub`  | `Dockerfile.fakegithub`   | In-memory GitHub API double (`cmd/fakegithub`)          |
| `artemis`     | `Dockerfile`              | The service under test, pointed at the fakes via env    |

`cmd/fakegithub` validates the App JWT (RS256 signature + `iss` + ≤600s `exp` cap, like real GitHub) and serves the identity (`/user`, `/user/teams`, team membership) and App (`access_tokens`, repo create/generate/get/list/contents) endpoints artemis calls. One staff user (`smoke-bot`) is a member of `staff` + `apollo-11-approvers`.

```sh
just smoke         # mint ephemeral App keypair, boot stack, run E2E, tear down
just compose-up    # boot the stack and leave it running
just compose-logs  # tail artemis logs
just compose-down  # tear down + drop volumes
```

`just smoke` mints a throwaway RSA keypair (private → artemis `GH_APP_PRIVATE_KEY`, public → `fakegithub`), then asserts `readyz → whoami → templates → repo create (pending) → approve (App creates repo → active) → list`. Set `KEEP_STACK=1` to leave the stack up after the run for inspection.

## Integration testing

Three separate suites, none of them in `just test`:

| Suite                       | Recipe                     | Runs against                                             |
| --------------------------- | -------------------------- | -------------------------------------------------------- |
| `internal/integration/`     | `just integration`         | A live, deployed artemis over HTTPS                      |
| `test/e2e/`                 | `just e2e-local`           | A locally composed full stack (pg + hatchet + R2 double) |
| `test/integration/hatchet/` | `just hatchet-integration` | A real `hatchet-lite` engine in compose                  |

The rest of this section covers the first of the three. It is build-tagged behind `integration` so it stays out of `just test`, and exercises the full deploy lifecycle:

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

`just integration-help` prints the full env-var reference. The suite is **safe to run against production**. It writes only under the `test` site (a staff-only smoke target registered in the artemis registry), and the cleanup cron (7-day retention) removes its prefixes.

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
