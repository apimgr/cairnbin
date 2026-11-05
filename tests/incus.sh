#!/usr/bin/env bash
# @@License : WTFPL
# tests/incus.sh — Incus integration tests for cairnbin
# Launches an Incus system container (Debian, systemd), installs the built
# binaries, and exercises the full systemd service workflow plus the same
# API/content-negotiation/endpoint coverage as tests/docker.sh, per PART 28.
# Usage: bash tests/incus.sh [--keep]
# Options:
#   --keep   Do not delete the container after the test run

set -eo pipefail

# ── Variables ────────────────────────────────────────────────────────────────
INCUS_SCRIPT_NAME="$(basename -- "$0")"
INCUS_PROJECT_DIR="$(cd -- "$(dirname -- "$0")/.." && pwd -P)"
INCUS_PROJECT_ORG="apimgr"
INCUS_PROJECT_NAME="cairnbin"
INCUS_CONTAINER_NAME="test-${INCUS_PROJECT_NAME}-$$"
INCUS_IMAGE="images:debian/trixie"
INCUS_KEEP=false
INCUS_PORT="80"
INCUS_ERRORS=0
INCUS_TEMP_DIR=""
INCUS_BUILD_DIR=""

# ── Helpers ──────────────────────────────────────────────────────────────────
__pass() { printf "[PASS] %s\n" "$*"; }
__fail() { printf "[FAIL] %s\n" "$*" >&2; INCUS_ERRORS=$((INCUS_ERRORS + 1)); }
__info() { printf "[INFO] %s\n" "$*"; }
__vm()   { incus exec "${INCUS_CONTAINER_NAME}" -- bash -c "$*"; }

__check_status() {
    local desc="$1" method="$2" url="$3" expected="$4"
    shift 4
    local code
    code=$(__vm "curl -s -o /dev/null -w '%{http_code}' -X ${method} $* '${url}'")
    if [[ "${code}" == "${expected}" ]]; then
        __pass "${desc} -> ${code}"
    else
        __fail "${desc} -> expected ${expected}, got ${code}"
    fi
}

__check_body() {
    local desc="$1" url="$2" pattern="$3"
    shift 3
    local body
    body=$(__vm "curl -sf $* '${url}'" 2>/dev/null || true)
    if printf '%s' "${body}" | grep -qi -- "${pattern}"; then
        __pass "${desc}"
    else
        __fail "${desc} — pattern '${pattern}' not found in: ${body}"
    fi
}

__cleanup() {
    if [[ "${INCUS_KEEP}" == "false" ]]; then
        __info "Deleting container ${INCUS_CONTAINER_NAME}"
        incus delete --force "${INCUS_CONTAINER_NAME}" 2>/dev/null || true
    else
        __info "Keeping container ${INCUS_CONTAINER_NAME} (--keep flag set)"
    fi
    if [[ -n "${INCUS_TEMP_DIR}" && -d "${INCUS_TEMP_DIR}" ]]; then
        rm -rf -- "${INCUS_TEMP_DIR}"
    fi
}
trap __cleanup EXIT

# ── Argument parsing ─────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --keep) INCUS_KEEP=true; shift ;;
        *) printf "%s: unknown argument: %s\n" "${INCUS_SCRIPT_NAME}" "$1" >&2; exit 2 ;;
    esac
done

# ── Prerequisite checks ───────────────────────────────────────────────────────
command -v incus >/dev/null 2>&1 || { echo "ERROR: incus not found. Install incus or use tests/docker.sh" >&2; exit 1; }

# ── Build in an isolated temporary directory ────────────────────────────────
cd "${INCUS_PROJECT_DIR}"
[[ -f "Makefile" ]] || { echo "ERROR: Makefile not found in ${INCUS_PROJECT_DIR}" >&2; exit 1; }
mkdir -p "${TMPDIR:-/tmp}/${INCUS_PROJECT_ORG}"
INCUS_TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/${INCUS_PROJECT_ORG}/${INCUS_PROJECT_NAME}-XXXXXX")"
__info "Building with make dev in an isolated temporary workspace..."
TMPDIR="${INCUS_TEMP_DIR}" make dev
INCUS_BUILD_DIR="$(find "${INCUS_TEMP_DIR}/${INCUS_PROJECT_ORG}" -mindepth 1 -maxdepth 1 -type d -name "${INCUS_PROJECT_NAME}-*" -print -quit)"
[[ -d "${INCUS_BUILD_DIR}" ]] || { echo "ERROR: temporary build directory not found" >&2; exit 1; }
[[ -f "${INCUS_BUILD_DIR}/${INCUS_PROJECT_NAME}" ]] || { echo "ERROR: binary not found after build" >&2; exit 1; }
__pass "Binaries built in temporary workspace"

# ── Launch container ─────────────────────────────────────────────────────────
__info "Launching Incus container (Debian + systemd) ${INCUS_CONTAINER_NAME}"
incus launch "${INCUS_IMAGE}" "${INCUS_CONTAINER_NAME}"

__info "Waiting for container to boot..."
for ((i = 1; i <= 60; i++)); do
    if __vm "systemctl is-system-running --wait" >/dev/null 2>&1 || \
       __vm "systemctl is-system-running" 2>/dev/null | grep -q -- "running\|degraded"; then
        break
    fi
    sleep 2
    [[ "${i}" -lt 60 ]] || { echo "ERROR: container did not come up in 120 seconds" >&2; exit 1; }
done
__pass "Container booted"

# ── Copy binaries to container ────────────────────────────────────────────────
__info "Copying binaries to container..."
incus file push "${INCUS_BUILD_DIR}/${INCUS_PROJECT_NAME}" "${INCUS_CONTAINER_NAME}/usr/local/bin/"
__vm "chmod +x /usr/local/bin/${INCUS_PROJECT_NAME}"

if [[ -f "${INCUS_BUILD_DIR}/${INCUS_PROJECT_NAME}-cli" ]]; then
    incus file push "${INCUS_BUILD_DIR}/${INCUS_PROJECT_NAME}-cli" "${INCUS_CONTAINER_NAME}/usr/local/bin/"
    __vm "chmod +x /usr/local/bin/${INCUS_PROJECT_NAME}-cli"
fi

__info "Preparing isolated service configuration..."
INCUS_CONFIG_DIR="${INCUS_TEMP_DIR}/config"
mkdir -p "${INCUS_CONFIG_DIR}"
cat >"${INCUS_CONFIG_DIR}/server.yml" <<'EOF'
server:
  port: "80"
  address: 0.0.0.0
  geoip:
    enabled: false
  notifications:
    email:
      enabled: false
      smtp:
        host: localhost
EOF
__vm "mkdir -p /etc/${INCUS_PROJECT_ORG}/${INCUS_PROJECT_NAME}"
incus file push "${INCUS_CONFIG_DIR}/server.yml" "${INCUS_CONTAINER_NAME}/etc/${INCUS_PROJECT_ORG}/${INCUS_PROJECT_NAME}/server.yml"

# Ensure curl and binary inspection tools are available for testing
__vm "command -v curl >/dev/null 2>&1 && command -v file >/dev/null 2>&1 && command -v useradd >/dev/null 2>&1 && command -v groupadd >/dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq curl file passwd)" >/dev/null 2>&1

# ── Version / help / binary info ──────────────────────────────────────────────
__vm "${INCUS_PROJECT_NAME} --version" >/dev/null || __fail "Binary failed to run --version"
__pass "Version check"
__vm "${INCUS_PROJECT_NAME} --help" >/dev/null || __fail "Binary failed to run --help"
__pass "Help check"
__vm "ls -lh /usr/local/bin/${INCUS_PROJECT_NAME} && file /usr/local/bin/${INCUS_PROJECT_NAME}" >/dev/null || \
    __fail "Binary info check failed"
__pass "Binary info"

# ── Service install / start / status ─────────────────────────────────────────
__vm "${INCUS_PROJECT_NAME} --service --install" || __fail "--service --install failed"
__pass "--service --install"

# inside container — not a host-service mutation
__vm "systemctl start ${INCUS_PROJECT_NAME}" || __fail "systemctl start failed"
sleep 3
# inside container — not a host-service mutation
if __vm "systemctl is-active ${INCUS_PROJECT_NAME}" | grep -q -- "^active$"; then
    __pass "Service is active"
else
    __fail "Service is not active after start"
fi
INCUS_BASE="http://localhost:${INCUS_PORT}"

# ── Health endpoints ──────────────────────────────────────────────────────────
__check_status "GET /server/healthz"        GET "${INCUS_BASE}/server/healthz"        200
__check_status "GET /api/v1/server/healthz" GET "${INCUS_BASE}/api/v1/server/healthz" 200
__check_body "healthz has version field" "${INCUS_BASE}/api/v1/server/healthz" "version"
__check_body "healthz has status field"  "${INCUS_BASE}/api/v1/server/healthz" "ok\|status\|healthy"

INCUS_SERVICE_PID=$(__vm "systemctl show -p MainPID --value ${INCUS_PROJECT_NAME}")
INCUS_SERVICE_USER=$(__vm "ps -o user= -p ${INCUS_SERVICE_PID}" | xargs)
if [[ "${INCUS_SERVICE_USER}" == "${INCUS_PROJECT_NAME}" ]]; then
    __pass "Service runs as the dedicated ${INCUS_PROJECT_NAME} account"
else
    __fail "Service runs as ${INCUS_SERVICE_USER:-unknown}, expected ${INCUS_PROJECT_NAME}"
fi

# Mutating requests use the same double-submit token as browser forms.
INCUS_CSRF_TOKEN=$(__vm "curl -sS -D - -o /dev/null '${INCUS_BASE}/create' | sed -n 's/^Set-Cookie: csrf_token=\\([^;]*\\).*/\\1/ip' | head -1")
[[ -n "${INCUS_CSRF_TOKEN}" ]] || __fail "Could not obtain CSRF token from /create"
INCUS_CSRF_ARGS="--cookie csrf_token=${INCUS_CSRF_TOKEN} --header 'X-CSRF-Token: ${INCUS_CSRF_TOKEN}'"

# ── Content negotiation: frontend routes ─────────────────────────────────────
__info "--- Content negotiation: frontend routes ---"
__check_body "/ Accept:text/html -> HTML" "${INCUS_BASE}/" "<!DOCTYPE html\|<html" \
    "--header 'Accept: text/html'"
__check_status "/ Accept:text/plain -> 200" GET "${INCUS_BASE}/" 200 \
    "--header 'Accept: text/plain'"
__check_status "GET /recent -> 200" GET "${INCUS_BASE}/recent" 200
__check_status "GET /create -> 200" GET "${INCUS_BASE}/create" 200

# ── Content negotiation: API routes ───────────────────────────────────────────
__info "--- Content negotiation: API routes ---"
__check_body "GET /api/v1/pastes -> JSON" "${INCUS_BASE}/api/v1/pastes" '"ok"' \
    "--header 'Accept: application/json'"
__check_status "GET /api/v1/pastes -> 200" GET "${INCUS_BASE}/api/v1/pastes" 200
__check_status "GET /api/v1/pastes.txt -> 200" GET "${INCUS_BASE}/api/v1/pastes.txt" 200

# ── Create / retrieve / delete paste ──────────────────────────────────────────
__info "--- Paste creation ---"
INCUS_CREATE_DATA='{"content":"hello from incus test","language":"text"}'
INCUS_RESPONSE=$(__vm "curl -sf --header 'Content-Type: application/json' \
    --header 'Accept: application/json' ${INCUS_CSRF_ARGS} --data '${INCUS_CREATE_DATA}' \
    ${INCUS_BASE}/api/v1/pastes")
INCUS_PASTE_ID=$(printf '%s' "${INCUS_RESPONSE}" | grep -o -- '"id": *"[^"]*"' | head -1 | cut -d'"' -f4)
INCUS_OWNER_TOKEN=$(printf '%s' "${INCUS_RESPONSE}" | grep -o -- '"owner_token": *"[^"]*"' | cut -d'"' -f4)
[[ -n "${INCUS_PASTE_ID}" ]] || __fail "Create paste failed: ${INCUS_RESPONSE}"
__pass "Create paste: id=${INCUS_PASTE_ID}"

__info "--- Paste retrieval ---"
INCUS_GET=$(__vm "curl -sf --header 'Accept: application/json' ${INCUS_BASE}/api/v1/pastes/${INCUS_PASTE_ID}")
printf '%s' "${INCUS_GET}" | grep -q -- "hello from incus test" || __fail "Get paste (JSON): content mismatch"
__pass "Get paste (JSON)"

INCUS_RAW=$(__vm "curl -sf ${INCUS_BASE}/api/v1/pastes/${INCUS_PASTE_ID}/raw")
[[ "${INCUS_RAW}" == "hello from incus test" ]] || __fail "Raw paste: unexpected content: ${INCUS_RAW}"
__pass "Get raw paste"

__info "--- Link paste (is_link) ---"
INCUS_LINK_TARGET="https://example.com/incus-test-target"
printf -v INCUS_LINK_DATA '{"content":"%s","is_link":true}' "${INCUS_LINK_TARGET}"
INCUS_LINK_RESPONSE=$(__vm "curl -sf --header 'Content-Type: application/json' \
    --header 'Accept: application/json' ${INCUS_CSRF_ARGS} --data '${INCUS_LINK_DATA}' \
    ${INCUS_BASE}/api/v1/pastes")
INCUS_LINK_ID=$(printf '%s' "${INCUS_LINK_RESPONSE}" | grep -o -- '"id": *"[^"]*"' | head -1 | cut -d'"' -f4)
[[ -n "${INCUS_LINK_ID}" ]] || __fail "Create paste (link) failed: ${INCUS_LINK_RESPONSE}"
printf '%s' "${INCUS_LINK_RESPONSE}" | grep -q -- '"is_link": *true' || \
    __fail "Create paste (link): is_link field missing/false in: ${INCUS_LINK_RESPONSE}"
__pass "Create paste (link): id=${INCUS_LINK_ID}"

INCUS_LINK_LOCATION=$(__vm "curl -s -D - -o /dev/null ${INCUS_BASE}/${INCUS_LINK_ID}" | tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')
[[ "${INCUS_LINK_LOCATION}" == "${INCUS_LINK_TARGET}" ]] || \
    __fail "Link paste view: expected Location ${INCUS_LINK_TARGET}, got ${INCUS_LINK_LOCATION}"
__check_status "Link paste view -> 302" GET "${INCUS_BASE}/${INCUS_LINK_ID}" 302
__pass "Link paste view redirects to target"

INCUS_LINK_RAW=$(__vm "curl -sf ${INCUS_BASE}/api/v1/pastes/${INCUS_LINK_ID}/raw")
[[ "${INCUS_LINK_RAW}" == "${INCUS_LINK_TARGET}" ]] || \
    __fail "Link paste raw: expected ${INCUS_LINK_TARGET}, got ${INCUS_LINK_RAW}"
__pass "Link paste raw returns target URL without redirecting"

__info "--- Paste deletion ---"
__check_status "Delete with invalid token -> 404" DELETE "${INCUS_BASE}/api/v1/pastes/${INCUS_PASTE_ID}" 404 \
    "--header 'Authorization: Bearer invalid'"
if [[ -n "${INCUS_OWNER_TOKEN}" ]]; then
    INCUS_DEL=$(__vm "curl -sf --request DELETE --header 'Authorization: Bearer ${INCUS_OWNER_TOKEN}' ${INCUS_BASE}/api/v1/pastes/${INCUS_PASTE_ID}")
    printf '%s' "${INCUS_DEL}" | grep -qi -- '"ok".*true\|deleted' || __fail "Delete paste: unexpected response: ${INCUS_DEL}"
    __pass "Delete paste with owner token"
    __check_status "Deleted paste returns 404" GET "${INCUS_BASE}/api/v1/pastes/${INCUS_PASTE_ID}" 404
fi

# ── Compat APIs ────────────────────────────────────────────────────────────────
__info "--- Compat API endpoints ---"
# Compat clients are tokenless and cookie-less: no CSRF token or auth header.
INCUS_COMPAT_RESPONSE=$(__vm "curl -sf --data 'api_paste_code=compat+test&api_paste_private=0' ${INCUS_BASE}/api/api_post.php")
printf '%s' "${INCUS_COMPAT_RESPONSE}" | grep -qi -- "http\|paste\|error" || \
    __fail "pastebin.com compat POST: unexpected response: ${INCUS_COMPAT_RESPONSE}"
__pass "pastebin.com compat POST /api/api_post.php"

__check_status "lenpaste /api/new -> 200" POST "${INCUS_BASE}/api/new" 200 \
	"--data 'body=Hello&expiration=86400'"
__check_status "lenpaste /api/v1/new -> 200" POST "${INCUS_BASE}/api/v1/new" 200 \
	"--data 'body=Hello&expiration=86400'"
__check_status "microbin /api/v1/pasta -> 201" POST "${INCUS_BASE}/api/v1/pasta" 201 \
	"--data 'content=Hello'"
__check_status "stikked /api/create -> 200" POST "${INCUS_BASE}/api/create" 200 \
	"--data 'text=Hello&title=t'"
__check_status "hastebin /documents -> 200" POST "${INCUS_BASE}/documents" 200 \
	"--data-binary Hello"
__check_status "dpaste /api/ -> 200" POST "${INCUS_BASE}/api/" 200 \
	"--data 'content=Hello'"
__check_status "sprunge POST / -> 200" POST "${INCUS_BASE}/" 200 \
	"--data 'sprunge=Hello'"
__check_status "ix.io POST / -> 200" POST "${INCUS_BASE}/" 200 \
	"--data 'f:1=Hello'"
__check_status "lenpaste /api/v1/getServerInfo -> 200" GET "${INCUS_BASE}/api/v1/getServerInfo" 200

# ── Swagger / GraphQL endpoints ───────────────────────────────────────────────
__info "--- API documentation endpoints ---"
__check_status "GET /api/swagger -> 200"           GET "${INCUS_BASE}/api/swagger"           200
__check_status "GET /api/v1/server/swagger -> 200" GET "${INCUS_BASE}/api/v1/server/swagger" 200
__check_status "GET /api/graphql -> 200"           GET "${INCUS_BASE}/api/graphql"           200
__check_status "GET /api/v1/server/graphql -> 200" GET "${INCUS_BASE}/api/v1/server/graphql" 200
__check_status "GET /server/docs/graphql -> 200"   GET "${INCUS_BASE}/server/docs/graphql"   200

# ── Static / SEO endpoints ────────────────────────────────────────────────────
__info "--- Static endpoints ---"
__check_status "GET /robots.txt -> 200"   GET "${INCUS_BASE}/robots.txt"   200
__check_status "GET /.well-known/security.txt -> 200" GET "${INCUS_BASE}/.well-known/security.txt" 200

# ── Binary rename tests ───────────────────────────────────────────────────────
__info "--- Binary rename tests ---"
INCUS_RENAME_DIR="/tmp/${INCUS_PROJECT_ORG}/${INCUS_PROJECT_NAME}-rename"
__vm "mkdir -p ${INCUS_RENAME_DIR} && cp /usr/local/bin/${INCUS_PROJECT_NAME} ${INCUS_RENAME_DIR}/renamed-server && chmod +x ${INCUS_RENAME_DIR}/renamed-server"
if __vm "${INCUS_RENAME_DIR}/renamed-server --help" 2>&1 | grep -q -- "renamed-server"; then
    __pass "Server binary rename works (--help shows actual name)"
else
    __fail "Server --help does not show renamed binary name"
fi

# ── Client tests (if built) ───────────────────────────────────────────────────
__info "--- Client tests (if exists) ---"
if __vm "test -f /usr/local/bin/${INCUS_PROJECT_NAME}-cli" 2>/dev/null; then
    __vm "${INCUS_PROJECT_NAME}-cli --version" >/dev/null || __fail "CLI --version failed"
    __vm "${INCUS_PROJECT_NAME}-cli --help" >/dev/null || __fail "CLI --help failed"
    __pass "CLI version/help"

    __vm "cp /usr/local/bin/${INCUS_PROJECT_NAME}-cli ${INCUS_RENAME_DIR}/renamed-cli && chmod +x ${INCUS_RENAME_DIR}/renamed-cli"
    if __vm "${INCUS_RENAME_DIR}/renamed-cli --help" 2>&1 | grep -q -- "renamed-cli"; then
        __pass "CLI binary rename works"
    else
        __fail "CLI --help does not show renamed binary name"
    fi

    if __vm "${INCUS_PROJECT_NAME}-cli --server ${INCUS_BASE} status" >/dev/null 2>&1; then
        __fail "CLI status without an API token unexpectedly succeeded"
    else
        __pass "CLI status rejects missing API token"
    fi
else
    __info "client not installed - skipping"
fi

# ── Service stop / uninstall ──────────────────────────────────────────────────
# inside container — not a host-service mutation
__vm "systemctl stop ${INCUS_PROJECT_NAME}" || __fail "systemctl stop failed"
sleep 2
INCUS_SERVICE_STATE=$(__vm "systemctl is-active ${INCUS_PROJECT_NAME}" 2>/dev/null || true)
if [[ "${INCUS_SERVICE_STATE}" == "active" ]]; then
    __fail "Service is still active after stop"
else
    __pass "Service stopped (${INCUS_SERVICE_STATE})"
fi

__vm "printf 'y\\n' | ${INCUS_PROJECT_NAME} --service --uninstall" || __fail "--service --uninstall failed"
__pass "--service --uninstall"

# ── Final result ─────────────────────────────────────────────────────────────
if [[ "${INCUS_ERRORS}" -gt 0 ]]; then
    printf "\n[RESULT] %d test(s) FAILED\n" "${INCUS_ERRORS}" >&2
    exit 1
else
    __info "All Incus integration tests passed."
fi
