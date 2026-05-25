# Releasing artemis

This file is the operator playbook for cutting a new version of artemis. Reader: a maintainer with push rights to the artemis repo. Downstream-deployment pin updates (helm/kustomize/ArgoCD/etc.) are operator-specific and live outside this file.

## Versioning rule

artemis follows [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html) with a single **pre-1.0 caveat**: until `v1.0.0` is cut, a `MINOR` bump may introduce a backwards-incompatible API change. Each such change is called out in the changelog with a `**[BREAKING]**` marker and a one-line migration note.

Post-1.0, the standard semver contract applies: `MAJOR` for breaks, `MINOR` for additive features, `PATCH` for fixes.

| Conventional Commit prefix                                                         | Pre-1.0 bump | Post-1.0 bump |
| ---------------------------------------------------------------------------------- | ------------ | ------------- |
| `feat(*):`                                                                         | `MINOR`      | `MINOR`       |
| `fix(*):`, `perf(*):`, `refactor(*):`                                              | `PATCH`      | `PATCH`       |
| any body with `BREAKING CHANGE:`                                                   | `MINOR` loud | `MAJOR`       |
| `chore(deps):`                                                                     | `PATCH`      | `PATCH`       |
| `test(*):`, `docs(*):`, `ci(*):`, `chore(*):` (non-deps), `style(*):`, `build(*):` | _no release_ | _no release_  |

A release is cut **only** when the unreleased section contains at least one `feat`, `fix`, `perf`, `refactor`, or `chore(deps)` commit. Pure test/docs/chore drift accumulates until a behaviour-bearing commit ships alongside it.

### `v1.0.0` trigger

Cut `v1.0.0` when the API surface is declared frozen — practically, after `GET /api/site/{site}/alias/{mode}`, the sites-registry CRUD, and the deploy/promote/rollback verbs have settled in production CLI use without breaking changes for two consecutive minor releases.

## Release flow

The flow is operator-driven, not CI-driven. CI only validates and publishes; the human picks the bump.

### 1. Audit the unreleased section

```bash
# What would the next release contain?
git-cliff --unreleased

# What does git-cliff think the next version should be?
git-cliff --bumped-version
```

`--bumped-version` reads `[bump]` rules in `cliff.toml`. Use it as a sanity check, not as the authoritative bump — the operator owns the final call (e.g. `--bumped-version` cannot detect a quiet behaviour break behind a `refactor` prefix).

### 2. Tag locally

```bash
# Replace v0.2.0 with the version chosen in step 1.
git tag -a v0.2.0 -m "v0.2.0 — <one-line summary>"
```

Tags are **annotated** (`-a`), never lightweight, so the tag carries authorship + a message that survives `git describe`.

### 3. Regenerate `CHANGELOG.md`

```bash
git-cliff -o CHANGELOG.md
git add CHANGELOG.md
git commit -m "chore(release): v0.2.0"
```

The release-chore commit lands on `main` **after** the tag so the tag itself points at the last behaviour-bearing commit (operators looking at `helm list` will see a release version that ties cleanly to the deployed code, not to a doc commit).

If the tag now points at the wrong commit because step 3 was committed first, fix with:

```bash
git tag -d v0.2.0
git tag -a v0.2.0 HEAD~1 -m "v0.2.0 — <summary>"
```

### 4. Push

```bash
git push --atomic origin main v0.2.0
```

`--atomic` pushes the branch update and the tag in a single transaction. Splitting the push into two commands (`git push origin main` followed by `git push origin v0.2.0`) can briefly expose the tag before the underlying commit has fully propagated across GitHub's edge nodes. When that race wins, `docker/metadata-action`'s default-branch lookup (`enable={{is_default_branch}}` → `repos.get()` Octokit call) intermittently returns `401 Bad credentials` even though the workflow's `GITHUB_TOKEN` scopes are correct. Atomic push avoids the race; see §6.

The tag push fires `.github/workflows/docker-ghcr.yml` (trigger: `push.tags: ['v[0-9]+.[0-9]+.[0-9]+', 'v[0-9]+.[0-9]+.[0-9]+-*']`). The workflow:

- builds + pushes the image to `ghcr.io/freecodecamp/artemis`, emitting tags
  - `0.2.0` (full semver — `docker/metadata-action` `type=semver,pattern={{version}}` **strips the leading `v`** from the git tag; the registry tag is the bare semver)
  - `0.2` (major.minor, for floating point-release pins — same `v`-stripping)
  - `sha-<full-sha>` (immutable audit anchor; always emitted)
- embeds `VERSION=0.2.0` + `COMMIT=<full-sha>` into the binary via `-X main.version=… -X main.commit=…` (visible in the startup log line `artemis: starting version=0.2.0 commit=<sha>`).

Git tag (`v0.2.0`) and registry tag (`0.2.0`) intentionally differ by the `v` prefix — this is the docker/metadata-action default and the broader OCI-registry convention. Don't try to "fix" the asymmetry by adding `pattern=v{{version}}`; downstream tooling (helm, kustomize, ArgoCD image-updater) expects the bare semver.

The same workflow is also `workflow_dispatch`-able for ad-hoc builds off `main`; those emit only `sha-<full-sha>`, `main`, and `latest` — never a semver tag.

GitHub Release notes are auto-published as part of the same `docker-ghcr.yml` run. After the build+push job finishes the image, two extra steps fire only on tag push: `Generate release notes` invokes `orhun/git-cliff-action` with `--current --strip all` to render the body for the tag directly from the commit log + `cliff.toml`, and `Publish GitHub Release` (softprops/action-gh-release pinned to v3.0.0) creates / updates the Release object with that body. The action is idempotent — re-running against an existing release updates rather than failing.

The notes come from the git history walked between the previous tag and the current one — **not** from `CHANGELOG.md` state at the tagged commit. Decoupling them lets the release-chore commit (`chore(release): vX.Y.Z`) land **after** the tag without breaking the workflow: the tag can keep pointing at the last behaviour-bearing commit per step 2 while still publishing a populated GH Release body. The checkout step uses `fetch-depth: 0` + `fetch-tags: true` so git-cliff can resolve `--current` against the full ref graph.

### 5. Downstream deployment pin

Once the workflow finishes, downstream deployments pin the new release. Resolve the digest:

```bash
docker buildx imagetools inspect ghcr.io/freecodecamp/artemis:X.Y.Z \
  --format '{{.Manifest.Digest}}'
```

The pin format is `image.tag: "X.Y.Z@sha256:<digest>"` — bare semver (no `v`-prefix; docker/metadata-action strips it from the git tag per OCI convention) plus the `@sha256:<digest>` immutable anchor. Never use `tag: X.Y.Z` without the digest, and never use `tag: latest` in production. Deployment mechanics (helm/kustomize/ArgoCD/etc.) are operator-specific.

## 6. CD remediation

If `docker-ghcr.yml` fails at the **Derive image tags** step with `##[error]Bad credentials`, the cause is almost always a transient GitHub API race, not a credential or workflow bug. Check the **Set up job → GITHUB_TOKEN Permissions** group of the failed run — if it shows the expected scopes (`Contents: write`, `Metadata: read`, `Packages: write`), do not edit the workflow. Re-run the failed jobs:

```bash
gh run rerun <run-id> --failed
```

The replayed run reuses the same ref + commit, so `metadata-action`'s default-branch lookup has had time to converge across edge nodes and succeeds. Confirmed empirically on 2026-05-23: v0.3.0 push failed at metadata-action, `gh run rerun --failed` succeeded on the first replay with no code change. Adopting `git push --atomic` (step 4 above) makes this race vanishingly rare in the first place.

If the rerun also fails, escalate — it is no longer a race:

1. Compare `GITHUB_TOKEN Permissions` against the workflow's declared scopes.
1. `gh api repos/freeCodeCamp/artemis/actions/permissions/workflow` to confirm `default_workflow_permissions: write`.
1. Check for upstream regressions: `gh api repos/docker/metadata-action/releases` for fixes published after the failure timestamp.

## Active deprecations

Behaviour-bearing warnings emitted by the running service. Each entry lists the log event, the removal trigger, and the replacement contract.

| event                 | emitted when                                                  | removal trigger                                                                                                                              | replacement                                                                      |
| --------------------- | ------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `promote.legacy_bare` | `POST /api/site/{site}/promote` with empty / zero-valued body | one release after first appearance — flip the empty-body branch to `400 Bad Request` in the sprint following the release that ships the warn | `{"deployId": "<id>"}` (direct-write) and/or `{"expectedCurrent": "<id>"}` (CAS) |

Telemetry consumers can grep `event=promote.legacy_bare` in the artemis access log to find remaining callers before the flip.

## Hotfix on an older release line

If `v0.3.x` is current but `v0.2.x` is still pinned in some downstream deployment and needs a fix:

1. `git checkout -b release/v0.2.x v0.2.0`
1. Cherry-pick the fix commit.
1. `git tag -a v0.2.1 -m "v0.2.1 — <hotfix summary>"`
1. Push branch + tag. Open a PR to merge `release/v0.2.x` back into `main` (so the fix also lands forward).

## Tooling

- **`git-cliff`** (Rust): reads Conventional Commits, emits `CHANGELOG.md`. Install via `brew install git-cliff` (macOS) or `cargo install git-cliff`. Config: [`cliff.toml`](./cliff.toml) at repo root.
- **No Node toolchain** is required. `release-please` was evaluated and rejected to keep this Go service free of JS dev-deps.

## Why this shape

- Operators map deployed releases back to changelog entries via the semver portion of `image.tag`. Even though `@sha256:<digest>` is the load-bearing pin, the `X.Y.Z` semver prefix (no `v`, per OCI tag convention) gives a `grep`-able human anchor that survives future tag-format migrations. Git tags carry the `v` (`v0.2.0`); registry tags do not (`0.2.0`) — intentional, consistent with docker/metadata-action defaults.
- Tags are local-cheap, push-discoverable. CI builds run only on `v*` tag push, so a typo in step 2 is a soft failure (the tag can be deleted before step 4).
- `MINOR-may-break` pre-1.0 is preserved in this file (not just in `cliff.toml` comments) because operators reading `CHANGELOG.md` need to see the caveat without diving into the tooling config.
