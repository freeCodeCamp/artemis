# Releasing artemis

This file is the operator playbook for cutting a new version of artemis. Reader: a maintainer with push rights to `freeCodeCamp/artemis` and edit rights to `freeCodeCamp/infra` (for the deploy pin).

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

Cut `v1.0.0` when ADR-016 §API surface is declared frozen — practically, after `GET /api/site/{site}/alias/{mode}`, the sites-registry CRUD, and the deploy/promote/rollback verbs have settled in production CLI use without breaking changes for two consecutive minor releases.

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
git push origin main
git push origin v0.2.0
```

The tag push fires `.github/workflows/docker-ghcr.yml` (trigger: `push.tags: ['v[0-9]+.[0-9]+.[0-9]+', 'v[0-9]+.[0-9]+.[0-9]+-*']`). The workflow:

- builds + pushes the image to `ghcr.io/freecodecamp/artemis`, emitting tags
  - `0.2.0` (full semver — `docker/metadata-action` `type=semver,pattern={{version}}` **strips the leading `v`** from the git tag; the registry tag is the bare semver)
  - `0.2` (major.minor, for floating point-release pins — same `v`-stripping)
  - `sha-<full-sha>` (immutable audit anchor; always emitted)
- embeds `VERSION=0.2.0` + `COMMIT=<full-sha>` into the binary via `-X main.version=… -X main.commit=…` (visible in the startup log line `artemis: starting version=0.2.0 commit=<sha>`).

Git tag (`v0.2.0`) and registry tag (`0.2.0`) intentionally differ by the `v` prefix — this is the docker/metadata-action default and the broader OCI-registry convention. Don't try to "fix" the asymmetry by adding `pattern=v{{version}}`; downstream tooling (helm, kustomize, ArgoCD image-updater) expects the bare semver.

The same workflow is also `workflow_dispatch`-able for ad-hoc builds off `main`; those emit only `sha-<full-sha>`, `main`, and `latest` — never a semver tag.

GitHub Release notes are auto-published as part of the same `docker-ghcr.yml` run. After the build+push job finishes the image, two extra steps fire only on tag push: `Slice CHANGELOG section for this tag` extracts the matching `[X.Y.Z]` section from `CHANGELOG.md` (note: section heading is the bare semver, no `v` prefix — `cliff.toml` strips it via `trim_start_matches`), and `Publish GitHub Release` (softprops/action-gh-release pinned to v3.0.0) creates / updates the Release object with that body. The action is idempotent — re-running against an existing release updates rather than failing. The slice step exits hard if the section is missing rather than publishing an empty release body, so a forgotten `git-cliff -o CHANGELOG.md` regen surfaces as a CI red, not a silent empty release.

### 5. Pin the new version in `freeCodeCamp/infra`

Edit `infra/k3s/gxy-management/apps/artemis/values.production.yaml`:

```yaml
image:
  # release: 0.2.0
  tag: "0.2.0@sha256:<digest>"
```

The `# release:` comment is redundant once the semver tag is in `image.tag`, but is kept for `grep`-ability and to survive future tag-format changes. The `@sha256:<digest>` suffix is the **load-bearing** part: it pins the image immutably regardless of whether the `0.2.0` registry tag is later overwritten (which we never do, but the digest is the audit-grade anchor). Never use `tag: 0.2.0` without the digest, and never use `tag: latest` in production values.

Resolve the digest after the workflow succeeds:

```bash
docker buildx imagetools inspect ghcr.io/freecodecamp/artemis:0.2.0 \
  --format '{{.Manifest.Digest}}'
```

Open a PR against `freeCodeCamp/infra`, merge after review, let the GitOps reconciler roll it out.

## Active deprecations

Behaviour-bearing warnings emitted by the running service. Each entry lists the log event, the removal trigger, and the replacement contract.

| event                 | emitted when                                                  | removal trigger                                                                                                                              | replacement                                                                      |
| --------------------- | ------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `promote.legacy_bare` | `POST /api/site/{site}/promote` with empty / zero-valued body | one release after first appearance — flip the empty-body branch to `400 Bad Request` in the sprint following the release that ships the warn | `{"deployId": "<id>"}` (direct-write) and/or `{"expectedCurrent": "<id>"}` (CAS) |

Telemetry consumers can grep `event=promote.legacy_bare` in the artemis access log to find remaining callers before the flip.

## Hotfix on an older release line

If `v0.3.x` is current but `v0.2.x` is still pinned in some galaxy and needs a fix:

1. `git checkout -b release/v0.2.x v0.2.0`
1. Cherry-pick the fix commit.
1. `git tag -a v0.2.1 -m "v0.2.1 — <hotfix summary>"`
1. Push branch + tag. Open a PR to merge `release/v0.2.x` back into `main` (so the fix also lands forward).

## Tooling

- **`git-cliff`** (Rust): reads Conventional Commits, emits `CHANGELOG.md`. Install via `brew install git-cliff` (macOS) or `cargo install git-cliff`. Config: [`cliff.toml`](./cliff.toml) at repo root.
- **No Node toolchain** is required. `release-please` was evaluated and rejected to keep this Go service free of JS dev-deps.

## Why this shape

- Operators map `helm list` releases to changelog entries via the semver portion of `image.tag` in the infra repo. Even though `@sha256:<digest>` is the load-bearing pin, the `X.Y.Z` semver prefix (no `v`, per OCI tag convention) and the parallel `# release:` comment give `grep`-able human anchors that survive future tag-format migrations. Git tags carry the `v` (`v0.2.0`); registry tags do not (`0.2.0`) — this is intentional and consistent with docker/metadata-action defaults.
- Tags are local-cheap, push-discoverable. CI builds run only on `v*` tag push, so a typo in step 2 is a soft failure (the tag can be deleted before step 4).
- `MINOR-may-break` pre-1.0 is preserved in this file (not just in `cliff.toml` comments) because operators reading `CHANGELOG.md` need to see the caveat without diving into the tooling config.

## Bootstrap note — `v0.1.0`

`v0.1.0` was tagged in commit `5cd947f` (`chore(release): adopt git-cliff + tag v0.1.0`) before the GHCR workflow learned to fire on tag push. The fix landed in a follow-up commit. Before pushing `v0.1.0`, the operator must:

1. Confirm the workflow change is on `main` (i.e. `docker-ghcr.yml` contains `push.tags`).
1. Re-point the local tag at the latest behaviour-bearing commit on `main` (see the retag recipe in step 3).
1. Push the tag.

Subsequent releases follow the standard 5-step flow with no special handling.
