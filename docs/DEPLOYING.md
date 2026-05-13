# Deploying artemis to `gxy-management`

Post-publish operator runbook. Picks up after the `Release` workflow finishes building + publishing the image to GHCR. Covers infra-pin → helm-upgrade → verify → closeout.

Release-flow (cut, tag, CHANGELOG, push) lives in [`../RELEASING.md`](../RELEASING.md). This file covers everything that happens **after** the tag push succeeds.

## When to use

The image at `ghcr.io/freecodecamp/artemis:X.Y.Z` exists, the GH Release object is published, and `helm list -n artemis` on `gxy-management` still shows the previous version. You want to roll the cluster forward.

## Prerequisites

- `doctl auth list` shows a token authorized for the Universe DO project.
- `kubectl` + `helm` on `PATH`.
- `docker` (only for digest resolution — `docker buildx imagetools inspect`).
- `direnv allow` clean on `~/DEV/fCC/infra/k3s/gxy-management/` (loads the cluster `KUBECONFIG` + cluster-scoped tokens — see infra repo `CLAUDE.md` "Working directory rule (HARD)").
- A clean working tree on `freeCodeCamp/infra` `main` (or the branch you intend to land the pin PR against).

## Step 1 — Watch the build, capture the digest

After the tag push, the `docker-ghcr.yml` workflow fires automatically. Watch it:

```bash
gh run watch --repo freeCodeCamp/artemis --exit-status
```

Once green, resolve the multi-arch manifest digest:

```bash
docker buildx imagetools inspect ghcr.io/freecodecamp/artemis:X.Y.Z \
  --format '{{.Manifest.Digest}}'
```

The output is the `sha256:<64-hex>` digest that becomes load-bearing in the helm values.

## Step 2 — Pin the new version in `freeCodeCamp/infra`

Edit `k3s/gxy-management/apps/artemis/values.production.yaml`:

```yaml
image:
  repository: ghcr.io/freecodecamp/artemis
  # release: X.Y.Z
  tag: "X.Y.Z@sha256:<digest>"
  pullPolicy: IfNotPresent
```

Both the `# release:` comment and the `tag:` value carry the bare semver (no `v`). The `@sha256:<digest>` suffix is the immutable anchor — never ship `tag: X.Y.Z` without the digest, and never ship `tag: latest`.

Commit + push per infra-repo conventions. Decide between direct commit (small-fix threshold) and PR-with-review (substantial change) using the project's `PR_WORKFLOW.md` rule of thumb.

## Step 3 — Helm release

From the `k3s/gxy-management/` working directory **only** (the `.envrc` at that level loads the cluster `KUBECONFIG` + DO Universe token; running from repo root will hit the wrong cluster):

```bash
cd ~/DEV/fCC/infra/k3s/gxy-management
just release artemis
```

This is a wrapper for `helm upgrade --install` against the artemis chart. The reconciler is operator-driven, not GitOps-driven today (no ArgoCD pull yet — see Universe ADR-018 epic 4 for the planned cutover).

## Step 4 — Verify the rollout

Three checks, lowest-cost first:

```bash
# (1) Rollout snapshot — 3/3 pods on the new image.
just verify-app gxy-management artemis

# (2) Wrapper around the artemis-repo `make integration` E2E suite (~84s).
just verify-artemis

# (3) Source-of-truth integration suite directly against live artemis.
cd ~/DEV/fCC/artemis
ARTEMIS_URL=https://uploads.freecode.camp \
  GH_TOKEN=$(gh auth token) \
  SITE=test ROOT_DOMAIN=freecode.camp \
  make integration
```

(1) catches image-pull / startup-crash regressions in seconds. (2) is the operator-friendly E2E. (3) is the canonical contract suite — only re-run if (2) flags drift, or after a release that changes a contract.

## Step 5 — Smoke-test the live binary

```bash
curl -sI https://uploads.freecode.camp/healthz
```

Expected: `200 OK`, `artemis-version: X.Y.Z` header (embedded at build via `-X main.version=`).

## Step 6 — Closeout

Append the deploy receipt to the active dossier closeout:

```
~/DEV/fCC/artemis/.scratchpad/dossier/closeout/phase-<N>-<slug>.md
```

Capture: deploy date, image digest, verify-app summary, verify-artemis pass/fail, any drift surfaced. The dossier is gitignored — this is internal scratchpad, not a public artifact.

Also update the infra sprint STATUS doc:

```
~/DEV/fCC/infra/.scratchpad/sprints/<active-sprint>/STATUS.md
```

Under "Done": one line linking the release tag, digest, and verify timestamp. Under "Cross-repo state": bump the artemis ahead-count back to 0.

## Rollback procedure

If verify catches a regression:

1. Revert `values.production.yaml` to the previous `X.Y.Z-1@sha256:<previous-digest>`.
1. Re-run `just release artemis` from `k3s/gxy-management/`.
1. Re-run verify steps (1) + (2). If still red, file an incident note in the dossier closeout `## Rollback` section.

The previous digest lives in the helm revision history:

```bash
helm -n artemis history artemis
helm -n artemis rollback artemis <revision>
```

`helm rollback` is the fastest path but doesn't update `values.production.yaml` — re-pin the file so the next `just release` doesn't undo the rollback.

## Cross-references

- Cut & publish a new version: [`../RELEASING.md`](../RELEASING.md).
- Cluster bring-up from scratch: `~/DEV/fCC/infra/docs/flight-manuals/gxy-management.md`.
- Image-residency rule (`ghcr.io/freecodecamp/artemis` direct, never `zot.management.*`): Universe ADR-017 Build-Run Residency + `MEMORY.md` "Universe pillar RUN-residency".
- Active deprecation warnings (e.g. `promote.legacy_bare`): [`../RELEASING.md` § Active deprecations](../RELEASING.md#active-deprecations).
