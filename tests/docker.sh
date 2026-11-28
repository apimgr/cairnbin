#!/usr/bin/env bash
# @@License : WTFPL
# tests/docker.sh — binary validation in a disposable Alpine container.
# Usage: bash tests/docker.sh

set -euo pipefail

# ── Variables ────────────────────────────────────────────────────────────────
DOCKER_SCRIPT_NAME="$(basename -- "$0")"
DOCKER_PROJECT_DIR="$(cd -- "$(dirname -- "$0")/.." && pwd -P)"
DOCKER_PROJECT_ORG="$(awk -F': *' '$1 == "project_org" { print $2; exit }' "${DOCKER_PROJECT_DIR}/IDEA.md")"
DOCKER_INTERNAL_NAME="$(awk -F': *' '$1 == "internal_name" { print $2; exit }' "${DOCKER_PROJECT_DIR}/IDEA.md")"
DOCKER_PROJECT_NAME="$(awk -F': *' '$1 == "project_name" { print $2; exit }' "${DOCKER_PROJECT_DIR}/IDEA.md")"
DOCKER_RANDOM_SUFFIX="$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n' | cut -c1-8)"
DOCKER_CONTAINER_NAME="${DOCKER_PROJECT_NAME}-${DOCKER_RANDOM_SUFFIX}"
DOCKER_NETWORK_NAME="${DOCKER_PROJECT_NAME}-${DOCKER_RANDOM_SUFFIX}"
DOCKER_PORT="$((62000 + RANDOM % 3000))"
DOCKER_BRIDGE_IP="172.17.0.1"
DOCKER_BASE_URL="http://${DOCKER_BRIDGE_IP}:${DOCKER_PORT}"
DOCKER_TEMP_DIR=""
DOCKER_ERRORS=0
DOCKER_TOR_WAIT="${TOR_WAIT:-300}"
DOCKER_I2P_WAIT="${I2P_WAIT:-600}"
DOCKER_GO_CACHE="${GO_CACHE:-${HOME}/go/pkg/mod}"
DOCKER_GO_BUILD="${GO_BUILD:-${HOME}/.cache/go-build/${DOCKER_PROJECT_NAME}}"
DOCKER_COMMIT_ID="$(git -C "${DOCKER_PROJECT_DIR}" rev-parse --short=7 HEAD)"
DOCKER_VERSION="${VERSION:-$(sed -n 's/[[:space:]]//gp' "${DOCKER_PROJECT_DIR}/release.txt" 2>/dev/null | head -n1)}"
DOCKER_BUILD_EPOCH="$(date -u +%s)"
DOCKER_OFFICIAL_SITE="${OFFICIAL_SITE:-}"
if [[ -f "${DOCKER_PROJECT_DIR}/site.txt" ]]; then
  DOCKER_OFFICIAL_SITE="$(cat "${DOCKER_PROJECT_DIR}/site.txt")"
fi
if [[ -z "${DOCKER_VERSION}" ]]; then
  DOCKER_VERSION="${DOCKER_COMMIT_ID}"
fi
DOCKER_TORMIG_SUFFIX="$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n' | cut -c1-8)"
DOCKER_TORMIG_NAME="${DOCKER_PROJECT_NAME}-${DOCKER_TORMIG_SUFFIX}"

for wait_value in "${DOCKER_TOR_WAIT}" "${DOCKER_I2P_WAIT}"; do
  case "${wait_value}" in
    '' | *[!0-9]*)
      printf 'TOR_WAIT and I2P_WAIT must be positive integers\n' >&2
      exit 2
      ;;
  esac
  if [[ "${wait_value}" -lt 1 ]]; then
    printf 'TOR_WAIT and I2P_WAIT must be positive integers\n' >&2
    exit 2
  fi
done

# ── Helpers ──────────────────────────────────────────────────────────────────
__pass() { printf "[PASS] %s\n" "$*"; }
__fail() {
  printf "[FAIL] %s\n" "$*" >&2
  DOCKER_ERRORS=$((DOCKER_ERRORS + 1))
}
__info() { printf "[INFO] %s\n" "$*"; }

__check_status() {
  local desc="$1" method="$2" url="$3" expected="$4"
  shift 4
  local code
  code=$(curl -q -LSs -o /dev/null -w "%{http_code}" -X "${method}" "$@" "${url}")
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
  body=$(curl -q -LSsf "$@" "${url}" 2>/dev/null || true)
  if printf '%s' "${body}" | grep -i -- "${pattern}" >/dev/null; then
    __pass "${desc}"
  else
    __fail "${desc} — pattern '${pattern}' not found in: ${body}"
  fi
}

__skip_overlay_if_network_missing() {
  local overlay="$1" url
  [[ "${REQUIRE_OVERLAY:-0}" == "1" ]] && return 1
  if [[ "${overlay}" == "i2p" ]]; then
    url="https://reseed.i2p.net/"
  else
    url="https://www.torproject.org/"
  fi
  if docker exec "${DOCKER_CONTAINER_NAME}" sh -c \
    'code=$(curl -q -LSs -o /dev/null -w "%{http_code}" --connect-timeout 3 --max-time 8 "$1" 2>/dev/null || true); [ -n "$code" ] && [ "$code" != 000 ]' \
    sh "${url}" 2>/dev/null; then
    return 1
  fi
  __info "SKIP: ${overlay^^} bootstrap network is unavailable"
  return 0
}

__cleanup() {
  docker rm -f "${DOCKER_CONTAINER_NAME}" >/dev/null 2>&1 || true
  docker rm -f "${DOCKER_TORMIG_NAME}" >/dev/null 2>&1 || true
  docker network rm "${DOCKER_NETWORK_NAME}" >/dev/null 2>&1 || true
  [[ -n "${DOCKER_TEMP_DIR}" && -d "${DOCKER_TEMP_DIR}" ]] && rm -rf "${DOCKER_TEMP_DIR}"
}
trap __cleanup EXIT
trap '__cleanup; exit 130' INT
trap '__cleanup; exit 143' TERM

# ── Argument parsing ─────────────────────────────────────────────────────────
for arg in "$@"; do
  case "${arg}" in
    *)
      printf "%s: unknown argument: %s\n" "${DOCKER_SCRIPT_NAME}" "${arg}" >&2
      exit 2
      ;;
  esac
done

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  printf 'ERROR: Docker is required and its daemon must be available\n' >&2
  exit 69
fi

# ── Build host-platform binaries with the Docker toolchain ───────────────────
mkdir -p "${TMPDIR:-/tmp}/${DOCKER_PROJECT_ORG}"
DOCKER_TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/${DOCKER_PROJECT_ORG}/${DOCKER_INTERNAL_NAME}-XXXXXX")"
mkdir -p "${DOCKER_PROJECT_DIR}/binaries" "${DOCKER_GO_CACHE}" "${DOCKER_GO_BUILD}" \
  "${DOCKER_TEMP_DIR}/config/${DOCKER_INTERNAL_NAME}" "${DOCKER_TEMP_DIR}/data/${DOCKER_INTERNAL_NAME}"
__info "Building server and CLI into binaries/ with casjaysdev/go:latest"
docker run --rm --no-healthcheck --name "${DOCKER_PROJECT_NAME}-build-${DOCKER_RANDOM_SUFFIX}" \
  --memory="${DOCKER_MEM:-4g}" --cpus="${DOCKER_CPUS:-2}" --cpu-shares=256 \
  -v "${DOCKER_PROJECT_DIR}:/app" -v "${DOCKER_GO_CACHE}:/usr/local/share/go/pkg/mod" \
  -v "${DOCKER_GO_BUILD}:/usr/local/share/go/cache" -w /app \
  -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
  -e BUILD_LDFLAGS="-X main.Version=${DOCKER_VERSION} -X main.CommitID=${DOCKER_COMMIT_ID} -X main.BuildEpoch=${DOCKER_BUILD_EPOCH} -X main.OfficialSite=${DOCKER_OFFICIAL_SITE}" \
  -e BUILD_GCFLAGS='all=-N -l' casjaysdev/go:latest sh -c 'set -e; go mod download; \
  go build -buildvcs=false -gcflags "$BUILD_GCFLAGS" -ldflags "$BUILD_LDFLAGS" -o binaries/cairnbin ./src; \
  if [ -d src/client ]; then go build -buildvcs=false -gcflags "$BUILD_GCFLAGS" -ldflags "$BUILD_LDFLAGS" -o binaries/cairnbin-cli ./src/client; fi'
cat >"${DOCKER_TEMP_DIR}/config/${DOCKER_INTERNAL_NAME}/server.yml" <<'EOF'
server:
  geoip:
    enabled: false
  tor:
    expose: true
  i2p:
    expose: true
    bootstrap_timeout: 10m
  notifications:
    email:
      enabled: false
      smtp:
        host: localhost
  rate_limit:
    write:
      requests: 1000
      window: 60
    global_burst: 2000
EOF
chmod 600 "${DOCKER_TEMP_DIR}/config/${DOCKER_INTERNAL_NAME}/server.yml"

# ── Start a disposable Alpine container and install test tools ────────────────
__info "Starting compiled binary in Alpine on port ${DOCKER_PORT}"
docker network create "${DOCKER_NETWORK_NAME}" >/dev/null
docker run --rm --detach --name "${DOCKER_CONTAINER_NAME}" \
  --network "${DOCKER_NETWORK_NAME}" \
  --publish "${DOCKER_BRIDGE_IP}:${DOCKER_PORT}:80" \
  --volume "${DOCKER_PROJECT_DIR}/binaries:/build:ro" \
  --volume "${DOCKER_TEMP_DIR}/config:/config" \
  --volume "${DOCKER_TEMP_DIR}/data:/data" \
  --entrypoint sh alpine:latest -c 'sleep infinity' >/dev/null
docker exec "${DOCKER_CONTAINER_NAME}" apk add --no-cache shadow curl bash file jq tor i2pd tzdata ca-certificates >/dev/null
docker exec "${DOCKER_CONTAINER_NAME}" /build/cairnbin --version
docker exec "${DOCKER_CONTAINER_NAME}" /build/cairnbin --help >/dev/null
docker exec "${DOCKER_CONTAINER_NAME}" /build/cairnbin-cli --version
docker exec "${DOCKER_CONTAINER_NAME}" /build/cairnbin-cli --help >/dev/null
docker exec "${DOCKER_CONTAINER_NAME}" sh -c 'cp /build/cairnbin /tmp/renamed-server && chmod 700 /tmp/renamed-server && /tmp/renamed-server --help' >"${DOCKER_TEMP_DIR}/renamed-server-help.txt"
if grep -F 'renamed-server' "${DOCKER_TEMP_DIR}/renamed-server-help.txt" >/dev/null; then
  __pass "Server binary rename is reflected in --help"
else
  __fail "Server --help did not show the renamed executable"
fi
docker exec "${DOCKER_CONTAINER_NAME}" sh -c 'cp /build/cairnbin-cli /tmp/renamed-cli && chmod 700 /tmp/renamed-cli && /tmp/renamed-cli --help' >"${DOCKER_TEMP_DIR}/renamed-cli-help.txt"
if grep -F 'renamed-cli' "${DOCKER_TEMP_DIR}/renamed-cli-help.txt" >/dev/null; then
  __pass "CLI binary rename is reflected in --help"
else
  __fail "CLI --help did not show the renamed executable"
fi
docker exec --detach "${DOCKER_CONTAINER_NAME}" sh -c \
  "/build/${DOCKER_PROJECT_NAME} --debug --address 0.0.0.0 --port 80 --config /config/${DOCKER_INTERNAL_NAME} --data /data/${DOCKER_INTERNAL_NAME} >/tmp/${DOCKER_PROJECT_NAME}-test.log 2>&1"

# Wait for server to become ready (up to 30 s)
__info "Waiting for server to start..."
for ((i = 1; i <= 30; i++)); do
  if curl -q -LSsf "${DOCKER_BASE_URL}/server/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
  if [[ "${i}" -eq 30 ]]; then
    docker exec "${DOCKER_CONTAINER_NAME}" cat "/tmp/${DOCKER_PROJECT_NAME}-test.log" >&2 || true
    curl -q -LSs -i "${DOCKER_BASE_URL}/server/healthz" >&2 || true
    __fail "Server did not start within 30 seconds"
    exit 1
  fi
done
__pass "Server is up"
__pass "Compiled server binary is running in Alpine"

# The scheduler is controlled through the server binary maintenance CLI.
docker exec "${DOCKER_CONTAINER_NAME}" "/build/${DOCKER_PROJECT_NAME}" \
  --config "/config/${DOCKER_INTERNAL_NAME}" --data "/data/${DOCKER_INTERNAL_NAME}" \
  --maintenance scheduler list >"${DOCKER_TEMP_DIR}/scheduler-list.txt"
if grep -F 'backup_daily' "${DOCKER_TEMP_DIR}/scheduler-list.txt" >/dev/null; then
  __pass "Scheduler maintenance CLI lists registered tasks"
else
  __fail "Scheduler maintenance CLI omitted backup_daily"
fi
docker exec "${DOCKER_CONTAINER_NAME}" "/build/${DOCKER_PROJECT_NAME}" \
  --config "/config/${DOCKER_INTERNAL_NAME}" --data "/data/${DOCKER_INTERNAL_NAME}" \
  --maintenance scheduler run database_check >"${DOCKER_TEMP_DIR}/scheduler-run.txt"
if grep -F 'Task database_check completed.' "${DOCKER_TEMP_DIR}/scheduler-run.txt" >/dev/null; then
  __pass "Scheduler maintenance CLI runs a registered task"
else
  __fail "Scheduler maintenance CLI did not complete database_check"
fi
docker exec "${DOCKER_CONTAINER_NAME}" "/build/${DOCKER_PROJECT_NAME}" \
  --config "/config/${DOCKER_INTERNAL_NAME}" --data "/data/${DOCKER_INTERNAL_NAME}" \
  --maintenance scheduler history database_check >"${DOCKER_TEMP_DIR}/scheduler-history.txt"
if grep -F 'success' "${DOCKER_TEMP_DIR}/scheduler-history.txt" >/dev/null; then
  __pass "Scheduler maintenance CLI reads persisted task history"
else
  __fail "Scheduler task history omitted the successful manual run"
fi

# Exercise CLI reads and writes against the running server.
docker exec "${DOCKER_CONTAINER_NAME}" /build/cairnbin-cli \
  --server http://127.0.0.1 list --limit 10 >"${DOCKER_TEMP_DIR}/cli-list.txt"
__pass "CLI list command"
DOCKER_CLI_CREATE=$(printf 'cairnbin cli integration test\n' | docker exec -i "${DOCKER_CONTAINER_NAME}" \
  /build/cairnbin-cli --server http://127.0.0.1 --output json create)
DOCKER_CLI_ID=$(printf '%s' "${DOCKER_CLI_CREATE}" | grep -o -- '"id": *"[^"]*"' | awk 'NR == 1 { print }' | cut -d'"' -f4)
DOCKER_CLI_TOKEN=$(printf '%s' "${DOCKER_CLI_CREATE}" | grep -o -- '"owner_token": *"[^"]*"' | awk 'NR == 1 { print }' | cut -d'"' -f4)
if [[ -z "${DOCKER_CLI_ID}" || -z "${DOCKER_CLI_TOKEN}" ]]; then
  __fail "CLI create did not return a paste ID and owner token"
else
  DOCKER_CLI_GET=$(docker exec "${DOCKER_CONTAINER_NAME}" /build/cairnbin-cli \
    --server http://127.0.0.1 get "${DOCKER_CLI_ID}")
  if printf '%s' "${DOCKER_CLI_GET}" | grep -F 'cairnbin cli integration test' >/dev/null; then
    __pass "CLI get command"
  else
    __fail "CLI get did not return the created paste"
  fi
  DOCKER_OPERATOR_TOKEN=$(docker exec "${DOCKER_CONTAINER_NAME}" sh -c \
    "awk -F': ' '\$1 == \"  token\" { print \$2; exit }' /config/${DOCKER_INTERNAL_NAME}/secrets.yml")
  if [[ -z "${DOCKER_OPERATOR_TOKEN}" ]]; then
    __fail "Could not read server.token for protected debug-route checks"
  else
    __check_status "Debug error route rejects missing token" GET "${DOCKER_BASE_URL}/debug/error500" 401
    __check_status "Debug error route rejects wrong token" GET "${DOCKER_BASE_URL}/debug/error500" 401 \
      --header "Authorization: Bearer invalid-token"
    __check_status "Debug error route rejects owner token" GET "${DOCKER_BASE_URL}/debug/error500" 403 \
      --header "Authorization: Bearer ${DOCKER_CLI_TOKEN}"
    __check_status "Debug error route accepts server.token" GET "${DOCKER_BASE_URL}/debug/error500" 500 \
      --header "Authorization: Bearer ${DOCKER_OPERATOR_TOKEN}"
    __check_status "Debug error route rejects query token" GET \
      "${DOCKER_BASE_URL}/debug/error500?token=${DOCKER_OPERATOR_TOKEN}" 401
  fi
  if docker exec "${DOCKER_CONTAINER_NAME}" /build/cairnbin-cli \
    --server http://127.0.0.1 delete "${DOCKER_CLI_ID}" own_invalid_token >/dev/null 2>&1; then
    __fail "CLI delete accepted an invalid owner token"
  else
    __pass "CLI delete rejects an invalid owner token"
  fi
  docker exec "${DOCKER_CONTAINER_NAME}" /build/cairnbin-cli \
    --server http://127.0.0.1 delete "${DOCKER_CLI_ID}" "${DOCKER_CLI_TOKEN}" >/dev/null
  __pass "CLI delete command with owner token"
fi

# ── Health endpoints ──────────────────────────────────────────────────────────
__check_status "GET /server/healthz" GET "${DOCKER_BASE_URL}/server/healthz" 200
__check_status "GET /api/v1/server/healthz" GET "${DOCKER_BASE_URL}/api/v1/server/healthz" 200

# Healthz must include required fields
__check_body "healthz has version field" "${DOCKER_BASE_URL}/api/v1/server/healthz" "version"
__check_body "healthz has status field" "${DOCKER_BASE_URL}/api/v1/server/healthz" "ok\|status\|healthy"
__check_body "healthz reports Tor status" "${DOCKER_BASE_URL}/api/v1/server/healthz" "features.tor.status"
__check_body "healthz reports I2P status" "${DOCKER_BASE_URL}/api/v1/server/healthz" "features.i2p.status"
__check_body "healthz selects i2pd provider in application container" \
  "${DOCKER_BASE_URL}/api/v1/server/healthz" 'features.i2p.provider[^,]*i2pd'

__wait_overlay_health() {
  local overlay="$1" timeout="$2" waited=0 status provider
  while [[ "${waited}" -lt "${timeout}" ]]; do
    status=$(docker exec "${DOCKER_CONTAINER_NAME}" sh -c \
      "curl -q -LSsf http://127.0.0.1/api/v1/server/healthz.txt | awk -F': ' '\$1 == \"features.${overlay}.status\" { print \$2 }'" 2>/dev/null || true)
    provider=""
    if [[ "${overlay}" == "i2p" ]]; then
      provider=$(docker exec "${DOCKER_CONTAINER_NAME}" sh -c \
        "curl -q -LSsf http://127.0.0.1/api/v1/server/healthz.txt | awk -F': ' '\$1 == \"features.i2p.provider\" { print \$2 }'" 2>/dev/null || true)
    fi
    if [[ "${status}" == "healthy" ]]; then
      if [[ "${overlay}" == "i2p" && "${provider}" != "i2pd" ]]; then
        __fail "I2P provider is ${provider:-missing}, expected i2pd"
        return 1
      fi
      __pass "${overlay^^} reached healthy${provider:+ with provider ${provider}} status"
      return 0
    fi
    if [[ "${status}" == "error" || "${status}" == "unavailable" ]]; then
      if __skip_overlay_if_network_missing "${overlay}"; then
        return 0
      fi
      __fail "${overlay^^} became ${status} before reaching healthy status"
      docker logs "${DOCKER_CONTAINER_NAME}" >&2 || true
      if [[ "${overlay}" == "i2p" ]]; then
        docker exec "${DOCKER_CONTAINER_NAME}" sh -c \
          'tail -n 40 "$1"' sh "/data/log/${DOCKER_INTERNAL_NAME}/i2pd.log" >&2 || true
      fi
      return 1
    fi
    sleep 2
    waited=$((waited + 2))
  done
  if __skip_overlay_if_network_missing "${overlay}"; then
    return 0
  fi
  __fail "${overlay^^} did not become healthy within ${timeout} seconds (last status: ${status:-unknown})"
  return 1
}

__wait_overlay_health tor "${DOCKER_TOR_WAIT}" || true
__wait_overlay_health i2p "${DOCKER_I2P_WAIT}" || true

# ── Content negotiation: frontend routes ─────────────────────────────────────
__info "--- Content negotiation: frontend routes ---"
# Home page — browser gets HTML, plain-text client gets text
__check_body "/ Accept:text/html → HTML" "${DOCKER_BASE_URL}/" "<!DOCTYPE html\|<html" \
  --header "Accept: text/html"
__check_status "/ Accept:text/plain → 200" GET "${DOCKER_BASE_URL}/" 200 \
  --header "Accept: text/plain"
__check_status "GET /recent → 200" GET "${DOCKER_BASE_URL}/recent" 200
__check_status "GET /create → 200" GET "${DOCKER_BASE_URL}/create" 200

# ── Content negotiation: API routes ─────────────────────────────────────────
__info "--- Content negotiation: API routes ---"
__check_body "GET /api/v1/pastes → JSON" "${DOCKER_BASE_URL}/api/v1/pastes" '"ok"' \
  --header "Accept: application/json"
__check_status "GET /api/v1/pastes → 200" GET "${DOCKER_BASE_URL}/api/v1/pastes" 200
# Plain-text negotiation on the same route
__check_status "GET /api/v1/pastes Accept:text/plain → 200" GET "${DOCKER_BASE_URL}/api/v1/pastes" 200 \
  --header "Accept: text/plain"
# .txt extension alias
__check_status "GET /api/v1/pastes.txt → 200" GET "${DOCKER_BASE_URL}/api/v1/pastes.txt" 200

# ── Create paste: multiple request formats ───────────────────────────────────
__info "--- Paste creation ---"

DOCKER_CSRF_TOKEN=$(curl -q -LSsf -D - -o /dev/null \
  --header "Accept: text/html" --user-agent "Mozilla/5.0" "${DOCKER_BASE_URL}/create" |
  awk 'BEGIN { IGNORECASE=1 } /^Set-Cookie: csrf_token=/ && !found { sub(/^Set-Cookie: csrf_token=/, ""); sub(/;.*/, ""); print; found=1 }')
[[ -n "${DOCKER_CSRF_TOKEN}" ]] || __fail "Could not obtain CSRF token from /create"
DOCKER_CSRF_ARGS=(--cookie "csrf_token=${DOCKER_CSRF_TOKEN}" --header "X-CSRF-Token: ${DOCKER_CSRF_TOKEN}")

# JSON body
DOCKER_RESPONSE=$(curl -q -LSsf \
  --header "Content-Type: application/json" \
  --header "Accept: application/json" \
  "${DOCKER_CSRF_ARGS[@]}" \
  --data '{"content":"hello from docker test","language":"text"}' \
  "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (JSON): request failed"
DOCKER_PASTE_ID=$(printf '%s' "${DOCKER_RESPONSE}" | grep -o -- '"id": *"[^"]*"' | awk 'NR == 1 { print }' | cut -d'"' -f4)
DOCKER_OWNER_TOKEN=$(printf '%s' "${DOCKER_RESPONSE}" | grep -o -- '"owner_token": *"[^"]*"' | cut -d'"' -f4)
[[ -n "${DOCKER_PASTE_ID}" ]] || __fail "Create paste (JSON): no ID in response: ${DOCKER_RESPONSE}"
__pass "Create paste (JSON): id=${DOCKER_PASTE_ID}"

# Raw body (curl pipe style)
DOCKER_RAW_RESPONSE=$(curl -q -LSsf \
  --header "Content-Type: text/plain" \
  --header "Accept: application/json" \
  "${DOCKER_CSRF_ARGS[@]}" \
  --data "raw paste from curl" \
  "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (raw): request failed"
printf '%s' "${DOCKER_RAW_RESPONSE}" | grep -- '"id"' >/dev/null ||
  __fail "Create paste (raw): no id in response: ${DOCKER_RAW_RESPONSE}"
__pass "Create paste (raw body)"

# Form submission (browser style — should redirect)
# --no-location: report the initial response code, not a followed redirect's
# (some environments set "location" in .curlrc, which would otherwise make
# curl auto-follow the 303 and report the redirected page's 200 instead)
DOCKER_FORM_CODE=$(curl -q -LSs -o /dev/null -w "%{http_code}" --no-location \
  --header "Content-Type: application/x-www-form-urlencoded" \
  "${DOCKER_CSRF_ARGS[@]}" \
  --data "content=form+paste+test" \
  "${DOCKER_BASE_URL}/pastes")
[[ "${DOCKER_FORM_CODE}" =~ ^(201|303|302)$ ]] ||
  __fail "Create paste (form): expected 201/302/303, got ${DOCKER_FORM_CODE}"
__pass "Create paste (form): code=${DOCKER_FORM_CODE}"

# With expiry
DOCKER_EXP_RESPONSE=$(curl -q -LSsf \
  --header "Content-Type: application/json" \
  --header "Accept: application/json" \
  "${DOCKER_CSRF_ARGS[@]}" \
  --data '{"content":"expiring paste","expires_in":"1h"}' \
  "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (expiry): request failed"
printf '%s' "${DOCKER_EXP_RESPONSE}" | grep -- '"expires_at"' >/dev/null ||
  __fail "Create paste (expiry): expires_at missing in: ${DOCKER_EXP_RESPONSE}"
__pass "Create paste (with expiry)"

# With burn_after
DOCKER_BURN_RESPONSE=$(curl -q -LSsf \
  --header "Content-Type: application/json" \
  --header "Accept: application/json" \
  "${DOCKER_CSRF_ARGS[@]}" \
  --data '{"content":"burn after 2","burn_after":2}' \
  "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (burn_after): request failed"
printf '%s' "${DOCKER_BURN_RESPONSE}" | grep -- '"burn_after"' >/dev/null ||
  __fail "Create paste (burn_after): field missing in: ${DOCKER_BURN_RESPONSE}"
__pass "Create paste (burn_after=2)"

# Unlisted
DOCKER_UNLIST_RESPONSE=$(curl -q -LSsf \
  --header "Content-Type: application/json" \
  --header "Accept: application/json" \
  "${DOCKER_CSRF_ARGS[@]}" \
  --data '{"content":"unlisted paste","visibility":"unlisted"}' \
  "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (unlisted): request failed"
printf '%s' "${DOCKER_UNLIST_RESPONSE}" | grep -- '"visibility"' >/dev/null ||
  __fail "Create paste (unlisted): field missing"
__pass "Create paste (unlisted)"

# Link paste (is_link)
DOCKER_LINK_TARGET="https://example.com/docker-test-target"
DOCKER_LINK_RESPONSE=$(curl -q -LSsf \
  --header "Content-Type: application/json" \
  --header "Accept: application/json" \
  "${DOCKER_CSRF_ARGS[@]}" \
  --data "{\"content\":\"${DOCKER_LINK_TARGET}\",\"is_link\":true}" \
  "${DOCKER_BASE_URL}/api/v1/pastes") || __fail "Create paste (link): request failed"
DOCKER_LINK_ID=$(printf '%s' "${DOCKER_LINK_RESPONSE}" | grep -o -- '"id": *"[^"]*"' | awk 'NR == 1 { print }' | cut -d'"' -f4)
[[ -n "${DOCKER_LINK_ID}" ]] || __fail "Create paste (link): no ID in response: ${DOCKER_LINK_RESPONSE}"
printf '%s' "${DOCKER_LINK_RESPONSE}" | grep -- '"is_link": *true' >/dev/null ||
  __fail "Create paste (link): is_link field missing/false in: ${DOCKER_LINK_RESPONSE}"
__pass "Create paste (link): id=${DOCKER_LINK_ID}"

# is_link is auto-detected from content only (never client-settable per
# IDEA.md); content that isn't a bare http(s) URL simply becomes an ordinary
# (non-link) paste rather than being rejected — the client-supplied
# "is_link":true flag here is ignored.
DOCKER_LINK_NONHTTP_RESPONSE=$(curl -q -LSsf \
  --header "Content-Type: application/json" \
  --header "Accept: application/json" \
  "${DOCKER_CSRF_ARGS[@]}" \
  --data '{"content":"javascript:alert(1)","is_link":true}' \
  "${DOCKER_BASE_URL}/api/v1/pastes") ||
  __fail "Create paste (link, non-http scheme): request failed"
printf '%s' "${DOCKER_LINK_NONHTTP_RESPONSE}" | grep -- '"is_link": *false' >/dev/null ||
  __fail "Create paste (link, non-http scheme): expected is_link:false in: ${DOCKER_LINK_NONHTTP_RESPONSE}"
__pass "Create paste (link, non-http scheme ignored, stored as ordinary paste)"

DOCKER_LINK_LOCATION=$(curl -q -LSs -D - -o /dev/null --no-location "${DOCKER_BASE_URL}/${DOCKER_LINK_ID}" |
  tr -d '\r' | awk '/^[Ll]ocation:/ {print $2}')
[[ "${DOCKER_LINK_LOCATION}" == "${DOCKER_LINK_TARGET}" ]] ||
  __fail "Link paste view: expected Location ${DOCKER_LINK_TARGET}, got ${DOCKER_LINK_LOCATION}"
# --no-location: check the redirect response itself, not a followed hop to
# the external target URL (which may itself 404/error independent of our app)
__check_status "Link paste view → 302" GET "${DOCKER_BASE_URL}/${DOCKER_LINK_ID}" 302 --no-location
__pass "Link paste view redirects to target"

DOCKER_LINK_RAW=$(curl -q -LSsf "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_LINK_ID}/raw") ||
  __fail "Link paste raw: request failed"
[[ "${DOCKER_LINK_RAW}" == "${DOCKER_LINK_TARGET}" ]] ||
  __fail "Link paste raw: expected ${DOCKER_LINK_TARGET}, got ${DOCKER_LINK_RAW}"
__pass "Link paste raw returns target URL without redirecting"

# ── Retrieve paste ───────────────────────────────────────────────────────────
__info "--- Paste retrieval ---"
DOCKER_GET=$(curl -q -LSsf \
  --header "Accept: application/json" \
  "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}") || __fail "Get paste: request failed"
printf '%s' "${DOCKER_GET}" | grep -- "hello from docker test" >/dev/null ||
  __fail "Get paste: content mismatch: ${DOCKER_GET}"
__pass "Get paste (JSON)"

# Raw text
DOCKER_RAW=$(curl -q -LSsf "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}/raw") || __fail "Get raw paste: request failed"
[[ "${DOCKER_RAW}" == "hello from docker test" ]] ||
  __fail "Get raw paste: unexpected content: ${DOCKER_RAW}"
__pass "Get raw paste"

# List pastes
DOCKER_LIST=$(curl -q -LSsf --header "Accept: application/json" "${DOCKER_BASE_URL}/api/v1/pastes") ||
  __fail "List pastes: request failed"
printf '%s' "${DOCKER_LIST}" | grep -- "${DOCKER_PASTE_ID}" >/dev/null ||
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
  DOCKER_DEL=$(curl -q -LSsf \
    --request DELETE \
    --header "Authorization: Bearer ${DOCKER_OWNER_TOKEN}" \
    "${DOCKER_BASE_URL}/api/v1/pastes/${DOCKER_PASTE_ID}") || __fail "Delete paste: request failed"
  printf '%s' "${DOCKER_DEL}" | grep -i -- '"ok".*true\|deleted' >/dev/null ||
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
DOCKER_COMPAT_RESPONSE=$(curl -q -LSsf \
  --data "api_paste_code=compat+test&api_paste_private=0" \
  "${DOCKER_BASE_URL}/api/api_post.php") || __fail "pastebin.com compat POST: request failed"
if [[ "${DOCKER_COMPAT_RESPONSE}" =~ ^/([[:alnum:]]{8})$ ]]; then
  DOCKER_COMPAT_ID="${BASH_REMATCH[1]}"
  __pass "pastebin.com compat POST /api/api_post.php"
  __check_body "pastebin.com compat raw retrieval" \
    "${DOCKER_BASE_URL}/api/api_raw.php?i=${DOCKER_COMPAT_ID}" "compat test"
else
  __fail "pastebin.com compat POST returned an invalid URL path: ${DOCKER_COMPAT_RESPONSE}"
fi

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
  --header "Content-Type: text/plain" \
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
__check_status "GET /api/swagger → 200" GET "${DOCKER_BASE_URL}/api/swagger" 200
__check_status "GET /api/v1/server/swagger → 200" GET "${DOCKER_BASE_URL}/api/v1/server/swagger" 200
__check_status "GET /api/graphql → 200" GET "${DOCKER_BASE_URL}/api/graphql" 200
__check_status "GET /api/v1/server/graphql → 200" GET "${DOCKER_BASE_URL}/api/v1/server/graphql" 200
__check_status "GET /server/docs/graphql → 200" GET "${DOCKER_BASE_URL}/server/docs/graphql" 200

# ── Static / SEO endpoints ───────────────────────────────────────────────────
__info "--- Static endpoints ---"
__check_status "GET /robots.txt → 200" GET "${DOCKER_BASE_URL}/robots.txt" 200
# RFC 9116 canonical path only — bare /security.txt is intentionally
# unregistered and returns 404 (see server.go route table, PART 13)
__check_status "GET /.well-known/security.txt → 200" GET "${DOCKER_BASE_URL}/.well-known/security.txt" 200

# ── Security headers ─────────────────────────────────────────────────────────
__info "--- Security headers ---"
DOCKER_HEADERS=$(curl -q -LSsf -D - -o /dev/null "${DOCKER_BASE_URL}/" | tr '[:upper:]' '[:lower:]')
printf '%s' "${DOCKER_HEADERS}" | grep -- "x-content-type-options" >/dev/null ||
  __fail "Missing X-Content-Type-Options header"
__pass "X-Content-Type-Options present"
printf '%s' "${DOCKER_HEADERS}" | grep -- "x-frame-options\|content-security-policy" >/dev/null ||
  __fail "Missing X-Frame-Options or Content-Security-Policy header"
__pass "Framing protection header present"

# ── --status flag ─────────────────────────────────────────────────────────────
docker exec "${DOCKER_CONTAINER_NAME}" "/build/${DOCKER_PROJECT_NAME}" \
  --config "/config/${DOCKER_INTERNAL_NAME}" --status >/dev/null ||
  __fail "--status exited non-zero"
__pass "--status flag: ok"

# ── Tor key-format migration (NO REGRESSIONS) ─────────────────────────────────
# Proves the compiled binary migrates a legacy key without changing its address.
__info "--- Tor key-format migration verification ---"

mkdir -p "${DOCKER_TEMP_DIR}/tormig-config/${DOCKER_INTERNAL_NAME}" "${DOCKER_TEMP_DIR}/tormig-data/${DOCKER_INTERNAL_NAME}"
cat >"${DOCKER_TEMP_DIR}/tormig-config/${DOCKER_INTERNAL_NAME}/server.yml" <<'EOF'
server:
  geoip:
    enabled: false
  i2p:
    enabled: false
  notifications:
    email:
      enabled: false
      smtp:
        host: localhost
EOF
chmod 600 "${DOCKER_TEMP_DIR}/tormig-config/${DOCKER_INTERNAL_NAME}/server.yml"

__tormig_wait_hostname() {
  local timeout="$1" waited=0
  while ! docker exec "${DOCKER_TORMIG_NAME}" test -s \
    "/data/${DOCKER_INTERNAL_NAME}/tor/site/hostname" 2>/dev/null; do
    sleep 2
    waited=$((waited + 2))
    [[ "${waited}" -lt "${timeout}" ]] || return 1
  done
  return 0
}

docker run --rm --detach --name "${DOCKER_TORMIG_NAME}" \
  --network "${DOCKER_NETWORK_NAME}" \
  --volume "${DOCKER_PROJECT_DIR}/binaries:/build:ro" \
  --volume "${DOCKER_TEMP_DIR}/tormig-config:/config" \
  --volume "${DOCKER_TEMP_DIR}/tormig-data:/data" \
  --entrypoint sh alpine:latest -c 'sleep infinity' >/dev/null
docker exec "${DOCKER_TORMIG_NAME}" apk add --no-cache shadow curl bash file jq tor i2pd tzdata ca-certificates >/dev/null
docker exec --detach "${DOCKER_TORMIG_NAME}" "/build/${DOCKER_PROJECT_NAME}" \
  --address 0.0.0.0 --port 80 --config "/config/${DOCKER_INTERNAL_NAME}" --data "/data/${DOCKER_INTERNAL_NAME}"

if __tormig_wait_hostname 200; then
  DOCKER_TORMIG_ONION_1=$(docker exec "${DOCKER_TORMIG_NAME}" cat "/data/${DOCKER_INTERNAL_NAME}/tor/site/hostname")
  DOCKER_TORMIG_KEYSIZE_1=$(docker exec "${DOCKER_TORMIG_NAME}" \
    stat -c%s "/data/${DOCKER_INTERNAL_NAME}/tor/site/hs_ed25519_secret_key")
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
  docker exec "${DOCKER_TORMIG_NAME}" sh -c "
        set -e
        cd /data/${DOCKER_INTERNAL_NAME}/tor/site
        tail -c 64 hs_ed25519_secret_key > hs_ed25519_secret_key.legacy
        mv hs_ed25519_secret_key.legacy hs_ed25519_secret_key
        chmod 600 hs_ed25519_secret_key
        rm -f hostname hs_ed25519_public_key
    "
  docker rm -f "${DOCKER_TORMIG_NAME}" >/dev/null 2>&1

  docker rm -f "${DOCKER_TORMIG_NAME}" >/dev/null 2>&1 || true
  docker run --rm --detach --name "${DOCKER_TORMIG_NAME}" \
    --network "${DOCKER_NETWORK_NAME}" \
    --volume "${DOCKER_PROJECT_DIR}/binaries:/build:ro" \
    --volume "${DOCKER_TEMP_DIR}/tormig-config:/config" \
    --volume "${DOCKER_TEMP_DIR}/tormig-data:/data" \
    --entrypoint sh alpine:latest -c 'sleep infinity' >/dev/null
  docker exec "${DOCKER_TORMIG_NAME}" apk add --no-cache shadow curl bash file jq tor i2pd tzdata ca-certificates >/dev/null
  docker exec --detach "${DOCKER_TORMIG_NAME}" "/build/${DOCKER_PROJECT_NAME}" \
    --address 0.0.0.0 --port 80 --config /config/cairnbin --data /data/cairnbin

  if __tormig_wait_hostname 200; then
    DOCKER_TORMIG_ONION_2=$(docker exec "${DOCKER_TORMIG_NAME}" cat /data/cairnbin/tor/site/hostname)
    DOCKER_TORMIG_KEYSIZE_2=$(docker exec "${DOCKER_TORMIG_NAME}" \
      stat -c%s /data/cairnbin/tor/site/hs_ed25519_secret_key)
    [[ "${DOCKER_TORMIG_KEYSIZE_2}" -eq 96 ]] ||
      __fail "Legacy key was not migrated to native 96-byte format (size=${DOCKER_TORMIG_KEYSIZE_2})"
    if [[ "${DOCKER_TORMIG_ONION_1}" == "${DOCKER_TORMIG_ONION_2}" ]]; then
      __pass "Legacy-format key migrated, .onion address preserved: ${DOCKER_TORMIG_ONION_2}"
    else
      __fail "NO REGRESSIONS violation: .onion address changed across key migration (${DOCKER_TORMIG_ONION_1} -> ${DOCKER_TORMIG_ONION_2})"
    fi
  else
    __fail "Tor did not publish hostname after key migration within timeout"
    docker logs "${DOCKER_TORMIG_NAME}" >&2
  fi
else
  __fail "Tor did not publish hostname on fresh install within timeout"
  docker logs "${DOCKER_TORMIG_NAME}" >&2
fi

docker rm -f "${DOCKER_TORMIG_NAME}" >/dev/null 2>&1 || true

# ── Final result ─────────────────────────────────────────────────────────────
if [[ "${DOCKER_ERRORS}" -gt 0 ]]; then
  printf "\n[RESULT] %d test(s) FAILED\n" "${DOCKER_ERRORS}" >&2
  exit 1
else
  __info "All Docker integration tests passed."
fi
