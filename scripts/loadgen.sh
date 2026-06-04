#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

GO="${GO:-go}"
SITES="${SITES:-500}"
DEPLOYS_PER_SITE="${DEPLOYS_PER_SITE:-40}"
CONCURRENCY="${CONCURRENCY:-16}"
PG_HOST_PORT="${PG_HOST_PORT:-55433}"
KEEP="${KEEP_STACK:-0}"
CONTAINER="artemis-loadgen-pg"

cleanup() {
  if [[ "$KEEP" != 1 ]]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
echo >&2 "==> starting ephemeral postgres on :${PG_HOST_PORT}"
docker run -d --name "$CONTAINER" \
  -e POSTGRES_USER=artemis -e POSTGRES_PASSWORD=artemis -e POSTGRES_DB=artemis \
  -p "${PG_HOST_PORT}:5432" postgres:17-alpine \
  -c max_connections=200 -c shared_buffers=256MB >/dev/null

echo >&2 "==> waiting for postgres"
ok=0
for _ in $(seq 1 30); do
  if docker exec "$CONTAINER" pg_isready -U artemis -d artemis >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 1
done
if [[ "$ok" != 1 ]]; then
  echo "FATAL: postgres never became ready on :${PG_HOST_PORT}" >&2
  docker logs "$CONTAINER" 2>/dev/null | tail -40
  exit 1
fi

DSN="${LOADGEN_DATABASE_URL:-postgres://artemis:artemis@localhost:${PG_HOST_PORT}/artemis?sslmode=disable}"
echo >&2 "==> running load harness sites=${SITES} deploys-per-site=${DEPLOYS_PER_SITE} concurrency=${CONCURRENCY}"
LOADGEN_DATABASE_URL="$DSN" "$GO" run -tags=load ./cmd/loadgen \
  -sites "$SITES" -deploys-per-site "$DEPLOYS_PER_SITE" -concurrency "$CONCURRENCY"
