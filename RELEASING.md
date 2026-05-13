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

CI (GitHub Actions, `.github/workflows/`) picks up the tag and:

- builds + pushes a multi-arch image to `ghcr.io/freecodecamp/artemis:v0.2.0`
- attaches `CHANGELOG.md` excerpt to the GitHub Release notes

(_Release workflow lives in `.github/workflows/release.yml`; if absent, the operator opens the Release manually from the tag and pastes the `v0.2.0` section of `CHANGELOG.md` into the description._)

### 5. Pin the new version in `freeCodeCamp/infra`

Edit `infra/k3s/gxy-management/apps/artemis/values.production.yaml`:

```yaml
image:
  # release: v0.2.0
  tag: sha-<full-sha>@sha256:<digest>
```

The `# release: vX.Y.Z` comment is the human-readable anchor. The `tag:` line stays as `sha-<full-sha>@sha256:<digest>` for immutability + audit traceability — never `tag: v0.2.0`, because tags can be moved on the registry side while a digest cannot.

Open a PR against `freeCodeCamp/infra`, merge after review, let the GitOps reconciler roll it out.

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

- Operators map `helm list` releases to changelog entries via the `# release:` comment in the infra repo. Without that anchor, the pinned digest is opaque.
- Tags are local-cheap, push-discoverable. CI runs only on `v*` tag push, so a typo in step 2 is a soft failure (the tag can be deleted before step 4).
- `MINOR-may-break` pre-1.0 is preserved in this file (not just in `cliff.toml` comments) because operators reading `CHANGELOG.md` need to see the caveat without diving into the tooling config.
