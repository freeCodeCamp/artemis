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
test: ## go test -race -cover all packages
	$(GO) test -race -cover $(PKG)

.PHONY: cover
cover: ## go test with coverage profile + html report
	$(GO) test -race -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "open coverage.html"

.PHONY: lint
lint: ## go vet (CI also runs golangci-lint)
	$(GO) vet $(PKG)

.PHONY: run
run: ## Boot artemis locally — expects .env (loaded by direnv)
	$(GO) run ./cmd/artemis

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
