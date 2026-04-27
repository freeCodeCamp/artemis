# Artemis

Static-apps deploy proxy for the freeCodeCamp Universe platform. Public hostname: `uploads.freecode.camp`.

Staff devs and CI run `universe deploy` and the artifact lands on R2 behind a Caddy `r2_alias` upstream. Zero R2 tokens leak into staff hands or CI secrets — Artemis is the sole holder of the admin S3 token. Identity is GitHub team membership.

## API

```
POST   /api/deploy/init                   { site, sha, files? } → { deployId, jwt, expiresAt }
PUT    /api/deploy/{deployId}/upload      multipart stream      → { received }
POST   /api/deploy/{deployId}/finalize    { mode }              → { url }
POST   /api/site/{site}/promote                                 → { url }
POST   /api/site/{site}/rollback          { to }                → { url }
GET    /api/site/{site}/deploys                                 → [{ deployId, ts, sha, size }]
GET    /api/whoami                                              → { login, authorizedSites }
GET    /healthz                                                 → { ok: true }
```

Auth headers (`/api/*` except `/healthz`):

| Endpoint                                                                    | Bearer                                                                       |
| --------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `POST /api/deploy/init`, `POST /api/site/*`, `GET /api/*`                   | GitHub token (PAT / OIDC)                                                    |
| `PUT /api/deploy/{deployId}/upload`, `POST /api/deploy/{deployId}/finalize` | Deploy-session JWT (HS256, ≤15 min, scoped to one `(login, site, deployId)`) |

## Configuration (env-driven)

| Variable                      | Default                      | Description                                    |
| ----------------------------- | ---------------------------- | ---------------------------------------------- |
| `PORT`                        | `8080`                       | HTTP listen port                               |
| `R2_ENDPOINT`                 | _(required)_                 | `https://<account>.r2.cloudflarestorage.com`   |
| `R2_ACCESS_KEY_ID`            | _(required)_                 | Admin S3 key                                   |
| `R2_SECRET_ACCESS_KEY`        | _(required)_                 | Admin S3 secret                                |
| `R2_BUCKET`                   | `universe-static-apps-01`    | Single shared bucket (prefix-scoped per site)  |
| `GH_CLIENT_ID`                | _(required)_                 | GitHub OAuth app client ID (CLI device flow)   |
| `GH_ORG`                      | `freeCodeCamp`               | GitHub org for team probes                     |
| `GH_API_BASE`                 | `https://api.github.com`     | GitHub REST API base                           |
| `SITES_YAML_PATH`             | `/etc/artemis/sites.yaml`    | Path to site→teams map                         |
| `JWT_SIGNING_KEY`             | _(required)_                 | 32-byte random; mounted from k8s Secret        |
| `JWT_TTL_SECONDS`             | `900`                        | Deploy-session JWT TTL (15 min)                |
| `GH_MEMBERSHIP_CACHE_TTL`     | `300`                        | GH `/user` + team membership cache TTL (5 min) |
| `ALIAS_PRODUCTION_KEY_FORMAT` | `<site>/production`          | R2 alias key for production env                |
| `ALIAS_PREVIEW_KEY_FORMAT`    | `<site>/preview`             | R2 alias key for preview env                   |
| `DEPLOY_PREFIX_FORMAT`        | `<site>/deploys/<ts>-<sha>/` | R2 prefix per immutable deploy                 |
| `LOG_LEVEL`                   | `info`                       | `debug`, `info`, `warn`, `error`               |

## R2 layout

```
<bucket>/
└── <site>/
    ├── deploys/
    │   ├── 20260420-141522-abc1234/   # immutable
    │   │   ├── index.html
    │   │   └── ...
    │   └── 20260421-091807-def5678/
    ├── preview                          # alias → "deploys/20260421-091807-def5678"
    └── production                       # alias → "deploys/20260420-141522-abc1234"
```

Atomic alias semantics: `PutObject` is atomic per-key in R2. Old deploy keeps serving until the alias `PUT` lands. Verify-then-PUT order means a partial deploy never becomes live.

## sites.yaml

```yaml
# /etc/artemis/sites.yaml — site → authorized GitHub teams
sites:
  www:
    teams:
      - team-eng
      - team-platform
  learn:
    teams:
      - team-eng
```

Hot-reloaded via `fsnotify`. On schema error the pod retains the last-good config and emits an alert.

## Local development

```sh
cp .env.example .env  # then fill values
make run              # boots HTTP server on $PORT
make test             # go test ./... -cover (unit only)
make image            # docker build
```

## Integration testing

End-to-end suite under `internal/integration/`. Build-tagged behind
`integration` so it stays out of `make test`. Hits a live, deployed
artemis over HTTPS and exercises the full deploy lifecycle:

```
healthz → whoami → init → upload → finalize(preview) → curl preview
       → promote → curl production → list deploys → rollback
```

Plus negative-path coverage (bad token → 401, missing token → 401,
unknown site → 403, missing required field → 400).

```sh
ARTEMIS_URL=https://uploads.freecode.camp \
  GH_TOKEN=$(gh auth token) \
  SITE=test ROOT_DOMAIN=freecode.camp \
  make integration
```

`make integration-help` prints the full env-var reference. The suite
is **safe to run against production** — it writes only under the `test`
site (a staff-only smoke target reserved in `config/sites.yaml`) and
relies on the cleanup cron (T22, 7-day retention) for prefix GC.

| Variable       | Default         | Purpose                                       |
| -------------- | --------------- | --------------------------------------------- |
| `ARTEMIS_URL`  | _(required)_    | Live artemis base URL, no trailing slash      |
| `GH_TOKEN`     | _(required)_    | GitHub bearer authorized for `SITE`           |
| `SITE`         | `test`          | Site key from `sites.yaml`                    |
| `ROOT_DOMAIN`  | `freecode.camp` | Root domain for preview/production URL derive |
| `PROD_SLO`     | `2m`            | Production-alias serve SLO (D38)              |
| `PREVIEW_SLO`  | `90s`           | Preview-alias serve SLO                       |
| `HTTP_TIMEOUT` | `30s`           | Per-request HTTP timeout                      |

## curl examples

```sh
# init a deploy
curl -X POST https://uploads.freecode.camp/api/deploy/init \
  -H "Authorization: Bearer $GITHUB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"site":"www","sha":"abc1234"}'
# → { "deployId": "20260420-141522-abc1234", "jwt": "<deploy-session-jwt>", "expiresAt": "..." }

# upload a file (deploy-session JWT)
curl -X PUT "https://uploads.freecode.camp/api/deploy/20260420-141522-abc1234/upload?path=index.html" \
  -H "Authorization: Bearer $DEPLOY_JWT" \
  --data-binary @index.html

# finalize → atomic alias
curl -X POST https://uploads.freecode.camp/api/deploy/20260420-141522-abc1234/finalize \
  -H "Authorization: Bearer $DEPLOY_JWT" \
  -H "Content-Type: application/json" \
  -d '{"mode":"preview"}'
# → { "url": "https://www.preview.freecode.camp" }

# promote preview → production
curl -X POST https://uploads.freecode.camp/api/site/www/promote \
  -H "Authorization: Bearer $GITHUB_TOKEN"

# rollback production
curl -X POST https://uploads.freecode.camp/api/site/www/rollback \
  -H "Authorization: Bearer $GITHUB_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"20260419-110000-old1234"}'

# whoami
curl https://uploads.freecode.camp/api/whoami -H "Authorization: Bearer $GITHUB_TOKEN"
# → { "login": "ahmadabdolsaheb", "authorizedSites": ["www","learn"] }
```

## License

BSD-3-Clause — see [`LICENSE`](LICENSE).
