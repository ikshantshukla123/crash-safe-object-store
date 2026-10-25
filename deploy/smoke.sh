#!/usr/bin/env bash
# Post-deploy smoke test: health, auth, bucket, PUT/GET checksum, node kill,
# quorum write while a node is down, heal, and three verified copies.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

BASE_URL=${BASE_URL:-http://localhost:8000}
DEMO_TOKEN=${DEMO_TOKEN:-}
COMPOSE=(docker compose --project-directory "$ROOT" -f deploy/docker-compose.yml)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

say() { printf '\n== %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

say "health"
curl -fsS "$BASE_URL/api/health" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["status"]=="ok", d; print("ok")'

say "register and create bucket"
BUCKET="smoke-$(date +%s)-$RANDOM"
EMAIL="$BUCKET@example.com"
TOKEN=$(curl -fsS -X POST "$BASE_URL/api/auth/register" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"smoke-password-123\"}" |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
curl -fsS -X POST "$BASE_URL/api/buckets" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$BUCKET\"}" >/dev/null
printf 'registered %s in bucket %s\n' "$EMAIL" "$BUCKET"

say "upload and download"
printf 'smoke payload %s\n' "$(date -u +%FT%TZ)" > "$TMP/object.txt"
SHA=$(python3 - "$TMP/object.txt" <<'PY'
import hashlib,sys
print(hashlib.sha256(open(sys.argv[1],'rb').read()).hexdigest())
PY
)
UPLOAD=$(curl -fsS -X PUT "$BASE_URL/api/buckets/$BUCKET/objects/hello.txt" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: text/plain' \
  --data-binary "@$TMP/object.txt")
VERSION=$(printf '%s' "$UPLOAD" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version_id"])')
ETAG=$(printf '%s' "$UPLOAD" | python3 -c 'import json,sys; print(json.load(sys.stdin)["etag"])')
[[ "$ETAG" == "$SHA" ]] || fail "ETag $ETAG != sha256 $SHA"
curl -fsS "$BASE_URL/api/buckets/$BUCKET/objects/hello.txt" -H "Authorization: Bearer $TOKEN" -o "$TMP/download.txt"
cmp "$TMP/object.txt" "$TMP/download.txt" || fail "downloaded bytes differ"
printf 'version %s verified\n' "$VERSION"

if [[ -z "$DEMO_TOKEN" ]]; then
  echo "DEMO_TOKEN unset; skipping node failure cycle"
  exit 0
fi

say "kill node1"
curl -fsS -X POST "$BASE_URL/chaos/kill/node1?token=$DEMO_TOKEN" -H "X-Demo-Token: $DEMO_TOKEN" >/dev/null
printf 'node1 killed\n'

say "upload while node1 is down (quorum still 2)"
printf 'written during node loss\n' > "$TMP/during.txt"
DURING=$(curl -fsS -X PUT "$BASE_URL/api/buckets/$BUCKET/objects/during.txt" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: text/plain' \
  --data-binary "@$TMP/during.txt")
DURING_VERSION=$(printf '%s' "$DURING" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version_id"])')
DURING_ETAG=$(printf '%s' "$DURING" | python3 -c 'import json,sys; print(json.load(sys.stdin)["etag"])')
printf 'version %s accepted\n' "$DURING_VERSION"

say "heal node1 and wait for repair"
curl -fsS -X POST "$BASE_URL/chaos/heal/node1?token=$DEMO_TOKEN" -H "X-Demo-Token: $DEMO_TOKEN" >/dev/null
CHUNK=""
for _ in $(seq 1 60); do
  CHUNK=$("${COMPOSE[@]}" exec -T postgres psql -U "${POSTGRES_USER:-objstore}" -d "${POSTGRES_DB:-objstore}" -tAc \
    "select id from chunks where version_id='$DURING_VERSION' and state='committed' limit 1" | tr -d '[:space:]')
  [[ -n "$CHUNK" ]] && break
  sleep 2
done
[[ -n "$CHUNK" ]] || fail "chunk row not found for $DURING_VERSION"

HEALTHY=0
for _ in $(seq 1 60); do
  HEALTHY=$("${COMPOSE[@]}" exec -T postgres psql -U "${POSTGRES_USER:-objstore}" -d "${POSTGRES_DB:-objstore}" -tAc \
    "select count(*) from chunk_replicas where chunk_id='$CHUNK' and status='healthy'" | tr -d '[:space:]')
  [[ "$HEALTHY" == "3" ]] && break
  sleep 2
done
[[ "$HEALTHY" == "3" ]] || fail "expected 3 healthy replicas for $CHUNK, got $HEALTHY"
printf 'repair complete: 3 healthy replicas\n'

say "compare chunk checksums across nodes"
PREFIX=${CHUNK:0:2}
for node in node1 node2 node3; do
  GOT=$("${COMPOSE[@]}" exec -T "$node" sha256sum "/data/chunks/$PREFIX/$CHUNK" | awk '{print $1}')
  [[ "$GOT" == "$DURING_ETAG" ]] || fail "$node checksum $GOT != $DURING_ETAG"
  printf '%s %s\n' "$node" "$GOT"
done

say "read after recovery"
curl -fsS "$BASE_URL/api/buckets/$BUCKET/objects/during.txt" -H "Authorization: Bearer $TOKEN" -o "$TMP/during-download.txt"
cmp "$TMP/during.txt" "$TMP/during-download.txt" || fail "recovered object differs"

printf '\nSMOKE OK\n'
