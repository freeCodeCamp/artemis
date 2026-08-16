# Artemis

Static-apps deploy proxy for the freeCodeCamp Universe platform. Public hostname: `uploads.freecode.camp`.

Staff developers and CI run `universe static deploy`. The CLI uploads the build artifact to artemis, artemis writes it to R2, and a Caddy `r2_alias` upstream serves it. Staff and CI hold no R2 tokens — artemis is the only holder of the admin S3 token. Caller identity comes from GitHub team membership.

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
- **[`docs/RELEASING.md`](docs/RELEASING.md)** — versioning rule, release-please flow, image build, downstream deploy pin.

ADR-016 (Universe platform repo) specifies the CLI ↔ artemis contract and the per-site authorization model.

## License

BSD-3-Clause — see [`LICENSE`](LICENSE).
