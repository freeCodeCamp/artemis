# syntax=docker/dockerfile:1.7

# ---- builder ----
FROM golang:1.26.2-alpine AS builder
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
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=builder /out/artemis /app/artemis

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/app/artemis"]
