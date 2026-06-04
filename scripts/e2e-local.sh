#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

GO="${GO:-go}"
COMPOSE_FILE="test/e2e/compose.e2e.yaml"
COMPOSE="docker compose -f ${COMPOSE_FILE}"
TENANT="${HATCHET_TENANT_ID:-707d0855-80ab-4e1f-a156-f1c4546cbf52}"
KEEP="${KEEP_STACK:-0}"

ARTEMIS_HOST_PORT="${ARTEMIS_HOST_PORT:-8080}"
PG_HOST_PORT="${PG_HOST_PORT:-55432}"
MINIO_HOST_PORT="${MINIO_HOST_PORT:-59000}"
HATCHET_GRPC_HOST_PORT="${HATCHET_GRPC_HOST_PORT:-7077}"
HATCHET_DASHBOARD_HOST_PORT="${HATCHET_DASHBOARD_HOST_PORT:-8888}"
export ARTEMIS_HOST_PORT PG_HOST_PORT MINIO_HOST_PORT
export HATCHET_GRPC_HOST_PORT HATCHET_DASHBOARD_HOST_PORT

ARTEMIS_URL="http://localhost:${ARTEMIS_HOST_PORT}"

TMP="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP"
  if [[ "$KEEP" != 1 ]]; then
    ${COMPOSE} down -v >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "==> minting ephemeral Apollo-11-style App keypair"
openssl genrsa -out "$TMP/app.key" 2048 2>/dev/null
openssl rsa -in "$TMP/app.key" -pubout -out "$TMP/app.pub" 2>/dev/null
GH_APP_PRIVATE_KEY="$(cat "$TMP/app.key")"
FAKE_GH_APP_PUBLIC_KEY="$(cat "$TMP/app.pub")"
export GH_APP_PRIVATE_KEY FAKE_GH_APP_PUBLIC_KEY

echo "==> minting self-signed TLS cert for the minio R2 stub"
CERTS_DIR="$TMP/certs"
mkdir -p "$CERTS_DIR"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$CERTS_DIR/private.key" -out "$CERTS_DIR/public.crt" \
  -subj "/CN=minio" \
  -addext "subjectAltName=DNS:minio,DNS:localhost,IP:127.0.0.1" 2>/dev/null
chmod 0644 "$CERTS_DIR/private.key" "$CERTS_DIR/public.crt"
export E2E_CERTS_DIR="$CERTS_DIR"

echo "==> bringing up hatchet engine (postgres + hatchet-lite)"
${COMPOSE} up -d --wait hatchet-postgres hatchet-lite

echo "==> minting hatchet client token"
HATCHET_CLIENT_TOKEN="$(${COMPOSE} exec -T hatchet-lite \
  /hatchet-admin token create --config /config --tenant-id "$TENANT" | tr -d '\r\n')"
export HATCHET_CLIENT_TOKEN
if [[ -z "$HATCHET_CLIENT_TOKEN" ]]; then
  echo "FATAL: hatchet token mint returned empty" >&2
  exit 2
fi

echo "==> bringing up full stack (postgres + valkey + minio + fakegithub + artemis)"
${COMPOSE} up -d --build --wait \
  postgres valkey minio minio-setup fakegithub artemis

echo "==> waiting for artemis /readyz at ${ARTEMIS_URL}"
ok=0
for _ in $(seq 1 60); do
  if curl -fsS "${ARTEMIS_URL}/readyz" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
if [[ "$ok" != 1 ]]; then
  echo "FATAL: artemis /readyz never green" >&2
  ${COMPOSE} logs artemis 2>/dev/null | tail -60
  exit 1
fi
echo "    readyz green"

echo "==> waiting for fakegithub-backed auth surface (whoami)"
GH_TOKEN_PROBE="${E2E_GH_TOKEN:-smoke-token-local}"
ok=0
for _ in $(seq 1 30); do
  code="$(curl -s -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${GH_TOKEN_PROBE}" "${ARTEMIS_URL}/api/whoami")"
  if [[ "$code" == "200" ]]; then
    ok=1
    break
  fi
  sleep 2
done
if [[ "$ok" != 1 ]]; then
  echo "FATAL: /api/whoami never returned 200 (fakegithub not reachable from artemis)" >&2
  ${COMPOSE} logs artemis fakegithub 2>/dev/null | tail -60
  exit 1
fi
echo "    auth surface green"

echo "==> running e2e suite (-tags=e2e) against ${ARTEMIS_URL}"
ARTEMIS_URL="${ARTEMIS_URL}" \
  AWS_CA_BUNDLE="${CERTS_DIR}/public.crt" \
  E2E_R2_CA_FILE="${CERTS_DIR}/public.crt" \
  E2E_PG_DSN="postgres://artemis:artemis@localhost:${PG_HOST_PORT}/artemis?sslmode=disable" \
  E2E_R2_ENDPOINT="https://localhost:${MINIO_HOST_PORT}" \
  E2E_R2_ACCESS_KEY_ID="minioadmin" \
  E2E_R2_SECRET_ACCESS_KEY="minioadmin" \
  E2E_R2_BUCKET="universe-static-apps-01" \
  E2E_GH_TOKEN="${E2E_GH_TOKEN:-smoke-token-local}" \
  "${GO}" test -tags=e2e -count=1 -timeout=10m -v ./test/e2e/...
