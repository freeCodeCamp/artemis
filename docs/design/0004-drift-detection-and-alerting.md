# 0004 — Drift detection and alerting

Status: proposed Date: 2026-08-16 Supersedes the reconcile scheduler part of [0001](0001-durable-execution-model.md).

## Decision

The reconciler does not repair on a schedule. A read-only sweep finds drift and sends an alert. A person does the repair.

## Why

The reconcile cron ran every day at 04:00 UTC and repaired nothing. At retirement (2026-08-16) Postgres held zero `gc.reconcile` audit rows against 39 `gc.tombstone` rows from the retention GC; the first `gc.reconcile` rows (2, on 2026-08-17) came from the manual `artemis reconcile --apply` run that validated the human-run repair path. The cause was a keyspace error: the scheduler listed registry slugs (`test`), but the bytes are under storage dirnames (`test.freecode.camp`). Every sweep looked at a prefix that does not exist.

A read-only sweep of production on 2026-08-16 measured the real drift:

| Class           | Count | Meaning                                                      |
| --------------- | ----- | ------------------------------------------------------------ |
| tombstone       | 32    | Byte prefixes with no index row, past grace. Failed uploads. |
| reindex         | 5     | Complete byte prefixes whose index row is lost.              |
| prune           | 0     | Index rows that describe bytes that do not exist.            |
| aliased-missing | 0     | An alias points at a deploy that is lost.                    |

The two dangerous classes are empty. The two reclaimable classes are small and they do not grow fast. A daily job that repairs this is not worth its risk: it holds the R2 admin token, it deletes bytes, and its failure mode is the loss of a live site.

An alert has the opposite risk profile. It cannot delete anything. It tells a person when the edge case occurs. The person then repairs with a command.

## What changes

### Removed

- The `reconcile-scheduler` cron workflow.
- The `site.reconcile` event topic and its per-site fan-out.
- The event-triggered `reconcile` workflow that repaired each site.

### Added

- One cron workflow, `drift-detect`, at the same 04:00 UTC slot. It runs the same sweep as the `artemis driftreport` command. It cannot write.
- An alert policy. The job sends a Sentry event only when a person must act.
- One command, `artemis reconcile <site> --apply`, for the repair.

### Kept

- The full repair path in `internal/gc`. Its site lock, its in-lock re-checks, its grace gate, and its blast cap are the safest way to do the repair. A person starts it. A schedule does not.
- The `artemis driftreport` command, for the fleet view.
- The `gc-site` and `tombstone-purge` workflows. This change does not touch them.

## Why the job cannot write

A boolean flag does not make a job safe. `dryRun=true` is a request, and a later edit can pass `false`.

The `drift-detect` job holds a reconciler whose store and mover are read-only types. Every write method returns `errReadOnlyViolation`. Its locker is `nil`, because a read needs no lock. A repair from this job is not a bug that a review must catch. It does not compile into a mutation at all.

## Alert policy

The job sends one event when a person must act. It stays silent when it finds only the steady state. A daily alert that a person always ignores is worse than no alert.

| Condition                  | Action                    | Why                                                                                              |
| -------------------------- | ------------------------- | ------------------------------------------------------------------------------------------------ |
| `aliased-missing > 0`      | Sentry event, error level | An alias points at bytes that are lost. A live site is or will be broken.                        |
| A site cannot be read      | Sentry event + job fails  | An unread site is unknown drift. It is never zero drift.                                         |
| `orphan-aliases > 0`       | Sentry event, error level | An alias serves a name with no registry row. A deregistered site is on the public internet. Added 2026-08-25 with ADR 0006. |
| The sweep self-check fails | Sentry event + job fails  | The sweep read a keyspace that no write path makes. This catches the original bug if it returns. |
| Only reclaimable drift     | Sentry event at `reclaimableAlertThreshold` (1 since the 2026-08-29 drain; 25 before), else log the counts | Corrected 2026-08-24. This row read "Log the counts. No event" and the code has never matched it: `cmd/artemis/driftalert.go:20,45` sends an event at 25 or more. See the note below — the threshold is a known-wrong instrument, not a mismatch to be resolved by silencing the alert. |
| `stuck-reclaim > 0` (a reserved row holds a `reclaim_started_at` older than the 30m run budget) | Sentry event, error level, op `drift.ledger`, additive to the ranked verdict above | Added 2026-09-03 (T13). The run that claimed the row died before releasing it. Its bytes are tombstoned; the 03:00 sweep re-emits once the claim is older than 12h. The alert names slugs and ages and says to escalate when a slug repeats on a second night. |
| `overdue-reclaim > 0` (a reserved row expired more than 24h plus one full batch ago and carries no claim) | Sentry event, error level, op `drift.ledger`, additive | Added 2026-09-03 (T13). The sweep never emitted the row (cap of 50, `CLEANUP_DRY_RUN`, unwired reservations) or the engine never ran the event. Reads `sites` only; no outbox join, so a never-emitted row is caught. |
| The ledger query fails | joined into the read-failure branch: Sentry event + job fails | An unreadable ledger is unknown drift, the same rule as an unread site. |

**A found verdict outranks a partial read.** Both the `aliased-missing` and `orphan-aliases` rows join
any read error into their event and let it decide whether the job fails, rather than returning
`unreadable` and discarding what they found. One flaky R2 HEAD on site 200 must not hide the orphan
proved at site 3, because that orphan is a live deregistered site and the next clean night may be
days away. `unreadable` is the verdict only when the scan found nothing AND could not see — which is
genuinely unknown drift.

A run that the engine kills for outrunning its budget is a third case. The timeout cancels the handler context, so `withCheckIn` still runs and closes the check-in red (`gcworkflows.go:41`). The cancellation is transient, but `drift.sweep` is a cron-shaped op (`internal/observability/sentry.go:377`), so it escapes the transient rate limiter and reaches Sentry on every occurrence instead of once.

The Sentry cron monitor shows that the job ran. The event shows what the job found. The two signals stay separate: a job that runs correctly and finds a problem is a green check-in with an event, not a failed job.

## The run budget

The old design did the work in 70 short workflow runs, one for each site. This design does it in one run that reads every site. That run lists every object under every site prefix: 22,745 objects across 76 sites in the 2026-08-16 production sweep, which takes minutes.

The engine kills a task that runs longer than its timeout. The default is short. A sweep that inherits the default is a cron that fails every night, which is the failure this change exists to stop. The `drift-detect` task therefore sets an explicit 30-minute budget.

Watch the check-in duration in Sentry. It records how long each run takes. A duration that grows toward the budget means the fleet outgrew one run, and the sweep then needs shards.

## Follow-up, not in this change

- A threshold that alerts when the reclaimable count GROWS. **This note predicted the trap and the code walked into it.** The threshold shipped at 25 against a count of 37, which is exactly the "under that number" case named here, so the alert has fired every night since. Production has reported an identical 35 across five consecutive nights (2026-08-20 to 08-24), so the count is flat, not growing — the level says nothing and only a change would. A level threshold cannot express that; the fix needs a durable high-water mark, which is state this job does not have today. Tracked as `#64`, and deliberately sequenced after the one-off sweep in `#67` so the number is chosen against a post-sweep baseline rather than invented a second time.
- The alert's own wording claimed a trend it never measured. Until 2026-08-24 it read "storage is accruing faster than it is collected" while the count was flat. Corrected; an alert may state what the sweep saw and not what it infers.
- A repair for bytes under a site name that Postgres does not know. **Partly delivered 2026-08-25.** The orphan-alias phase enumerates site dirnames from the bucket, so an alias key with no registry row is now visible and reported. The bytes under such a prefix are still not reclaimed by any automated path; the reported repair is a staff `DELETE`, which unpublishes the name and answers 200. Everything else in the sweep, and the whole reconciler, still enumerate from Postgres.
