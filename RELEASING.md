# Releasing artemis

This file is the operator playbook for cutting a new version of artemis. Reader: a maintainer with merge rights to the artemis repo. Downstream-deployment pin updates (helm/kustomize/ArgoCD/etc.) are operator-specific and live outside this file.

Releases are driven by [release-please](https://github.com/googleapis/release-please) in **manifest mode**. The operator does not tag by hand — release-please reads Conventional Commits on `main` and maintains a standing **release PR**; merging that PR cuts the tag, publishes the GitHub Release, and triggers the image build.

## Versioning rule

artemis follows [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html) with a single **pre-1.0 caveat**: until `v1.0.0` is cut, a `MINOR` bump may introduce a backwards-incompatible API change. This caveat is enforced mechanically by `bump-minor-pre-major: true` in `release-please-config.json` — a `BREAKING CHANGE` commit on a `0.x` line bumps `MINOR`, not `MAJOR`.

Post-1.0, the standard semver contract applies: `MAJOR` for breaks, `MINOR` for additive features, `PATCH` for fixes.

| Conventional Commit prefix                                                         | Pre-1.0 bump | Post-1.0 bump |
| ---------------------------------------------------------------------------------- | ------------ | ------------- |
| `feat(*):`                                                                         | `MINOR`      | `MINOR`       |
| `fix(*):`, `perf(*):`, `refactor(*):`                                              | `PATCH`      | `PATCH`       |
| any body with `BREAKING CHANGE:`                                                   | `MINOR` loud | `MAJOR`       |
| `chore(deps):`                                                                     | `PATCH`      | `PATCH`       |
| `test(*):`, `docs(*):`, `ci(*):`, `chore(*):` (non-deps), `style(*):`, `build(*):` | _no release_ | _no release_  |

`feat:` deliberately bumps `MINOR` even pre-1.0 — `bump-patch-for-minor-pre-major` is left **off**. A release PR is opened **only** when the unreleased commits contain at least one releasable change (`feat`, `fix`, `perf`, `refactor`, `chore(deps)`); pure test/docs/chore drift accumulates silently until a behaviour-bearing commit ships alongside it.

### `v1.0.0` trigger

Cut `v1.0.0` when the API surface is declared frozen — practically, after `GET /api/site/{site}/alias/{mode}`, the sites-registry CRUD, and the deploy/promote/rollback verbs have settled in production CLI use without breaking changes for two consecutive minor releases. Force it with a `Release-As: 1.0.0` footer (see step 2) or by editing the release PR.

## Release flow

The flow is PR-driven. release-please computes the bump from commit history; the operator owns the **merge** and may override the version before merging.

### 1. Review the release PR

Every push to `main` runs `.github/workflows/release.yml`. The `release-please` job opens or updates a PR titled `chore(main): release X.Y.Z` that:

- bumps `version.txt` and `.release-please-manifest.json` to the computed version, and
- regenerates the `CHANGELOG.md` section from the Conventional Commits since the last release.

Read the PR. The diff **is** the proposed release: the version bump and the rendered changelog. Edit the changelog body directly in the PR if wording needs work — release-please preserves manual edits to its PR.

### 2. (Optional) Override the version

release-please's computed bump is a strong default, not gospel — it cannot detect a quiet behaviour break hidden behind a `refactor:` prefix. To force a specific version, add a `Release-As:` footer to any commit that lands on `main` (e.g. an empty commit), then let release-please re-groom the PR:

```bash
git commit --allow-empty -m "chore: release 0.4.0" -m "Release-As: 0.4.0"
git push origin main
```

### 3. Merge the PR

Merging the release PR is the release. release-please then:

- creates the **annotated git tag** `vX.Y.Z` at the merge commit (`include-v-in-tags` defaults true — git tags carry the `v`),
- publishes the **GitHub Release** with the changelog body, and
- emits `release_created=true`, which gates the `build-and-push` job in the same workflow run.

> **Note — tag points at the release commit.** Unlike a hand-tagged flow, the tag sits on the release-PR merge commit (version bump + changelog), not on the last behaviour-bearing commit. The image is still built from that exact commit (`checkout` uses `ref: ${{ needs.release-please.outputs.sha }}`), so `helm list` version ↔ deployed code stays consistent.

### 4. Image build (automatic)

The `build-and-push` job builds and pushes to `ghcr.io/freecodecamp/artemis`, emitting tags:

- `0.2.0` (full semver — release-please's `version` output is already **v-stripped**; the registry tag is the bare semver),
- `0.2` (major.minor, for floating point-release pins — same bare form),
- `sha-<full-sha>` (immutable audit anchor; always emitted),
- `latest` (newest release; the job only runs on a real release, so `latest` always maps to a published version).

It embeds `VERSION=0.2.0` + `COMMIT=<full-sha>` into the binary via `-X main.version=… -X main.commit=…` (visible in the startup log line `artemis: starting version=0.2.0 commit=<sha>`).

Git tag (`v0.2.0`) and registry tag (`0.2.0`) intentionally differ by the `v` prefix — release-please's git-tag default plus the OCI-registry convention. Do **not** "fix" the asymmetry; downstream tooling (helm, kustomize, ArgoCD image-updater) expects the bare semver.

### 5. Downstream deployment pin

Once the workflow finishes, downstream deployments pin the new release. Resolve the digest:

```bash
docker buildx imagetools inspect ghcr.io/freecodecamp/artemis:X.Y.Z \
  --format '{{.Manifest.Digest}}'
```

The pin format is `image.tag: "X.Y.Z@sha256:<digest>"` — bare semver (no `v`-prefix) plus the `@sha256:<digest>` immutable anchor. Never use `tag: X.Y.Z` without the digest, and never use `tag: latest` in production. Deployment mechanics (helm/kustomize/ArgoCD/etc.) are operator-specific.

## Configuration

| File                            | Role                                                                                                                                       |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `release-please-config.json`    | `release-type: simple`, `bump-minor-pre-major: true`. Language-agnostic — no Node/JS toolchain pulled in.                                  |
| `.release-please-manifest.json` | Source of truth for the current released version (`{".": "X.Y.Z"}`). release-please reads + bumps this.                                    |
| `version.txt`                   | The `simple` strategy mirrors the current version here; embedded nowhere at runtime (build identity comes from the workflow's build-args). |
| `.github/workflows/release.yml` | The `release-please` + `build-and-push` two-job workflow.                                                                                  |

## CD remediation

Because the build runs in the **same workflow run** as release-please (gated on `release_created`), the older tag-push race that produced intermittent `Bad credentials` at the metadata step no longer applies. If `build-and-push` fails for a transient reason, re-run the failed jobs — the tag and release already exist, so the replay is idempotent:

```bash
gh run rerun <run-id> --failed
```

If release-please itself fails to open or update the PR, confirm the `release-please` job has `contents: write` + `pull-requests: write` and that repo settings allow Actions to create PRs (`Settings → Actions → General → Allow GitHub Actions to create and approve pull requests`).

## Active deprecations

Behaviour-bearing warnings emitted by the running service. Each entry lists the log event, the removal trigger, and the replacement contract.

| event                 | emitted when                                                  | removal trigger                                                                                                                              | replacement                                                                      |
| --------------------- | ------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `promote.legacy_bare` | `POST /api/site/{site}/promote` with empty / zero-valued body | one release after first appearance — flip the empty-body branch to `400 Bad Request` in the sprint following the release that ships the warn | `{"deployId": "<id>"}` (direct-write) and/or `{"expectedCurrent": "<id>"}` (CAS) |

Telemetry consumers can grep `event=promote.legacy_bare` in the artemis access log to find remaining callers before the flip.

## Hotfix on an older release line

If `v0.3.x` is current but `v0.2.x` is still pinned in some downstream deployment and needs a fix, run release-please against a maintenance branch:

1. `git checkout -b release/v0.2.x v0.2.0` and push the branch.
1. Cherry-pick the fix commit(s) onto it.
1. Point a release-please run at that branch via the `target-branch` input (a dedicated `workflow_dispatch` invocation or a branch-scoped workflow). release-please opens a release PR against `release/v0.2.x`; merging it cuts `v0.2.1` and builds the image off that branch.
1. Open a PR to merge `release/v0.2.x` back into `main` so the fix also lands forward.

## Tooling

- **`release-please`** runs entirely in CI via [`googleapis/release-please-action`](https://github.com/googleapis/release-please-action) (pinned by SHA in `release.yml`). **No local tool and no Node toolchain** are required for a release — the action is a runner-side dependency, not a repo dev-dep. `release-please` (the `node`/`go`/etc. release-types) was configured as `simple` precisely to keep this Go service free of language-specific manifest coupling.

## Why this shape

- Operators map deployed releases back to changelog entries via the semver portion of `image.tag`. Even though `@sha256:<digest>` is the load-bearing pin, the `X.Y.Z` semver prefix (no `v`, per OCI tag convention) gives a `grep`-able human anchor. Git tags carry the `v` (`v0.2.0`); registry tags do not (`0.2.0`) — intentional, consistent with release-please + docker/metadata-action defaults.
- The release decision is a **PR review**, not a local tag. The diff under review is exactly what ships (version + changelog), so a mistaken bump is caught before merge, and the version can be overridden with a `Release-As:` footer.
- `MINOR-may-break` pre-1.0 is enforced by `bump-minor-pre-major: true` and documented here so operators reading `CHANGELOG.md` see the caveat without diving into the tooling config.
