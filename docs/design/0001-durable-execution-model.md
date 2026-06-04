# Local ADR 0001 — Durable execution model + deploy retention GC

> **Status:** Accepted (direction locked) · **Date:** 2026-06-01 **Amends:** ADR-016 §"interaction-agnostic, synchronous, no background tasks" + ADR-017 §Pillar (artemis stateless→stateful) · **Lifted →** Universe **ADR-020 — Platform Durable Execution + Windmill Role Reframe** (Proposed, 2026-06-02; cross-ref `~/DEV/fCC-U/Architecture/decisions/020-durable-execution.md`). ADR-020 generalises this design to platform-ops and EXPANDS scope beyond the cleanup cron — see §13 note. **Reasoning trail / research:** `.scratchpad/2026-06-01-retention-gc-design.md` (GC flow research, 23 verified claims), dossier `.scratchpad/dossier/2026-06-01-artemis-retention-gc/`. **Platform alignment:** see §13 (ADR-001/008/017/019 drift).

______________________________________________________________________

## 1. Context

Moving deploy-retention GC off the Windmill cron into artemis surfaced a deeper truth: **every state-mutating artemis operation (deploy, promote, rollback, delete, GC, purge) is a durable, multi-step, crash-prone, concurrent saga**, not a request/response. Today they run synchronously in HTTP handlers with ad-hoc coordination. That mismatch is the root of the races, the orphan classes, and the "where does the sweep run" problem.

Target scale: **10s of thousands of sites → millions of deploys** (Vercel/Netlify-class). At that scale full-bucket-scan GC is impossible (event-driven incremental is mandatory) and in-RAM state is untenable (need disk-durable, queryable metadata).

Operator constraints (locked):

- **No infra-primitive coupling** — app owns scheduling, coordination, reclaim. Object store stays the dumb vendor-neutral S3 subset (ADR-016: "swap R2 for MinIO = config change, not code").
- **Self-hosted OSS only**, no SaaS, no feature paywall.
- **artemis owns its own data** — dedicated Postgres, not the platform's shared DB.

## 2. Decision — the architecture

```text
 commands (CLI / CI / curl)
        │  HTTP API (public contract, unchanged)
        ▼
 ┌───────────────────────────────────────────────────────────┐
 │ artemis (HTTP + Hatchet workers, same binary/deployment)   │
 │   handler ──tx──▶ Postgres(artemis-owned): metadata+outbox │
 │   Hatchet ───────▶ durable workflows, fair-sched by site   │
 │   activities ────▶ R2 (bytes: dumb S3 put/get/list/delete) │
 │   hot reads ─────▶ Valkey (cache only, reconstructable)    │
 └───────────────────────────────────────────────────────────┘

 end users ──▶ CF ──▶ Caddy r2_alias (gxy-cassiopeia, RO token) ──▶ R2   [serve plane, independent]
```

| Plane                                    | Owns                     | Authoritative for                                             |
| ---------------------------------------- | ------------------------ | ------------------------------------------------------------- |
| **R2** `universe-static-apps-01`         | bytes                    | object existence (dumb, swappable S3)                         |
| **Postgres** (artemis-dedicated)         | metadata + jobs + outbox | deploy index, alias pointers, lifecycle state, workflow state |
| **Hatchet** (on artemis's Postgres, MIT) | durable execution        | retries, timers, fair-scheduling, event triggers              |
| **Valkey**                               | hot cache                | nothing — pure speed layer, loss-safe                         |
| **Caddy `r2_alias`**                     | serve                    | independent; reads R2 directly, never calls artemis           |

Engine **Hatchet** (verified MIT, Postgres-native, no engine paywall — gating is hosting/SLA/RPS only). Chosen centerpiece feature: **concurrency / fair-scheduling with dynamic keys** — `key = site` gives structural per-tenant isolation (one site's burst cannot starve another's GC/deploy), replacing all hand-rolled per-site serialization. Composes with **event triggers** (event-driven incremental GC) and **dynamic rate-limit keys** (protect the shared GitHub App quota per tenant).

## 3. Data & trust ownership

- **artemis owns a dedicated Postgres** (bounded context — no shared-DB coupling with Apollo or other services). Same custody class as the R2 admin token + Valkey: provisioned to artemis, creds in `infra-secrets/management/artemis.env.enc` (sops+age).
- **Engine, phased.** **M1 = app-bundled single-instance Postgres** (Hatchet's own chart PG or a simple PG subchart) — matching the **live precedent on gxy-management/backoffice** (Windmill's bundled `postgresql` subchart, Outline's `postgres:16-alpine`; **CNPG operator is not deployed anywhere yet**). M1 backup = Windmill-style `postgres-rclone` CronJob → R2. **Later = CNPG sweep** (platform-wide, operator-managed, when multiple PG instances get standardized) folds artemis's PG in + adds the formal stateful-pillar backup floor.
- **Stateful-pillar trajectory.** ADR-017 lists artemis as a *stateless* pillar. Any in-cluster primary store makes it stateful — but M1's bundled-PG + rclone→R2 backup follows the **already-blessed Windmill pattern** (pragmatic, no new ceremony). The **formal ADR-019 carve-out** (CNPG T1/T2 + per-galaxy `management-cnpg-backups` + RPO ≤ 5 min / RTO ≤ 60 min + restore drill) lands at the CNPG sweep, gating artemis-PG **GA** — not M1 build. See §13 D1.
- **Hatchet co-locates** on artemis's Postgres (own schema) — required so the outbox relay + app tables share one instance for the §6 single-tx outbox.
- **HA scope = artemis only.** A Postgres outage pauses *new deploys + GC* only — the **serve plane (Caddy + R2) keeps serving every site** (ADR-016 consequence, verified in serve code §8).

## 4. Retention GC policy — safety invariants (engine-independent)

Policy rides *on top of* the engine; it does not change with the substrate. Retain predicate = **reachability from live aliases + keep-N + grace**, NOT raw age (industry-verified: Docker/Harbor/ Vercel/Netlify all converge; age can't identify in-use objects under pointer indirection).

| id  | invariant (never violate)                                                                                       |
| --- | --------------------------------------------------------------------------------------------------------------- |
| V1  | a deploy any alias targets is never deleted; **re-checked immediately before delete** (TOCTOU)                  |
| V2  | newest `recentKeep` (3) per site never deleted, any age (rollback floor)                                        |
| V3  | deploy younger than `graceMs` never deleted; `graceMs ≥ JWT_TTL` (max upload→finalize)                          |
| V4  | no flow deletes bytes another is writing (grace + finalize-marker reachability)                                 |
| V5  | every delete is a tombstone move first; byte-reclaim is a later app-driven purge pass                           |
| V6  | sweep aborts a site (deletes nothing) if its plan exceeds the blast-cap; plan persisted pre-delete              |
| V7  | per-site mutations serialized + fairly scheduled (Hatchet concurrency key = site)                               |
| V8  | alias mutations are last-writer-safe (single-writer-per-site via V7; optimistic re-read)                        |
| V9  | same store state + same `now()` → identical delete set (pure predicate, injectable clock)                       |
| V10 | every activity idempotent: re-run deletes nothing new; tombstone/delete of gone prefix = no-op                  |
| V11 | a deploy is not deleted within `serve_cache_ttl` (15s) of losing alias status; `graceMs ≥ serve_cache_ttl` (§8) |

Orphan class (never-finalized / aborted upload): finalize writes a `_artemis_meta.json` marker; a prefix **without** a marker past `graceMs` is an orphan → reclaimed fast (separate from the 7d retention for completed deploys). Reclaim is two-phase: tombstone → `_trash/<site>/<id>/`, a later GC pass purges tombstones past the recovery window. **App-driven, not R2 lifecycle** (§11).

## 5. Engine invariants (durability layer)

| id  | invariant                                                                                           |
| --- | --------------------------------------------------------------------------------------------------- |
| E1  | every activity idempotent (no non-idempotent side effect) — the keystone making at-least-once safe  |
| E2  | all events for one site process in per-site order (Hatchet concurrency key)                         |
| E3  | R2 authoritative for bytes; Postgres authoritative for metadata; Valkey reconstructable             |
| E4  | every event-GC gap (orphan, missed event) is closed by the reconciliation drift-audit               |
| E5  | no operation holds a lock across a crash; durability is the engine's, resume is replay-from-journal |
| E6  | a workflow exceeding max attempts dead-letters + alerts; never blocks its concurrency key           |

## 6. Transactional integrity — outbox-row + relay

Hatchet events are sent via its API (not inside the app's Postgres tx), so we use the **transactional outbox**: a mutation's handler, in **one Postgres tx**, writes the metadata change **and** an `outbox` row; a relay worker reads new outbox rows and emits the Hatchet event at-least-once; idempotent consumers (E1) give exact-once effect. This closes the dual-write / event-loss / leak class (Harbor's 74 TB storage-orphan lesson) *at the metadata layer* — stronger than grace-window patching. Possible because artemis owns the Postgres (outbox + app tables, one instance, §3).

## 7. Event-driven incremental GC

Steady-state GC is **never** a full-bucket scan. A successful finalize/promote/rollback emits `site.changed{site}` (via outbox) → a **debounced** per-site GC workflow (Hatchet `concurrency:{key:site}` + debounce) evaluates retention for that site only → O(changed sites). A low-cadence **reconciliation** workflow does a sharded R2 ↔ Postgres drift audit (orphan bytes w/ no row → tombstone; row w/ no bytes → alert) — DR + the event-miss backstop (E4), never the steady-state path.

## 8. Serve plane coupling (verified)

`infra/docker/images/caddy-s3/modules/r2alias/r2alias.go`: `<site>.freecode.camp` → Caddy → in-process **alias LRU (TTL 15s, 10k entries)** → miss = R2 `GetObject <site>/<alias>` → deployID → path-rewrite `/<site>/deploys/<id>/<path>` → `file_server` streams R2 bytes. Caddy holds a **separate read-only R2 token**, never calls artemis (serve plane independent). 404s negative-cached; R2 5xx → `503 Retry-After:30`, never cached.

Consequence = **V11**: a just-superseded deploy may still be served from a 15s-stale cache; deleting it inside that window 404s in-flight requests. Covered by keep-N + grace (1h ≫ 15s), now explicit.

## 9. Valkey role — hot cache only (justified, not load-bearing)

With Postgres as durable truth, Valkey is **optional speed**. **Operator directive: the gxy-management Valkey stays artemis-EXCLUSIVE** — it is already a dedicated instance in its own `valkey` namespace, AOF on PVC, **NetworkPolicy-locked to artemis pods** (CiliumNetworkPolicy, `valkey.valkey.svc.cluster.local:6379`). No other component shares it. Caddy or any other stack component needing a hot cache provisions **its own** Valkey instance (e.g. a cassiopeia-local Valkey for a serve-plane alias cache) — **never** this one.

artemis-only Valkey use, justified:

- **Shared GitHub team-membership cache** across artemis replicas (today in-process) → protects the shared GitHub App quota.
- Registry + repo-queue hot reads (today's use) — may stay on Valkey as cache fronting Postgres, or fold into Postgres; decide at M2.

Loss = cache-cold (slower), never wrong. (A serve-plane Caddy alias cache is a **separate** instance on cassiopeia — out of artemis scope, noted for the platform, not this dossier.)

## 10. Migration (phased — prove engine on new surface first)

- **M1** — Stand up artemis-owned Postgres + Hatchet; model `deploys`/`aliases`/`tombstones`/`outbox` tables; run **retention GC + manual delete + purge** as Hatchet workflows (concurrency key=site). Backfill index from a one-time R2 scan. **Retire Windmill cron.** Deploy hot path untouched.
- **M2** — Emit `site.changed` via outbox from existing deploy/promote/rollback handlers (additive) → event-driven incremental GC (§7). Migrate registry + repo-queue off Valkey to Postgres.
- **M3** — (optional) move deploy/promote/rollback execution onto workflows; upload streaming likely stays synchronous, finalize becomes a durable workflow.
- **M4** — Reconciliation drift-audit workflow + observability (Hatchet/queue metrics, drift counts); optional Valkey serve-cache + GH-membership cache.

## 11. Alternatives rejected

| Considered                                                     | Rejected because                                                                                                                                         |
| -------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| k8s CronJob for scheduling                                     | infra-primitive coupling; app must own scheduling                                                                                                        |
| R2 object-lifecycle for reclaim                                | app correctness would depend on a bucket policy; R2 lifecycle is **prefix+age(days) only, no tag filters** (verified) — can't target tombstones in place |
| R2 conditional-write (`If-None-Match`/`If-Match`) for lock/CAS | vendor-specific S3 extension; breaks ADR-016 vendor-neutrality                                                                                           |
| In-process Valkey-Streams hand-rolled runtime                  | RAM-bound; can't be durable metadata SOT at 10k+ sites; reinvents a DB's job/lock/ordering primitives                                                    |
| Temporal                                                       | full engine, but heaviest ops (multi-service cluster + DB[+ES]) for lean gxy-management                                                                  |
| River (Postgres job-queue lib)                                 | viable lighter floor, but a job-queue, not event-driven; Hatchet chosen for fair-scheduling keys + event triggers + DAG                                  |
| Inngest                                                        | excellent event DX, but self-hosted **server is SSPL** (source-available); Hatchet is MIT                                                                |
| Asynq (Valkey) + Postgres                                      | splits queue (Valkey) from store (Postgres) → reintroduces dual-write, loses §6 transactional integrity                                                  |
| Shared platform Postgres                                       | violates artemis data-ownership / bounded context (§3)                                                                                                   |

## 12. Open questions

- **Postgres HA posture** on gxy-management: single + PITR backups (MVP) vs replicated operator — artemis mutation availability now depends on it (serve plane does not).
- **Hatchet self-host footprint** on gxy-management (API server + engine + dashboard) — confirm the ops weight is acceptable vs River-floor fallback.
- **Platform-wide Hatchet**: adopt as shared orchestration for Apollo (constellations) + repo-mgmt too, or artemis-scoped first? (Multi-lang SDKs make platform-wide viable.)
- **M1 scope**: GC/delete/purge only, or also migrate registry/repo-queue to Postgres in M1?
- Platform Postgres scout (2026-06-01, corrected): **two live PG instances, both app-bundled single-node** — **Windmill** (`postgresql` subchart on gxy-management + `postgres-rclone`→R2 backup) and **Outline** (`postgres:16-alpine` on ops-backoffice-tools). **CNPG operator NOT deployed anywhere** (Veritas, the first CNPG user, still future). Apollo has no SQL DB. → artemis provisions **net-new bundled single-node PG** matching the Windmill/Outline precedent; joins the future CNPG sweep. (Earlier "none live" note was wrong — it missed Windmill's bundled subchart.)

______________________________________________________________________

## 13. Platform alignment & drift (ADR-001 / 008 / 017 / 019)

Audited this plan against the Universe Architecture ADRs. Building blocks + placement (verified): artemis = **gxy-management** (P1 control-plane brain); caddy-s3 serve plane = **gxy-cassiopeia** (P2); Valkey platform-svc = **gxy-management** (backs artemis registry, ADR-008 2026-05-25); **Windmill** = gxy-management, **permanent**, the sanctioned platform workflow engine (provisions DNS/OIDC/DB, constellation teardown, the cleanup cron we're replacing). Static bucket `universe-static-apps-01` on R2 (→ Ceph RGW only on bare metal; gxy-management is cloud-forever, so artemis stays on R2). Observability = Vector→ClickHouse, vmagent→VictoriaMetrics, **GlitchTip** (Sentry-compat), Grafana (ADR-015).

> **2026-06-02 (ADR-020):** Universe ADR-020 (Proposed) EXTENDS the scope of the Windmill→artemis transfer beyond the cleanup cron analysed below. Constellation provisioning (DNS / OIDC / DB) + teardown ALSO transfer to artemis durable-exec (Hatchet, concurrency key = constellation; governance only, impl deferred). Windmill is demoted from "permanent P1 platform-ops shepherd" to staff/interactive tooling — it stays physically on gxy-management for now, relocation to gxy-backoffice deferred (not retired). Apollo Chat full-code app stays on Windmill (out of scope). The §13 analysis below (D2 + the ✓ "cleanup cron only" row) remains the historical 2026-06-01 record; ADR-020 §Scope supersedes the "Windmill stays permanent, only the cron retired" framing.

| #   | Finding                                                                                         | Severity                                | Resolution                                                                                                                                                                                                                                                                    |
| --- | ----------------------------------------------------------------------------------------------- | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1  | **artemis is an ADR-017 *stateless* pillar**. In-cluster PG makes it stateful.                  | **drift — but not an M1 blocker**       | **M1**: bundled single-node PG + `postgres-rclone`→R2, the already-blessed **Windmill precedent** — no new ceremony. **GA (CNPG sweep)**: formal ADR-019 carve-out — CNPG + T1/T2 floor + `management-cnpg-backups` R2 + restore drill; amend ADR-017 §Pillar + ADR-008. (§3) |
| D2  | **Two workflow engines** — Hatchet alongside the sanctioned Windmill (P1, permanent).           | **coherence — needs ADR justification** | ADR-020 must position them: Windmill = platform-ops automation (provisioning/teardown); Hatchet = service-level durable execution (artemis sagas, later Apollo). Precedent: Windmill already bundles its own PG on gxy-management, so a 2nd engine+DB there isn't novel.      |
| D3  | **gxy-management P1 charter = "thin and reliable"** (ADR-019); adding CNPG+Hatchet thickens it. | tension — accept w/ note                | Mitigated by the Windmill+PG precedent already on P1. Keep artemis CNPG small (local-path, replicas for HA), headroom-aware.                                                                                                                                                  |
| D4  | **ADR number**: doc said ADR-018 (taken=early-access; 019=cassiopeia).                          | doc bug                                 | → **ADR-020** (fixed in header).                                                                                                                                                                                                                                              |
| D5  | **Postgres engine**: doc implied generic PG/Patroni.                                            | align                                   | Use **CNPG** (ADR-008 sanctioned), local-path PV, CNPG-native HA — matches Veritas (§3).                                                                                                                                                                                      |
| D6  | **Per-galaxy backup bucket** convention (never shared).                                         | align                                   | New `management-cnpg-backups` R2 bucket; T3 cross-vendor shipper runs on an orthogonal-blast-radius galaxy (per ADR-019, cassiopeia's shipper runs on mgmt — for a mgmt pillar, pick cassiopeia/external).                                                                    |
| ✓   | R2 = dumb swappable S3 (ADR-008 "swap R2→Ceph RGW = config change")                             | **aligned**                             | Our dumb-S3 port stance matches exactly.                                                                                                                                                                                                                                      |
| ✓   | Valkey hot-cache on gxy-management                                                              | **aligned**                             | Already a platform svc there; our cache role fits. (Caddy alias-cache would be cassiopeia-local Valkey — cross-galaxy, flag in M4.)                                                                                                                                           |
| ✓   | Observability slog→Vector / Sentry→GlitchTip / /metrics→vmagent                                 | **aligned**                             | artemis already wired; Hatchet metrics ride the same. Hatchet's own dashboard is an extra UI vs Grafana/HyperDX — minor.                                                                                                                                                      |
| ✓   | Retiring Windmill **cleanup cron** (not Windmill itself)                                        | **aligned**                             | Windmill stays for platform-ops; only its `cleanup_old_deploys` flow is boneyard'd.                                                                                                                                                                                           |

**Net:** the plan is *architecturally sound* but lands two platform decisions that need ADR-020 + amendments to ADR-017/008 before GA: **(D1) artemis becomes a stateful pillar**, and **(D2) Hatchet joins Windmill as a second, role-distinct engine.** Neither is a blocker — both have precedent (Veritas for stateful-on-cloud-via-CNPG; Windmill+PG for engine+DB on gxy-management) — but both must be ratified, not assumed. The dossier carries these as explicit tasks.

### Appendix — verified-fact citations (fetched 2026-06-01)

- Hatchet MIT, Postgres-only, no `ee/` dir, engine features un-paywalled — `gh api repos/hatchet-dev/hatchet/license`; pricing gates hosting/SLA/RPS/retention/HIPAA only.
- R2 lifecycle = prefix + age(days), no tag filters, has AbortIncompleteMultipartUpload — developers.cloudflare.com/r2/buckets/object-lifecycles.
- Serve plane: Caddy `r2_alias` 15s LRU, separate RO token — `infra/docker/images/caddy-s3/modules/r2alias/r2alias.go`.
- GC patterns (reachability-not-age, registry-GC race, grace window, soft-delete) — Docker/Distribution #3045, Harbor #10167/#23199, Vercel/Netlify retention docs, Git GC; full set in the research scratchpad.
