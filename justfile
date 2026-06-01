# Artemis — static-apps deploy proxy. Common dev tasks.

go := env_var_or_default("GO", "go")
goflags := env_var_or_default("GOFLAGS", "")
pkg := "./..."
bin := "bin/artemis"
image := env_var_or_default("IMAGE", "ghcr.io/freecodecamp/artemis")
version := `git rev-parse --short HEAD 2>/dev/null || echo dev`
commit := `git rev-parse HEAD 2>/dev/null || echo unknown`

# List available recipes
default:
    @just --list

# Build the artemis binary into ./bin/artemis
build:
    @mkdir -p bin
    {{go}} build {{goflags}} -trimpath \
        -ldflags="-s -w -X main.version={{version}} -X main.commit={{commit}}" \
        -o {{bin}} ./cmd/artemis

# go test -race -cover (unit only — integration excluded by build tag)
test:
    {{go}} test -race -cover {{pkg}}

# go test with coverage profile + html report (unit only)
cover:
    {{go}} test -race -coverprofile=coverage.out {{pkg}}
    {{go}} tool cover -html=coverage.out -o coverage.html
    @echo "open coverage.html"

# go test -tags=integration ./internal/integration/... (live E2E)
integration:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -z "${ARTEMIS_URL:-}" ]; then
        echo "ARTEMIS_URL is required. See: just integration-help"
        exit 2
    fi
    if [ -z "${GH_TOKEN:-}" ]; then
        echo 'GH_TOKEN is required (try: GH_TOKEN=$(gh auth token) just integration). See: just integration-help'
        exit 2
    fi
    {{go}} test -v -tags=integration -count=1 -timeout=10m ./internal/integration/...

# Print integration-suite usage
integration-help:
    @echo "Integration suite — full E2E against a live artemis deployment."
    @echo
    @echo "Required env:"
    @echo "  ARTEMIS_URL   Base URL of a deployed artemis (no trailing slash)"
    @echo "  GH_TOKEN      GitHub bearer token authorized for SITE"
    @echo
    @echo "Optional env:"
    @echo "  SITE          Registered site slug      (default: test)"
    @echo "  ROOT_DOMAIN   Public root domain        (default: freecode.camp)"
    @echo "  PROD_SLO      Production-alias SLO       (default: 2m)"
    @echo "  PREVIEW_SLO   Preview-alias SLO          (default: 90s)"
    @echo "  HTTP_TIMEOUT  Per-request timeout        (default: 30s)"
    @echo
    @echo "Usage:"
    @echo '  ARTEMIS_URL=https://uploads.freecode.camp \'
    @echo '    GH_TOKEN=$(gh auth token) \'
    @echo "    SITE=test ROOT_DOMAIN=freecode.camp \\"
    @echo "    just integration"

# go vet (CI also runs golangci-lint)
lint:
    {{go}} vet {{pkg}}

# Boot artemis locally — expects .env (loaded by direnv)
run:
    {{go}} run ./cmd/artemis

# Smoke-test Apollo-11 App creds against GitHub (reads GH_APP_* env)
preflight:
    {{go}} run ./cmd/preflight

# docker build — multi-stage distroless
image:
    docker build \
        --build-arg VERSION={{version}} \
        --build-arg COMMIT={{commit}} \
        -t {{image}}:{{version}} \
        -t {{image}}:latest \
        .

# go mod tidy
tidy:
    {{go}} mod tidy

# remove build artifacts
clean:
    rm -rf bin coverage.out coverage.html
