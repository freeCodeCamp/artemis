# Artemis — caller-visible behaviour changes, v1.6.0 → unreleased

Audience: a staff engineer who integrates with the artemis HTTP API, or who runs an artemis deployment other than freeCodeCamp production.

This file records behaviour that a caller can observe changing between two releases. It is not a feature list. Every entry names the release that shipped the change, the old observable, the new observable, and the source line that proves it.

## Why this file and not `CHANGELOG.md`

`CHANGELOG.md` is generated. release-please owns it: `release-please-config.json` declares the `simple` release type for the repository root, and `docs/RELEASING.md` describes the flow — release-please rewrites the file from Conventional Commit subjects on every release PR. A hand-written entry there is lost at the next release. Do not edit `CHANGELOG.md`.

The other two candidates document the present, not the delta. `README.md` is the project overview. `docs/README.md` is the reference for the surface as it stands today; its route table already prints `422 missing_index` as if it had always been there. Neither tells a caller what changed under them.

So this file is hand-maintained. Add an entry here whenever a change alters a status code, an error code, a validation rule, a stored value, or a cancellation guarantee.

## Scope

Range: `v1.6.0` (tagged 2026-07-17) through `v1.9.1` (tagged 2026-08-21), the release running in production on 2026-08-21, plus entries 9 to 12, which are committed and **not yet released**.

The audit that produced this file found no accidental breaks. Every entry below is intentional. Thirteen entries: seven change a response an API caller reads, one changes how a client disconnect is logged and metered, two change a value stored in the audit trail, one changes a cancellation guarantee, one changes operator configuration, and one changes how errors group in Sentry.

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
| 9 | A purge whose registry row is already absent returns `200`, not `404` | unreleased | API callers |
| 10 | A purge can now fail with `r2_verify_failed` or `r2_move_incomplete` | unreleased | API callers |
| 11 | `audit_log.outcome` gains `failure`, and a failed purge is recorded | unreleased | Audit-trail readers |
| 12 | `PATCH /api/site/{slug}` commits on a detached context | unreleased | API callers |
| 13 | Background Sentry issues re-bucket by error class | unreleased | Sentry and alert-rule readers |

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

**Release:** unreleased. Commit `ff32268`.

**Old:** `DELETE /api/site/{slug}?purge=true` ran `RecordSitePurge`, then `MovePrefix`, then `Registry.Delete`. An absent registry row made the last call return `ErrNotFound`, and the handler answered `404` — *after* the destructive work had already landed. It also skipped the audit write, so the destruction left no `audit_log` row at all.

Proven on production 2026-08-21. A purge of `e2e-probe-20260513b` answered `404` while the alias rows went 2 to 0, the public URL went `200` to `404`, a tombstone landed, the deploy rows vanished, and `audit_log` recorded nothing.

An orphaned site has no registry row by definition, so this was the normal path for exactly the sites that need purging, not an edge case.

**New:** `ErrNotFound` from `Registry.Delete` satisfies the purge. The handler answers `200 {"slug","status":"purged","moved"}` and writes the audit row (`internal/handler/site_register.go`).

**The bare `DELETE` is unchanged.** Without `?purge=true` an absent slug still returns `404`.

**Action:** stop treating `404` from a purge as "nothing happened". On `v1.9.1` and earlier it meant the opposite.

## 10 — A purge can now fail with `r2_verify_failed` or `r2_move_incomplete`

**Release:** unreleased. Commit `ff32268`.

**Old:** `MovePrefix` copies and deletes one object at a time inside a 10-minute `destructiveMoveTimeout` (`internal/handler/deploy_delete.go:17`), which caps a purge at roughly 215 objects. Measured on production: `languagegames` moved 218 of 799 and stopped; `prd-with-scaffolding` moved 214 of 906. Because the alias objects sort after `deploys/`, a stalled move never reached them and **the site kept serving**.

**New:** two changes.

The two alias objects move first, so the site stops serving within the 15-second serve-cache TTL whatever its size. A stalled bulk move no longer leaves a public site.

After the bulk move the handler probes `HasPrefix(<dirname>/)`. A probe error answers `502 r2_verify_failed`; a prefix that still lists objects answers `502 r2_move_incomplete`. Neither reaches the `200` or the `Registry.Delete`.

**Consequence:** a large site may now need several `DELETE ?purge=true` calls. The operation is idempotent — `RecordSitePurge` is `ON CONFLICT DO UPDATE` (`internal/pg/repo.go:183-184`) and `MovePrefix` re-lists the source each time — so repeating the request resumes it. Each retry resets `trashed_at`, restarting the recovery clock.

**Action:** treat `502 r2_move_incomplete` as "call again", not as an error to escalate. Do not infer completion from a single `200` on a large site; the `moved` count in the body is authoritative for that call only.

## 11 — `audit_log.outcome` gains `failure`, and a failed purge is recorded

**Release:** unreleased. Commit `ff32268`.

**Old:** a purge wrote one audit row, and only when the whole sequence succeeded.

**New:** every purge that reaches R2 writes exactly one row. Either `outcome=success` with `detail.moved`, or `outcome=failure` with `detail.stage` and `detail.moved` naming what was already destroyed. `stage` is one of `tombstone`, `unpublish`, `move`, `verify`, `incomplete`, `registry`.

A purge refused before it reaches R2 — by the site lock, or by a missing tombstone store — changes nothing and writes no row.

**`outcome` is unconstrained `TEXT`** (`internal/pg/migrations/0006_audit_log.sql:8`), so no migration is needed, and `failure` is not the first non-`success` value: `repo.approve` already writes `approved_failed`. The only closed-set consumer is `DeployActors` (`internal/pg/audit.go:94`), which filters `action='deploy.finalize' AND outcome='success'` and is unaffected.

**Action:** any dashboard or query over `audit_log` that assumes `outcome='success'` needs a look. Filter explicitly rather than relying on a single value.

## 12 — `PATCH /api/site/{slug}` commits on a detached context

**Release:** unreleased. Commit `ff32268`. This completes entry 6.

**Old:** `SiteUpdate` was the only handler that took the per-site advisory lock on `r.Context()`. The `GetSite` read and the `UpdateTeams` write inside the lock ran on it too. A client disconnecting between the two cancelled the operation with the lock held.

**New:** the same pattern as entry 6 — `context.WithTimeout(context.WithoutCancel(r.Context()), aliasCommitTimeout)`, 60 seconds.

**Consequence:** as in entry 6, the server may complete an update the client believes it cancelled. The blast radius is smaller: this is a registry team-list write, not an R2 alias flip, so a cancelled update never left a site serving the wrong bytes.

**Action:** read the row back with `GET /api/sites` rather than inferring the team list from a cancelled request.

## 13 — Background Sentry issues re-bucket by error class, and transient escalation is first-occurrence

**Release:** unreleased.

**This is an operational change, not an API one.** No status code, error code, validation rule, stored value or cancellation guarantee moves. The audience is the operator reading Sentry, not the API caller.

**Old:** `CaptureBackground` fingerprinted on the op alone — `scope.SetFingerprint([]string{op})` at `internal/observability/sentry.go:410`, and `[]string{op, "sustained"}` at `:402` for the escalated transient branch. Every cause on one op collapsed into a single Sentry issue.

**New:** the fingerprint is `{op, class}` for a non-transient error and `{op, "transient", class}` for an escalated transient, where `class` is a closed token drawn from `errorClass` (`internal/observability/errorclass.go`). The token set is the named classes — `ctx.canceled`, `ctx.deadline`, `grpc.canceled`, `grpc.deadline`, `grpc.<Code>`, `pg.in_recovery`, `pg.lock_timeout`, `pg.conn_closed`, `io.unexpected_eof`, `net.dns_temporary`, `net.dns` — plus `pg.<SQLSTATE>` for any other Postgres error and `unclassified` for the rest. It is derived from the error class, never from the message, so hosts, ports, ids and durations cannot multiply the bucket count.

**This re-buckets existing issues and breaks continuity.** `ARTEMIS-5` and `ARTEMIS-7` stop receiving events and go stale; Sentry creates new issues under the new fingerprints and offers no redirect. `ARTEMIS-7` currently holds four unrelated shapes — 12 × `57P03`, 4 × `io.ErrUnexpectedEOF`, 1 × `connLockError`, 1 × `net.DNSError` — and has been retitled twice while the defect never changed; Sentry's own Seer analysis read the merged issue as a result. After this release those become up to four issues. Resolve the old issues by hand rather than letting them age out.

**Tag rename:** `transient_sustained: "true"` is replaced by `transient: "true"` plus a new `error_class` tag carrying the token. Any saved search or alert rule filtering on `transient_sustained` stops matching.

**Escalation change:** an environmental background transient now creates an issue on its **first** occurrence, per pod, per op, per class, per 24 hours, instead of after three occurrences inside one process. The old counter could not measure a fleet-wide rate: workflows rotate across pods, so a fault failing once per pod never reached the threshold, and a rolling deploy reset the count. A `context.Canceled` or gRPC-`Canceled` transient now creates **no** issue at all — previously three inside 26 hours escalated one. A stuck process context is covered by pod-restart alerting, and the failure remains a `WARN` log line under the unchanged message `background.transient`, which reaches Sentry Logs at the live `LOG_LEVEL=info`.

**Expect a bounded spike on the first incident after release.** A Postgres StatefulSet replacement can now raise roughly one event per pod per op per class in the window — on the order of tens of events across around a dozen issues, against roughly zero today. That is the intended trade and it is self-limiting.

**Action:** expect new issue IDs for background failures. Re-point any alert rule, saved search or dashboard keyed on `ARTEMIS-5`, `ARTEMIS-7` or the `transient_sustained` tag. Enumerate those rules before the release, not after.
