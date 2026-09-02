# Artemis — static-apps deploy proxy. Common dev tasks.

go := env_var_or_default("GO", "go")
gotestcoverage := "v2.19.0"
# CI resolves the toolchain from go.mod (setup-go go-version-file). A newer
# local Go reports different statement counts, which silently miscalibrates
# every coverage threshold. Pin local runs to the same toolchain.
export GOTOOLCHAIN := env_var_or_default("GOTOOLCHAIN", `awk '/^go /{n=split($2,p,"."); print "go" $2 (n<3 ? ".0" : ""); exit}' go.mod 2>/dev/null || echo auto`)
goflags := env_var_or_default("GOFLAGS", "")
pkg := "./..."
staticcheck := "honnef.co/go/tools/cmd/staticcheck@v0.8.1"
govulncheck := "golang.org/x/vuln/cmd/govulncheck@v1.7.0"
gofumpt := "mvdan.cc/gofumpt@v0.11.0"
goimports := "golang.org/x/tools/cmd/goimports@v0.49.0"
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
    {{go}} test -race -shuffle=on -cover {{pkg}}

# CI's coverage gate: statement coverage, every package, per .testcoverage.yml
covgate:
    {{go}} test -race -shuffle=on -coverprofile=coverage.out {{pkg}}
    {{go}} run github.com/vladopajic/go-test-coverage/v2@{{gotestcoverage}} --config=.testcoverage.yml \
        --threshold-file=70 --threshold-package=80 --threshold-total=80

# go test with coverage profile + html report (unit only)
cover:
    {{go}} test -race -shuffle=on -coverprofile=coverage.out {{pkg}}
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
    env -u ARTEMIS_LIFECYCLE_OK {{go}} test -v -tags=integration -count=1 -timeout=10m ./internal/integration/...

# Opt-in site-lifecycle E2E: delete / hold / undelete / release (artemis 1.10.0+)
integration-lifecycle:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -z "${ARTEMIS_URL:-}" ] || [ -z "${GH_TOKEN:-}" ]; then
        echo "ARTEMIS_URL and GH_TOKEN are required. See: just integration-help"
        exit 2
    fi
    if [ -z "${GH_APPROVER_TOKEN:-}" ]; then
        echo 'GH_APPROVER_TOKEN is required — without it the throwaway slug cannot be released and every run leaks a live site. See: just integration-help'
        exit 2
    fi
    ARTEMIS_LIFECYCLE_OK=1 {{go}} test -v -tags=integration -count=1 -timeout=60m \
        -run 'TestSiteLifecycle' ./internal/integration/...

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
    @echo "Site-lifecycle suite (delete / hold / undelete / release) — OPT-IN:"
    @echo "  ARTEMIS_LIFECYCLE_OK=1   Required. Registers, deletes and RELEASES a"
    @echo "                           throwaway slug on the target deployment."
    @echo "  GH_APPROVER_TOKEN        Required. A bearer on REPO_APPROVE_AUTHZ_TEAM;"
    @echo "                           without it the slug cannot be freed and every"
    @echo "                           run would leak a live site."
    @echo "  REGISTRY_TEAM            Team to register under (default: staff)"
    @echo "  Run it with: just integration-lifecycle  (carries the required timeout)"
    @echo "  Requires artemis 1.10.0+ — undelete and release do not exist before it."
    @echo
    @echo "Usage:"
    @echo '  ARTEMIS_URL=https://uploads.freecode.camp \'
    @echo '    GH_TOKEN=$(gh auth token) \'
    @echo "    SITE=test ROOT_DOMAIN=freecode.camp \\"
    @echo "    just integration"

# Real-Hatchet suite: spins up hatchet-lite via compose, mints a token, runs R3/R4/R5
hatchet-integration:
    #!/usr/bin/env bash
    set -euo pipefail
    cd test/integration/hatchet
    compose="docker compose -f compose.hatchet.yaml"
    tenant="707d0855-80ab-4e1f-a156-f1c4546cbf52"
    trap "$compose down -v" EXIT
    $compose up -d --wait
    token=$($compose exec -T hatchet-lite /hatchet-admin token create --config /config --tenant-id "$tenant" | tr -d '\r\n')
    if [ -z "$token" ]; then
        echo "hatchet-admin returned an empty tenant token"
        exit 1
    fi
    env -u ARTEMIS_LIFECYCLE_OK HATCHET_CLIENT_TOKEN="$token" \
        HATCHET_CLIENT_HOST_PORT="${HATCHET_CLIENT_HOST_PORT:-127.0.0.1:7077}" \
        HATCHET_CLIENT_TLS_STRATEGY=none \
        HATCHET_COMPOSE_FILE="$PWD/compose.hatchet.yaml" \
        {{go}} test -v -race -tags=integration -count=${HATCHET_COUNT:-1} -timeout=20m ../../../internal/hatchet/...

# Repeat one test N times in a single binary against one stack, and report the
# flake rate. Measures the test, not the docker-compose setup a shell loop
# would rebuild on every iteration.
flake test n="20" pkg="./internal/...":
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{n}}" in ''|*[!0-9]*) echo "n must be a positive integer, got '{{n}}'"; exit 2;; esac
    echo "running {{test}} x{{n}} in one binary"
    set +e
    ARTEMIS_RUN_QUARANTINED=1 {{go}} test -race -tags=integration -count={{n}} -run '{{test}}' -timeout=30m {{pkg}} > /tmp/flake.log 2>&1
    code=$?
    set -e
    ran=$(grep -cE '^(\s*)--- (PASS|FAIL|SKIP): ' /tmp/flake.log || true)
    if [ "$ran" -eq 0 ]; then
        echo "NO TESTS RAN — '{{test}}' matched nothing in {{pkg}}. This is not a pass."
        tail -5 /tmp/flake.log
        exit 2
    fi
    fails=$(grep -cE '^\s*--- FAIL' /tmp/flake.log || true)
    if [ "$code" -eq 0 ]; then
        echo "PASS — {{n}} iterations, ${ran} test results, 0 failures"
    else
        echo "FAIL — {{n}} iterations, ${ran} test results, ${fails} failures; see /tmp/flake.log"
        grep -E '^\s*--- FAIL|Error:|Messages:' /tmp/flake.log | head -20
        exit 1
    fi

# List every quarantined test: the quarantine.Skip call sites are the registry.
quarantined:
    scripts/quarantine-check.sh list

# CI gate: expired, non-literal, aliased or production-linked quarantines fail here, never as a flaky red.
quarantine-check:
    scripts/quarantine-check.sh check

# Repeat the Hatchet suite N times against ONE compose stack (HATCHET_COUNT).
flake-hatchet n="3":
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{n}}" in ''|*[!0-9]*) echo "n must be a positive integer, got '{{n}}'"; exit 2;; esac
    HATCHET_COUNT={{n}} just hatchet-integration

# go vet under every build tag CI vets
lint:
    {{go}} vet {{pkg}}
    {{go}} vet -tags=load {{pkg}}
    {{go}} vet -tags=e2e {{pkg}}
    {{go}} vet -tags=integration {{pkg}}

# staticcheck under every build tag; version pinned, no go.mod entry
staticcheck:
    {{go}} run {{staticcheck}} {{pkg}}
    {{go}} run {{staticcheck}} -tags=load {{pkg}}
    {{go}} run {{staticcheck}} -tags=e2e {{pkg}}
    {{go}} run {{staticcheck}} -tags=integration {{pkg}}

# Reachable-vulnerability scan; exits non-zero only on a call path we reach
vulncheck:
    {{go}} run {{govulncheck}} {{pkg}}

# Rewrite every Go file: gofumpt (stricter gofmt), then goimports
fmt:
    {{go}} run {{gofumpt}} -w .
    {{go}} run {{goimports}} -w .

# CI's formatting gate: prints every unformatted file and fails on any
fmtcheck:
    #!/usr/bin/env bash
    set -euo pipefail
    out=$({{go}} run {{gofumpt}} -l . ; {{go}} run {{goimports}} -l .)
    if [ -n "$out" ]; then printf '%s\n' "$out"; echo "unformatted Go: run 'just fmt'"; exit 1; fi

# Boot artemis locally — expects .env (loaded by direnv)
run:
    {{go}} run ./cmd/artemis

# Smoke-test Apollo-11 App creds against GitHub (reads GH_APP_* env)
preflight:
    {{go}} run ./cmd/preflight

# Boot the full local stack (valkey + minio + fakegithub + artemis)
compose-up:
    docker compose up -d --build

# Tear down the local stack and its volumes
compose-down:
    docker compose down -v

# Tail artemis logs from the running local stack
compose-logs:
    docker compose logs -f artemis

# Full repo create->approve->list E2E against the local stack
smoke:
    ./scripts/smoke.sh

# Full-stack E2E: boots artemis + pg + valkey + minio + hatchet, runs the e2e suite
e2e-local:
    ./scripts/e2e-local.sh

# Scalability load harness: ephemeral pg + registry/outbox/gc throughput (R14)
loadgen:
    ./scripts/loadgen.sh

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
