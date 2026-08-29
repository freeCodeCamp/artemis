# Artemis

Static-apps deploy proxy for the freeCodeCamp Universe platform. Public hostname: `uploads.freecode.camp`.

Staff developers and CI run `universe static deploy`. The CLI uploads the build artifact to artemis, artemis writes it to R2, and a Caddy `r2_alias` upstream serves it. Staff and CI hold no R2 tokens — artemis is the only holder of the admin S3 token. Caller identity comes from GitHub team membership.

## Core tenets

Artemis makes these promises. A change that serves none of them does not belong here.

1. **Artemis holds the only R2 admin credential.** No caller ever receives one.
2. **Identity is GitHub team membership.** Artemis stores no users and no passwords.
3. **Artemis does not serve site traffic.** It writes bytes and flips aliases; the serve plane reads R2 directly.
4. **A deploy goes live in one alias write, or not at all.** A partial upload never serves.
5. **Deploys are immutable.** There is no edit and no partial delete — only a new deploy.
6. **Every destructive act is reversible for a stated window, then final.**
7. **A name is freed only after its bytes are gone.**
8. **What is served is kept correct by prevention, under the site lock. What is stored is kept correct by detection, in reconcile, repaired by a person.**

[`CONTEXT.md`](CONTEXT.md) fixes the vocabulary these promises use.

## Quick start

```sh
cp .env.example .env   # fill values (loaded by direnv)
just run               # boot the HTTP server on $PORT
just test              # unit tests (-race -cover)
just                   # list every recipe
```

## Docs

- **[`docs/ORIENTATION.md`](docs/ORIENTATION.md)** — the read sequence for a new contributor.
- **[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)** — what the service does and how it is built, written from the source code.
- **[`docs/README.md`](docs/README.md)** — API contract, configuration, observability, R2 layout, sites registry, integration testing, curl examples.
- **[`docs/COMPATIBILITY.md`](docs/COMPATIBILITY.md)** — caller-visible behaviour changes between releases, with the release each one landed in.
- **[`docs/RELEASING.md`](docs/RELEASING.md)** — versioning rule, release-please flow, image build, downstream deploy pin.

- **[`docs/design/`](docs/design/)** — the specifying documents for mechanisms internal to artemis: the durable-execution model, drift detection, and the delete semantics.

## Where a design decision lives

Cross-repo ADRs own the contracts *between* services — the API surface, the identity chain, the R2 layout, the deploy-session JWT scope. ADR-016 and ADR-022 govern artemis there. `docs/design/` owns the mechanisms internal to artemis, and no cross-repo ADR duplicates them. [`CONTEXT.md`](CONTEXT.md) fixes the vocabulary both use.

ADR-016 (Universe platform repo) specifies the CLI ↔ artemis contract and the per-site authorization model.

## License

BSD-3-Clause — see [`LICENSE`](LICENSE).
