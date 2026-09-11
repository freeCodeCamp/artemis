# 0007 — readyz degradation semantics

Status: ruling 2026-09-11.

`/readyz` is the Kubernetes readinessProbe
(`infra/k3s/gxy-management/apps/artemis/charts/artemis/templates/deployment.yaml`).
A 503 from this endpoint removes the pod from the Service endpoints. All
replicas share one Valkey, one R2 bucket and one Postgres cluster, so a
fault in any of them hits every replica at the same time. A 503 on a
shared-fault upstream therefore empties the Service and stops all traffic.

`/healthz` is the livenessProbe. It makes no upstream call and can never
go red on an upstream outage.

## readyz ruling

No upstream failure makes `/readyz` return 503. Valkey, R2 and Postgres
each set `degraded: true` in a 200 response. See
`internal/handler/readyz.go`.

ADR-019 (2026-06-04 amendment) already sets this rule for Postgres and
Hatchet: "`/readyz` reports degraded (not down) in this state ... so the
rolling-update + load-balancer plane treat a GC-paused artemis as
serving, not failed." This ruling applies the same rule to Valkey.

### Why Valkey no longer fails closed

Before the Postgres cutover, Valkey held the sites registry and a Valkey
outage meant artemis could not resolve a site. After the cutover
(`cmd/artemis/main.go`, `openRegistry`), `pg.RegistryStore` is the Writer
and the Reader source. Valkey is the cache front and the
`registry.changed` transport. A Valkey outage loses no registry data.

Three facts make the 503 unnecessary:

- The registry Reader serves from an in-memory snapshot and refreshes it
  from Postgres on a TTL ticker (`internal/registry/valkey/reader.go`).
  The pub-sub channel from go-redis reconnects on a connection loss and
  closes only on `Close`, so the TTL refresh continues through the
  outage.
- The deploy paths fail closed by themselves. The upload handler and the
  finalize handler both return 503 `fence_unavailable` when the deploy
  fence read fails (`internal/handler/deploy.go`). Write safety does not
  depend on readyz.
- A registry write does not fail when the `registry.changed` publish
  fails. `PublishOnChange` logs a warning
  (`internal/registry/valkey/store.go`). The commit in Postgres holds and
  the TTL refresh picks the change up.

### The team cache

`GitHubClient.userTeamsThroughDurableCache` used to return the durable
cache read error, which broke every authenticated request during a Valkey
outage. It now logs a warning and calls the GitHub API
(`internal/auth/github.go`). GitHub is the authority for team
membership, so the fall-through is more authoritative than the cache, not
less.

### Boot

Boot survives a Valkey outage after the Postgres cutover, and fails
before it. The rule falls out of what the registry Reader can read, not
from a flag.

`openRegistry` still calls `valkey.NewWithRetry` first. When the retry
window ends it falls back to `valkey.NewUnverified`, which builds the
client without dialing, and logs `valkey.connect.degraded`. Every later
call reports the outage on its own. `openTeamCache` does the same and
logs `teamcache.connect.degraded`.

`NewReaderFromSource` then decides the outcome:

- **After the cutover** the source is `pg.RegistryStore`, the initial
  `Refresh` reads Postgres and succeeds, and only `Subscribe` fails. That
  is a warning, `registry.subscribe.failed`, and the reader serves from
  the TTL refresh alone. The TTL ticker retries `Subscribe` on every tick
  and logs `registry.resubscribed` when Valkey returns.
- **Before the cutover** the source is Valkey itself, the initial
  `Refresh` fails, and boot fails with it. A pod that cannot read the
  registry must not serve.

An empty `VALKEY_ADDR` still fails boot. That is a configuration fault,
not an outage, so `NewUnverified` returns nil for it.

What a degraded boot costs: deploys fail closed with `503
fence_unavailable`, team-membership reads go to the GitHub API, and a
registry change reaches the pod on the TTL refresh instead of at once. The fix for that is a second Valkey replica,
tracked in the infra wave `2026-09-11-gxy-platform-resilience` T2.

## Paging

A 200 is quiet in `kubectl get pods`, so Sentry must page. Valkey and R2
each keep their own strike counter (`probeState`) and page once per
outage episode at three consecutive failures, with the fingerprint
`["readyz", "<op>"]`. Recovery re-arms the latch. Neither masks the
other.

Postgres is the exception. Its branch logs `readyz.postgres.degraded` at
Warn and pages nothing. This predates the ruling and is unchanged by it.
