# 0005 — Drift at source: fixing the cause, not the cleanup

Status: P0/P2/P3 implemented on `fix/artemis-drift-at-source`; P1 deferred (see Sequencing) Supersedes the framing of [0004](0004-drift-detection-and-alerting.md) §rationale.

## Why 0004 needs superseding

0004 justified making the drift cron report-only on the grounds that repairable drift was "an edge case, not worth the risk of an automated repair loop". The first half of that sentence is wrong and the audit that produced this document proves it:

| measure                                    | value                | source                 |
| ------------------------------------------ | -------------------- | ---------------------- |
| `deploy.init` audit rows, 33 days          | 75                   | `audit_log`, prod      |
| `deploy.finalize` audit rows, same window  | 55                   | same                   |
| abandoned deploy sessions                  | 20                   | difference             |
| abandoned sessions that had uploaded bytes | 18                   | drift report, per-site |
| all-time orphan prefixes in R2             | 32 of 37 drift items | `artemis driftreport`  |
| `gc.reconcile` audit rows, all time        | 2                    | both mine, tonight     |

Roughly **18 orphan prefixes per month**, accumulating since the service was deployed, and nothing had ever collected them.

The *decision* in 0004 stands — the cron stays read-only. The *reason* changes. Repair does not belong in a cron because repair should not be needed at all: an abandoned upload is not drift to be detected and reconciled after the fact, it is a deploy session the system never recorded. This document closes that hole, and in doing so changes what the cron's silence means — from "we chose not to look" to "there is genuinely nothing".

## The root cause

`POST /api/deploy/init` mints a JWT and returns. It writes an `audit_log` row (`internal/handler/deploy.go:90`) and nothing else. No row in `deploys` exists until `FinalizeAtomic` (`internal/pg/saga.go:11`) writes one at the *end* of a successful deploy.

Between those two calls the client PUTs files into `<site-dirname>/deploys/<id>/`. If the client then dies — CI cancelled, laptop closed, network dropped — those bytes exist in R2 and **no row anywhere names them**. Every reaper in the service walks the index:

- `gc-site` plans from `DeploysForSite` (`internal/pg/repo.go:72`) → cannot see them.
- `tombstone-purge` walks `tombstones` → cannot see them.
- Only `reconcile`, which lists R2 directly and diffs against the index, can.

That is the entire reason `reconcile` exists. It is a scanner built to find what a missing write should have recorded.

## Not a problem: the domain layout

Raised during review, and worth recording so it is not re-litigated. The doubled string `test.freecode.camp.freecode.camp` **is not a hostname**. It is the malformed R2 object-key prefix that the P0-2 bug renders. Nothing resolves it, nothing requests a certificate for it; it 404s inside R2 and that is the whole of its blast radius.

The real subdomain layout is coherent and verified:

| host                         | R2 alias key                    | labels under root |
| ---------------------------- | ------------------------------- | ----------------- |
| `test.freecode.camp`         | `test.freecode.camp/production` | 1                 |
| `test.preview.freecode.camp` | `test.freecode.camp/preview`    | 2                 |

The edge derives the key from the Host header — `parseSiteAndAlias` (`infra-backup/docker/images/caddy-s3/modules/r2alias/host.go:17-40`) strips the root domain, and if the last remaining label is the preview subdomain it returns `site = <sitePrefix>.<rootDomain>`, `alias = "preview"`. So both hosts collapse to the *same* single-label site dirname and differ only by alias name. That is why `ALIAS_*_KEY_FORMAT` carry the FQDN (`values.production.yaml:53-55`) — the key must match what the edge computes.

Two-label preview hosts are covered by TLS: the live certificate SAN is `*.freecode.camp, *.preview.freecode.camp, freecode.camp` (probed 2026-08-17 against both hosts). A single `*.freecode.camp` wildcard would **not** match `test.preview.freecode.camp` — RFC 6125 wildcards span exactly one label — but a second explicit wildcard is present, so preview is correctly served. This is load-bearing: production holds **70 preview aliases across 70 sites** versus 57 production aliases, most recent 2026-08-16.

No change proposed here.

## Design

Four phases, in dependency order. P0 and P1 are independent of each other; P2 depends on neither but is the one that removes the drift class; P3 depends on P2.

______________________________________________________________________

### P0 — Stop the bleeding

Four small changes. Every one is a live defect today.

#### P0-1. The production-format test harness (do this first)

`DEPLOY_PREFIX_FORMAT` defaults to `<site>/deploys/<ts>-<sha>/` (`internal/config/config.go:226`), which makes the registry slug and the storage dirname **identical strings**. Production uses `<site>.freecode.camp/deploys/…`, where they differ. Every test in `internal/gc` and `cmd/artemis` runs under the default, so every keyspace confusion in this codebase is invisible to CI by construction — including both bugs below.

Add a suite variant that runs the gc and CLI tests under an FQDN-shaped prefix format. This is the single highest-leverage change in the whole document: it is the harness that would have caught the original reconcile no-op *and* P0-2, and it is the harness that keeps P1 honest.

Land it **RED first**, in the same commit as P0-2.

#### P0-2. `LiveAliases` is inert in production (confirmed bug)

`newLiveAliasReader` (`cmd/artemis/gcwire.go:133-157`) substitutes its `site` argument into the alias key format. Both call sites (`gcwire.go:190` for `SiteGC`, `gcwire.go:208` for `Reconciler`) pass a **storage dirname**, because that is what the sweep enumerates. The format expects a **slug**. In production that renders:

```
test.freecode.camp  →  test.freecode.camp.freecode.camp/production   → always 404
```

`r2.IsNotFound` is treated as "no alias" (`gcwire.go:150`), so the function returns an empty map for every site, silently. `LiveAliases` is the last-second re-read that stops gc from trashing a deploy an alias points at (`internal/gc/gcsite.go:107-112`) and the same guard in reconcile. **The safety net is currently inert in production.** It is not load-bearing today only because gc's plan already excludes aliased deploys from the index — but it was added precisely for the race where the plan is stale.

The minimal principled fix, no slug plumbing required:

`SiteDirname` is defined as the first path segment of the deploy prefix head (`internal/handler/deploykey.go:56-62`). The alias formats and the deploy prefix format share that head by construction in every real configuration — `<site>.freecode.camp/production` and `<site>.freecode.camp/deploys/…`. But **nothing validates it**. So:

1. Validate at boot that each alias key format's first segment is byte-identical to the deploy prefix format's first segment. Refuse to start otherwise.
1. Given that invariant, the alias key for a dirname is exactly `dirname + "/" + tail`, where `tail` is the alias format after its first slash. No reverse lookup, no second keyspace conversion.

The boot check is what makes step 2 correct rather than merely usually-correct, and it makes any future misconfiguration a startup failure instead of a silent 404.

#### P0-3. `CLEANUP_BLAST_CAP` defaults to unlimited

**Severity corrected after probing infra: production is _not_ at risk.** `values.production.yaml:73` sets `CLEANUP_BLAST_CAP: "10"`, so the live service is capped. The defect is that both fallbacks are `0` = uncapped — the chart default (`charts/artemis/values.yaml:141`, commented "0 = uncapped") and the code default below. Any new environment, or a values file that forgets the override, runs destructive cleanup with no ceiling. A footgun, not a live fire.

`config.go:545` reads the variable only `if ok` — there is no default, so `BlastCap` is `0`. Both consumers read `0` as *no cap*:

```go
if rc.BlastCap <= 0 || destructive <= rc.BlastCap { return }   // reconcile.go:213
```

The env-var error message says "0 disables" (`config.go:548`), which is true but reads as "disables destruction" when it means "disables the limit on destruction". A safety valve whose default is off is not a safety valve.

Change: default to a real number (10 is the natural choice — larger than any legitimate single-site cleanup, small enough to be a tripwire), and make `0` mean **refuse to perform destructive work**, not *unlimited*. Update the error string to match.

#### P0-4. `gc-site` writes bytes before the row

`gcsite.go` moves the prefix (`MovePrefix`, `internal/gc/gcsite.go:117`) and *then* records the tombstone (`Store.Tombstone`, `:120`). A crash between them leaves bytes in `_trash/` that no tombstone dates — invisible to `tombstone-purge`, so they are never hard-deleted, and invisible to the index. That is a third orphan class, created by the cleanup path itself.

`reconcile` already does it in the safe order — `RecordTombstone` (`internal/gc/reconcile.go:275`) then `MovePrefix` (`:279`) — so a crash leaves a tombstone for bytes still in place, which is self-healing on retry. Make `gc-site` match. Row first, always.

Note while touching this: `reconcile.go:275` hardcodes `bytes = 0` on the tombstone. Reclaimed-bytes accounting is therefore wrong for every reconcile repair. Low severity, fix in passing.

______________________________________________________________________

### P1 — Make the bug class impossible

P0-2 is the third bug in this class found in this codebase. Patching the third one does not stop the fourth.

Introduce two distinct types — `Slug` and `Dirname` — so the compiler rejects the substitution that produced P0-2, and give the layout exactly one renderer. Today there are multiple key-rendering paths that are not cross-checked against each other; `DeployPrefixTemplate` is the authoritative one and the others should be deleted in its favour.

Scope note: Postgres currently stores dirnames in `deploys.site`, while the registry and JWT carry slugs. Normalising the database to slugs is the *clean* end state but requires a migration and touches every query. **That is out of scope here.** The type split alone kills the class without any migration — `Dirname` is simply the type that crosses the R2 and Postgres boundaries, and `Slug` the type that crosses the HTTP and registry boundaries, with `SiteDirname` the single sanctioned conversion.

______________________________________________________________________

### P2 — Remove the orphan source

This is the answer to "what should we do about the GC".

**Have `deploy.init` write a `state='pending'` row in `deploys`.**

The seam is already there and unused. Verified:

- `deploys.state` defaults to `'active'`; all 198 rows in production are `'active'`.
- Every read path filters `state = 'active'` (`internal/pg/repo.go:72,96`), so a `pending` row is invisible to gc planning, drift accounting and the API until it is promoted.
- Only `internal/backfill/backfill.go:96` and `cmd/loadgen/main.go:125` pass a state explicitly.
- `FinalizeAtomic` already upserts `ON CONFLICT (site, id) DO UPDATE SET … state = 'active'` (`internal/pg/saga.go:11-25`). **The promotion needs no code change at all** — finalize flips pending→active as a side effect of what it already does. `ReindexDeploy` does the same for the marker-written-but-crashed case.

Then teach `gc-site` one rule: a `pending` row older than the grace window is an abandoned deploy — trash its prefix and record its tombstone, same as any retention deletion. The abandoned upload stops being an anomaly that requires a special scanner and becomes an ordinary retention case handled by the event-driven job that already works.

Details that must be in the implementation, not discovered during it:

- **The pending row must be written in the same keyspace finalize uses.** `deploy.go:285` passes `h.DeployPrefix.SiteDirname(claims.Site)` to `FinalizeAtomic` — a **dirname**. Init holds `claims.Site`, a **slug**. If the pending write uses the slug, `ON CONFLICT (site, id)` never matches: the row never flips to active, and at grace+72h `gc-site` "expires" a deploy that finalized perfectly well, trashing the prefix the *live* site is serving. This is P0-2's bug class reappearing inside its own fix. The pending→active promotion must be exercised under the P0-1 FQDN harness, where slug and dirname differ; under the default format the mistake is invisible.

- **Failure mode of the init write.** Follow the existing audit precedent exactly (`internal/handler/handler.go:198-215`): nil-check the store, detached 5s timeout, log + Sentry on failure, **never fail the request**. Deploy-only mode with no index must keep working, and making Postgres a hard dependency of every `deploy/init` trades a rare storage leak for a total outage. Reconcile remains the backstop for the leak that survives this.

- **Atomicity of expiry.** The pending row must be retired in the same transaction as the tombstone insert, or the next sweep counts the same deploy twice.

- **The no-bytes path.** 2 of the 20 abandoned sessions in the audit window uploaded nothing. Expiring those must drop the row and skip the tombstone — a tombstone for a prefix that never existed is drift the sweep will then report forever.

- **Threshold.** Grace must sit far above the deploy JWT TTL (15 minutes), so no in-flight deploy is ever reapable. 72h is the obvious choice and matches existing retention language.

Effect: the tombstone/orphan drift class — 32 of the 37 items in the current report — stops being generated at all.

______________________________________________________________________

### P3 — Then, and only then, re-tune the alerting

This is the answer to "what should we do about the CRON".

**Keep `drift-detect` exactly as shipped in 1.7.0.** Read-only by type (`readOnlyStore` / `readOnlyMover` / `Locker=nil`), report-only, nightly, Sentry check-in. That decision was right and nothing here changes it. What changes is what its silence *means*: today a clean report means "we deliberately do not collect the ~18/month we know accrues"; after P2 it means "there is genuinely nothing".

That in turn simplifies the alert policy. The growth-derivative threshold deferred in 0004 becomes unnecessary — once the baseline is zero by construction, a small static threshold is real signal:

- `aliased-missing > 0` → page. (unchanged, this is already a hard failure)
- self-check mismatch or unreadable sites → page. (unchanged)
- `reindex + tombstone > N` for small N → alert, because after P2 it means a new leak class exists that P2 does not cover.

Also: the report should name deploy IDs and ages, not just counts. An operator reading a nightly alert needs to know *which* deploys and *how old* to decide whether it is one bad CI run or a systemic regression.

**Reconcile stays human-run.** After P2 its remaining job is the genuine break-glass case: bytes in R2 with no `deploy.init` record at all — written out of band, or surviving a Postgres restore. That is rare, dangerous, and should require a human at the keyboard.

#### The gap between now and P2

P0–P2 will take time to ship, and roughly 18 orphans per month accrue while the alert policy stays deliberately silent about them. Two honest options:

1. Add the static `reclaimable > 50` branch now — one branch in `classifyDrift` and one test — so the accrual is at least visible.
1. Accept the window explicitly and rely on the manual `artemis reconcile` run.

Recommendation: **(1)**. It is genuinely one branch, and an unwatched accrual with a known rate is exactly what monitoring is for.

______________________________________________________________________

## Bug disposition

Everything verified during this audit, with a decision against each. "Accept, documented" is a real disposition; silence is not.

| #   | Finding                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Disposition                                                                                                                                                                                                                                                                                            |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | `LiveAliases` receives a dirname, alias format expects a slug → always 404, safety net inert in prod                                                                                                                                                                                                                                                                                                                                                                                                        | **P0-2**, fix now                                                                                                                                                                                                                                                                                      |
| 2   | Whole gc/CLI suite runs under a format where slug == dirname, hiding the entire bug class                                                                                                                                                                                                                                                                                                                                                                                                                   | **P0-1**, fix now                                                                                                                                                                                                                                                                                      |
| 3   | `CLEANUP_BLAST_CAP` has no default → `0` → unlimited destruction                                                                                                                                                                                                                                                                                                                                                                                                                                            | **P0-3**, fix now                                                                                                                                                                                                                                                                                      |
| 4   | `gc-site` moves bytes before writing the tombstone row                                                                                                                                                                                                                                                                                                                                                                                                                                                      | **P0-4**, fix now                                                                                                                                                                                                                                                                                      |
| 5   | `reconcile` records tombstones with hardcoded `bytes = 0`                                                                                                                                                                                                                                                                                                                                                                                                                                                   | **P0-4**, in passing                                                                                                                                                                                                                                                                                   |
| 6   | Abandoned `deploy.init` sessions leave unowned bytes (~18/month)                                                                                                                                                                                                                                                                                                                                                                                                                                            | **P2**, the root cause                                                                                                                                                                                                                                                                                 |
| 7   | Two keyspaces with no type separation                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | **P1**                                                                                                                                                                                                                                                                                                 |
| 8   | Postgres stores dirnames, registry stores slugs                                                                                                                                                                                                                                                                                                                                                                                                                                                             | **Out of scope** — migration; P1's types make it survivable                                                                                                                                                                                                                                            |
| 9   | `runDriftReport` is called with no argv (`cmd/artemis/main.go:49`) so `driftreport <anything>` silently ignores it; and any unrecognised subcommand falls through to `run()` (`:62`), i.e. **a mistyped subcommand starts the server**                                                                                                                                                                                                                                                                      | **P0-adjacent** — operators started running these subcommands against production *this week*. A typo that boots a server, and a report that ignores the argument an operator typed, are both how a run gets misread as authoritative. Fix with P0: reject unknown subcommands, reject unexpected args. |
| 10  | A finalized deploy remains writable for the JWT's remaining TTL (up to 15 min)                                                                                                                                                                                                                                                                                                                                                                                                                              | **Accept, documented.** Real, but requires an authorized token holder; tightening it means invalidating the JWT at finalize, which is its own design. Record in ONBOARDING traps.                                                                                                                      |
| 11  | `outbox` has no retention — unbounded growth                                                                                                                                                                                                                                                                                                                                                                                                                                                                | **Backlog.** Small table, slow growth, no correctness impact. Needs a purge job eventually; not part of this wave.                                                                                                                                                                                     |
| 12  | Dead worker code paths                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | **Backlog**, cosmetic.                                                                                                                                                                                                                                                                                 |
| 13  | `RequireScope` / latched rate limiter behaviours                                                                                                                                                                                                                                                                                                                                                                                                                                                            | **Accept, documented.** Both behave as designed; the surprise is documentation, not code. Already captured in ONBOARDING §10.                                                                                                                                                                          |
| 16  | `PlanSite` appended `in.Expired` (mtime ASC, `internal/pg/pending.go:29`) onto `Retain`'s output (mtime DESC, `internal/gc/retain.go:33-37`) without re-sorting, while the blast cap truncates from the tail (`internal/gc/plan.go`). Over-cap runs therefore reaped the **newest** abandoned sessions and starved retention entirely, while the reason string claimed "reaping oldest". Introduced by this sprint; found by the adversarial review, which reproduced it with a probe.                      | **Fixed** — merged set sorted newest-first before the cap; the test now asserts *which* deploys survive, not how many.                                                                                                                                                                                 |
| 17  | The blast cap ran before the dry-run branch in `ReconcileSite`, so a cap of 0 emptied the drift **report**. `drift-detect` runs dry with the same config, so a misconfigured ceiling would have reported a clean fleet. Introduced by this sprint.                                                                                                                                                                                                                                                          | **Fixed** — cap applies only to live runs; the dry run reports full drift and sets `Capped`/`CapReason` as a warning.                                                                                                                                                                                  |
| 18  | `newLiveAliasReader` validated the site *segment* but not the tail, so a format like `<site>.freecode.camp/aliases-<site>/production` passed boot and then fetched a key containing a literal `<site>` — the same 404-for-every-site class P0-2 closed.                                                                                                                                                                                                                                                     | **Fixed** — boot refuses a `<site>` token after the site segment.                                                                                                                                                                                                                                      |
| 15  | Every drift verdict op (`drift.selfcheck`, `drift.unreadable`, `drift.aliased_missing`) is absent from `cronShapedOps` (`internal/observability/sentry.go:377`), so `alertOnDrift`'s `captureBackground(v.Op, ...)` falls through to the transient-rate tracker: threshold 3 with a 26h reset window means a nightly alert is swallowed for two nights, and the in-memory counter resets on every pod restart, so with 3 replicas it may never escalate. **A live site serving nothing could page nobody.** | **Fixed** — all four verdict ops added, pinned by a test that fails if a new one is missed.                                                                                                                                                                                                            |
| 14  | `PublicURLForSite` (`internal/handler/handler.go:152`) is never assigned outside tests, so the hardcoded fallback at `deploy.go:377-382` always runs — the public URL returned to the CLI bakes `freecode.camp` and `.preview.` into the binary, while every other domain fact comes from config. There is no `ROOT_DOMAIN` setting.                                                                                                                                                                        | **Fix with P0** (one-liner): derive the URL from the configured alias formats, or add the root domain to config. Cosmetic today, silently wrong the day the root domain or preview label changes.                                                                                                      |

## Sequencing

Shipped together on `fix/artemis-drift-at-source` at the operator's direction — one sprint covering every bug, each change RED-first. Read the branch log rather than a table here; an earlier revision of this section pinned a commit count that a later round of review fixes immediately invalidated.

Phases map to commits by subject: `fix(gc): read live aliases...` is P0-1 + P0-2, `fix(cli): reject unknown subcommands...` is finding 9, `fix(gc): record the tombstone row...` is P0-4, `fix(gc): make a zero blast cap refuse...` is P0-3, `fix(handler): build public URLs...` is finding 14, the three `feat(...)` commits are P2, and `fix(drift): alert on accruing reclaimable drift` is P3 + finding 15. The remaining `fix`/`test` commits are review follow-ups, including three defects the sprint introduced into its own work: the blast cap silencing the drift report, the unsorted merge of expired-pending into the delete set, and a false claim that reconcile could repair bytes stranded by a failed tombstone-move.

**P1 is deferred, deliberately.** The `Slug`/`Dirname` type split is prevention, not a bug, and a codebase-wide mechanical refactor landing alongside a behaviour change on the deploy hot path would make the review surface unreadable. What ships instead is the cheap structural half: boot validation that the alias formats and the deploy prefix share a site segment (so the dirname reconstruction is correct by construction, not by convention), plus a test that cross-checks all three rendered keys — site, deploy, trash — between the two renderers under the production FQDN format. The type split remains the right next wave.

The `reclaimable` threshold shipped at **25**, not the 50 originally sketched: with P2 collecting abandoned sessions at source the steady-state baseline is zero, so a lower bar is signal rather than noise.

This document is the seed artifact for a new dossier. It does not belong in `artemis-audit-fixes`, which is at 19/20 with a pending converge.

______________________________________________________________________

## Wave outcome (2026-08-17)

The second wave on `fix/artemis-drift-at-source` closed the whole-codebase read findings: destructive write-ordering unified (row before bytes everywhere, `internal/handler/destructive_ordering_test.go` pins purge and deploy-delete), one selection function `capOldest` owns every blast-cap decision, tombstone-purge gained the cap it was missing, GitHub throttles (403 and 429, primary and secondary) classify as rate-limited and are never negative-cached, auth caches are bounded at 4096 entries, uploads carry a 10-minute request deadline, gc workflows carry explicit 30-minute execution timeouts, and ~1,500 lines of dead code left (debounce, deployflows, SetAliasCAS, GetOrFetch, emitSiteChanged, the valkey repo-request store). Adversarial review confirmed 9 findings, all closed; three were defects in this wave's own new code (429s bypassing the rate-limit classifier, a cache-bound test that asserted nothing, a wrong recovery claim in ARCHITECTURE.md). The two closing review-fix commits passed the full gate and mutation checks but received no further review round.

## Open decisions

Everything below waits on an operator call. The evidence is cited so the decision does not need re-derivation.

### audit_log keyspace (finding: two keyspaces in one column)

HTTP writers record the registry slug; the GC writers record the storage dirname (`cmd/artemis/gcwire.go:49-77` pass through the site value gc hands them, which is a dirname). The only reader that joins on the column, `DeployActors` (`internal/pg/audit.go:91`), receives the URL slug (`internal/handler/site.go:311`) — so **slug is the correct keyspace** and the GC writers are the ones to fix. No backfill of existing rows: `0006_audit_log.sql` installs BEFORE UPDATE/DELETE/TRUNCATE triggers that raise, so the table is append-only by design and rewriting history means dropping triggers on production. Recommended: convert the GC writers to slugs and record the cutover date here.

### outbox retention

`Enqueue` only inserts (`internal/pg/outbox.go:31`); published rows are never deleted, so the table grows without bound. Small and slow, no correctness impact. Needs a retention-window decision; the purge can ride the nightly tombstone-purge workflow once a window is chosen.

### Slug/Dirname type split (P1, still deferred)

Its own wave. First deliverable is the compiler-produced coercion-site list — change the type, read every resulting error — BEFORE any behaviour change, so the refactor is provably zero-runtime-effect.

### Orphan reclaim (operator run, time-sensitive)

The live drift report against production proposed 37 repairs (32 failed-upload prefixes, 5 lost index rows) across 9 sites. `drift.reclaimable` alerts at threshold 25, so the first nightly sweep after 1.8.0 deploys will fire until the backlog is reclaimed: `artemis reconcile <site> --apply` per site, and the blast cap of 10 means any site holding more than 10 items needs repeat runs.
