# Artemis — static-apps deploy proxy.
# Common dev tasks. Used by humans + CI.

GO              ?= go
GOFLAGS         ?=
PKG             := ./...
BIN             := bin/artemis
IMAGE           ?= ghcr.io/freecodecamp/artemis
VERSION         ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
COMMIT          ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)

.PHONY: all
all: build

.PHONY: build
build: ## Build the artemis binary into ./bin/artemis
	@mkdir -p bin
	$(GO) build $(GOFLAGS) -trimpath \
		-ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
		-o $(BIN) ./cmd/artemis

.PHONY: test
test: ## go test -race -cover (unit only — integration excluded by build tag)
	$(GO) test -race -cover $(PKG)

.PHONY: cover
cover: ## go test with coverage profile + html report (unit only)
	$(GO) test -race -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "open coverage.html"

# Integration suite — hits a live artemis deployment over HTTPS.
# Requires env: ARTEMIS_URL, GH_TOKEN. Optional: SITE, ROOT_DOMAIN,
# PROD_SLO, PREVIEW_SLO, HTTP_TIMEOUT. See `internal/integration/doc.go`.
.PHONY: integration
integration: ## go test -tags=integration ./internal/integration/... (live E2E)
	@if [ -z "$$ARTEMIS_URL" ]; then \
		echo "ARTEMIS_URL is required. See: make integration-help"; \
		exit 2; \
	fi
	@if [ -z "$$GH_TOKEN" ]; then \
		echo "GH_TOKEN is required (try: GH_TOKEN=\$$(gh auth token) make integration). See: make integration-help"; \
		exit 2; \
	fi
	$(GO) test -v -tags=integration -count=1 -timeout=10m ./internal/integration/...

.PHONY: integration-help
integration-help: ## Print integration-suite usage
	@echo "Integration suite — full E2E against a live artemis deployment."
	@echo
	@echo "Required env:"
	@echo "  ARTEMIS_URL   Base URL of a deployed artemis (no trailing slash)"
	@echo "  GH_TOKEN      GitHub bearer token authorized for SITE"
	@echo
	@echo "Optional env:"
	@echo "  SITE          Registered site slug          (default: test)"
	@echo "  ROOT_DOMAIN   Public root domain        (default: freecode.camp)"
	@echo "  PROD_SLO      Production-alias SLO      (default: 2m)"
	@echo "  PREVIEW_SLO   Preview-alias SLO         (default: 90s)"
	@echo "  HTTP_TIMEOUT  Per-request timeout       (default: 30s)"
	@echo
	@echo "Usage:"
	@echo "  ARTEMIS_URL=https://uploads.freecode.camp \\"
	@echo "    GH_TOKEN=\$$(gh auth token) \\"
	@echo "    SITE=test ROOT_DOMAIN=freecode.camp \\"
	@echo "    make integration"

.PHONY: lint
lint: ## go vet (CI also runs golangci-lint)
	$(GO) vet $(PKG)

.PHONY: run
run: ## Boot artemis locally — expects .env (loaded by direnv)
	$(GO) run ./cmd/artemis

.PHONY: preflight
preflight: ## Smoke-test Apollo-11 App creds against GitHub (reads GH_APP_* env)
	$(GO) run ./cmd/preflight

.PHONY: image
image: ## docker build — multi-stage distroless
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		.

.PHONY: tidy
tidy: ## go mod tidy
	$(GO) mod tidy

.PHONY: clean
clean: ## remove build artifacts
	rm -rf bin coverage.out coverage.html

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F ':.*?## ' '{printf "  %-12s %s\n", $$1, $$2}'
