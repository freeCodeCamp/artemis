# Artemis

Static-apps deploy proxy for the freeCodeCamp Universe platform. Public hostname: `uploads.freecode.camp`.

Staff devs and CI run `universe deploy`; the artifact lands on R2 behind a Caddy `r2_alias` upstream. Zero R2 tokens reach staff hands or CI secrets — Artemis is the sole holder of the admin S3 token. Identity is GitHub team membership.

## Quick start

```sh
cp .env.example .env   # fill values (loaded by direnv)
just run               # boot the HTTP server on $PORT
just test              # unit tests (-race -cover)
just                   # list every recipe
```

## Docs

- **[`docs/README.md`](docs/README.md)** — API contract, configuration, observability, R2 layout, sites registry, integration testing, curl examples.
- **[`docs/RELEASING.md`](docs/RELEASING.md)** — versioning rule, release-please flow, image build, downstream deploy pin.

The CLI ↔ artemis contract and per-site authorization model are specified in ADR-016 (Universe platform repo).

## License

BSD-3-Clause — see [`LICENSE`](LICENSE).
