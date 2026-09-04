# Artemis — caller-visible behaviour changes, v1.6.0 → unreleased

Audience: a staff engineer who integrates with the artemis HTTP API, or who runs an artemis deployment other than freeCodeCamp production.

This file records behaviour that a caller can observe changing between two releases. It is not a feature list. Every entry names the release that shipped the change, the old observable, the new observable, and the source line that proves it.

## Why this file and not `CHANGELOG.md`

`CHANGELOG.md` is generated. release-please owns it: `release-please-config.json` declares the `simple` release type for the repository root, and `docs/RELEASING.md` describes the flow — release-please rewrites the file from Conventional Commit subjects on every release PR. A hand-written entry there is lost at the next release. Do not edit `CHANGELOG.md`.

The other two candidates document the present, not the delta. `README.md` is the project overview. `docs/README.md` is the reference for the surface as it stands today; its route table already prints `422 missing_index` as if it had always been there. Neither tells a caller what changed under them.

So this file is hand-maintained. Add an entry here whenever a change alters a status code, an error code, a validation rule, a stored value, or a cancellation guarantee.

## Scope

Range: `v1.6.0` (tagged 2026-07-17) through `v1.10.2` (tagged 2026-08-28), the release running in production on 2026-09-04, plus entries 29 to 36, which are committed and **not yet released**.

The audit that produced this file found no accidental breaks. Every entry below is intentional. The summary table's "Who feels it" column is the breakdown, and it is derived from the rows rather than restated in prose, because a hand-kept tally has drifted three times in this file's short life.

## Summary

| # | Change | Release | Who feels it |
| --- | --- | --- | --- |
| 1 | Upload `?path=` no longer strips a leading slash | v1.6.4 | API callers |
| 2 | A client disconnect is classified 499, not 502 | v1.6.4 | Log, metric and Sentry readers |
| 3 | `finalize`, `promote` and `rollback` require a root `index.html` | v1.6.3 | API callers |
| 4 | `sha` at `deploy/init` must match `[A-Za-z0-9-]{1,64}` | v1.6.4 | API callers |
| 5 | A GitHub throttle surfaces as `429`, not `401` or `503` | v1.8.0 | API callers |
| 6 | `promote`, `rollback` and `finalize` commit on a detached context | v1.6.4 | API callers |
| 7 | GC audit rows key on the registry slug, not the storage dirname | v1.9.0 | Audit-trail readers |
| 8 | `CLEANUP_BLAST_CAP` gains a default, and an explicit `0` inverts | v1.8.0 | Operators |
| 9 | A purge whose registry row is already absent returns `200`, not `404` | v1.10.0 | API callers |
| 10 | A purge can now fail with `r2_verify_failed` or `r2_move_incomplete` | v1.10.0 | API callers |
| 11 | `audit_log.outcome` gains `failure`, and a failed purge is recorded | v1.10.0 | Audit-trail readers |
| 12 | `PATCH /api/site/{slug}` commits on a detached context | v1.10.0 | API callers |
| 13 | A lock-release failure no longer fails the request it followed | v1.10.0 | API callers |
| 14 | `finalize` retries its index write and audits a partial commit | v1.10.0 | API callers and audit-trail readers |
| 15 | `GET /readyz` with R2 unreachable returns `200` degraded, not `503` | v1.10.0 | Operators and probe readers |
| 16 | Background Sentry issues re-bucket by error class | v1.10.0 | Sentry and alert-rule readers |
| 17 | Alias and deploy key formats are validated at boot, not at first use | v1.10.0 | Operators |
| 18 | DNS faults split into three error classes, and a non-NXDOMAIN resolver fault is now transient | v1.10.0 | Sentry and alert-rule readers |
| 19 | `DELETE /api/site/{slug}` takes the site dark and reserves its name | v1.10.0 | API callers |
| 20 | `?purge=true` is retired; `POST /api/site/{slug}/undelete` is new | v1.10.0 | API callers |
| 21 | A large prefix move finishes in one call | v1.10.0 | API callers and operators |
| 22 | `DELETE` on an orphaned alias answers `200`, not `404` | v1.10.0 | API callers |
| 23 | An orphaned alias is a new `drift-detect` verdict | v1.10.0 | Sentry and alert-rule readers |
| 24 | `POST /api/site/{slug}/release` is new — approver-gated early reclaim | v1.10.0 | API callers and operators |
| 25 | A reserved site answers `409 site_reserved` on every authenticated site endpoint, not `403` | v1.10.0 | API callers and CI pipelines |
| 26 | `GET /api/sites` omits reserved names unless `?state=reserved` | v1.10.2 | API callers |
| 27 | Restoring a deploy whose bytes are gone answers `410`, not `200` | v1.10.2 | API callers |
| 28 | `DELETE` refuses a registered site whose alias it cannot read | v1.10.2 | API callers |
| 29 | Every alias write purges the Cloudflare edge for the host it moved | withdrawn | Nobody; never released |
| 30 | An abandoned pending deploy is swept nightly, not only on the next site event | unreleased | API callers and operators |
| 31 | `drift-detect` alerts on one reclaimable deploy, not 25 | unreleased | Sentry and alert-rule readers |
| 32 | An upload into a finalized deploy answers `409`, not `200` | unreleased | API callers and CI pipelines |
| 33 | An expired reservation is reclaimed by a `site.lifecycle` run that writes a `site.reclaim` audit row | unreleased | Audit readers and operators |
| 34 | A deploy-session JWT without `exp` is rejected with `403 jwt_invalid` | unreleased | Nobody in practice; hand-built tokens only |
| 35 | `SENTRY_TRACES_SAMPLE_RATE=NaN` refuses to boot, not silently disables tracing | unreleased | Operators |
| 36 | A failed outbox publish retries after 60 s, not 5 minutes, and the rest of the batch is not held | unreleased | Operators and alert-rule readers |

## 1 — Upload `?path=` no longer strips a leading slash

**Release:** v1.6.4. Commit `949bbd8`.

**Old:** `PUT /api/deploy/{deployId}/upload?path=/index.html` returns `200`. artemis strips the leading slash and stores the object at `<deployPrefix>index.html`. See `internal/handler/deploy.go:111` at `v1.6.0`:

```go
relPath := strings.TrimPrefix(r.URL.Query().Get("path"), "/")
```

**New:** the same request returns `400` with error code `bad_request` and the message `path must be relative and not traverse`. The handler reads the raw query value (`internal/handler/deploy.go:118`) and `isCleanRelPath` rejects any leading `/` (`internal/handler/deploy.go:409-427`).

**Not a change:** `?path=//index.html` returns `400` at both versions. `TrimPrefix` removes one slash. The second slash still fails `isCleanRelPath` at `v1.6.0` (`internal/handler/deploy.go:359`).

**Action:** send the path relative to the deploy root. Send `index.html`, not `/index.html`.

**Client effect:** universe-cli maps `400` to exit code `10` (`EXIT_USAGE`).

## 2 — A client disconnect is classified 499, not 502

**Release:** v1.6.4. Commit `949bbd8`.

**Old, at the call sites that reached it:** `writeUpstreamError` never inspects the error (`internal/handler/handler.go:256-264` at `v1.6.0`). A `context.Canceled` raised by the caller hanging up is logged as `upstream.error`, reported to Sentry, and recorded with the status its call site passes. Forty-two of the forty-three `writeUpstreamError` call sites pass `http.StatusBadGateway`, so a disconnect is recorded as `502`. The one exception among them is `internal/handler/repo.go:353` at `v1.6.0`, which passes `503`.

**The upload path did not reach it.** A disconnect during `PUT /api/deploy/{deployId}/upload` never called `writeUpstreamError` at `v1.6.0`. `internal/handler/deploy.go:156-162` caught the `context.Canceled` returned by the R2 `PUT` first, logged `deploy.upload.canceled` at warn, and returned without writing a response — so the access log recorded the `statusWriter` default of `200` (`internal/handler/middleware.go:212`) and Sentry saw nothing. Commit `949bbd8` deleted that branch in the same change that added the `499` classification, so upload aborts now take the shared path. Weigh this above its share of the call sites: an upload body is the request most likely to be aborted mid-flight, which is why the special case existed.

**New:** `writeUpstreamError` tests for `context.Canceled` first (`internal/handler/handler.go:274-299`). It logs `client.disconnect` at warn level and records `499` with error code `client_closed_request` (`internal/handler/handler.go:301`). `reportUpstream` returns early on `context.Canceled` (`internal/handler/handler.go:303-305`), so a disconnect no longer raises a Sentry event.

**Read this as a classification change, not a new response.** artemis writes the `499` response body only when the request context is still live (`internal/handler/handler.go:281-283`). A caller that has genuinely hung up reads nothing. When the request context is already cancelled, artemis stamps `499` and `client_closed_request` on the access-log writer and writes no body (`internal/handler/handler.go:285-289`).

**Action:** update any alert, dashboard or log query that counts `502` as an upstream failure. Client aborts now land in `499`. Two consequences are specific to uploads. An aborted upload used to be access-logged as `200`, so an upload success-rate panel counted it as a success and will now show a new `499` band. And any query on the `deploy.upload.canceled` event has nothing left to match — move it to `client.disconnect`, the event every disconnect now emits.

## 3 — `finalize`, `promote` and `rollback` require a root `index.html`

**Release:** v1.6.3. Commit `90739ef`.

**Old:** no such check exists. `git grep missing_index v1.6.0` returns nothing. A deploy without a root `index.html` finalizes, promotes and rolls back. `docs/README.md` names that object as the one the serve plane requires for `/`, so the site goes live unservable.

**New:** all three verbs refuse the deploy with `422` and error code `missing_index`.

| Verb | Source | What it inspects |
| --- | --- | --- |
| `POST /api/deploy/{deployId}/finalize` | `internal/handler/deploy.go:215-223` | the `files` manifest in the request body |
| `POST /api/site/{site}/promote` | `internal/handler/site.go:137-146` | an R2 `HEAD` on `<deployPrefix>index.html` |
| `POST /api/site/{site}/rollback` | `internal/handler/site.go:231-240` | an R2 `HEAD` on `<deployPrefix>index.html` |

`promote` and `rollback` also gain a new failure: `502` with error code `r2_head_failed` when the `HEAD` itself errors (`internal/handler/site.go:139` and `internal/handler/site.go:233`). `finalize` cannot return `r2_head_failed`. It reads the manifest the client sent and issues no `HEAD`.

**The match is exact string equality.** `const rootIndexKey = "index.html"` (`internal/handler/deploy.go:313`). `hasIndexHTML` compares each manifest entry with `==` (`internal/handler/deploy.go:319-326`). `promote` and `rollback` `HEAD` the key `<deployPrefix>` + that same constant. Therefore:

- `Index.html` does not satisfy the check. The comparison is case-sensitive.
- `index.htm` does not satisfy the check.
- `sub/index.html` does not satisfy the check. Only the deploy root counts.

**`finalize` trusts the manifest.** An `index.html` that is present in R2 but absent from the `files` array still returns `422`. Send the complete manifest.

**Action:** emit a root `index.html` with exactly that name. Configure a static export for framework builds. On `finalize` the `422` body carries an advisory `hint` field when the manifest looks like a raw framework build directory rather than a static export (`internal/handler/deploy.go:216-219`).

**Client effect:** universe-cli maps `422` to exit code `13` (`EXIT_STORAGE`).

## 4 — `sha` at `deploy/init` must match `[A-Za-z0-9-]{1,64}`

**Release:** v1.6.4. Commit `949bbd8`. This is a security fix.

**Old:** `POST /api/deploy/init` requires only that `sha` is non-empty (`internal/handler/deploy.go:49-52` at `v1.6.0`). Values such as `a-b_c.d`, `a/b` and `..` are accepted.

**New:** a `sha` that does not match `^[A-Za-z0-9-]{1,64}$` returns `400` with error code `bad_request` and the message `sha must match [A-Za-z0-9-]{1,64}`. The check is at `internal/handler/deploy.go:55-58`. The pattern is at `internal/handler/site.go:34`.

**Why.** `h.NewDeployID(req.SHA)` embeds the value in the deploy id. The deploy id becomes an R2 key segment and a deploy-session JWT claim. At `v1.6.0` the create path validated nothing, while four consuming paths validated the resulting deploy id against `deployIDPattern`, `^\d{8}-\d{6}-[A-Za-z0-9-]{1,64}$` — delete (`internal/handler/deploy_delete.go:23`), restore (`internal/handler/deploy_restore.go:23`), promote (`internal/handler/site.go:68`) and rollback (`internal/handler/site.go:196`), all at `v1.6.0`. Upload and finalize validated nothing; they trust the JWT scope. A `sha` of `a/b` therefore minted a deploy id containing a `/` and uploaded objects under an extra key segment. Delete, restore and rollback then refused that deploy outright. Promote refused it only when the caller passed it explicitly: the gate reads `req.DeployID != "" && !deployIDPattern.MatchString(req.DeployID)` (`internal/handler/site.go:68` at `v1.6.0`). A legacy bare promote reads the preview alias and checks nothing (`internal/handler/site.go:110-128` at `v1.6.0`), so a slashed deploy id could still reach production. The unvalidated value broke the guarantee that a deploy id is a single key segment. R2 stores keys opaquely and applies no path normalisation, so this is key shaping, not filesystem traversal.

**Action:** send a git SHA, a git short SHA, or another value drawn from letters, digits and hyphens. Underscores, dots and slashes are the values most likely to break. A seven-character git short SHA is unaffected.

## 5 — A GitHub throttle surfaces as 429, not 401 or 503

**Release:** v1.8.0. Commits `a4c3abc` and `6b1ad31`.

artemis now classifies three response shapes as a throttle: `X-RateLimit-Remaining: 0`, a `403` carrying a `Retry-After` header, and a bare `429`. The source comment reads the first as GitHub's primary limit and the second as GitHub's secondary (abuse) limit, on which `X-RateLimit-Remaining` is typically non-zero (`internal/auth/github.go:525-530`).

**Old:** `isRateLimited` tests only `X-RateLimit-Remaining == "0"` (`internal/auth/github.go:504-506` at `v1.6.0`), and every call site pre-gates it on a `403` status (`internal/auth/github.go:173`, `:310`, `:418` at `v1.6.0`). The effective test is therefore `403 AND Remaining == "0"`. Neither a secondary throttle nor a `429` matches it.

**New:** `isRateLimited` matches all three signals (`internal/auth/github.go:531-539`), and the call sites drop the `403` gate (`internal/auth/github.go:173`, `:338`, `:448`).

```go
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	return resp.StatusCode == http.StatusForbidden && resp.Header.Get("Retry-After") != ""
}
```

**The old status depends on which GitHub call was throttled.** State it per path:

| GitHub response | artemis path | v1.6.0 | v1.9.1 |
| --- | --- | --- | --- |
| `403` + `Retry-After` | bearer validation (`GET /user`) | `401 unauthenticated` | `429 rate_limited` |
| `429` | bearer validation (`GET /user`) | `401 unauthenticated` | `429 rate_limited` |
| `403` + `Retry-After` | site authorization (`GET /orgs/…/teams/…/memberships/…`) | `503 upstream_unavailable` | `429 rate_limited` |
| `429` | site authorization | `503 upstream_unavailable` | `429 rate_limited` |

Bearer validation runs on every authenticated route. At `v1.6.0` a plain `403` returns `ErrGitHubUnauthenticated` (`internal/auth/github.go:176-178` at `v1.6.0`), and a `429` falls to the `default` branch as an unclassified error. `RequireGitHubBearer` maps both to `401 unauthenticated` — the typed error at `internal/handler/middleware.go:103-104`, the unclassified one at the `default` branch, `internal/handler/middleware.go:105-106`. That switch is byte-identical at both versions; only the classification feeding it changed.

Site authorization runs on `deploy/init`, `promote`, `rollback` and the other per-site verbs. `AuthorizeForSite` calls `IsTeamMember` at both versions (`internal/auth/github.go:493-504` at `v1.9.1`, `:468-479` at `v1.6.0`). The `IsTeamMember` switch carries no plain-`403` case at either version, so at `v1.6.0` both a secondary throttle and a `429` fall to `default` as an unclassified error. `writeGitHubProbeError` maps an unclassified error to `503 upstream_unavailable` (`internal/handler/handler.go:229-235`; `:222-228` at `v1.6.0`).

**The old `401` outlived the throttle.** At `v1.6.0` the `403` branch calls `cacheNegative`, which caches the failure for up to `negCacheCap`, 30 seconds (`internal/auth/github.go:223` and `:225-228` at `v1.6.0`). A caller kept receiving `401 unauthenticated` after GitHub had released the throttle. A rate-limit classification is never cached (`internal/auth/github.go:173-175`).

**Client effect.** universe-cli maps status to an exit code in `mapExitCode` (`src/lib/proxy-client.ts:302-305`). `401` and `403` yield `EXIT_CREDENTIALS`, 12. `429`, `422`, `0` and any `5xx` yield `EXIT_STORAGE`, 13 (`src/output/exit-codes.ts:4-5`). So a throttle during bearer validation changes the CLI exit code from 12 to 13. A throttle during site authorization keeps exit code 13, because `503` already mapped there.

**Action:** treat `429 rate_limited` as retryable with backoff. Stop treating `401` during a deploy as a definitely bad token.

## 6 — `promote`, `rollback` and `finalize` commit on a detached context

**Release:** v1.6.4. Commit `949bbd8`.

**Old:** `SitePromote` runs the site lock, the CAS read, the alias `PUT` and the index write on `r.Context()` (`internal/handler/site.go:85-146` at `v1.6.0`). Aborting the HTTP request cancels the alias `PUT`. A cancelled promote does not land.

**New:** the commit runs on a context detached from the request:

```go
commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(r.Context()), aliasCommitTimeout)
```

`internal/handler/site.go:89` for `promote`, `internal/handler/site.go:218` for `rollback`, `internal/handler/deploy.go:268` for the alias write in `finalize`. `aliasCommitTimeout` is 60 seconds (`internal/handler/deploy_delete.go:19`).

**Consequence:** the server may complete a promote that the client believes it cancelled. Once the request enters the locked commit, disconnecting no longer stops it. The trade is deliberate. A cancellation mid-commit could leave the R2 alias and the Postgres index disagreeing.

**Action:** never infer the alias state from a cancelled or timed-out request. Read `GET /api/site/{site}/alias/production` to learn where production points.

## 7 — GC audit rows key on the registry slug, not the storage dirname

**Release:** v1.9.0. Commit `762e6de`.

artemis carries two site keyspaces. A **slug** is the registry key. It names every URL, JWT claim and audit row. A **dirname** is the R2 storage key: the slug rendered through the site segment of `DEPLOY_PREFIX_FORMAT`. Production sets `DEPLOY_PREFIX_FORMAT=<site>.freecode.camp/deploys/<ts>-<sha>/`, so the dirname for slug `sudoku` is `sudoku.freecode.camp`.

**Old:** the GC auditors write whatever the GC layer hands them, which is the dirname (`cmd/artemis/gcwire.go:50-56` at `v1.6.0`). Rows for `gc.purge`, `gc.tombstone` and `gc.reconcile` therefore carry `audit_log.site = "sudoku.freecode.camp"`. The `gc.reconcile.prune` action, added after `v1.6.0`, behaved the same way until `v1.9.0`.

**New:** `auditSite` inverts the dirname back to the slug before the write (`cmd/artemis/gcwire.go:56-66`), using `DeployPrefixTemplate.SiteSlug` (`internal/handler/deploykey.go:72-87`). A dirname that the template could not have produced keeps its raw value and gains `detail.site_unmapped = true`, because `audit_log` is append-only and a dropped row can never be repaired.

**Do not overstate the change.** Rows written by request handlers were always slugs. Only the GC-emitted rows moved.

**There is no backfill.** `audit_log` is append-only and the database enforces it. The table carries `audit_log_no_update`, `audit_log_no_delete` and `audit_log_no_truncate` triggers, each calling `audit_log_reject_mutation()`. History therefore spans two keyspaces at the v1.9.0 boundary.

**Measured on production, read-only, 2026-08-21:**

- 105 of 7177 `audit_log` rows carry a dirname, 1.5 per cent.
- All 105 are GC rows: `gc.purge` 64, `gc.tombstone` 39, `gc.reconcile` 2.
- The newest is dated 2026-08-17. None is newer.
- No request-handler row carries a dirname.
- Every `gc.*` row in the table is still dirname-keyed. Zero slug-keyed GC rows exist yet. v1.9.1 is the running image, and GC has written no audit row since it was deployed. The slug side of the boundary is prospective.

**Action:** to read GC history from before the boundary, query `GET /api/audit?site=<slug>.freecode.camp` — the dirname — as well as `GET /api/audit?site=<slug>`. The same applies to `universe audit ls --site`. Expect the dirname form to stop appearing and the slug form to start.

## 8 — `CLEANUP_BLAST_CAP` gains a default, and an explicit `0` inverts

**Release:** v1.8.0. Commits `5b9bb0f` and `7a92fde`. This entry is for operators of deployments other than freeCodeCamp production. Production sets `CLEANUP_BLAST_CAP=10` explicitly, so the default and the inversion do not reach it.

**Default.** `v1.6.0` has no default. The field takes the Go zero value, `0`. `v1.9.1` defaults it to `10` — `defaultCleanupBlastCap = 10` (`internal/config/config.go:187`), applied at `internal/config/config.go:254`.

**Meaning of an explicit `0`.** The two versions read the same value in opposite directions.

| Version | `CLEANUP_BLAST_CAP=0` | Source |
| --- | --- | --- |
| v1.6.0 | no ceiling. Every planned delete runs. | `internal/gc/plan.go:17` at `v1.6.0` caps only when `blastCap > 0` |
| v1.9.1 | refuse. The whole destructive plan is dropped. | `internal/gc/plan.go:45-50` aborts when `blastCap <= 0` |

The validation message tracks the flip. `v1.6.0` reads `must be non-negative integer (0 disables)` (`internal/config/config.go:548`). `v1.9.1` reads `must be non-negative integer (0 refuses every destructive repair)` (`internal/config/config.go:574-575`).

**Scope widened.** `v1.6.0` applies the cap to site GC only (`internal/gc/gcsite.go:76` at `v1.6.0`). `v1.9.1` also applies it to tombstone purge (`internal/gc/tombstone.go:79-89`) and to reconcile repairs (`internal/gc/reconcile.go:217-237`).

**Action:** set `CLEANUP_BLAST_CAP` explicitly. An operator who set `0` to mean "no ceiling" now gets "delete nothing". An operator who never set it gets a ceiling of 10 rather than none.

## 9 — A purge whose registry row is already absent returns `200`, not `404`

**Release:** v1.10.0. Commit `b201e6a` (#46).

**Old:** `DELETE /api/site/{slug}?purge=true` ran `RecordSiteTombstone` (then named `RecordSitePurge`), then `MovePrefix`, then `Registry.Delete`. An absent registry row made the last call return `ErrNotFound`, and the handler answered `404` — *after* the destructive work had already landed. It also skipped the audit write, so the destruction left no `audit_log` row at all.

Proven on production 2026-08-21. A purge of `e2e-probe-20260513b` answered `404` while the alias rows went 2 to 0, the public URL went `200` to `404`, a tombstone landed, the deploy rows vanished, and `audit_log` recorded nothing.

An orphaned site has no registry row by definition, so this was the normal path for exactly the sites that need purging, not an edge case.

**New:** `ErrNotFound` from `Registry.Delete` satisfies the purge. The handler answers `200 {"slug","status":"purged","moved"}` and writes the audit row (`internal/handler/site_register.go`).

**Superseded by entries 19, 20 and 22.** The purge branch this entry describes is deleted; `DELETE` now refuses `?purge` outright. Entry 9 is kept as the record of what `v1.9.1` did. For current `DELETE` behaviour read entry 19, and entry 22 for the absent-row case.

**Action:** stop treating `404` from a purge as "nothing happened". On `v1.9.1` and earlier it meant the opposite.

## 10 — A purge can now fail with `r2_verify_failed` or `r2_move_incomplete`

**Release:** v1.10.0. Commit `b201e6a` (#46).

**Old:** `MovePrefix` copies and deletes one object at a time inside a 10-minute `destructiveMoveTimeout` (`internal/handler/deploy_delete.go:17`), which caps a purge at roughly 215 objects. Measured on production: `languagegames` moved 218 of 799 and stopped; `prd-with-scaffolding` moved 214 of 906. Because the alias objects sort after `deploys/`, a stalled move never reached them and **the site kept serving**.

**New:** two changes.

The two alias objects move first, so the site stops serving within the 15-second serve-cache TTL whatever its size. A stalled bulk move no longer leaves a public site.

After the bulk move the handler probes `HasPrefix(<dirname>/)`. A probe error answers `502 r2_verify_failed`; a prefix that still lists objects answers `502 r2_move_incomplete`. Neither reaches the `200` or the `Registry.Delete`.

**Consequence:** a large site may now need several `DELETE ?purge=true` calls. The operation is idempotent — `RecordSiteTombstone` is `ON CONFLICT DO UPDATE` (`internal/pg/repo.go:184-185`) and `MovePrefix` re-lists the source each time — so repeating the request resumes it. Each retry resets `trashed_at`, restarting the recovery clock.

**Action:** treat `502 r2_move_incomplete` as "call again", not as an error to escalate. Do not infer completion from a single `200` on a large site; the `moved` count in the body is authoritative for that call only.

**Superseded by entries 19 and 21.** The purge branch this describes is unreachable on 1.10.0, and entry 21 removed the size ceiling from `MovePrefix` itself.

## 11 — `audit_log.outcome` gains `failure`, and a failed purge is recorded

**Release:** v1.10.0. Commit `b201e6a` (#46).

**Old:** a purge wrote one audit row, and only when the whole sequence succeeded.

**New:** every purge that reaches R2 writes exactly one row. Either `outcome=success` with `detail.moved`, or `outcome=failure` with `detail.stage` and `detail.moved` naming what was already destroyed. `stage` is one of `tombstone`, `unpublish`, `move`, `verify`, `incomplete`, `registry`.

A purge refused before it reaches R2 — by the site lock, or by a missing tombstone store — changes nothing and writes no row.

**`outcome` is unconstrained `TEXT`** (`internal/pg/migrations/0006_audit_log.sql:8`), so no migration is needed, and `failure` is not the first non-`success` value: `repo.approve` already writes `approved_failed`. The only closed-set consumer is `DeployActors` (`internal/pg/audit.go:94`), which filters `action='deploy.finalize' AND outcome='success'` and is unaffected.

**Action:** any dashboard or query over `audit_log` that assumes `outcome='success'` needs a look. Filter explicitly rather than relying on a single value.

## 12 — `PATCH /api/site/{slug}` commits on a detached context

**Release:** v1.10.0. Commit `b201e6a` (#46). This completes entry 6.

**Old:** `SiteUpdate` was the only handler that took the per-site advisory lock on `r.Context()`. The `GetSite` read and the `UpdateTeams` write inside the lock ran on it too. A client disconnecting between the two cancelled the operation with the lock held.

**New:** the same pattern as entry 6 — `context.WithTimeout(context.WithoutCancel(r.Context()), aliasCommitTimeout)`, 60 seconds.

**Consequence:** as in entry 6, the server may complete an update the client believes it cancelled. The blast radius is smaller: this is a registry team-list write, not an R2 alias flip, so a cancelled update never left a site serving the wrong bytes.

**Action:** read the row back with `GET /api/sites` rather than inferring the team list from a cancelled request.

## 13 — A lock-release failure no longer fails the request it followed

**Release:** v1.10.0. Commits `469ce44`, `9202659`.

**Old:** the deferred unlock inside `lockSession.WithSiteLock` overwrote the named return whenever `pg_advisory_unlock` failed — `if err == nil { err = fmt.Errorf("site unlock %s: %w", …) }`. Seven endpoints read that error as "the work failed" before checking whether their own closure had already succeeded, so committed work could answer `502 site_lock_failed` with no audit row: `PATCH /api/site/{slug}`, `DELETE /api/site/{slug}?purge=true`, `DELETE /api/site/{site}/deploys/{id}`, `POST …/deploys/{id}/restore`, `POST …/promote`, `POST …/rollback` and `POST /api/deploy/{id}/finalize`. On the first four the closure had already written its own JSON body, so the response carried **two concatenated JSON objects**. On promote, rollback and finalize the alias was already in R2 and Caddy was already serving the new deploy while the caller was told `502`.

**New:** two changes, one per layer.

The advisory lock is session-scoped on a dedicated connection, and a failed unlock closes that connection (`internal/pg/lock.go:55-66`), which terminates the backend session and releases every lock it held. The unlock error is therefore informational and is now reported only through the existing `lock.site.unlock_failed` warn line. A non-nil return from `WithSiteLock` means either the closure ran and returned that error, or the closure never ran because acquisition failed.

`Handlers.withSiteLock` (`internal/handler/handler.go:163`) now returns the closure's own verdict whenever the closure ran, and the locker's error only when it did not. The `SiteLocker` interface cannot express the contract in its type, so the handler layer no longer depends on it.

**Consequence:** all seven endpoints return their documented success status, exactly one JSON body and exactly one audit row once the work commits. A `409 site_locked` on lock **acquisition** is unchanged. `DELETE ?purge=true` loses its bespoke `site.purge.unlock_failed` warn line; the generic `lock.site.unlock_failed` covers it.

**Action:** callers that treated `502 site_lock_failed` as "the operation definitely did not happen" were already wrong on the six broken paths and may stop compensating. Any client that tolerated a double JSON body on `PATCH`, `DELETE …/deploys/{id}` or `…/restore` can drop that tolerance.

## 14 — `finalize` retries its index write and audits a partial commit

**Release:** v1.10.0. Commit `9e81645`.

**Old:** `DeployFinalize` wrote the R2 marker, published the alias, then wrote the Postgres row once. A fault on that last leg answered `502 pg_write_failed` and wrote **no audit row at all**, leaving a marked and published deploy with no index row — the `reindex` class `drift-detect` reports and only `artemis reconcile --apply` repairs. `promote` and `rollback` had the identical R2-then-Postgres window, and nothing reconciles R2 aliases against the `aliases` table.

**New:** the Postgres leg of `finalize`, `promote` and `rollback` is retried up to `indexCommitAttempts` (3) times with a 150 ms backoff that doubles. Retrying is safe because both writes are idempotent end to end — `ON CONFLICT (site, id) DO UPDATE` for the deploy row and `ON CONFLICT (site, name) DO UPDATE` for the alias row (`internal/pg/saga.go:16-19`, `:48-52`). Worst-case added hold on the per-site advisory lock is 450 ms, against a `lock_timeout` of 30 s and a 60 s commit budget.

`finalize` also writes a `deploy.finalize` audit row with `outcome=failure` and `detail.stage` on every path that has already committed something to R2. `stage` is one of `registry`, `alias`, `index`. A `410 site_gone` commits nothing beyond the marker and is a caller error, so it writes no row.

**Consequence:** `audit_log` now carries `deploy.finalize` rows with `outcome=failure`. As in entry 11, `outcome` is unconstrained `TEXT` so no migration is needed, and the only closed-set consumer, `Repo.DeployActors`, filters `action='deploy.finalize' AND outcome='success'` (`internal/pg/audit.go:91-96`) and is unaffected. A retried commit whose acknowledgement was lost can enqueue a duplicate `site.changed` outbox row; `gc-site` is idempotent and keyed by site, so this is benign.

**Action:** any dashboard counting `deploy.finalize` rows must filter on `outcome`. A `502 pg_write_failed` from finalize now means the write failed three times, not once.

## 15 — `GET /readyz` with R2 unreachable returns `200` degraded, not `503`

**Release:** v1.10.0.

**Old:** an R2 probe failure returned `503` with `{"code":"r2_unreachable"}` and logged `readyz.probe.unavailable` at Error, tagged `op=r2.has_prefix`. Kubernetes removed the pod from the Service after three consecutive failures.

**New:** an R2 probe failure returns `200 {"ready":true,"degraded":true}` and logs `readyz.r2.degraded` at Warn (`internal/handler/readyz.go:67-75`). The pod stays in the Service. Valkey is now the only upstream that produces a `503`.

**Why:** all replicas share one bucket, one token and one endpoint, so an R2 fault is correlated across every pod and the `503` emptied the Service rather than shedding load onto a healthy replica. The endpoints that need R2 already fail per request with `r2_put_failed`, `r2_list_failed`, `r2_get_failed`, `r2_move_failed` and `r2_has_prefix_failed`, which name the failing operation; an empty Service returns a connection failure with no code, no request id and no audit row.

**Paging is unchanged.** R2 still reaches Sentry once per outage at `readyzPageThreshold` (3) consecutive failures, but the fingerprint moves from `[readyz r2.has_prefix]` to `[readyz r2.ping]`, so the existing Sentry issue stops receiving events and a new one opens on the first post-release R2 fault.

**Action:** an operator grepping `readyz.probe.unavailable` for R2 must switch to `readyz.r2.degraded`; that key is Warn, so `LOG_LEVEL=error` drops it entirely. A rollout no longer wedges on a broken R2 endpoint, a revoked token or a wrong bucket — new pods become Ready immediately — so the postdeploy check is now the gate on R2 credentials, not a confirmation of one.

## 16 — Background Sentry issues re-bucket by error class, and transient escalation is first-occurrence

**Release:** v1.10.0.

**This is an operational change, not an API one.** No status code, error code, validation rule, stored value or cancellation guarantee moves. The audience is the operator reading Sentry, not the API caller.

**Old:** `CaptureBackground` fingerprinted on the op alone — `scope.SetFingerprint([]string{op})` at `internal/observability/sentry.go:410`, and `[]string{op, "sustained"}` at `:402` for the escalated transient branch. Every cause on one op collapsed into a single Sentry issue.

**New:** the fingerprint is `{op, class}` for a non-transient error and `{op, "transient", class}` for an escalated transient, where `class` is a closed token drawn from `errorClass` (`internal/observability/errorclass.go`). The token set is the named classes — `ctx.canceled`, `ctx.deadline`, `grpc.canceled`, `grpc.deadline`, `grpc.<Code>`, `pg.in_recovery`, `pg.lock_timeout`, `pg.conn_closed`, `io.unexpected_eof`, `net.dns_temporary`, `net.dns_resolver`, `net.dns_notfound` — plus `pg.<SQLSTATE>` for any other Postgres error and `unclassified` for the rest. It is derived from the error class, never from the message, so hosts, ports, ids and durations cannot multiply the bucket count.

**This re-buckets existing issues and breaks continuity.** `ARTEMIS-5` and `ARTEMIS-7` stop receiving events and go stale; Sentry creates new issues under the new fingerprints and offers no redirect. `ARTEMIS-7` currently holds four unrelated shapes — 12 × `57P03`, 4 × `io.ErrUnexpectedEOF`, 1 × `connLockError`, and 18 × `net.DNSError` on 2026-08-23 alone (a further 3 landed in `ARTEMIS-8`) — and has been retitled twice while the defect never changed; Sentry's own Seer analysis read the merged issue as a result. After this release those become up to four issues. Resolve the old issues by hand rather than letting them age out.

**Tag rename:** `transient_sustained: "true"` is replaced by `transient: "true"` plus a new `error_class` tag carrying the token. Any saved search or alert rule filtering on `transient_sustained` stops matching.

**Escalation change:** an environmental background transient now creates an issue on its **first** occurrence, per pod, per op, per class, per 24 hours, instead of after three occurrences inside one process. The old counter could not measure a fleet-wide rate: workflows rotate across pods, so a fault failing once per pod never reached the threshold, and a rolling deploy reset the count. A `context.Canceled` or gRPC-`Canceled` transient now creates **no** issue at all — previously three inside 26 hours escalated one. A stuck process context is covered by pod-restart alerting, and the failure remains a `WARN` log line under the unchanged message `background.transient`, which reaches Sentry Logs at the live `LOG_LEVEL=info`.

**One formerly-silent case now pages, by name:** a Postgres `55P03` lock timeout raised by `gc-site` was pinned silent by an earlier wave (`cmd/artemis/gcworkflows_test.go:314`). It now raises one issue under `{gc.site.run, transient, pg.lock_timeout}`. Lock contention is a low-severity fault, not self-inflicted cancellation, so it does not qualify for the shutdown exemption; suppressing it outright would mean *sustained* contention never escalates, which is worse than the behaviour being replaced. If the operator wants it silent again, the lever is `shutdownClasses` in `internal/observability/errorclass.go` — not a return to a per-process counter.

**Expect a bounded spike on the first incident after release.** A Postgres StatefulSet replacement can now raise roughly one event per pod per op per class in the window — on the order of tens of events across around a dozen issues, against roughly zero today. That is the intended trade and it is self-limiting.

**Action:** expect new issue IDs for background failures. Re-point any alert rule, saved search or dashboard keyed on `ARTEMIS-5`, `ARTEMIS-7` or the `transient_sustained` tag. Enumerate those rules before the release, not after.

## 17 — Alias and deploy key formats are validated at boot, not at first use

**Release:** v1.10.0. Commits `46344c0`, `b22ce44`.

**Old:** `Config.validate()` checked only that `DEPLOY_PREFIX_FORMAT` carried both a `<site>` and a `<ts>`/`<sha>` token. The stricter structural rules lived in `cmd/artemis/gcwire.go` `aliasTails`, which runs only when GC wiring runs — and GC wiring is gated on Postgres being configured. A deploy-only instance with no `DATABASE_URL` therefore booted with a malformed key format and failed later, at the first operation that rendered a key, or never.

**New:** `internal/config/config.go` enforces the rules at `Load()`, before anything else starts, for `DEPLOY_PREFIX_FORMAT`, `ALIAS_PRODUCTION_KEY_FORMAT` and `ALIAS_PREVIEW_KEY_FORMAT`. A violation is a boot failure with a named variable and a reason. Four rules:

- The site segment — everything before the first `/` — must contain `<site>` (`config.go:526`). `sites/<site>/deploys/<ts>-<sha>/` is now refused, because its segment is the literal `sites`.
- The site segment must not contain `<ts>` or `<sha>` (`config.go:531`).
- Each alias format's site segment must equal `DEPLOY_PREFIX_FORMAT`'s, or the alias is unreachable for every site and a whole-site purge cannot find it.
- An alias format must name an object after its site segment. `<site>/` is refused, because it renders to the bare site prefix and the purge's unpublish stage would `MovePrefix` the whole site instead of one alias object.

**Consequence:** a configuration that previously booted and misbehaved now refuses to boot. That is the intended direction — the failure it prevents is a purge moving an entire site — but it is a behaviour change for anyone running a non-default format, and specifically for a deploy-only instance where the old check never ran at all.

Production's format is `<site>.freecode.camp/deploys/<ts>-<sha>/` with aliases `<site>.freecode.camp/production` and `/preview`, and all three pass every rule. Verified against the live `artemis-env` ConfigMap.

**Action:** if you run artemis outside freeCodeCamp production with a custom `DEPLOY_PREFIX_FORMAT` or alias format, check it against the four rules before upgrading. A boot failure names the variable and the rule it broke.

## 18 — DNS faults split into three error classes, and a non-NXDOMAIN resolver fault is now transient

**Release:** v1.10.0.

**This is an operational change, not an API one.** Like entry 16 it moves no status code, error code, validation rule, stored value or cancellation guarantee. The audience is the operator reading Sentry.

**Old:** `errorClass` split every `*net.DNSError` two ways on `Temporary()` — `net.dns_temporary` (transient) when `IsTimeout || IsTemporary`, and `net.dns` (**not** transient, so it pages on every event) for everything else. That draws the line where the Go standard library happens to put a flag, not where the remedy changes. A SERVFAIL answer becomes `errServerTemporarilyMisbehaving`, a `*temporaryError` (`net/dnsclient_unix.go:53,247`), so `newDNSError` sets `IsTemporary` (`net/net.go:686-714`) and the fault classifies transient. A REFUSED, NOTIMP or FORMERR answer becomes `errServerMisbehaving`, a plain `errors.errorString` (`net/dnsclient_unix.go:49,249`), so the same fault classifies permanent. **Both stringify as `server misbehaving`**, so two events with an identical Sentry title were classified oppositely, and nothing recorded in Sentry can tell an operator which of the two arrived.

**New:** three tokens, split on `IsNotFound` first (`internal/observability/errorclass.go`):

| Token | Shape | Transient? |
| --- | --- | --- |
| `net.dns_notfound` | NXDOMAIN — the name will not resolve however long you wait | no; pages on every event |
| `net.dns_temporary` | SERVFAIL, DNS timeout, and anything else the resolver marks temporary | yes (unchanged) |
| `net.dns_resolver` | every other DNS fault — REFUSED, NOTIMP, FORMERR, lame referral, unmarshalable response, socket error | **yes — this is the change** |

NXDOMAIN is the only DNS answer that names a configuration fault, so it keeps its own class and its own page. Everything else is a resolver-side or transport fault that a retry can clear.

**Consequence for grouping.** A non-NXDOMAIN resolver fault moves from fingerprint `{op, "net.dns"}` to `{op, "transient", "net.dns_resolver"}` and becomes subject to the 24-hour cooldown at `internal/observability/sentry.go:400`. Neither `registry.refresh` nor `relay.run` is in `cronShapedOps` (`internal/observability/sentry.go:381-388`), so the cooldown applies to both.

The cooldown is keyed per op and per class but the tracker is a package variable (`internal/observability/transientrate.go:37`), so its scope is one process — **one pod**. It suppresses repeats inside a pod, never across pods. The recorded morning of 2026-08-23 shows the size of that effect. Twenty-one DNS events arrived in two bursts, 05:50:58-05:51:11 and 06:03:19-06:03:30, each burst hitting all three replicas: eighteen under `op=relay.run` (ARTEMIS-7), three events per pod across six distinct pods, and three under `op=registry.refresh` (ARTEMIS-8), one per pod across three distinct pods. Under this release the `relay.run` repeats collapse to one per pod and the eighteen become six; the three `registry.refresh` events come from three different pods and stay three. Twenty-one becomes nine, not one.

The trade is symmetric and deliberate: a **sustained** resolver outage now pages less loudly than it does today. Scope the mitigation carefully, because it is narrower than it looks. A **resolver-wide** outage also fails the readyz Valkey probe, which answers 503 and ejects the pod from the Service (`internal/handler/readyz.go:57-62`), and pod alerting covers that. A **Postgres-only** name failure — which is exactly what ARTEMIS-8 records, `lookup artemis-postgresql` — does not: the Postgres branch only sets `degraded` and still answers 200 with the pod in the Service (`internal/handler/readyz.go:77-84`), and unlike the R2 branch at `:69-70` it captures nothing to Sentry at all. For that class the floor after this change is one cooled event per pod per 24 hours plus a `WARN` log, and nothing else pages. The one signal that survives is a pod restarted mid-outage, which crashloops at boot inside the 45-second connect window and is visible to pod alerting.

**Bucket moves.** NXDOMAIN opens a new issue under `net.dns_notfound`; any existing issue under `net.dns` goes stale and Sentry offers no redirect, exactly as in entry 16.

**Action:** re-point saved searches and alert rules keyed on `error_class:net.dns`. There is no longer a token by that name.

## 19 — `DELETE /api/site/{slug}` takes the site dark and reserves its name

**Release:** v1.10.0. Commit `bd2bcd0`. Implements steps 1-4 of `docs/design/0006-unpublish-is-not-reclaim.md`.

**This is the headline behaviour change of the release.**

**Old:** a `DELETE` without `?purge=true` removed the registry row and nothing else. The R2 alias objects survived, and because the serve plane is Caddy reading those objects directly and never consulting the registry, **the site kept serving**. Seven such orphans were found live on 2026-08-22 and purged by hand. The name also freed instantly, so the next claimant of the slug inherited the previous owner's production bytes.

**New:** a `DELETE` first removes both alias objects, then flips the registry row to `reserved` with an expiry of `SITE_RESERVATION_GRACE` (default 72h). The site is dark as soon as the aliases are gone — subject to the 15-second serve-cache TTL — and the name is held for the grace period rather than freed.

**Delete is not instant for assets.** The HTML goes dark inside the 15-second serve cache. Cloudflare caches non-HTML assets for up to 4h (`max-age=14400`), so an asset URL can still answer from an edge after the site is dark. The window self-heals and is accepted; no purge call is made. A caller that must prove a site is gone should check the HTML, not an asset. Since infra `ef71932d` (2026-09-02) the serve plane sends `Cache-Control: public, max-age=0, must-revalidate`, so the edge revalidates every request and the asset window is the 15-second serve cache only; see entry 29.

**The ordering is the safety property and it is pinned by a test.** Aliases are removed *before* the registry state flips, inside the per-site advisory lock. If alias removal fails the operation ABORTS: the site stays registered and published, which is visible and retryable. No ordering produces deregistered-and-still-serving, which is the failure the whole design exists to prevent. `TestSiteDelete_AliasFailureAbortsAndLeavesTheSiteRegistered` fails if the two steps are transposed.

**Status codes:** `204` on success, unchanged. A failure to remove an alias is `502 r2_delete_failed`. A failure to reserve keeps the existing registry-delete error mapping, except for the absent-row case in entry 22.

**`audit_log`:** `site.delete` now writes an `outcome=failure` row carrying `detail.stage`, one of `unpublish` or `reserve`. As in entries 11 and 14, `outcome` is unconstrained `TEXT` so no migration is needed.

**`undelete` refuses a reservation past its deadline.** `POST /api/site/{slug}/undelete` answers `404` once `reserved_until` has passed, rather than restoring the row. From that moment the nightly sweep owns the name: it trashes the origin bytes and then frees the row, and a restore landing between those two steps would return a site to service whose bytes were already moving. Both the refusal and the sweep's release compare against Postgres `now()`, not a Go clock, so the handler pod and the GC worker cannot disagree about the deadline.

**When the bytes are actually gone: roughly 11 days, not 3.** The grace period is only the first leg.

1. `DELETE` — the aliases go, the row is reserved, `reserved_until = now + 72h`. The origin bytes are untouched.
1. The 03:00 sweep, on the first run after the deadline — `reclaimSiteBytes` moves `<dirname>/` into `_trash/` and writes the tombstone, then frees the name.
1. The 03:00 `tombstone-purge`, `CLEANUP_RECOVERY_DAYS` (default 7) after `trashed_at` — the bytes are hard-deleted.

72h plus up to a day of sweep latency plus 7 days plus up to a day of purge latency is about 11 days end to end. The name is reusable after leg 2. Anyone reasoning about storage cost or a data-removal request must use the full chain, not the 72h figure.

**Steps 5, 6 and 7 land in entry 20.**

**Action:** any caller that relied on `DELETE` freeing the slug immediately must wait out the grace period or use the release path once step 7 ships. Any caller that relied on `DELETE` leaving the site serving was relying on the defect this fixes.

## 20 — `?purge=true` is retired; `POST /api/site/{slug}/undelete` is new

**Release:** v1.10.0. Completes `docs/design/0006-unpublish-is-not-reclaim.md`, steps 5-7.

**`?purge=true` no longer reclaims.** Once a delete unpublishes and reserves, the flag would have meant "skip the grace period and destroy the bytes now" — the one irreversible action in this design, sharing a URL and a permission with the safe form. A query parameter that silently escalates an operation from reversible to final cannot be read from a route table, and the two forms need different authorization: `REGISTRY_AUTHZ_TEAM` may delete, only `REPO_APPROVE_AUTHZ_TEAM` may release early. The flag is now **refused**: any true-ish value (`true`, `1`, `TRUE`, `t`, `yes`, `on`, or a bare `?purge`) answers `400 purge_retired` and performs no delete at all. Ignoring it was tried first and was worse — a `204` satisfies a caller who meant "make it dark" while lying to the caller who meant "reclaim the bytes", and only the second runs storage accounting and takedown compliance. Refusing lies to neither. An explicit `?purge=false` is an ordinary delete.

**This fails closed.** The destructive reading of the flag is the one that stops working, so a caller that sent it gets *less* destruction than it asked for, never more. `universe-cli` never sent it (`src/lib/proxy-client.ts:667-674`), so no shipped caller is affected.

The pre-ADR-0006 purge code is **deleted**, not merely unreachable. It had been left standing on the argument that no boot configuration produced the `Tombstones`-without-`Reservations` pair it needed — true, but it ran under `REGISTRY_AUTHZ_TEAM` while its replacement requires `REPO_APPROVE_AUTHZ_TEAM`, so any later change decoupling those two fields would have silently re-armed irreversible whole-site deletion for the weaker team. Removing it took its tests with it; `TestSiteDelete_LeavesEveryDeployByteInPlace` replaces the one that guarded surviving behaviour.

**Registering a reserved name is `409 site_reserved`, not `502`.** `internal/pg/registry.go:56` already returned `registry.ErrReserved`; the handler's error switch did not name it and fell through to a generic upstream failure. A caller can act on the difference between "someone holds this name for another two days" and "the registry is broken". This closes lifecycle gap E, where re-registering a deleted slug inherited the previous owner's live production bytes.

**`POST /api/site/{slug}/undelete`** returns a reserved name to its owner before the grace expires. Authz: `REGISTRY_AUTHZ_TEAM`. `200` with `{slug, prevProduction, prevPreview}` — the two alias pointers captured at delete time, so the caller knows what re-publishing would restore. `404` if the name is not reserved. Without this the grace period promised something nobody could deliver: `restore` is per-deploy and nothing restored a site.

**`audit_log`** gains `site.undelete` with `success` and `failure` outcomes.

**A reserved name is freed by the nightly `tombstone-purge` run.** `sweepExpiredReservations` (`cmd/artemis/reservationsweep.go`) selects names whose `reserved_until` has passed and deletes their registry row, releasing the slug. It runs inside the existing 03:00 UTC workflow, honours `CLEANUP_DRY_RUN`, is capped at 50 names per run and logs `reservation.sweep.capped` when it hits that cap. So the grace period is a ceiling as well as a floor: a name is held for `SITE_RESERVATION_GRACE` and then released, with no operator action.

**The same run reclaims the bytes.** Before freeing the name, the sweep records a tombstone for the site and moves everything remaining under `<dirname>/` into `_trash/<dirname>/`, which is what makes `tombstone-purge` responsible for collecting it. The order matters and is pinned: bytes first, name second. Freeing the name while its objects are still at the origin is exactly how a new owner inherits a stranger's site, and `TestRunSiteReclaim_KeepsTheNameWhenTheMoveFails` fails if the two are transposed.

Superseded from the release after `v1.10.2`: the nightly run only emits one `site.lifecycle` event per
expired name, and a separate workflow reclaims each site. Entry 33 has the current behaviour.

An origin-prefix move is only safe because the reservation has expired and the register path answers `409` for a reserved name, so the slug cannot have been re-claimed in the window. That guard is the precondition; do not lift this sweep out of the reservation flow and point it at arbitrary prefixes.

**Early release shipped.** `POST /api/site/{slug}/release` ends a reservation before its deadline and reclaims the bytes in the same order this sweep uses — tombstone, move, then free the name. It is gated on `REPO_APPROVE_AUTHZ_TEAM`, not the team that may delete; entry 24 has the full shape. The object-count ceiling that also stood here is closed by entry 21.

**Action:** drop `?purge=true` from any script — it is now refused with `400 purge_retired` and the delete does not happen, so a script that still sends it stops working rather than silently under-delivering. If you relied on it to reclaim immediately, the replacement is `POST /api/site/{slug}/release`, gated on `REPO_APPROVE_AUTHZ_TEAM` — see entry 24. Without an approver the name and bytes are held for `SITE_RESERVATION_GRACE` (default 72h).

## 21 — a large prefix move finishes in one call

**Release:** v1.10.0. Commit `d24258b`.

**Old:** `MovePrefix` copied and deleted one object at a time, serially, at roughly 0.36 objects per second. Inside the 10-minute `destructiveMoveTimeout` that is a ceiling near 215 objects. Measured on production: site `languagegames` moved 218 of 799 objects and `prd-with-scaffolding` 214 of 906. `DELETE /api/site/{site}/deploys/{deployId}` on a large deploy answered `502 r2_move_incomplete`, and the only way to finish was to repeat the call four or five times. The operation was idempotent and re-listed its source, so repeating worked — but nothing repeated it. The one caller that knew to repeat was a human reading a runbook.

**New:** each listing page moves through a bounded worker pool of 16. A 900-object site completes in one call. The bound is deliberate: unbounded fan-out would trade a slow purge for an R2 rate-limit outage, and `TestMovePrefix_FinishesASiteFarLargerThanOneSerialRunCould` fails under both mutations — set the limit to 1 and the concurrency assertion fails, remove the limit and the bound assertion fails.

**The `moved` count is unchanged in meaning.** It is the number of objects that completed both their copy and their delete, counted atomically. Audit rows and the `r2_move_incomplete` verdict read the same number they always did.

**Nightly reclaim benefits identically.** `reclaimSiteBytes` in the reservation sweep calls the same `MovePrefix`, so a reserved site's origin bytes now clear in one sweep pass rather than across nights.

**Action:** none. A caller that looped on `502 r2_move_incomplete` can keep the loop; it will exit on the first pass.

## 22 — `DELETE` on an orphaned alias answers `200`, not `404`

**Release:** v1.10.0.

**An orphaned alias is a name the serve plane answers with no registry row behind it** — the state entry 19 describes as the old defect, and the state entry 23's new `drift-detect` verdict reports. Seven were live on 2026-08-22.

**Old:** the `DELETE` removed both alias objects, then `Reserve` found no `sites` row and returned `ErrNotFound` (`internal/pg/reservation.go:24`). The handler answered `404 not_found` and wrote an `audit_log` row with `outcome=failure` and `detail.stage=reserve`. The public exposure was closed and the operator was told nothing happened — on exactly the sites `drift-detect` tells the operator to clear this way.

**New:** the handler probes each alias key before deleting it. If any alias served and `Reserve` then reports no row, the call answers `200 {"slug","status":"unpublished","reserved":false}` and writes `outcome=success` with `detail.orphan=true`.

**`reserved: false` is load-bearing.** No name is held, so `POST /api/site/{slug}/undelete` cannot bring that site back. That is correct — there was no owner to return it to.

**A name that nothing served and nothing registered still answers `404`,** which is what makes a second `DELETE` on the same orphan idempotent: the first cleared the aliases, so the second finds nothing to unpublish.

**Action:** treat `200` from a `DELETE` as success, and read `reserved` to decide whether `undelete` is available. Stop treating `404` as proof that nothing changed — on `v1.9.1` and earlier it could mean the opposite.

## 23 — an orphaned alias is a new `drift-detect` verdict

**Release:** v1.10.0.

**This is an operational change, not an API one.** The audience is the operator reading Sentry.

**New op:** the nightly 04:00 `drift-detect` run now enumerates site dirnames from the bucket and reports any alias key with no registry row as `drift.orphan_aliases`, at error level. Everything else in the sweep, and the whole reconciler, still enumerate from Postgres. The repair the event names is a staff `DELETE`, which behaves per entry 22.

**A found verdict outranks a partial read.** A read failure part-way through the scan no longer discards the orphans already proved: the scan continues and the verdict carries the read error alongside. `drift.unreadable` is now returned only when the scan found nothing AND could not see. Without this, one flaky R2 HEAD hid a live deregistered site until a night with no hiccup.

**Two new cron-shaped ops:** `drift.orphan_aliases` and `reservation.sweep` are added to `cronShapedOps` (`internal/observability/sentry.go`). As with `drift.sweep`, they escape the transient rate limiter and reach Sentry on every occurrence rather than once. A nightly job that fails silently after its first report is a job nobody trusts.

**Action:** expect a new Sentry issue shape under `op=drift.orphan_aliases`. Any alert rule enumerating background ops by name needs both new tokens added.

## 24 — `POST /api/site/{slug}/release` is new — approver-gated early reclaim

**Release:** v1.10.0. Implements ADR 0006 step 7b, the last open step.

**Old:** nothing freed a reserved name before its grace period expired. `?purge=true` had been the
operator's remedy and entry 20 retires it, so between entry 20 and this entry there was no path at
all — a DMCA takedown that must also free the name needed a manual `psql` write.

**New:** `POST /api/site/{slug}/release` ends the reservation immediately and reclaims the bytes in
the same order the nightly sweep uses: refuse anything that is not `state='reserved'`, record the
whole-site tombstone, move `<dirname>/` into `_trash/`, and free the registry row **last**.
`tombstone-purge` becomes responsible for the bytes either way and `CLEANUP_RECOVERY_DAYS` still
applies.

**Bytes first, name second — the rule entry 19 states for the sweep binds here too, and for a
sharper reason.** `POST /api/site/register` takes no site lock, so the only thing refusing a
concurrent claim on the slug is the reserved registry row still being there. Free the name before
the move and a new owner can register in the gap and then have their own freshly-uploaded bytes
swept into `_trash/` by the release still in flight — a window of minutes on a large site, not a
nanosecond. `TestSiteRelease_FreesTheNameOnlyAfterTheBytesAreTrashed` fails if the two are
transposed. `POST …/undelete` now takes the same site lock, so it cannot return an emptied site to
service mid-release.

**The authorization is deliberately not `REGISTRY_AUTHZ_TEAM`.** That team may delete, and a delete
is reversible for 72h through `undelete`. Release is not reversible. It is gated on
`REPO_APPROVE_AUTHZ_TEAM` — the same approvers as repo creation — so the irreversible form of the
operation needs a different, smaller set of people than the safe form.
`TestSiteRelease_RefusesACallerWhoMayOnlyDelete` fails if the two gates are swapped.

**Status codes:** `200 {"slug","status":"released","moved"}` on success. `400 invalid_slug`.
`403 user_unauthorized` for a caller outside the approver team. `404 not_found` when the slug has no
**reserved** row — an active site is not releasable, and the handler returns before it touches any
bytes. `502` on a tombstone or R2 failure — and on that path the name stays reserved, so the call is safe
to retry. `503 unavailable` on a deployment with no reservation store.

**`audit_log`:** one `site.release` row, `outcome=success` with `detail.moved`, or `outcome=failure`
with `detail.stage` of `state`, `disarm`, `tombstone`, `reclaim` or `release` naming how far it got.

**Action:** none for existing callers — the endpoint is purely additive and no shipped client calls
it. Operators handling a takedown should use it instead of a manual database write.

## 25 — a reserved site answers `409 site_reserved` on every authenticated site endpoint

**Release:** v1.10.0.

**Old:** at v1.9.1 a name had no reserved state. A slug that was not in the registry failed
authorization and every authenticated site endpoint answered `403 site_unauthorized`.

**New:** entry 19 makes `DELETE` reserve the name instead of freeing it, so a slug can now exist and
be unwritable. Entry 20 records the `409 site_reserved` that `POST /api/site/register` returns.
**That same fence applies to every other authenticated site endpoint, and entry 20 did not say so.**

`denyUnregisteredSite` and `writeFenceError` (`internal/handler/handler.go:217-239`) write
`409 site_reserved` — with `error.reservedUntil` in the body — or `410 site_gone`. They are reached
from deploy init (`internal/handler/deploy.go:65`), the deploy-session JWT middleware that fronts
upload and finalize (`internal/handler/middleware.go:147`), and `requireSiteAuthz`
(`internal/handler/site.go:366`), which fronts promote, rollback, deploys, deploy delete, deploy
restore, trash and alias. The in-lock fences in finalize, promote, rollback, restore and PATCH
repeat it against the authoritative row rather than the cached snapshot.

`410 site_gone` on promote and rollback is likewise newly reachable; at v1.9.1 those paths could not
return it.

**Why a caller feels this.** `universe-cli` maps HTTP status to an exit code
(`src/lib/proxy-client.ts:302-306`): `403` becomes `EXIT_CREDENTIALS` (12), `409` becomes
`EXIT_USAGE` (10). A CI job deploying to a site staff deleted an hour earlier used to exit 12, the
code a pipeline branches on to mean "rotate the token". It now exits 10. A pipeline that retries or
pages on 12 changes behaviour silently.

**Action:** treat `409 site_reserved` as "the name is held; ask the owner to `undelete`, or wait for
the grace to expire". Do not treat it as a credentials failure. Read `error.reservedUntil` for the
deadline.

## 26 — `GET /api/sites` omits reserved names unless `?state=reserved`

**Release:** v1.10.2.

**Old:** at v1.9.1 a `DELETE` removed the `sites` row, so a deleted site left the list immediately.
With entry 19 the row survives as `state='reserved'`, and the list returned it like any other site.

**New:** `GET /api/sites` returns active sites only. `GET /api/sites?state=reserved` returns the
held names instead, so an operator can still see what a delete is holding.

**Why this is not merely cosmetic.** The list carries `state` and `reservedUntil`, but
`universe-cli` 0.19.0's `sites ls` prints only SLUG, TEAMS, CREATED BY and CREATED AT and runs no
response validator, so the new fields are dropped. A deleted site was therefore
byte-for-byte indistinguishable from a live one — while `sites ls --mine` and `whoami` *did* drop
it, because those read the cached snapshot, which already filtered reserved rows. An operator
reading the unqualified list as authoritative would conclude the delete had not taken and issue it
again.

`?state=active` is accepted and is the default. Any other value answers `400 invalid_state` rather
than silently returning the active list.

**Action:** to see held names, pass `?state=reserved`. A client that wants both must make two calls.

## 27 — restoring a deploy whose bytes are gone answers `410`, not `200`

**Release:** v1.10.2.

**Old:** `POST /api/site/{slug}/deploys/{deployId}/restore` answered
`200 {"status":"restored","moved":0,"bytes":0}` whenever the tombstone row existed but the objects
under `_trash/` did not, and it inserted an active `deploys` row for a deploy with no bytes.
Promoting that deploy served an empty site.

**New:** when the move relocates nothing and the live prefix holds nothing, the handler answers
`410 already_purged` and writes no registry row. This is the same verdict the endpoint already
returned when the tombstone row itself was gone; it now also covers the case where the row outlived
its objects.

**Action:** treat `410 already_purged` as final — the bytes are unrecoverable. It was previously
reported as a successful restore.

## 28 — `DELETE` refuses a registered site whose alias it cannot read

**Release:** v1.10.2.

**Old:** the delete probed each alias with a HEAD, ignored a probe failure, and deleted the object
anyway. Entry 19's reservation then recorded `prev_production` from the `aliases` table.

**New:** the delete reads the alias **body**, because the serve plane reads the R2 alias object and
never consults Postgres — the object is the live pointer and the table is a shadow copy that
`SitePromote` and `SiteRollback` can leave stale when their R2 write succeeds and their Postgres
write then fails. The observed value is what the reservation stores, so `undelete` republishes what
the edge actually served.

If that read fails for a **registered** site, the handler now answers `502 r2_get_failed` with an
`audit_log` row `outcome=failure detail.stage=unpublish`, and deletes nothing. Destroying a pointer
it could not read would leave `undelete` to republish the stale table value with no copy of the
right one anywhere.

An **orphaned** alias — one with no registry row — is unchanged: entry 22 still applies, the
unreadable probe still reports `aliasProbe: "unreadable"` with `orphan: false`, and the exposure is
still closed.

**Action:** retry the delete. The site stays live and served until it succeeds.

## 29 — every alias write purges the Cloudflare edge for the host it moved

**Release:** withdrawn before release. Commits `4aed372` (takedown) and the alias-purge seam were removed on 2026-09-04.

**What it was:** each alias mutation (finalize, promote, rollback, delete, undelete) purged the site's
public host from the Cloudflare edge before answering, gated on `CLOUDFLARE_ZONE_ID` and
`CLOUDFLARE_API_TOKEN`.

**Why it went:** the serve plane closed the gap first. Caddy sends
`Cache-Control: public, max-age=0, must-revalidate` on every served object (infra `ef71932d`,
2026-09-02), so the edge revalidates each request against the origin and answers `REVALIDATED`, and
the browser revalidates too. A promote, rollback or takedown is visible on the next request. A purge
added nothing, and a Cloudflare outage would have held every alias write for up to 15 s.

**Action:** none. The two `CLOUDFLARE_*` variables are not read; drop them from any envelope that
carries them.

## 30 — an abandoned pending deploy is swept nightly, not only on the next site event

**Release:** unreleased. Commit `c9a4e97`.

**Old:** `gc-site` swept an expired pending deploy as ordinary retention, but it ran only on a
`site.changed` event and had no schedule. A site whose last activity WAS the abandoned deploy
emitted no further event, so its pending row sat indefinitely until a human ran `artemis reconcile`.
Two such rows were observed live on 2026-08-29, at 5d5h and roughly 3d. Worse, `reconcile`
classified the deploy as ownerless drift, because it reads `DeploysForSite`, which filters
`state = 'active'` and therefore cannot see a pending row at all.

**New:** an all-sites pending sweep joins the existing 03:00 UTC `tombstone-purge` cron, ahead of
the reservation sweep, and visits at most 50 sites per night. `reconcile` now consults a pending
reader and reports the new `tombstoneSkipPending` verdict instead of mistaking a live pending deploy
for drift. No new workflow and no new cron.

**Action:** none for API callers. Operators should expect an abandoned deploy to disappear within a
night of passing `CLEANUP_GRACE` rather than persisting until someone notices it.

## 31 — `drift-detect` alerts on one reclaimable deploy, not 25

**Release:** unreleased.

**Old:** the nightly `drift-detect` cron raised `drift.reclaimable` only once `reindex + tombstone`
reached 25 (`cmd/artemis/driftalert.go`). That number was chosen while production carried a standing
backlog of 36 reclaimable deploys, so the alert fired every night on the steady state and could not
detect a new orphan. Below 25 the cron logged `drift.clean` and raised nothing.

**New:** the threshold is 1. The 2026-08-29 reconcile drain took the backlog to zero, and the
nightlies of 2026-08-29, 08-30 and 08-31 each reported `reclaimable=0`, so the floor is measured
rather than assumed. Recent deploy churn is one to two per day and the busiest day on record is
twelve, so a single reclaimable deploy is an anomaly. The verdict still carries `Fails: false`: it
raises a Sentry event and logs `drift.detected` at ERROR, and it does not fail the cron run.

**Action:** operators tuning on `drift.reclaimable` should expect the alert to be rare and
actionable rather than nightly. Each event names the sites; run `artemis reconcile <site> --apply`
for each.

## 32 — an upload into a finalized deploy answers `409`, not `200`

**Release:** unreleased. Commits `575c6bb`, `0423bc2`.

**Old:** `PUT /api/deploy/{deployId}/upload` wrote unconditionally. It checked the deploy-session JWT,
that the deploy id matched the URL, and that the site was registered — never whether the deploy was
already finalized. Since the permit stays valid for its full TTL (15 minutes by default), the same
token could keep writing into the prefix the production alias now pointed at, and the serve plane
served those bytes. This contradicted tenet 5, "Deploys are immutable", and `drift-detect` could not
see it: reconcile classifies on presence, alias and age, never content.

**New:** `finalize` records the deploy as finalized in Valkey with a TTL equal to the JWT TTL, and an
upload into a finalized deploy answers `409 deploy_finalized` and writes nothing. Start a new deploy
instead. If artemis cannot read that record, the upload answers `503 fence_unavailable` rather than
writing — refusing is the safe side, because answering would reinstate exactly the overwrite this
check exists to prevent.

One limit stated plainly: an upload already in flight when `finalize` commits still lands. The upload
path takes no site lock, so a request that passed the check microseconds earlier completes normally.
The exposure is the duration of that one upload, not the remaining permit.

**Action:** a client that uploads after calling `finalize` must call `POST /api/deploy/init` for a
new deploy id. A CI pipeline that retries a whole deploy step should re-run `init`, not reuse the
previous permit.

## 33 — an expired reservation is reclaimed by a `site.lifecycle` run that writes a `site.reclaim` audit row

**Release:** unreleased.

**Old:** the nightly `tombstone-purge` cron reclaimed every expired reservation itself, in one
process, up to 50 names per night. It moved the site's bytes under `_trash/`, deleted the row, and wrote
no audit row. A reclaim that died mid-move left no record beyond a Sentry event, and the next night
started the same batch again from the top.

**New:** `tombstone-purge` only emits one `site.lifecycle` event per expired reservation, still at
most 50 per night, oldest expiry first, in one outbox transaction. A separate Hatchet workflow named
`site.lifecycle` reclaims one site per run: it claims the row by setting `sites.reclaim_started_at`,
takes the site lock, records the tombstone, moves the dirname's bytes under `_trash/`, and deletes the row and
writes the audit row in one transaction. Runs are serialised per slug and capped at four at a time.

Observable to a caller:

- `GET /api/audit` shows one `site.reclaim` row per reclaimed site, `actor=system:gc`,
  `outcome=success`, `detail.moved` and `detail.tombstoned`. A reclaim that fails writes no row and
  is retried the next night. `site.release` rows are unchanged.
- `sites.reclaim_started_at` is a new nullable column. A reserved row with the column set is a
  reclaim in flight, or one that failed and that the nightly sweep has not re-emitted yet (the claim TTL is 12 hours, so the sweep after the failure re-emits it). `POST /api/site/{slug}/undelete`
  refuses such a row with the same `404` it answers for an expired reservation.
- A reclaim that fails is retried once per night, so a site that keeps failing is N nights late,
  as before. A claim older than the 30-minute run budget is a stuck reclaim: `drift-detect` logs
  `drift.ledger` at ERROR and raises a Sentry event (`docs/design/0004-drift-detection-and-alerting.md`).

**Action:** an audit consumer that keys on `site.release` alone now misses nightly reclaims; add
`site.reclaim`. An operator who releases artemis around 03:00 UTC should release outside
02:55–03:35 UTC, because a worker still on the previous release can take that night's cron run and
reclaim without audit rows. Proof: `cmd/artemis/reservationsweep.go`, `cmd/artemis/reclaim.go`,
`internal/pg/reservation.go` (`ClaimReclaim`, `ReleaseReservationAudited`),
`internal/pg/migrations/0011_reclaim_claim.sql`.

## 34 — a deploy-session JWT without `exp` is rejected with `403 jwt_invalid`

**Release:** unreleased.

**Old:** the verifier checked the signature, the algorithm and the issuer. A token that carried no
`exp` claim passed every check and verified forever.

**New:** the verifier requires `exp`. A token without it answers `403 jwt_invalid` with the message
"invalid deploy-session jwt" on every deploy-session route. An expired token still answers `401
jwt_expired`.

**Action:** none for a client. artemis mints every deploy-session token itself and always sets `exp`,
so only a hand-built token is affected.

## 35 — `SENTRY_TRACES_SAMPLE_RATE=NaN` refuses to boot, not silently disables tracing

**Release:** unreleased.

**Old:** the range gate at boot was `rate < 0 || rate > 1`. Both comparisons are false for NaN, and
`strconv.ParseFloat` accepts `NaN` and `nan`, so the value passed the gate and reached the Sentry SDK.
The SDK then sampled no transaction and reported no error.

**New:** the gate checks `math.IsNaN` first, on the same error path. `Load()` fails with
`invalid SENTRY_TRACES_SAMPLE_RATE NaN: must be in [0,1]` and the pod does not start.

**Action:** none unless the variable is literally `NaN`. Unset it for the default, or set a number in
`[0,1]`. Source: `internal/config/config.go:511`.

## 36 — a failed outbox publish retries after 60 s, not 5 minutes, and the rest of the batch is not held

**Release:** unreleased. Commits `d2eba02` (backoff), `3df30de` (batch scope).

**Old:** the relay stamped a 5-minute claim on up to 100 rows before any publish and stopped at the
first error, so one gRPC fault froze the whole batch for 5 minutes with no log line.

**New:** a failed publish releases its row with a 60-second backoff (`internal/pg/outbox.go`,
`relayRetryBackoff`) and logs the failure; the other rows in the batch publish as normal.

**Action:** none for a caller. An alert rule that assumed a 5-minute silence after a relay fault
now sees a retry within a minute.
