#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

BASE="${ARTEMIS_URL:-http://localhost:8080}"
TOKEN="${SMOKE_TOKEN:-smoke-token-local}"
KEEP="${KEEP_STACK:-0}"

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() {
  printf '  \033[31mFAIL\033[0m %s\n' "$1"
  [ "$KEEP" = 1 ] || docker compose logs artemis 2>/dev/null | tail -50
  exit 1
}

TMP="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP"
  if [ "$KEEP" != 1 ]; then docker compose down -v >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "==> minting ephemeral Apollo-11-style App keypair"
openssl genrsa -out "$TMP/app.key" 2048 2>/dev/null
openssl rsa -in "$TMP/app.key" -pubout -out "$TMP/app.pub" 2>/dev/null
GH_APP_PRIVATE_KEY="$(cat "$TMP/app.key")"
FAKE_GH_APP_PUBLIC_KEY="$(cat "$TMP/app.pub")"
export GH_APP_PRIVATE_KEY FAKE_GH_APP_PUBLIC_KEY

echo "==> bringing up stack (valkey + minio + fakegithub + artemis)"
docker compose up -d --build

echo "==> waiting for /readyz"
ok=0
for _ in $(seq 1 45); do
  if curl -fsS "$BASE/readyz" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
if [ "$ok" = 1 ]; then pass "readyz green (valkey + R2 reachable)"; else fail "readyz never green"; fi

echo "==> GET /api/whoami"
who="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$BASE/api/whoami")"
if printf '%s' "$who" | grep -q '"login":"smoke-bot"'; then pass "whoami: $who"; else fail "whoami: $who"; fi

echo "==> GET /api/repo/templates"
tmpl="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$BASE/api/repo/templates")"
if printf '%s' "$tmpl" | grep -q 'universe-static-template'; then pass "templates: $tmpl"; else fail "templates: $tmpl"; fi

echo "==> POST /api/repo (create -> pending)"
created="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"smoke-repo","visibility":"public"}' "$BASE/api/repo")"
id="$(printf '%s' "$created" | sed -E 's/.*"id":"([^"]+)".*/\1/')"
if printf '%s' "$created" | grep -q '"status":"pending"'; then pass "create id=$id"; else fail "create: $created"; fi

echo "==> POST /api/repo/$id/approve (App creates repo -> active)"
approved="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" "$BASE/api/repo/$id/approve")"
if printf '%s' "$approved" | grep -q '"outcome":"ok"'; then pass "approve outcome ok"; else fail "approve: $approved"; fi
if printf '%s' "$approved" | grep -q '"status":"active"'; then pass "repo now active"; else fail "approve status: $approved"; fi

echo "==> GET /api/repos?status=active (list shows active row)"
list="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$BASE/api/repos?status=active")"
if printf '%s' "$list" | grep -q 'smoke-repo'; then pass "list contains active smoke-repo"; else fail "list: $list"; fi

printf '\n\033[32mSMOKE OK\033[0m — full repo create->approve->list E2E green against the local stack.\n'
