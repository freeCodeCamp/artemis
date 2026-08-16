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

RPO today: up to 24 h. RTO today: manual — new PVC + `psql < dump` + repoint; unrehearsed (unverified — no restore drill is recorded anywhere in this repo or the infra runbooks).

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

**B first, C later if warranted.** PITR closes the real gap (24 h RPO on an unrebuildable `audit_log`) with the smallest operational delta, reusing R2 and the existing backup credentials. C (CloudNativePG) is the right end-state if the platform later needs minutes-level RTO or wants the `hatchet` tenant isolated, but it replaces the whole chart and deserves its own wave with a rehearsed cutover. Whichever option is picked, the first task of that wave is a **restore drill** — today's dump path has never been proven end-to-end (unverified, and that is itself the finding).

Non-goals here: multi-region, connection pooling, capacity scaling (0002 covers capacity).
