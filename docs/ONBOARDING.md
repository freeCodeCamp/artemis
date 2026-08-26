# artemis, from scratch

Written for someone who has never opened this repo. Read it top to bottom once; the order is deliberate, because each section depends on the one before it.

Every claim cites `file:line`. Where a claim is not verified, it says so.

______________________________________________________________________

## 1. What this service is

artemis is a deploy proxy for static sites. Staff push a folder of files; artemis puts those files in Cloudflare R2 and points a name at them. A separate serve plane (Caddy) reads that name and serves the bytes. artemis never serves site content itself.

It exists because R2 admin credentials must live in exactly one place. artemis holds them. Everyone else asks artemis.

Three things it owns:

1. **The bytes** — deploy folders in R2.
1. **The pointer** — which deploy is currently "production" or "preview" for a site.
1. **The index** — a Postgres mirror of both, so it can answer questions and clean up.

Everything difficult in this codebase comes from the fact that those three things live in two different systems and can disagree.

______________________________________________________________________

## 2. The one thing that will confuse you: two names for every site

A site has **two different names**, in two different keyspaces, and they are not the same string in production.

| name            | example              | lives in                                                         |
| --------------- | -------------------- | ---------------------------------------------------------------- |
| registry slug   | `test`               | the `sites` table, URLs, the CLI, JWT claims                     |
| storage dirname | `test.freecode.camp` | R2 keys, and `deploys.site` / `aliases.site` / `tombstones.site` |

The conversion is one function: `handler.DeployPrefixTemplate.SiteDirname()` (`internal/handler/deploykey.go:56`). It is defined as "everything before the first `/` of the rendered deploy prefix".

Production sets `DEPLOY_PREFIX_FORMAT="<site>.freecode.camp/deploys/<ts>-<sha>/"`, so `SiteDirname("test")` is `test.freecode.camp`.

**Why this is a trap.** The default format in code and in most tests is `"<site>/deploys/<ts>-<sha>/"`. Under that format slug and dirname are *the same string*, so any code that confuses them still works — in tests, and only in tests. Every keyspace bug in this service's history has hidden exactly there, and `docs/ARCHITECTURE.md:349` says so explicitly.

Rule of thumb when reading a function: **HTTP handlers speak slug; everything under `internal/gc` speaks dirname.** The boundary is where a handler calls `SiteDirname`. You can see both in two adjacent lines — `internal/handler/deploy.go:264` passes the *slug* to `aliasKey`, and `:266` passes the *dirname* to `withSiteLock`.

______________________________________________________________________

## 3. Who is allowed to do anything

There are exactly two credentials, and the **route decides which one applies**. There is no priority chain and no fallback, despite what ADR-016's prose suggests.

- **GitHub bearer token** — `internal/handler/middleware.go:86`. The token is exchanged for a login via `GET /user` (`internal/auth/github.go:114`), cached ~5 minutes. Then the *handler* decides authorization per resource by intersecting the caller's GitHub teams with the teams the registry lists for that site (`internal/handler/site.go:349`).
- **Deploy-session JWT** — `internal/handler/middleware.go:130`. artemis mints it itself at `POST /api/deploy/init` (`internal/handler/deploy.go:41`), HS256, scoped to `{login, site, deployId}`, 15-minute default TTL (`internal/auth/jwt.go:34`).

The two route groups are disjoint (`internal/server/server.go:69` vs `:102`). Uploads and finalize live on the JWT plane and make **no** GitHub call at all.

Consequences worth internalising:

- Team-membership changes are **not** immediate. A minted JWT keeps working until it expires. There is no re-probe on upload.
- Every team probe is made **as the caller**, using the caller's own token — that is why the raw token is kept on the request context (`middleware.go:110`).
- There are **two GitHub orgs** in play: `h.GH` for site teams, `h.RepoGH` for repo and audit teams. Confusing them is easy and silent.

______________________________________________________________________

## 4. A deploy, end to end

This is the path that produces every object you will later see in R2.

1. `POST /api/deploy/init` — bearer auth, site-team check, server generates the `deployId` from the caller's sha, mints the JWT (`deploy.go:41-97`).
1. `PUT /api/deploy/{id}/*` — JWT auth, each file written under `DeployPrefix(site, deployId)` (`deploy.go:110-125`). Thousands of these per deploy.
1. `POST /api/deploy/{id}/finalize` — the interesting one (`deploy.go:193-300`):
   1. verify every file in the manifest actually landed in R2 (`VerifyDeployComplete`),
   1. write the marker `_artemis_meta.json` **after** that check, so the marker is never part of what was verified (`deploy.go:224` vs `:242`),
   1. take the Postgres site lock, and inside it: check the site still exists → `PUT` the alias object in R2 → write the index row in Postgres,
   1. all of step 3 runs on a **detached** context with its own 60s budget, so a client hanging up cannot abort a half-committed alias swap (`deploy.go:265`).

**The marker is the whole ballgame.** `_artemis_meta.json` is what distinguishes "a finished deploy" from "a pile of bytes someone abandoned". Everything in section 7 keys off its presence.

Order matters and is not uniform across the codebase — see section 7.4.

______________________________________________________________________

## 5. What is actually in the bucket

The top level of the bucket **is** the site namespace. There is no `sites/` prefix.

```
test.freecode.camp/deploys/20260816-081716-sB68682/index.html
test.freecode.camp/deploys/20260816-081716-sB68682/_artemis_meta.json   <- the marker
test.freecode.camp/production                                            <- alias object
test.freecode.camp/preview                                               <- alias object
_trash/test.freecode.camp/20260609-062751-it09864/index.html             <- soft-deleted
```

An **alias object** is a tiny object whose *body* is a deploy id. That is the pointer. Caddy reads it to decide what to serve. This is why the architecture says **R2 is the only truth about what is live** — Postgres merely mirrors it.

Three layout renderers exist and nothing cross-checks them at runtime:

- `handler.DeployPrefixTemplate` — takes a **slug** (`internal/handler/deploykey.go`)
- `cmd/artemis/gcLayout` — takes a **dirname** (`cmd/artemis/gcwire.go:101`)
- the alias key formats — `ALIAS_PRODUCTION_KEY_FORMAT`, `ALIAS_PREVIEW_KEY_FORMAT`

Nothing validates that they agree on where a site's tree begins. If they diverge, bytes and pointers land in different places and no test notices.

`MovePrefix` (`internal/r2/r2.go:304`) is **not** a rename and **not** atomic: it is a per-object copy-then-delete loop that returns on first failure, leaving a deploy split between the live prefix and `_trash/`.

______________________________________________________________________

## 6. The Postgres side

| table        | what it holds                                                              |
| ------------ | -------------------------------------------------------------------------- |
| `sites`      | the registry: **slug**, authorized teams, creator                          |
| `deploys`    | one row per indexed deploy, keyed `(site=dirname, id)`, with mtime + bytes |
| `aliases`    | mirror of the R2 alias objects, keyed `(site=dirname, name)`               |
| `tombstones` | one row per soft-deleted deploy — the purge worklist                       |
| `outbox`     | events waiting to be published to the workflow engine                      |
| `audit_log`  | append-only record of every privileged action                              |

Note the keyspace split *inside the database*: `sites.slug` is a slug, every other `site` column is a dirname.

**The site lock** is a Postgres advisory lock keyed on the site, taken by every mutation of that site — finalize, promote, rollback, delete, restore, purge, GC, and each reconcile repair. It is never explicitly released: the code opens a dedicated connection outside the pool and relies on closing it to drop the lock (`internal/pg/lock.go:15-34`). That works, but it means every locked request opens a new Postgres connection.

With no Postgres configured the lock becomes a **silent no-op**, and concurrent alias writes race with no error. That is stated as a known limitation, not a bug.

**The outbox** is the standard transactional-outbox pattern: a handler writes its state change and an event row in the same transaction, and a relay loop publishes the event afterwards. Nothing ever deletes published rows — the table grows forever.

______________________________________________________________________

## 7. Cleanup: three different jobs people confuse constantly

This is where most of the last 65 hours went, so it gets the most space.

### 7.1 `gc-site` — retention

Triggered by a `site.changed` event whenever a site's deploys change. Walks the **index** and retires deploys that are old and unreferenced: keeps anything an alias points at, keeps the N most recent, keeps anything inside the retention window (7 days by default, `internal/config/config.go:180`). Moves the rest to `_trash/` and writes a tombstone row.

Because it walks the index, **a prefix with no index row is invisible to it, forever.** Hold that thought.

### 7.2 `tombstone-purge` — the hard delete

A nightly cron. Reads tombstone rows older than the recovery window (7 days), deletes the `_trash/` prefix for real, and clears the row (`internal/gc/tombstone.go:83-86`). This is the only path that destroys bytes irreversibly.

### 7.3 `reconcile` — the drift repairer

Compares R2 against Postgres for one site and classifies every disagreement into one of four **drift classes**. Learn these four; every report and alert is phrased in them.

| class               | what it means                                                            | danger   |
| ------------------- | ------------------------------------------------------------------------ | -------- |
| **reindex**         | complete, marked bytes in R2 with no index row                           | storage  |
| **tombstone**       | unmarked bytes past the grace window, no index row — an abandoned upload | storage  |
| **prune**           | an index row whose bytes are gone                                        | **high** |
| **aliased-missing** | an alias points at a deploy that no longer exists                        | **high** |

The first two are *reclaimable*: unreferenced bytes and forgotten rows. The last two mean something is already broken — `prune` deletes index rows, and `aliased-missing` means a live site serves nothing.

Safety rails on the repair path: the site lock, a **second read inside the lock** before acting, a grace window so young deploys are never touched, and a blast cap.

### 7.4 Why both reaping paths write the row first

Every reaping path records the tombstone row **before** it moves the bytes — `gc-site` at `internal/gc/gcsite.go:135` then `:138`, `reconcile` at `internal/gc/reconcile.go:284` then `:288`.

That order is forced by the purge being row-driven. Bytes moved into `_trash/` without a tombstone are invisible to `tombstone-purge` (which walks `tombstones`), invisible to the index, and invisible to `reconcile` (which lists the *site* prefix, not `_trash/`) — a permanent, undetectable leak. The inverse failure is bounded and visible: a row with its bytes still at the deploy prefix shows up as reindex drift, which the nightly sweep reports. `ReindexDeploy` refuses while the tombstone stands (`internal/pg/repo.go:46`); once `tombstone-purge` drops the row after `CLEANUP_RECOVERY_DAYS`, an operator `artemis reconcile` re-indexes the bytes. The purge clears the row, not the bytes — reclaiming or restoring them is the reconcile's job. Both paths log `tombstone_move_deferred` when they land in that state.

`gc-site` carried the leaky order until the drift-at-source sprint; if you find a doc or comment claiming otherwise, it predates that change.

______________________________________________________________________

## 8. How scheduled work runs

There are no Kubernetes CronJobs for any of this. Workflows and crons are declared **in the Go binary** and registered with a Hatchet engine at boot (`cmd/artemis/gcworkflows.go:107`). Three workflows exist:

| workflow          | trigger              | what it does               |
| ----------------- | -------------------- | -------------------------- |
| `gc-site`         | `site.changed` event | retention (7.1)            |
| `tombstone-purge` | cron `0 3 * * *`     | hard delete (7.2)          |
| `drift-detect`    | cron `0 4 * * *`     | read-only sweep + alerting |

An event travels: handler writes an `outbox` row in its transaction → the relay loop polls every 5s and publishes it to Hatchet → Hatchet starts the workflow.

**Registration is additive and never subtractive.** The SDK only ever calls `PutWorkflow`; nothing deregisters a workflow the binary stopped declaring, and the engine's cron poller selects on `enabled` and the *version's* deleted flag — never on the workflow's. A retired cron therefore keeps firing forever with no worker to serve it, until someone disables it in the engine's database. That is operational knowledge you cannot recover from this repo alone.

______________________________________________________________________

## 9. Why `drift-detect` is read-only *by type*, not by flag

The old design had a `reconcile-scheduler` cron that repaired automatically. It was retired. The reasoning is worth understanding because it shapes the code:

- The old cron enumerated **registry slugs** and handed them to a reconciler that expects **dirnames** (section 2). Under the production format those differ, so every nightly run looked at a prefix that does not exist, found zero drift, and exited successfully. It repaired nothing for months and never once alerted.
- When a read-only sweep finally measured production honestly, the two **dangerous** classes were empty and the two **storage** classes were not.

So the repair capability stayed, but it is now started by a person. The cron that remains cannot repair — and crucially, not because a flag says so. `drift-detect` holds a reconciler whose store and mover are **read-only types** whose write methods return `errReadOnlyViolation`, and whose locker is `nil`. A repair from that job does not compile into a mutation. `TestGCWorkflowDefs_NoWorkflowCanRepairOnASchedule` pins it.

The design principle: **a flag is a request; a type is a guarantee.** Every failure in this subsystem's history came from a job that *could* write and was merely asked not to.

______________________________________________________________________

## 10. Where the bodies are buried

Verified traps, each a real line of code. None of these are hypothetical.

1. **`CLEANUP_BLAST_CAP` of `0` refuses every destructive repair.** It used to mean *unlimited*, and the code default was `0` — a safety valve that defaulted to off. It now defaults to `10` and a literal `0` is a refusal, reported as `Aborted` with a reason. Both consumers agree: `PlanSite` (`internal/gc/plan.go`) and `Reconciler.applyBlastCap` (`internal/gc/reconcile.go`).
1. **A deploy's mtime is parsed out of its ID string**, not read from R2 metadata (`internal/gc/reconcile.go:494`). An ID whose first 15 characters are not `20060102-150405` gets a zero time.
1. **The marker extends a deploy's life, it does not shorten it.** A marked deploy is kept for the full retention window; an unmarked one only for the grace window.
1. **Reconcile records `bytes = 0`** on the tombstones it creates (`internal/gc/reconcile.go:275`), so purge's "bytes reclaimed" figure under-reports.
1. **Subcommand dispatch is closed.** `dispatchSubcommand` (`cmd/artemis/main.go:50`) returns `handled=false` only for an empty argv; anything unrecognised is an error, and `driftreport` rejects arguments outright rather than sweeping the fleet while appearing scoped. Both used to fall through — `artemis --help` once started a web server.
1. **`BACKFILL_ON_BOOT` is a different program.** `runWith` does the backfill and returns before any listener starts, so the process exits 0 having served nothing.
1. **`DELETE /api/site/{slug}` inverted its meaning in 1.10.0.** It used to remove the registry row and leave the alias objects live, so a "deleted" site kept serving. It now removes both alias objects first, then reserves the name for 72h. Bytes and index rows still survive — the delete is an unpublish, not a reclaim (ADR 0006). `?purge` is refused with `400 purge_retired` and performs no delete. Any runbook, script or memory older than 1.10.0 has this backwards.
1. **There is no "already finalized" guard on upload.** A valid JWT can keep writing into a prefix that is already the live production target, for the rest of its TTL. Known and accepted; see design 0005.
1. **A deploy row exists from `init`, not from `finalize`.** `deploy.init` writes `state = 'pending'` (`internal/pg/pending.go`); `FinalizeAtomic`'s existing `ON CONFLICT ... SET state = 'active'` promotes it with no extra write. Every read filters `state = 'active'`, so a pending row is invisible to retention planning, the drift denominator and the API — its only reader is `ExpiredPendingDeploys`, which `gc-site` uses to reap sessions abandoned past the grace window. The write is best-effort: a failure logs and raises to Sentry but never fails the deploy.
1. **`site-purge` writes a sentinel tombstone with `id = ''`**, and that row blocks reindexing of *every* deploy in that site until the recovery window clears it. That is deliberate and easy to mistake for a bug. No 1.10.0 code path reaches it: the purge branch needs `Tombstones` without `Reservations`, and both arrive from `DATABASE_URL`. The sentinel rows in production predate the release.
1. **Finalize, promote and rollback never touch the workflow engine.** All three run inline in the HTTP handlers; the only registered workflows are the three GC jobs. A `RegisterDeployWorkflows` shim once suggested otherwise and has been deleted.

______________________________________________________________________

## 11. Reading order for the code itself

1. `internal/handler/deploy.go` — the whole product in one file.
1. `internal/handler/deploykey.go` — 60 lines, and the source of every keyspace bug.
1. `internal/pg/repo.go` — every query, in one place.
1. `internal/gc/reconcile.go` — the hardest file; read `plan.go` first for the classes.
1. `cmd/artemis/main.go` + `gcwire.go` — how it is all assembled.

To watch it work end to end without touching production: `go test ./cmd/artemis/ -run E2E` spins a real Postgres via testcontainers and a fake S3.
