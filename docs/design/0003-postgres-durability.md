# Local design 0003 — Postgres durability options (I3)

> **Status:** Scoring note (2026-08-15) · **Decision:** none — the operator picks an option; the migration itself is out of scope for the `artemis-audit-fixes` wave and becomes its own dossier. **Probed live state (2026-08-15, read-only):** one `artemis-postgresql-0` pod, 70 d uptime, 10 Gi `local-path` PVC (RWO, node-pinned), on a 3-node k3s cluster (`gxy-vm-management-k3s-{1,2,3}`); nightly `artemis-backup` CronJob (`0 2 * * *`) runs `pg_dumpall` and rclones `artemis-<ts>.sql.gz` to R2 under `artemis/${GALAXY}/`.

## 1. What the database carries, and what loss means

One Postgres instance carries two tenant DBs:

- **`artemis`** — deploy index, aliases, outbox, tombstones, `audit_log`, sites registry, repo queue.
- **`hatchet`** — durable-execution state, including the `v1_*_olap` run history.

Loss is NOT a serving outage: the serve plane (Caddy `r2_alias` → R2) never touches Postgres (local ADR 0001 §3, §8). Loss means: no new deploys/GC until restore, a rebuildable deploy index (`BACKFILL_ON_BOOT` re-scans R2), an **unrebuildable `audit_log`** (the forensic system-of-record), and lost in-flight Hatchet runs. The `audit_log` sets the durability requirement.

## 2. Failure modes the current setup does and does not cover

| Failure                                             | Covered today?                                                                        |
| --------------------------------------------------- | ------------------------------------------------------------------------------------- |
| Pod restart, node reboot (same node)                | yes — PVC remounts                                                                    |
| Data corruption / bad migration noticed within 24 h | partial — restore to last 02:00 dump, losing up to 24 h of writes                     |
| **Node loss** (local-path PVC is node-pinned)       | **no** — PVC is unschedulable elsewhere; restore from dump onto a new PVC, RPO ≤ 24 h |
| Disk loss on the node                               | no — same as node loss                                                                |
| R2 bucket loss (backup target)                      | out of scope here — R2 is the platform's own durability domain                        |

RPO today: up to 24 h. RTO today: manual — new PVC + `psql < dump` + repoint — against a stated floor of \<= 60 min. The restore leg **is** rehearsed: `infra:docs/runbooks/08-artemis-pg-restore-drill.md` records the R8 drill PASSED on 2026-06-05, restoring the newest R2 dump into a scratch Postgres with both tenants back and 6/6 artemis tables present. What is not rehearsed is the StatefulSet re-provision that precedes it; that is the remaining wall-time inside the 60 min (runbook 08 §F).

## 3. Options scored

Scale context from local design 0002: at the 10k-site target the control-plane DB stays small (3 M deploy rows ≈ single-digit GiB); durability, not capacity, is the constraint.

| #   | Option                                                                    | RPO                                     | RTO                         | Op burden                                                                     | Fit                                                                                                                 | Score                         |
| --- | ------------------------------------------------------------------------- | --------------------------------------- | --------------------------- | ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- | ----------------------------- |
| A   | Status quo (nightly `pg_dumpall` → R2)                                    | ≤ 24 h                                  | hours, manual               | none new                                                                      | already running                                                                                                     | 2/5                           |
| B   | A + WAL archiving / PITR (e.g. `wal-g` or `pgBackRest` → R2)              | minutes                                 | ≤ 1 h, semi-manual          | one sidecar + bucket lifecycle; restore drill needed                          | strong — R2 already the backup target; no new infra primitive                                                       | **4/5**                       |
| C   | Streaming replica on a second node (operator-managed, e.g. CloudNativePG) | ~0                                      | minutes, automated failover | adopt an operator; chart rewrite; the current Bitnami StatefulSet is replaced | strong at steady state, largest migration step                                                                      | 3/5                           |
| D   | Longhorn/replicated storage under the existing StatefulSet                | node-loss survives; corruption does not | minutes                     | new storage layer on k3s; performance tax on etcd-colocated nodes             | weak — replicates the block device, not the database; corruption replicates too                                     | 1/5                           |
| E   | Managed Postgres (off-cluster)                                            | provider SLO                            | provider SLO                | least ops                                                                     | conflicts with the locked operator constraint "self-hosted OSS only, artemis owns its own data" (local ADR 0001 §1) | 0/5 — ruled out by constraint |

## 4. Recommendation to the operator

**B first, C later if warranted.** PITR closes the real gap (24 h RPO on an unrebuildable `audit_log`) with the smallest operational delta, reusing R2 and the existing backup credentials. C (CloudNativePG) is the right end-state if the platform later needs minutes-level RTO or wants the `hatchet` tenant isolated, but it replaces the whole chart and deserves its own wave with a rehearsed cutover. Whichever option is picked, the first task of that wave is a **re-run of the restore drill against the current schema**. The drill itself is not missing — `infra:docs/runbooks/08-artemis-pg-restore-drill.md` passed on 2026-06-05 (§2) — but its §D gate asserts six tables (`deploys`, `aliases`, `tombstones`, `outbox`, `sites`, `repo_requests`), which was the entire schema on that date. `audit_log` arrived a month later in `internal/pg/migrations/0006_audit_log.sql` (commit `025cc73`, 2026-07-06). The one table §1 calls unrebuildable is the one table the drill has never asserted. §5 says what the gate needs.

Non-goals here: multi-region, connection pooling, capacity scaling (0002 covers capacity).

## 5. What the restore drill must add before it is re-run (handover to infra)

`infra:docs/runbooks/08-artemis-pg-restore-drill.md` lives in a sibling repo and is not edited from here. This section is the change list, with the evidence for each item. Probed read-only against `artemis-postgresql-0` on 2026-08-24.

**(a) The artefact is not the gap.** The backup CronJob runs `pg_dumpall --clean --if-exists | gzip` with no `--exclude-table` and no per-database selection (infra chart `artemis/templates/backup-cronjob.yaml:52-56`). It is a full cluster dump, so `audit_log` is inside today's artefact. Every item below is an assertion the drill does not make — not a hole in what gets backed up.

**(b) Six tables becomes seven.** Runbook `08:118` names the artemis-owned set and `08:139` asserts it; both go from six to seven with `audit_log` added, and the header claim at `08:5` goes from `6/6` to `7/7`. The drill was **complete when it ran** — `audit_log` postdates it by a month — so this is staleness, not a past failure.

**(c) `audit_log` needs a row-count assertion, not a presence assertion.** The drill prints `count(*)` for all six tables (`08:126-133`) but asserts a count for only one of them, `sites` (`08:140`). The remaining five, and `audit_log` when it is added, pass on presence alone — a restored-but-empty table clears that gate. `audit_log` held 7257 rows live at the probe. It is the table §1 names as unrebuildable, so it is the one that most needs the count.

**(d) The drill must assert the append-only guards came back.** `audit_log` carries three triggers (`0006_audit_log.sql:16-29`, all three confirmed live):

```sh
kubectl -n artemis exec "$SCRATCH" -- psql -U postgres -d artemis -At -c \
  "SELECT tgname FROM pg_trigger WHERE tgrelid='audit_log'::regclass AND NOT tgisinternal ORDER BY 1"
# expect: audit_log_no_delete, audit_log_no_truncate, audit_log_no_update
```

A restore that replays every row but loses the triggers passes the presence gate and the row-count gate in (c), and leaves the forensic system-of-record silently **mutable**. No gate above can see that.

**(e) `schema_migrations` is a restore gate, not bookkeeping.** `0006_audit_log.sql:1` is a bare `CREATE TABLE audit_log` with no `IF NOT EXISTS`, unlike `0001`-`0003`. A restore that brings the tables back but loses the migration ledger makes the next artemis boot re-run `0006`, `pg.Migrate` returns an error, `openPostgres` fails (`cmd/artemis/main.go:382-386`) and the pod does not start. The drill must assert `schema_migrations` restored with its nine rows, `0001_init.sql` through `0009_outbox_claim.sql` (nine confirmed live).

**(f) The root fix for the staleness.** Derive the expected table set from `internal/pg/migrations/` at drill time instead of hard-coding it in the runbook. Migration `0006` staled this gate silently for two months; `0010` will do the same to whatever list replaces it.
