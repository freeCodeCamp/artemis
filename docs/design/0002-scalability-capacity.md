# Local design 0002 -- Scalability posture + capacity envelope (R14)

> **Status:** Measured baseline (2026-06-04) -- numbers below come from the gated load harness `cmd/loadgen` (build tag `load`) and direct PG/Valkey probes, NOT from synthetic estimates. Re-run `just loadgen` to refresh. **Invariant:** R14 (dossier `artemis-durable-exec-cutover`) -- per-site Hatchet concurrency bounds fan-out at target scale (10k sites -> millions of historical deploys); PG pool + Valkey cache sized + documented. **Engine bound cite:** `cd3b012` (T12 real-Hatchet integration suite).

______________________________________________________________________

## 1. Target scale

Local ADR 0001 fixes the design target at **10s of thousands of sites -> millions of deploys** (Vercel/Netlify-class). For capacity planning this doc uses a concrete reference point:

- **10,000 sites**
- **300 deploys/site retained in the index** (active + recently tombstoned) -> **3,000,000 deploy rows**

The serve plane (Caddy + R2) is untouched by artemis scale -- every site keeps serving from R2 even when PG/Hatchet are down (ADR 0001 sections 3 and 8). This doc covers only the control plane: PG metadata, the outbox relay, the Hatchet worker fan-out, and the Valkey registry cache.

## 2. How the numbers were measured

`cmd/loadgen` drives the real store code paths -- `pg.RegistryStore.Register`, `pg.Repo.UpsertDeploy`, `pg.Repo.EnqueueSiteChanged`, `worker.Relay.RunOnce`, and `gc.SiteGC.Run` (dry-run, no-op Mover) -- against a throwaway Postgres and reports per-stage throughput and latency percentiles as JSON. The R2 Mover and the Hatchet Publisher are stubbed because they are not the control-plane scale bound (R2 is the serve plane; the per-site engine bound is covered separately in section 5).

Reference run environment:

- Host: darwin/arm64, `NumCPU=10`, Go 1.26.3
- PG: `postgres:17-alpine` (PostgreSQL 17.10), `max_connections=200`, `shared_buffers=256MB`, single container
- Workload: `-sites 500 -deploys-per-site 40` = **20,000 deploy rows**

This is a laptop single-node Postgres over the Docker network loopback, NOT the bundled production StatefulSet. Treat the absolute throughput as a conservative floor (production PG on real disk with a warm cache does better); treat the **ratios and the per-row sizing as the portable result**.

## 3. Measured throughput

Two runs, default pool vs an enlarged pool, both at 20,000 deploy rows, zero errors across every stage:

| stage            | default pool (10 conns, 16 goroutines) | enlarged pool (25 conns, 24 goroutines) |
| ---------------- | -------------------------------------- | --------------------------------------- |
| `register`       | 3,895 ops/s                            | 4,937 ops/s                             |
| `deploy_upsert`  | 5,760 ops/s (20k rows in 3.47s)        | 10,309 ops/s (20k rows in 1.94s)        |
| `outbox_enqueue` | 4,232 ops/s                            | 7,426 ops/s                             |
| `relay_drain`    | 39,869 rows/s (batch 100)              | 58,095 rows/s (batch 100)               |
| `gc_plan_dryrun` | 10,266 sites/s                         | 13,944 sites/s                          |

Latency at the default pool (microseconds):

| stage            | p50   | p99   | max    |
| ---------------- | ----- | ----- | ------ |
| `register`       | 3,723 | 9,738 | 12,566 |
| `deploy_upsert`  | 2,611 | 7,343 | 27,705 |
| `outbox_enqueue` | 3,347 | 8,274 | 9,336  |
| `relay_drain`    | 1,612 | 3,370 | 4,215  |
| `gc_plan_dryrun` | 1,388 | 4,355 | 4,920  |

### What this means for the target scale

- **Backfill of 3M deploy rows** (one-shot `BACKFILL_ON_BOOT`) at the measured default-pool `deploy_upsert` rate of 5,760 rows/s = **~8.7 minutes**; at the enlarged-pool rate of 10,309 rows/s = **~4.9 minutes**. Backfill is a single cold run, so even the conservative figure is acceptable. Formula: `seconds = rows / upsert_ops_per_sec`.
- **Steady-state event fan-out:** every site mutation enqueues one outbox row, drained by the relay every 5s (`relayInterval`, `cmd/artemis/gcworkflows.go`). A 100-row batch drains in ~2.5ms (39,869 rows/s); even a burst of 10,000 queued events drains in `10000 / 39869 = ~0.25s` of relay work, far inside one tick. The relay is never the bottleneck at this scale.
- **GC sweep of 10,000 sites** (the scheduled per-site pass) plans at 10,266 sites/s = **under 1s** of planning work for the whole fleet. Actual reclaim is gated by R2 MovePrefix latency and `CLEANUP_BLAST_CAP`, not PG.

## 4. Postgres pool sizing

### What the code sets today

`pg.New` (`internal/pg/pg.go`) calls `pgxpool.New(ctx, DatabaseURL)` with **no explicit `MaxConns`**. pgx v5 therefore defaults to `max(4, runtime.NumCPU())` (`pgxpool/pool.go`). On the production pod the connection cap is whatever the container sees as `NumCPU`, floored at 4. On the 10-core reference host the load harness reported `pool_max_conns: 10` -- confirming the default path.

### Why the default under-provisions, and the fix

The default ties the pool to CPU count, but artemis's PG concurrency is driven by **three independent producers per pod**: HTTP handlers, the Hatchet worker callbacks, and the outbox relay loop. The measured runs show the cost: at the default 10-conn pool `deploy_upsert` tops out at 5,760 ops/s; widening to 25 conns nearly doubles it to 10,309 ops/s with no error increase. The pool, not PG, was the limiter.

No code change is required to tune it: **pgx honours `pool_max_conns` as a DSN query parameter**. The harness proved this -- a DSN ending in `...?sslmode=disable&pool_max_conns=25` reported `pool_max_conns: 25` and the enlarged-pool throughput above. Operators set it in `DATABASE_URL`:

```bash
DATABASE_URL=postgres://artemis:pw@artemis-postgresql:5432/artemis?sslmode=disable&pool_max_conns=20
```

### Per-replica multiplication at N >= 2

Total PG connections = `pool_max_conns x replica_count` (R13: N >= 2 stateless replicas). The bundled PG StatefulSet must size `max_connections` above that ceiling plus headroom for the Hatchet engine's own pool and for admin/backup sessions. Recommended starting point:

| knob                          | value        | rationale                                              |
| ----------------------------- | ------------ | ------------------------------------------------------ |
| `pool_max_conns` (per pod)    | 15-20        | ~2x the worst measured limiter, room to spare          |
| artemis replicas (R13/HPA)    | 2-6          | T29 HPA bound                                          |
| artemis PG conns (worst case) | 20 x 6 = 120 | pool x max replicas                                    |
| Hatchet engine conns          | ~20          | separate role/db on the same instance (ADR 0001 / T13) |
| PG `max_connections`          | 200          | 120 + 20 + ~60 headroom (admin, backup, autovac)       |

The reference harness ran PG with `max_connections=200`, which comfortably holds this envelope. Below ~120 effective `max_connections` the fleet risks `too many clients` at full replica scale -- that is the first hard cliff (section 6).

## 5. Hatchet per-site concurrency bound

The fan-out safety property is enforced by the engine, not by PG throughput. The adapter (`internal/hatchet/adapter.go`) registers every per-site workflow with:

```go
types.Concurrency{
    Expression:    "input.site",
    MaxRuns:       1,
    LimitStrategy: GroupRoundRobin,
}
```

`MaxRuns=1` keyed on `input.site` means **at most one workflow run executes per site at any instant**; `GroupRoundRobin` fairly interleaves distinct sites so a hot site cannot starve the rest. This is the property that makes 10k sites x millions of deploys tractable: concurrency is bounded per key, never globally unbounded, and the engine queues same-site events rather than running them concurrently.

This is empirically validated, not assumed. The T12 gated integration suite (`cd3b012`) ran against real `hatchet-lite v0.88.1`:

- `TestR3SameSiteNeverConcurrent` -- three events for one site, observed peak concurrency `<= 1`.
- `TestR3DistinctSitesRunConcurrent` -- two distinct sites both start and run concurrently (the round-robin does not serialise the whole fleet).
- `TestR4` (poison) -- a dead-lettering workflow never blocks its own key.

Because the bound is per key, adding sites adds independent queues; it does not raise the concurrency any single site sees. The engine, its own Postgres, and worker slot count -- not artemis's metadata pool -- govern aggregate workflow throughput, and are sized in the infra chart (T13/T14).

## 6. Valkey cache envelope

Valkey is a reconstructable cache, not a source of truth (ADR 0001). The registry cache holds one hash per site plus a `sites:all` index set (`internal/registry/valkey/store.go`): `site:<slug>` with fields `teams`, `created_at`, `updated_at`, `created_by`.

Measured against `valkey/valkey:8-alpine`, representative rows (two teams, RFC3339 timestamps):

| population         | `used_memory`         | delta over empty      |
| ------------------ | --------------------- | --------------------- |
| empty              | 955,096 B (0.91 MB)   | --                    |
| 10,000 sites + set | 3,851,624 B (3.67 MB) | 2,896,528 B (2.76 MB) |

That is **~290 bytes/site** of resident memory (dataset portion ~263 B/site). The full 10k-site registry cache fits in **under 3 MB** on top of the ~1 MB Valkey baseline. Extrapolation is linear in site count (one hash + one set member per site); deploy count does NOT enter the registry cache. Formula: `valkey_bytes ~= 1_000_000 + 290 * sites`.

The auth `teamcache` (`internal/teamcache`) is a separate key space bounded by distinct GitHub logins seen within the membership TTL, not by site count; at fCC staff cardinality (hundreds of logins) it is negligible. A 256 MB Valkey `maxmemory` -- already over 80x the projected registry envelope -- leaves ample room; Valkey stays artemis-exclusive and NetworkPolicy-locked.

## 7. PG storage envelope

Measured table sizes after the 20,000-row run (heap + indexes, `pg_total_relation_size`):

| table     | rows   | total bytes | bytes/row                             |
| --------- | ------ | ----------- | ------------------------------------- |
| `deploys` | 20,000 | 6,529,024   | **326.5**                             |
| `sites`   | 500    | 344,064     | 688 (inflated at low row count)       |
| `outbox`  | 500    | 212,992     | 426 (transient -- drained + prunable) |

The deploy row is the dominant term at scale. At **326.5 bytes/row** including the `deploys_site_mtime_idx` index, **3,000,000 deploy rows ~= 980 MB**. Add the sites table (10k x ~500 B heap ~= 5 MB) and tombstones (bounded by the recovery window) and the artemis metadata DB lands **comfortably under 2 GB** at full target scale -- well inside a single bundled StatefulSet PVC with backup headroom (T13/T17). Formula: `deploys_bytes ~= 327 * deploy_rows`.

## 8. Known cliffs + headroom

| cliff                    | trigger                                                       | mitigation                                                                                                                               |
| ------------------------ | ------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `too many clients` on PG | `pool_max_conns x replicas + hatchet` > `max_connections`     | size `max_connections` >= 200 (section 4); cap `pool_max_conns`                                                                          |
| outbox unbounded growth  | relay stalled (PG/engine down) -> rows never marked published | published rows are prunable; relay is at-least-once and resumes (R5); `/readyz` degraded surfaces the stall                              |
| relay batch starvation   | sustained enqueue rate > drain-per-tick                       | drain rate (~40k rows/s) is ~10,000x the realistic enqueue rate; raise `Batch` (currently 100) or shorten `relayInterval` if ever needed |
| hot-site queue backlog   | one site mutated faster than its single workflow drains       | by design (MaxRuns=1); GroupRoundRobin keeps other sites moving; surfaced via `artemis_worker_queue_depth{workflow}`                     |
| Valkey eviction          | `maxmemory` set below registry envelope                       | envelope is < 3 MB / 10k sites; keep `maxmemory` >= 64 MB (80x headroom)                                                                 |
| backfill window          | 3M-row cold backfill                                          | ~5-9 min one-shot (section 3); acceptable, runs before serving                                                                           |

## 9. Observability hooks for capacity

The control-plane counters are exposed at `GET /metrics` (no auth). Capacity signals to watch:

| metric                                                 | what it tells you                                   |
| ------------------------------------------------------ | --------------------------------------------------- |
| `artemis_worker_workflow_runs_total{workflow,outcome}` | per-workflow run volume + failure ratio             |
| `artemis_worker_queue_depth{workflow}`                 | per-workflow backlog (hot-site / engine saturation) |
| `artemis_worker_dlq_depth`                             | dead-lettered runs awaiting operator                |
| `artemis_relay_published_total`                        | outbox drain volume (relay liveness)                |
| `artemis_relay_failures_total`                         | relay passes that errored before draining           |
| `artemis_gc_runs_total{workflow,outcome}`              | GC pass volume + aborts (blast-cap trips)           |
| `artemis_gc_deploys_tombstoned_total`                  | reclaim progress                                    |

The `artemis_worker_*` and `artemis_relay_*` counters were wired into the boot path as part of R14 (worker-run + relay observation deferred from the readyz work, `d075130`).

## 10. Reproducing

```bash
just loadgen                                  # default 500 sites x 40 deploys
SITES=1000 DEPLOYS_PER_SITE=50 just loadgen    # heavier local run
```

The script spins up an ephemeral `postgres:17-alpine`, runs migrations via the harness, drives the load, prints the JSON report to stdout, and tears the container down on exit. To tune the pool during a run, pass a DSN with `pool_max_conns`:

```bash
LOADGEN_DATABASE_URL='postgres://artemis:artemis@localhost:55433/artemis?sslmode=disable&pool_max_conns=25' \
  CONCURRENCY=24 just loadgen
```

Every number in sections 3, 6, and 7 is reproducible from this harness plus the direct PG/Valkey size probes documented inline. No figure here is estimated.
