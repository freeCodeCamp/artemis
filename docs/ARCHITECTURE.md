# Artemis — architecture overview

This document tells you what the artemis service does and how it is built. It is written from the source code, not from the design records. Where this document and an ADR disagree, read the code.

For the API reference, see [`README.md`](README.md). For the durable-execution rationale, see [`design/0001-durable-execution-model.md`](design/0001-durable-execution-model.md).

## 1. What artemis is, and the problem it solves

Artemis is one Go service. It is a deploy proxy for static sites.

The service keeps the R2 credentials in its own configuration. A developer holds only a GitHub token. The developer never gets the R2 credentials. This is the one problem artemis solves: it gives staff a controlled write path into one object store.

For each site, artemis writes two kinds of object into R2:

- the site files, under one key prefix for each deploy;
- a small pointer object, called an **alias**.

Artemis does not serve the site traffic. Its HTTP surface holds only the `/api/*` routes, plus `/healthz` and `/readyz`.

The default R2 layout is:

| Thing               | Default R2 key                                 |
| ------------------- | ---------------------------------------------- |
| Deploy files        | `<site>/deploys/<deployId>/...`                |
| Production alias    | `<site>/production`                            |
| Preview alias       | `<site>/preview`                               |
| Completeness marker | `<site>/deploys/<deployId>/_artemis_meta.json` |
| Trash               | `_trash/<site>/<deployId>/`                    |

## 2. The parts artemis depends on

| Part         | What it holds                                                                                                      | Required |
| ------------ | ------------------------------------------------------------------------------------------------------------------ | -------- |
| **R2**       | All deploy bytes, the alias objects, and the trash tree                                                            | Yes      |
| **Valkey**   | The site registry when Postgres is absent, the change events, and the GitHub team cache                            | Yes      |
| **Postgres** | The deploy index, the alias rows, the tombstones, the outbox, the audit log, the site registry, and the repo queue | No       |
| **GitHub**   | The caller identity and the team membership                                                                        | Yes      |
| **Hatchet**  | The background workflow runs                                                                                       | No       |
| **Sentry**   | The errors and the cron check-ins                                                                                  | No       |

Three switches control the optional parts:

- `DATABASE_URL` starts Postgres and all the work that needs it.
- `HATCHET_ADDR`, together with Postgres, starts the worker and the event relay.
- `SENTRY_DSN` starts the error reporting.

Valkey is not optional. The configuration rejects an empty `VALKEY_ADDR`, and the boot code always dials Valkey.

Postgres is optional, but many functions need it. With no Postgres, artemis keeps no deploy index, writes no audit rows, runs no cleanup, and takes no site lock.

## 3. The deploy lifecycle of a developer

A deploy has three calls. Artemis makes the deploy id, and the client never chooses one.

### Step 1 — init

`POST /api/deploy/init` with a GitHub bearer token and a body of `{site, sha}`.

1. Artemis reads the caller login from the GitHub token.
1. Artemis reads the teams of the site from the cached registry. An empty team list gives 403.
1. Artemis asks GitHub if the caller is a member of one of those teams. A negative answer gives 403.
1. Artemis makes the deploy id. The shape is `<yyyymmdd-hhmmss>-<sha7>`, in UTC.
1. Artemis signs a **deploy-session JWT**. The token holds the login, the site, and the deploy id. The default life is 15 minutes.

Init writes nothing to R2, and the deploy prefix does not exist yet. Init does write one `audit_log` row in Postgres. That write is best effort, and a failure does not fail the request.

### Step 2 — upload

`PUT /api/deploy/{deployId}/upload?path=<relative-path>`, one call for each file, with the JWT.

1. Artemis checks that the deploy id in the URL agrees with the deploy id in the JWT.
1. Artemis validates the `?path=` value as received: an absolute path, a non-canonical path, a `..` segment, a control byte, and a backslash are each rejected with 400.
1. Artemis streams the request body straight into R2 at `<deployPrefix><path>`.

There is no staging area. Each object lands at its final key immediately. The default limit is 100 MiB for each file.

> **Note.** Step 2 used to remove the leading `/` *before* it examined the path, so `?path=/index.html` was silently rewritten to `index.html`. The raw value is now validated first, and an absolute path gives 400.

### Step 3 — finalize

`POST /api/deploy/{deployId}/finalize` with `{mode, files}`. The mode is `preview` or `production`.

Artemis runs these gates in order, and it stops at the first failure:

| Order | Gate                                           | Failure                    |
| ----- | ---------------------------------------------- | -------------------------- |
| 1     | The mode must be `preview` or `production`     | 400                        |
| 2     | The file manifest must not be empty            | 400                        |
| 3     | The manifest must hold a root `index.html`     | 422                        |
| 4     | R2 must hold every file in the manifest        | 422, with the missing list |
| 5     | Artemis writes the `_artemis_meta.json` marker | 502                        |
| 6     | Artemis measures the deploy size               | not fatal                  |

Artemis then takes a **per-site lock** in Postgres, and it does the last steps inside that lock:

7. Artemis reads the site from the registry again. A deleted site gives 410.
1. Artemis writes the alias object. **This one PUT makes the deploy live.**
1. Artemis writes one Postgres transaction. The transaction adds or updates the deploy row, marks the previous alias target as released, adds or updates the alias row, and puts a `site.changed` event in the outbox.

The response holds the public URL, the deploy id, and the mode.

Two properties follow from this order:

- Each failure before step 8 leaves the alias untouched. A partial upload never becomes live.
- The alias write and the Postgres write are not one atomic unit. If step 9 fails, the alias already points at the new deploy, and the client gets 502.

## 4. What a deployment alias is

An alias is one small R2 object. Its body is the deploy id string, and nothing more. Its content type is `text/plain`.

Each site has two aliases:

- `<site>/production`
- `<site>/preview`

The alias is the pointer that tells the edge which deploy to serve. To make a deploy live, artemis replaces the body of one alias object.

Artemis depends on R2 to make one PUT atomic for each key. No code in this repository can prove that property. It is an assumption of the design, and the safety of every alias write depends on it.

### Promote and rollback

Both verbs move the **production** alias. Both hold the per-site lock. Both refuse to point the alias at a deploy that has no root `index.html`.

|                                | `POST .../promote`                            | `POST .../rollback`                       |
| ------------------------------ | --------------------------------------------- | ----------------------------------------- |
| Body                           | Optional                                      | Required                                  |
| Target                         | `deployId`, or else the current preview alias | `to` (required)                           |
| Examines the deploy prefix     | No                                            | Yes (422 `deploy_missing`)                |
| Examines the root `index.html` | Yes                                           | Yes                                       |
| Order of the checks            | Guard first, then the index check             | Existence and index first, then the guard |

Both verbs accept an optional `expectedCurrent` field. Artemis reads the current alias, and it gives 409 `alias_drift` on a mismatch. The guard is a read and then a write. Artemis never sets the `IfMatch` or `IfNoneMatch` fields of the PUT, although the S3 client supports them. The per-site lock, not a conditional write, is what makes the guard safe.

An empty `expectedCurrent` stops the guard. It does not assert that no production alias exists yet.

After the alias write, artemis writes the alias row and puts a `site.changed` event in the outbox, in one transaction.

## 5. How a developer removes a deploy or a site

Each removal in artemis is a move first, and a hard delete much later. All the destructive moves run on a context that ignores a client disconnect, with a limit of 10 minutes.

### Remove one deploy

`DELETE /api/site/{site}/deploys/{deployId}`

1. Artemis reads both aliases. If one of them points at this deploy, artemis gives 409 `deploy_aliased`. A live deploy is not removable.
1. Artemis measures the deploy size.
1. Artemis moves each object from `<site>/deploys/<deployId>/` to `_trash/<site>/<deployId>/`.
1. Artemis writes a tombstone row and removes the deploy row, in one transaction.

The move is a copy and then a delete, for each object. It has no rollback. A failure in the middle leaves the deploy divided across the two prefixes.

### Remove a site

`DELETE /api/site/{slug}` has two behaviours:

- **Without `?purge=true`** — artemis removes the registry row only. It touches no R2 bytes. It returns 204.
- **With `?purge=true`** — artemis moves the full `<slug>/` prefix into `_trash/<slug>/`, writes a whole-site tombstone, and then removes the registry row. It returns 200.

The purge moves the alias objects too, because they are under the same `<slug>/` prefix.

### What you can recover

| State                                       | Recoverable          | How                                                                       |
| ------------------------------------------- | -------------------- | ------------------------------------------------------------------------- |
| Deploy in the trash, tombstone present      | Yes                  | `POST .../deploys/{deployId}/restore`                                     |
| Deploy in the trash, recovery window passed | No                   | The nightly purge deletes the bytes                                       |
| Site removed without a purge                | The bytes stay in R2 | Register the slug again                                                   |
| Site purged                                 | Not through the API  | The whole-site tombstone holds an empty deploy id, and restore rejects it |

The recovery window is 7 days by default. `GET /api/site/{site}/trash` shows each tombstone with its `expiresAt` time.

Restore moves the bytes back and makes the deploy row again. **Restore never writes an alias.** A restored deploy is in storage again, but it is not live. The developer must promote it.

## 6. What reconciliation is

Reconciliation finds the drift between the two stores. R2 holds the bytes. Postgres holds the index. The two can disagree, because no write spans both stores atomically.

A daily job only reports the drift. It cannot write. An operator does the repair with the `artemis reconcile <site> --apply` command. Design note [0004](design/0004-drift-detection-and-alerting.md) gives the reason.

The reconciler receives one site name, and that name must be the storage dirname, not the registry slug (section 9 defines both). The reconciler builds the R2 prefix and the Postgres query from this one name. The drift sweep converts each registry slug to a dirname before it reads a site. A reconciler that receives the bare slug instead builds a prefix and a query that no write path ever produces, and it finds zero drift on every site, silently. The repair command refuses a name that neither the index nor the registry knows, for the same reason.

For one site, the reconciler compares two sets of deploy ids:

- **Side A** — the ids in the R2 key listing under `<site>/deploys/`.
- **Side B** — the ids in the Postgres `deploys` table with the state `active`.

It uses two signals to decide what to do: the set of alias targets, and the presence of the `_artemis_meta.json` marker. The marker means that a finalize completed at that prefix.

The daily job reports every row of the table below and repairs none of them. An operator starts the repair.

| Drift | Condition                                                                   | Repair                                   |
| ----- | --------------------------------------------------------------------------- | ---------------------------------------- |
| 1     | In R2, not in Postgres, aliased, marker present                             | Write the index row again                |
| 2     | In R2, not in Postgres, aliased, no marker                                  | Report only. Never remove                |
| 3     | In R2, not in Postgres, not aliased, marker present                         | Write the index row again                |
| 4     | In R2, not in Postgres, not aliased, no marker, older than the grace window | Move to the trash, and write a tombstone |
| 5     | In Postgres, not in R2, aliased                                             | Report only                              |
| 6     | In Postgres, not in R2, not aliased                                         | Remove the index row                     |

An unmarked orphan that is younger than the grace window (72 hours by default) agrees with no case. The reconciler does not touch it. This protects an upload that is still in progress. Drift 6 uses the same window. The reconciler does not remove an index row that is younger than the grace window, because the bytes for that row can still be on their way.

The reconciler reads the two sides one time, at the start of the run. It then repairs one deploy at a time. Each repair takes the site lock, and each repair reads the state again while it holds that lock. This second read is necessary. Another workflow, or a deploy from a user, can change the site in the interval between the first read and the repair. The reconciler stops the repair if the second read shows any one of these:

- The deploy has an index row again.
- An alias points at the deploy.
- The bytes are gone from R2.
- The marker is present, which means that a finalize completed.

The alias check reads the alias objects in R2, and it also reads the alias rows in Postgres. A deploy that either store calls live is live. Section 9 gives the reason: the R2 object is the truth, and Postgres only mirrors it. Reconciliation runs against sites whose Postgres state is suspect, because a wrong Postgres state is the condition it exists to repair. It must therefore not ask Postgres alone which deploy the users get.

Postgres refuses a drift 1 or a drift 3 repair for a deploy that holds a tombstone. This refusal is part of one SQL statement, so no interval exists between the check and the write. The reconciler can therefore never return a deleted deploy to the active set.

The reconciler stops the run for a site if Postgres holds deploys for that site and the R2 listing returns no deploy prefix at all. An empty listing looks the same as total drift, and total drift would delete every index row for the site. The reconciler treats it as a fault instead, and it reports the fault. This is the shape the name defect in section 10 had, and it is also the shape of a wrong bucket or a wrong prefix.

The blast cap limits how many deploys one run can move to the trash or remove from the index. A run that plans more repairs than the cap repairs the oldest deploys first, reports the cap, and leaves the remainder for the next run. The cap does not limit the repairs that write an index row, because those repairs remove nothing.

Two limits are important:

- The reconciler finds **presence** drift only. If an id is on both sides, the reconciler compares no field. It never repairs a wrong size or a wrong time.
- Each repair holds the site lock for its own operation only. The reconciler does not hold the lock for the full run. The first read of R2 has no time limit, and a long hold would make a deploy from a user fail.

Drift 2 and drift 5 are the dangerous cases. The `drift-detect` cron reports them at the error level and sends one Sentry event with the op `drift.aliased_missing`.

## 7. Background work

Artemis starts the background plane only when Postgres exists **and** `HATCHET_ADDR` is set. It registers three workflows.

| Workflow          | Trigger                  | What it does                                       |
| ----------------- | ------------------------ | -------------------------------------------------- |
| `gc-site`         | The `site.changed` event | Runs the retention cleanup for one site            |
| `tombstone-purge` | Cron `0 3 * * *`         | Deletes the trash of each expired tombstone        |
| `drift-detect`    | Cron `0 4 * * *`         | Reads each site, reports the drift, writes nothing |

The `gc-site` workflow runs one at a time for each site. The `drift-detect` cron takes no site lock, because it only reads.

### The event path

Artemis uses a transactional outbox, so a data write and its event commit together:

1. A finalize, a promote, or a rollback adds a `site.changed` row to the `outbox` table, **inside the same transaction** as the data write.
1. A relay loop runs every 5 seconds. It claims a maximum of 100 unpublished rows, in id order.
1. The relay sends each row to Hatchet as an event, and then it sets `published_at`.

The claim transaction commits before the relay sends anything. Delivery is therefore **at least once**, and never exactly once. A workflow must tolerate a repeat.

Every event goes through the outbox. The two cron workflows need no event, because each one enumerates its own work.

### The retention rules

The `gc-site` workflow keeps a deploy if **one** of these conditions is true:

1. An alias points at it.
1. It is one of the newest N deploys (3 by default).
1. An alias released it less than 15 seconds ago.
1. It is younger than the grace window (72 hours by default).
1. It holds the marker **and** it is younger than the retention window (7 days by default).

Condition 5 is important. The retention window applies only to a deploy that holds the marker. An unmarked deploy gets the grace window only.

Before it moves a deploy, `gc-site` takes the per-site lock and reads the **live R2 aliases** again. The plan uses the Postgres alias rows, but the execution uses the R2 objects. A deploy that became live in the interval is skipped.

The `gc-site` workflow deletes no bytes. It only moves a prefix into the trash. The `drift-detect` cron writes nothing at all: its store and its mover are read-only types, and every write method returns an error. The `tombstone-purge` workflow is the only hard delete in the service.

## 8. How identity and authorization work

There are two credentials, and they gate two separate groups of routes.

| Credential          | Routes                                         | What it proves                       |
| ------------------- | ---------------------------------------------- | ------------------------------------ |
| GitHub bearer token | Each `/api/*` route except upload and finalize | The caller is a real GitHub user     |
| Deploy-session JWT  | `PUT .../upload` and `POST .../finalize`       | The holder has a live deploy session |

**Identity.** Artemis sends the bearer token to `GET /user` on the GitHub API and gets a login. It caches the result under a hash of the token, and never under the token itself. A positive result lives 5 minutes. A negative result lives a maximum of 30 seconds.

**Authorization.** Each decision uses GitHub team membership. Artemis reads the team list of the site from the cached registry, and then it asks GitHub if the caller is a member of one of those teams. An empty team list always denies.

Each GitHub team probe runs inside a handler body. The JWT middleware is not identity-only: it also refuses a site that has no teams in the registry, before any handler runs.

There are four separate team gates:

| Gate             | Guards                                                          | Default team          |
| ---------------- | --------------------------------------------------------------- | --------------------- |
| Per-site teams   | promote, rollback, delete, restore, trash, alias, deploys, init | from the registry row |
| Registry team    | register a site, update a site, remove a site                   | `staff`               |
| Repo create team | `POST /api/repo`                                                | `staff`               |
| Audit read team  | `GET /api/audit`                                                | `staff`               |

**The deploy-session JWT.** Artemis signs it with HS256. The signing key must be 32 bytes or more. Artemis fixes the algorithm in two places, so an `alg=none` token and a key-confusion token both fail. Artemis also examines the issuer claim.

On upload and finalize, artemis examines the teams of the site again. It does **not** examine the subject again. If an operator removes a user from the team, that user keeps the live deploy session until the token expires.

Two more gaps are important:

- `GET /api/sites` has no team gate. Any valid GitHub token can list each registered site.
- A failed GitHub probe becomes 503, even when the true cause is a bad token.

## 9. Where the state lives

| State               | Store                        | Authoritative                                 |
| ------------------- | ---------------------------- | --------------------------------------------- |
| Deploy files        | R2                           | **R2**                                        |
| Alias pointer       | R2 object                    | **R2** — the alias object decides what serves |
| Alias row           | Postgres `aliases`           | Follower. The cleanup plan reads it           |
| Deploy index        | Postgres `deploys`           | Follower of R2. An operator repairs it        |
| Site registry       | Postgres `sites`, or Valkey  | **Postgres when it exists**, else Valkey      |
| Registry read cache | In-process map               | Follower                                      |
| Tombstones          | Postgres `tombstones`        | **Postgres**                                  |
| Outbox              | Postgres `outbox`            | **Postgres**                                  |
| Audit log           | Postgres `audit_log`         | **Postgres**                                  |
| Repo queue          | Postgres `repo_requests`     | **Postgres**                                  |
| GitHub teams        | Valkey and an in-process map | Follower of GitHub                            |

### Two names for one site

Artemis uses two different strings for one site.

- The **registry slug** is the bare name a developer chooses at `POST /api/site/register`. The `sites` table uses this slug as its primary key. The HTTP routes and the deploy-session JWT carry this slug.
- The **storage dirname** is the first path segment of the rendered R2 key for that site. Artemis computes the dirname by rendering the site prefix template with the slug, then taking the text up to the first `/`.

The default `DEPLOY_PREFIX_FORMAT` is `<site>/deploys/<ts>-<sha>/`. With this format, the storage dirname equals the slug. The deployed format adds a domain suffix, for example `<site>.freecode.camp/deploys/<ts>-<sha>/`. With this format, the slug `test` renders to the dirname `test.freecode.camp`.

R2 keys always come from the raw slug, rendered through the template. The `deploys` table, the `aliases` table, the `tombstones` table, and the `site.changed` outbox payload always hold the storage dirname, never the slug. Each write path converts the slug to a dirname before it touches one of these stores.

A caller that reads the registry and skips this conversion sends the bare slug to a store that expects the dirname. The query or the R2 prefix then matches nothing, even though the site is real. Section 10 describes one case where this happened.

### The registry, in detail

The boot code selects the registry source of truth with one test: does a Postgres pool exist?

- With Postgres, artemis copies the Valkey rows into Postgres one time, and then it uses Postgres for each read and each write. Valkey stays only as the change transport.
- Without Postgres, Valkey is the source of truth.

An in-process cache sits in front of whichever store wins. It holds `slug -> teams`, and nothing more. **Each authorization check reads this cache, and not the store.** A write publishes the changed slug on a Valkey channel. Each replica then reads the full registry again. A timer of 60 seconds is the fallback.

Team revocation is therefore eventually consistent, and not immediate.

### Two invariants that hold the design together

1. **The alias object is the only truth about what is live.** Postgres mirrors it. When the two disagree, reconciliation and the cleanup job both trust R2.
1. **One Postgres advisory lock, keyed by the site, serializes each mutation of that site.** Finalize, promote, rollback, delete, restore, purge, the cleanup job, and each reconciler repair all take the same key. The timeout is 30 seconds, and a contended request gets 409.

The second invariant has one dangerous limit. With no Postgres, the lock becomes a silent no-op, and concurrent alias writes race with no error.

## 10. Divergence between this code and the deployed release

This document describes what the code at HEAD does. The deployed release still runs a nightly repair cron. HEAD removes it.

The deployed `reconcile-scheduler` workflow reads the registry slugs and publishes each one straight into a `site.reconcile` event, with no conversion to a storage dirname (section 9 defines both names). Every other write path converts the slug first. The deployed reconciler therefore builds an R2 prefix and a Postgres query from a name that no store recognizes. With the default `DEPLOY_PREFIX_FORMAT`, the slug and the dirname are the same string, so the reconciler still works. With a format that adds a suffix, such as the deployed `<site>.freecode.camp/deploys/<ts>-<sha>/`, the slug and the dirname differ, and the reconciler finds nothing on either side. It reports zero drift and completes without error, on every site, every night. This is why the split must stay documented even though the default format hides it.

A read-only sweep at HEAD replaces that scheduler. The sweep converts each registry slug to its storage dirname, and it also reads the site names that only the index knows. It writes nothing.

The deployed reconciler also carries a second hazard. It reads the two sides one time and then repairs from that first read, with no lock and no second read. It has no blast cap, and it removes an index row of any age. The retirement removes the hazard, because no scheduled job repairs anything. The repair path at HEAD takes the site lock and reads the state again inside that lock, and only an operator can start it.

Until the release carrying this change is deployed, the running service still shows the old behaviour: the nightly reconcile workflow completes and repairs nothing.
