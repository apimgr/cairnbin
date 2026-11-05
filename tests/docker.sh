#!/usr/bin/env bash
# @@License : WTFPL
# tests/docker.sh — Docker integration tests for cairnbin
# Builds the local image, brings it up via the mandated
# docker/docker-compose.test.yml (copied to a temp dir, never run from the
# project tree), and exercises the API with content negotiation and endpoint
# coverage per PART 28 requirements.
# Usage: bash tests/docker.sh [--keep]
# Options:
#   --keep   Do not tear down the stack / temp dir after the test run

set -euo pipefail

# ── Variables ────────────────────────────────────────────────────────────────
DOCKER_SCRIPT_NAME="$(basename -- "$0")"
DOCKER_PROJECT_DIR="$(cd -- "$(dirname -- "$0")/.." && pwd -P)"
DOCKER_PROJECT_ORG="apimgr"
DOCKER_PROJECT_NAME="cairnbin"
# Local build tag — the temp compose file is rewritten to use this image with
# pull_policy:never so we exercise the current source, not a published image.
DOCKER_IMAGE_NAME="${DOCKER_PROJECT_NAME}:test-$$"
DOCKER_COMPOSE_PROJECT="${DOCKER_PROJECT_NAME}-test-$$"
# Fixed by docker/docker-compose.test.yml (Test context → 172.17.0.1:64581:80).
# The compose file binds the published port to the Docker bridge gateway IP
# only (AI.md PART 26 "Production binds to Docker bridge IP for security"),
# never 127.0.0.1/all interfaces — the host reaches it via the bridge
# address, not loopback.
DOCKER_CONTAINER_NAME="${DOCKER_PROJECT_NAME}-test"
DOCKER_PORT="64581"
DOCKER_BRIDGE_IP="172.17.0.1"
DOCKER_BASE_URL="http://${DOCKER_BRIDGE_IP}:${DOCKER_PORT}"
DOCKER_TEMP_DIR=""
DOCKER_KEEP=false
DOCKER_ERRORS=0
DOCKER_TORMIG_NAME="${DOCKER_PROJECT_NAME}-tormig-$$"
DOCKER_TORMIG_VOLUME="${DOCKER_PROJECT_NAME}-tormig-vol-$$"

# ── Helpers ──────────────────────────────────────────────────────────────────
__pass() { printf "[PASS] %s\n" "$*"; }
__fail() { printf "[FAIL] %s\n" "$*" >&2; DOCKER_ERRORS=$((DOCKER_ERRORS + 1)); }
__info() { printf "[INFO] %s\n" "$*"; }

__check_status() {
    local desc="$1" method="$2" url="$3" expected="$4"
    shift 4
    local code
    code=$(curl -q -s -o /dev/null -w "%{http_code}" -X "${method}" "$@" "${url}")
    if [[ "${code}" == "${expected}" ]]; then
        __pass "${desc} → ${code}"
    else
        __fail "${desc} → expected ${expected}, got ${code}"
    fi
}

__check_body() {
    local desc="$1" url="$2" pattern="$3"
    shift 3
    local body
    body=$(curl -q -sf "$@" "${url}" 2>/dev/null || true)
    if printf '%s' "${body}" | grep -qi -- "${pattern}"; then
        __pass "${desc}"
    else
        __fail "${desc} — pattern '${pattern}' not found in: ${body}"
    fi
}

__cleanup() {
    if [[ "${DOCKER_KEEP}" == "false" ]]; then
        if [[ -n "${DOCKER_TEMP_DIR}" && -f "${DOCKER_TEMP_DIR}/docker-compose.yml" ]]; then
            __info "Tearing down compose project ${DOCKER_COMPOSE_PROJECT}"
            (cd "${DOCKER_TEMP_DIR}" && docker compose -p "${DOCKER_COMPOSE_PROJECT}" down -v 2>/dev/null) || true
        fi
        docker rmi -f "${DOCKER_IMAGE_NAME}" 2>/dev/null || true
        [[ -n "${DOCKER_TEMP_DIR}" && -d "${DOCKER_TEMP_DIR}" ]] && rm -rf "${DOCKER_TEMP_DIR}"
        docker rm -f "${DOCKER_TORMIG_NAME}" >/dev/null 2>&1 || true
        docker volume rm -f "${DOCKER_TORMIG_VOLUME}" >/dev/null 2>&1 || true
    else
        __info "Keeping compose project ${DOCKER_COMPOSE_PROJECT} and temp dir ${DOCKER_TEMP_DIR} (--keep flag set)"
        __info "Keeping Tor migration container ${DOCKER_TORMIG_NAME} and volume ${DOCKER_TORMIG_VOLUME} (--keep flag set)"
    fi
}
trap __cleanup EXIT

# ── Argument parsing ─────────────────────────────────────────────────────────
for arg in "$@"; do
    case "${arg}" in
        --keep) DOCKER_KEEP=true ;;
        *) printf "%s: unknown argument: %s\n" "${DOCKER_SCRIPT_NAME}" "${arg}" >&2; exit 2 ;;
    esac
done

# ── Build test image ─────────────────────────────────────────────────────────
__info "Building test image ${DOCKER_IMAGE_NAME}"
docker build \
    --file "${DOCKER_PROJECT_DIR}/docker/Dockerfile" \
    --tag "${DOCKER_IMAGE_NAME}" \
    "${DOCKER_PROJECT_DIR}"

# ── Prepare compose stack in a temp dir (PART 28: never run from project tree) ─
mkdir -p "${TMPDIR:-/tmp}/${DOCKER_PROJECT_ORG}"
DOCKER_TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/${DOCKER_PROJECT_ORG}/${DOCKER_PROJECT_NAME}-XXXXXX")"
mkdir -p "${DOCKER_TEMP_DIR}/volumes/config" "${DOCKER_TEMP_DIR}/volumes/data"
mkdir -p "${DOCKER_TEMP_DIR}/volumes/config/cairnbin"
cat >"${DOCKER_TEMP_DIR}/volumes/config/cairnbin/server.yml" <<'EOF'
server:
  geoip:
    enabled: false
  tor:
    binary: /__cairnbin_e2e__/tor
  notifications:
    email:
      enabled: false
      smtp:
        host: localhost
rate_limit:
  enabled: true
  write:
    requests: 1000
    window: 60
  global_burst: 2000
EOF
chmod 600 "${DOCKER_TEMP_DIR}/volumes/config/cairnbin/server.yml"
mkdir -p "${DOCKER_TEMP_DIR}/tormig-config/cairnbin"
cat >"${DOCKER_TEMP_DIR}/tormig-config/cairnbin/server.yml" <<'EOF'
server:
  geoip:
    enabled: false
  notifications:
    email:
      enabled: false
      smtp:
        host: localhost
EOF
chmod 600 "${DOCKER_TEMP_DIR}/tormig-config/cairnbin/server.yml"
cp "${DOCKER_PROJECT_DIR}/docker/docker-compose.test.yml" "${DOCKER_TEMP_DIR}/docker-compose.yml"
# Point the stack at the locally built image and never pull a published one.
sed -i \
    -e "s|image: ghcr.io/apimgr/cairnbin:.*|image: ${DOCKER_IMAGE_NAME}|" \
    -e "s|pull_policy: always|pull_policy: never|" \
    "${DOCKER_TEMP_DIR}/docker-compose.yml"

# ── Start stack ──────────────────────────────────────────────────────────────
__info "Starting compose project ${DOCKER_COMPOSE_PROJECT} on port ${DOCKER_PORT}"
(cd "${DOCKER_TEMP_DIR}" && docker compose -p "${DOCKER_COMPOSE_PROJECT}" up --detach)

# Wait for server to become ready (up to 30 s)
__info "Waiting for server to start..."
for ((i = 1; i <= 30; i++)); do
    if curl -q --silent --fail "${DOCKER_BASE_URL}/server/healthz" >/dev/null 2>&1; then
        break
    fi
    sleep 1
    if [[ "${i}" -eq 30 ]]; then
        (cd "${DOCKER_TEMP_DIR}" && docker compose -p "${DOCKER_COMPOSE_PROJECT}" logs) >&2 || true
        __fail "Server did not start within 30 seconds"
        exit 1
    fi
done
__pass "Server is up"
if docker logs "${DOCKER_CONTAINER_NAME}" 2>&1 | grep -q -- "privilege: dropped to ${DOCKER_PROJECT_NAME}"; then
    __pass "Server dropped to the dedicated ${DOCKER_PROJECT_NAME} account"
else
    __fail "Server did not drop to the dedicated ${DOCKER_PROJECT_NAME} account"
fi

# ── Health endpoints ──────────────────────────────────────────────────────────
__check_status "GET /server/healthz"        GET "${DOCKER_BASE_URL}/server/healthz"        200
__check_status "GET /api/v1/server/healthz" GET "${DOCKER_BASE_URL}/api/v1/server/healthz" 200

# Healthz must include required fields
__check_body "healthz has version field" "${DOCKER_BASE_URL}/api/v1/server/healthz" "version"
__check_body "healthz has status field"  "${DOCKER_BASE_URL}/api/v1/server/healthz" "ok\|status\|healthy"

# ── Content negotiation: frontend routes ─────────────────────────────────────
__info "--- Content negotiation: frontend routes ---"
# Home page — browser gets HTML, plain-text client gets text
__check_body "/ Accept:text/html → HTML"  "${DOCKER_BASE_URL}/" "<!DOCTYPE html\|<html" \
    --header "Accept: text/html"
__check_status "/ Accept:text/plain → 200" GET "${DOCKER_BASE_URL}/" 200 \
    --header "Accept: text/plain"
__check_status "GET /recent → 200"         GET "${DOCKER_BASE_URL}/recent" 200
__check_status "GET /create → 200"         GET "${DOCKER_BASE_URL}/create" 200

# ── Content negotiation: API routes ─────────────────────────────────────────
__info "--- Content negotiation: API routes ---"
__check_body   "GET /api/v1/pastes → JSON"  "${DOCKER_BASE_URL}/api/v1/pastes" '"ok"' \
    --header "Accept: application/json"
__check_status "GET /api/v1/pastes → 200"   GET "${DOCKER_BASE_URL}/api/v1/pastes" 200
# Plain-text negotiation on the same route
__check_status "GET /api/v1/pastes Accept:text/plain → 200" GET "${DOCKER_BASE_URL}/api/v1/pastes" 200 \
    --header "Accept: text/plain"
# .txt extension alias
__check_status "GET /api/v1/pastes.txt → 200" GET "${DOCKER_BASE_URL}/api/v1/pastes.txt" 200

# ── Create paste: multiple request formats ───────────────────────────────────
__info "--- Paste creation ---"

DOCKER_CSRF_TOKEN=$(curl -q -sS -D - -o /dev/null "${DOCKER_BASE_URL}/create" | \
    sed -n 's/^Set-Cookie: csrf_token=\([^;]*\).*/\1/ip' | head -1)
[[ -n "${DOCKER_CSRF_TOKEN}" ]] || __fail "Could not obtain CSRF token from /create"
DOCKER_CSRF_ARGS=(--cookie "csrf_token=${DOCKER_CSRF_TOKEN}" --header "X-CSRF-Token: ${DOCKER_CSRF_TOKEN}")

# JSON body
DOCKER_RESPONSE=$(curl -q -sf \
    --header "Content-Type: application/json" \
    --header "Accept: application/json" \
    "${DOCKER_CSRF_ARGS[@]}" \
    --data '{"content":"hello from docker test","language":"text"}' \
    "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (JSON): request failed"
DOCKER_PASTE_ID=$(printf '%s' "${DOCKER_RESPONSE}" | grep -o -- '"id": *"[^"]*"' | head -1 | cut -d'"' -f4)
DOCKER_OWNER_TOKEN=$(printf '%s' "${DOCKER_RESPONSE}" | grep -o -- '"owner_token": *"[^"]*"' | cut -d'"' -f4)
[[ -n "${DOCKER_PASTE_ID}" ]] || __fail "Create paste (JSON): no ID in response: ${DOCKER_RESPONSE}"
__pass "Create paste (JSON): id=${DOCKER_PASTE_ID}"

# Raw body (curl pipe style)
DOCKER_RAW_RESPONSE=$(curl -q -sf \
    --header "Content-Type: text/plain" \
    --header "Accept: application/json" \
    "${DOCKER_CSRF_ARGS[@]}" \
    --data "raw paste from curl" \
    "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (raw): request failed"
printf '%s' "${DOCKER_RAW_RESPONSE}" | grep -q -- '"id"' || \
    __fail "Create paste (raw): no id in response: ${DOCKER_RAW_RESPONSE}"
__pass "Create paste (raw body)"

# Form submission (browser style — should redirect)
# --no-location: report the initial response code, not a followed redirect's
# (some environments set "location" in .curlrc, which would otherwise make
# curl auto-follow the 303 and report the redirected page's 200 instead)
DOCKER_FORM_CODE=$(curl -q -s -o /dev/null -w "%{http_code}" --no-location \
    --header "Content-Type: application/x-www-form-urlencoded" \
    "${DOCKER_CSRF_ARGS[@]}" \
    --data "content=form+paste+test" \
    "${DOCKER_BASE_URL}/pastes")
[[ "${DOCKER_FORM_CODE}" =~ ^(201|303|302)$ ]] || \
    __fail "Create paste (form): expected 201/302/303, got ${DOCKER_FORM_CODE}"
__pass "Create paste (form): code=${DOCKER_FORM_CODE}"

# With expiry
DOCKER_EXP_RESPONSE=$(curl -q -sf \
    --header "Content-Type: application/json" \
    --header "Accept: application/json" \
    "${DOCKER_CSRF_ARGS[@]}" \
    --data '{"content":"expiring paste","expires_in":"1h"}' \
    "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (expiry): request failed"
printf '%s' "${DOCKER_EXP_RESPONSE}" | grep -q -- '"expires_at"' || \
    __fail "Create paste (expiry): expires_at missing in: ${DOCKER_EXP_RESPONSE}"
__pass "Create paste (with expiry)"

# With burn_after
DOCKER_BURN_RESPONSE=$(curl -q -sf \
    --header "Content-Type: application/json" \
    --header "Accept: application/json" \
    "${DOCKER_CSRF_ARGS[@]}" \
    --data '{"content":"burn after 2","burn_after":2}' \
    "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (burn_after): request failed"
printf '%s' "${DOCKER_BURN_RESPONSE}" | grep -q -- '"burn_after"' || \
    __fail "Create paste (burn_after): field missing in: ${DOCKER_BURN_RESPONSE}"
__pass "Create paste (burn_after=2)"

# Unlisted
DOCKER_UNLIST_RESPONSE=$(curl -q -sf \
    --header "Content-Type: application/json" \
    --header "Accept: application/json" \
    "${DOCKER_CSRF_ARGS[@]}" \
    --data '{"content":"unlisted paste","visibility":"unlisted"}' \
    "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (unlisted): request failed"
printf '%s' "${DOCKER_UNLIST_RESPONSE}" | grep -q -- '"visibility"' || \
    __fail "Create paste (unlisted): field missing"
__pass "Create paste (unlisted)"

# Link paste (is_link)
DOCKER_LINK_TARGET="https://example.com/docker-test-target"
DOCKER_LINK_RESPONSE=$(curl -q -sf \
    --header "Content-Type: application/json" \
    --header "Accept: application/json" \
    "${DOCKER_CSRF_ARGS[@]}" \
    --data "{\"content\":\"${DOCKER_LINK_TARGET}\",\"is_link\":true}" \
    "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (link): request failed"
DOCKER_LINK_ID=$(printf '%s' "${DOCKER_LINK_RESPONSE}" | grep -o -- '"id": *"[^"]*"' | head -1 | cut -d'"' -f4)
[[ -n "${DOCKER_LINK_ID}" ]] || __fail "Create paste (link): no ID in response: ${DOCKER_LINK_RESPONSE}"
printf '%s' "${DOCKER_LINK_RESPONSE}" | grep -q -- '"is_link": *true' || \
    __fail "Create paste (link): is_link field missing/false in: ${DOCKER_LINK_RESPONSE}"
__pass "Create paste (link): id=${DOCKER_LINK_ID}"

# is_link is auto-detected from content only (never client-settable per
# IDEA.md); content that isn't a bare http(s) URL simply becomes an ordinary
# (non-link) paste rather than being rejected — the client-supplied
# "is_link":true flag here is ignored.
DOCKER_LINK_NONHTTP_RESPONSE=$(curl -q -sf \
    --header "Content-Type: application/json" \
    --header "Accept: application/json" \
    "${DOCKER_CSRF_ARGS[@]}" \
    --data '{"content":"javascript:alert(1)","is_link":true}' \
    "${DOCKER_BASE_URL}/api/v1/pastes") || \
    __fail "Create paste (link, non-http scheme): request failed"
printf '%s' "${DOCKER_LINK_NONHTTP_RESPONSE}" | grep -q -- '"is_link": *false' || \
    __fail "Create paste (link, non-http scheme): expected is_link:false in: ${DOCKER_LINK_NONHTTP_RESPONSE}"
__pass "Create paste (link, non-http scheme ignored, stored as ordinary paste)"

DOCKER_LINK_LOCATION=$(curl -q -s -D - -o /dev/null --no-location "${DOCKER_BASE_URL}/${DOCKER_LINK_ID}" | \
    tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')
[[ "${DOCKER_LINK_LOCATION}" == "${DOCKER_LINK_TARGET}" ]] || \
    __fail "Link paste view: expected Location ${DOCKER_LINK_TARGET}, got ${DOCKER_LINK_LOCATION}"
# --no-location: check the redirect response itself, not a followed hop to
# the external target URL (which may itself 404/error independent of our app)
__check_status "Link paste view → 302" GET "${DOCKER_BASE_URL}/${DOCKER_LINK_ID}" 302 --no-location
__pass "Link paste view redirects to target"

DOCKER_LINK_RAW=$(curl -q -sf "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_LINK_ID}/raw") || \
    __fail "Link paste raw: request failed"
[[ "${DOCKER_LINK_RAW}" == "${DOCKER_LINK_TARGET}" ]] || \
    __fail "Link paste raw: expected ${DOCKER_LINK_TARGET}, got ${DOCKER_LINK_RAW}"
__pass "Link paste raw returns target URL without redirecting"

# ── Retrieve paste ───────────────────────────────────────────────────────────
__info "--- Paste retrieval ---"
DOCKER_GET=$(curl -q -sf \
    --header "Accept: application/json" \
    "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}") || __fail "Get paste: request failed"
printf '%s' "${DOCKER_GET}" | grep -q -- "hello from docker test" || \
    __fail "Get paste: content mismatch: ${DOCKER_GET}"
__pass "Get paste (JSON)"

# Raw text
DOCKER_RAW=$(curl -q -sf "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}/raw") || __fail "Get raw paste: request failed"
[[ "${DOCKER_RAW}" == "hello from docker test" ]] || \
    __fail "Get raw paste: unexpected content: ${DOCKER_RAW}"
__pass "Get raw paste"

# List pastes
DOCKER_LIST=$(curl -q -sf --header "Accept: application/json" "${DOCKER_BASE_URL}/api/v1/pastes") || \
    __fail "List pastes: request failed"
printf '%s' "${DOCKER_LIST}" | grep -q -- "${DOCKER_PASTE_ID}" || \
    __fail "List pastes: paste ID not found"
__pass "List pastes"

# ── Delete paste ─────────────────────────────────────────────────────────────
__info "--- Paste deletion ---"

# Wrong token → 404
__check_status "Delete with wrong token → 404" DELETE \
    "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}" 404 \
    --header "Authorization: Bearer tok_wrongwrongwrongwrongwrongwrongwx"

# No owner token → 401
__check_status "Delete with no owner token → 401" DELETE \
    "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}" 401 \
    "${DOCKER_CSRF_ARGS[@]}"

# Correct owner token → 200
if [[ -n "${DOCKER_OWNER_TOKEN}" ]]; then
    DOCKER_DEL=$(curl -q -sf \
        --request DELETE \
        --header "Authorization: Bearer ${DOCKER_OWNER_TOKEN}" \
        "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}") || __fail "Delete paste: request failed"
    printf '%s' "${DOCKER_DEL}" | grep -qi -- '"ok".*true\|deleted' || \
        __fail "Delete paste: unexpected response: ${DOCKER_DEL}"
    __pass "Delete paste (owner token)"

    # Verify it's gone
    __check_status "Deleted paste returns 404" GET \
        "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}" 404
fi

# ── Error cases ──────────────────────────────────────────────────────────────
__info "--- Error cases ---"
__check_status "Get nonexistent paste → 404" GET "${DOCKER_BASE_URL}/api/v1/pastes/notexist" 404
__check_status "Empty content → 400" POST "${DOCKER_BASE_URL}/api/v1/pastes" 400 \
    "${DOCKER_CSRF_ARGS[@]}" \
    --header "Content-Type: application/json" \
    --header "Accept: application/json" \
    --data '{"content":""}'

# ── Compat APIs ───────────────────────────────────────────────────────────────
__info "--- Compat API endpoints ---"
# Compat clients are tokenless and cookie-less (curl, CI scripts): none of
# these requests may carry a CSRF token or Authorization header.
# pastebin.com compat
DOCKER_COMPAT_RESPONSE=$(curl -q -sf \
    --data "api_paste_code=compat+test&api_paste_private=0" \
    "${DOCKER_BASE_URL}/api/api_post.php") || __fail "pastebin.com compat POST: request failed"
printf '%s' "${DOCKER_COMPAT_RESPONSE}" | grep -qi -- "http\|paste\|error" || \
    __fail "pastebin.com compat POST: unexpected response: ${DOCKER_COMPAT_RESPONSE}"
__pass "pastebin.com compat POST /api/api_post.php"

__check_status "pastebin.com login stub → 200" POST "${DOCKER_BASE_URL}/api/api_login.php" 200 \
    --data "api_dev_key=x&api_user_name=x&api_user_password=x"

# lenpaste compat — wire protocol is form-encoded (netshare.PasteAddFromForm
# in forksmgr/lcomrade-lenpaste reads req.PostForm, not a JSON body); the
# content field is "body", not "text"
__check_status "lenpaste /api/new → 200/201" POST "${DOCKER_BASE_URL}/api/new" 200 \
    --data "body=Hello&expiration=86400"
__check_status "lenpaste /api/v1/new → 200" POST "${DOCKER_BASE_URL}/api/v1/new" 200 \
    --data "body=Hello&expiration=86400"

# microbin compat
__check_status "microbin /api/v1/pasta → 201" POST "${DOCKER_BASE_URL}/api/v1/pasta" 201 \
    --data "content=Hello"

# stikked compat
__check_status "stikked /api/create → 200" POST "${DOCKER_BASE_URL}/api/create" 200 \
    --data "text=Hello&title=t&name=n&lang=text"

# hastebin compat
__check_status "hastebin /documents → 200" POST "${DOCKER_BASE_URL}/documents" 200 \
    --data-binary "Hello"

# dpaste compat
__check_status "dpaste /api/ → 200" POST "${DOCKER_BASE_URL}/api/" 200 \
    --data "content=Hello&lexer=text"
__check_status "dpaste /api/v2/ → 200" POST "${DOCKER_BASE_URL}/api/v2/" 200 \
    --data "content=Hello&format=json"

# curl-upload family (POST / dispatches by field)
__check_status "0x0.st POST / file → 200" POST "${DOCKER_BASE_URL}/" 200 \
    --form "file=Hello;filename=a.txt"
__check_status "sprunge POST / → 200" POST "${DOCKER_BASE_URL}/" 200 \
    --data "sprunge=Hello"
__check_status "ix.io POST / → 200" POST "${DOCKER_BASE_URL}/" 200 \
    --data "f:1=Hello"

__check_status "lenpaste /api/v1/getServerInfo → 200" GET "${DOCKER_BASE_URL}/api/v1/getServerInfo" 200

# ── Swagger / GraphQL endpoints ───────────────────────────────────────────────
__info "--- API documentation endpoints ---"
__check_status "GET /api/swagger → 200"             GET "${DOCKER_BASE_URL}/api/swagger"           200
__check_status "GET /api/v1/server/swagger → 200"   GET "${DOCKER_BASE_URL}/api/v1/server/swagger" 200
__check_status "GET /api/graphql → 200"             GET "${DOCKER_BASE_URL}/api/graphql"           200
__check_status "GET /api/v1/server/graphql → 200"   GET "${DOCKER_BASE_URL}/api/v1/server/graphql" 200
__check_status "GET /server/docs/graphql → 200"     GET "${DOCKER_BASE_URL}/server/docs/graphql"   200

# ── Static / SEO endpoints ───────────────────────────────────────────────────
__info "--- Static endpoints ---"
__check_status "GET /robots.txt → 200"   GET "${DOCKER_BASE_URL}/robots.txt"   200
# RFC 9116 canonical path only — bare /security.txt is intentionally
# unregistered and returns 404 (see server.go route table, PART 13)
__check_status "GET /.well-known/security.txt → 200" GET "${DOCKER_BASE_URL}/.well-known/security.txt" 200

# ── Security headers ─────────────────────────────────────────────────────────
__info "--- Security headers ---"
DOCKER_HEADERS=$(curl -q -sI "${DOCKER_BASE_URL}/" | tr '[:upper:]' '[:lower:]')
printf '%s' "${DOCKER_HEADERS}" | grep -q -- "x-content-type-options" || \
    __fail "Missing X-Content-Type-Options header"
__pass "X-Content-Type-Options present"
printf '%s' "${DOCKER_HEADERS}" | grep -q -- "x-frame-options\|content-security-policy" || \
    __fail "Missing X-Frame-Options or Content-Security-Policy header"
__pass "Framing protection header present"

# ── --status flag ─────────────────────────────────────────────────────────────
docker exec "${DOCKER_CONTAINER_NAME}" /usr/local/bin/cairnbin --status >/dev/null || \
    __fail "--status exited non-zero"
__pass "--status flag: ok"

# ── Tor key-format migration (NO REGRESSIONS) ─────────────────────────────────
# Proves the app's legacy-key migration (src/tor/tor.go) preserves the .onion
# address: a pre-migration key (old ADD_ONION-era saveKey() format — the raw
# 64-byte expanded ed25519 key, no header) must yield the SAME hostname after
# migration to the native 96-byte format as it did before. All file
# inspection/mutation runs via `docker exec` (root inside the container,
# regardless of host UID) — never touching the volume from the host, per
# PART 28 (no project-directory/runtime-data host coupling).
__info "--- Tor key-format migration verification ---"

__tormig_wait_hostname() {
    local timeout="$1" waited=0
    while ! docker exec "${DOCKER_TORMIG_NAME}" test -s \
        /data/cairnbin/tor/site/hostname 2>/dev/null; do
        sleep 2
        waited=$((waited + 2))
        [[ "${waited}" -lt "${timeout}" ]] || return 1
    done
    return 0
}

docker volume create "${DOCKER_TORMIG_VOLUME}" >/dev/null
docker run -d --name "${DOCKER_TORMIG_NAME}" \
    -e MODE=dev -e DEBUG=1 \
    -v "${DOCKER_TEMP_DIR}/tormig-config:/config:z" \
    -v "${DOCKER_TORMIG_VOLUME}:/data" \
    "${DOCKER_IMAGE_NAME}" >/dev/null

if __tormig_wait_hostname 200; then
    DOCKER_TORMIG_ONION_1=$(docker exec "${DOCKER_TORMIG_NAME}" cat /data/cairnbin/tor/site/hostname)
    DOCKER_TORMIG_KEYSIZE_1=$(docker exec "${DOCKER_TORMIG_NAME}" \
        stat -c%s /data/cairnbin/tor/site/hs_ed25519_secret_key)
    if [[ "${DOCKER_TORMIG_KEYSIZE_1}" -eq 96 ]]; then
        __pass "Fresh install: native 96-byte key, .onion=${DOCKER_TORMIG_ONION_1}"
    else
        __fail "Fresh key file is ${DOCKER_TORMIG_KEYSIZE_1} bytes, expected 96 (native format)"
    fi

    # Synthesize a pre-migration legacy key: strip the 32-byte native header
    # ("== ed25519v1-secret: type0 ==\0\0") off the 96-byte native key,
    # leaving the same 64-byte expanded key the old saveKey() wrote raw.
    # Remove the derived hostname/public-key so only the legacy secret
    # key remains for the app to discover and migrate on next startup.
    docker exec --user cairnbin "${DOCKER_TORMIG_NAME}" sh -c '
        set -e
        cd /data/cairnbin/tor/site
        tail -c 64 hs_ed25519_secret_key > hs_ed25519_secret_key.legacy
        mv hs_ed25519_secret_key.legacy hs_ed25519_secret_key
        chmod 600 hs_ed25519_secret_key
        rm -f hostname hs_ed25519_public_key
    '
    docker rm -f "${DOCKER_TORMIG_NAME}" >/dev/null 2>&1

    docker run -d --name "${DOCKER_TORMIG_NAME}" \
        -e MODE=dev -e DEBUG=1 \
        -v "${DOCKER_TEMP_DIR}/tormig-config:/config:z" \
        -v "${DOCKER_TORMIG_VOLUME}:/data" \
        "${DOCKER_IMAGE_NAME}" >/dev/null

    if __tormig_wait_hostname 200; then
        DOCKER_TORMIG_ONION_2=$(docker exec "${DOCKER_TORMIG_NAME}" cat /data/cairnbin/tor/site/hostname)
        DOCKER_TORMIG_KEYSIZE_2=$(docker exec "${DOCKER_TORMIG_NAME}" \
            stat -c%s /data/cairnbin/tor/site/hs_ed25519_secret_key)
        [[ "${DOCKER_TORMIG_KEYSIZE_2}" -eq 96 ]] || \
            __fail "Legacy key was not migrated to native 96-byte format (size=${DOCKER_TORMIG_KEYSIZE_2})"
        if [[ "${DOCKER_TORMIG_ONION_1}" == "${DOCKER_TORMIG_ONION_2}" ]]; then
            __pass "Legacy-format key migrated, .onion address preserved: ${DOCKER_TORMIG_ONION_2}"
        else
            __fail "NO REGRESSIONS violation: .onion address changed across key migration (${DOCKER_TORMIG_ONION_1} -> ${DOCKER_TORMIG_ONION_2})"
        fi
    else
        __fail "Tor did not publish hostname after key migration within timeout"
        docker logs "${DOCKER_TORMIG_NAME}" 2>&1 | tail -40 >&2
    fi
else
    __fail "Tor did not publish hostname on fresh install within timeout"
    docker logs "${DOCKER_TORMIG_NAME}" 2>&1 | tail -40 >&2
fi

docker rm -f "${DOCKER_TORMIG_NAME}" >/dev/null 2>&1 || true

# ── Final result ─────────────────────────────────────────────────────────────
if [[ "${DOCKER_ERRORS}" -gt 0 ]]; then
    printf "\n[RESULT] %d test(s) FAILED\n" "${DOCKER_ERRORS}" >&2
    exit 1
else
    __info "All Docker integration tests passed."
fi
