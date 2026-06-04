# syntax=docker/dockerfile:1.7

# ---- builder ----
# Digest pinned for reproducible builds. Refresh via:
#   docker buildx imagetools inspect golang:1.26.4-alpine
FROM golang:1.26.4-alpine@sha256:103c743516b0d9dd69c203ed64f730eb342cae4b85d3f6c5cb376d91abbc6bcb AS builder
WORKDIR /src

# Copy go.mod / go.sum first to maximize layer cache reuse on dep changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown

# Static binary — distroless final stage cannot resolve dynamic libs.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /out/artemis ./cmd/artemis

# ---- final ----
# Digest pinned for reproducible builds. Refresh via:
#   docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12:nonroot@sha256:a9329520abc449e3b14d5bc3a6ffae065bdde0f02667fa10880c49b35c109fd1
WORKDIR /app

COPY --from=builder /out/artemis /app/artemis

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/app/artemis"]
