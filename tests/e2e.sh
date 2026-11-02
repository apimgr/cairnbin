#!/usr/bin/env bash
# @@License : WTFPL
# tests/e2e.sh — Browser E2E test runner for pastebin (PART 28).
# Builds the local image, brings it up via docker/docker-compose.test.yml
# (copied to a temp dir, never run from the project tree), then runs the
# `e2e`-tagged Go test package (tests/e2e/) against it inside a Docker
# container carrying the toolchain image plus a headless Chromium sidecar.
#
# This is a manual/on-demand runner — it is never part of `make test` and
# never part of the pre-commit gate (tests/e2e/*_test.go are compiled only
# behind the `e2e` build tag).
#
# Three Mandatory Tiers exercised by the Go test package:
#   Tier 1 — SSR:        plain net/http, no browser (works today)
#   Tier 2 — No-JS:      chromedp with script execution disabled
#   Tier 3 — Full browser: chromedp with JS on, zero console errors
#
# Tiers 2/3 connect to the headless-shell service in the test compose stack.
#
# Usage: bash tests/e2e.sh [--keep]
# Options:
#   --keep   Do not tear down the stack / temp dir after the test run

set -euo pipefail

# ── Variables ────────────────────────────────────────────────────────────────
E2E_SCRIPT_NAME="$(basename -- "$0")"
E2E_PROJECT_DIR="$(cd -- "$(dirname -- "$0")/.." && pwd -P)"
E2E_PROJECT_ORG="apimgr"
E2E_PROJECT_NAME="pastebin"
E2E_IMAGE_NAME="${E2E_PROJECT_NAME}:e2e-$$"
E2E_COMPOSE_PROJECT="${E2E_PROJECT_NAME}-e2e-$$"
E2E_CONTAINER_NAME="${E2E_PROJECT_NAME}-app"
E2E_PORT="80"
E2E_BASE_URL="http://pastebin:${E2E_PORT}"
E2E_BROWSER_URL="http://pastebin-e2e-browser:9222"
E2E_SERVER_IP=""
E2E_BROWSER_IP=""
E2E_TEMP_DIR=""
E2E_ARTIFACT_DIR=""
E2E_KEEP=false
E2E_ERRORS=0

# ── Helpers ──────────────────────────────────────────────────────────────────
__pass() { printf "[PASS] %s\n" "$*"; }
__fail() { printf "[FAIL] %s\n" "$*" >&2; E2E_ERRORS=$((E2E_ERRORS + 1)); }
__info() { printf "[INFO] %s\n" "$*"; }

# shellcheck disable=SC2329 # Called indirectly by the EXIT/INT/TERM trap.
__cleanup() {
    if [[ "${E2E_KEEP}" == true ]]; then
        __info "Keeping stack and temp dir at ${E2E_TEMP_DIR} (--keep)"
        return 0
    fi
    if [[ -n "${E2E_TEMP_DIR}" && -d "${E2E_TEMP_DIR}" ]]; then
        (cd -- "${E2E_TEMP_DIR}" && docker compose -p "${E2E_COMPOSE_PROJECT}" down -v --remove-orphans >/dev/null 2>&1 || true)
        if [[ "${E2E_ERRORS}" -gt 0 && -n "${E2E_ARTIFACT_DIR}" && -d "${E2E_ARTIFACT_DIR}" ]]; then
            local preserved_dir
            preserved_dir="$(mktemp -d "${TMPDIR:-/tmp}/${E2E_PROJECT_ORG}/${E2E_PROJECT_NAME}-e2e-artifacts-XXXXXX")"
            cp -a -- "${E2E_ARTIFACT_DIR}/." "${preserved_dir}/"
            __info "Failure artifacts preserved at ${preserved_dir}"
        fi
        \rm -rf -- "${E2E_TEMP_DIR}"
    fi
    docker rmi -f "${E2E_IMAGE_NAME}" >/dev/null 2>&1 || true
}
trap "__cleanup" EXIT INT TERM

for arg in "$@"; do
    case "${arg}" in
        --keep) E2E_KEEP=true ;;
        *) __fail "unknown option: ${arg}"; exit 2 ;;
    esac
done

command -v docker >/dev/null 2>&1 || { __fail "docker not found — required for ${E2E_SCRIPT_NAME}"; exit 1; }

# ── Temp dir (mandated pattern) ─────────────────────────────────────────────
mkdir -p "${TMPDIR:-/tmp}/${E2E_PROJECT_ORG}"
E2E_TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/${E2E_PROJECT_ORG}/${E2E_PROJECT_NAME}-XXXXXX")"
mkdir -p "${E2E_TEMP_DIR}/volumes/config" "${E2E_TEMP_DIR}/volumes/data"
mkdir -p "${E2E_TEMP_DIR}/volumes/config/pastebin"
E2E_ARTIFACT_DIR="${E2E_TEMP_DIR}/artifacts"
mkdir -p "${E2E_ARTIFACT_DIR}"
cat >"${E2E_TEMP_DIR}/volumes/config/pastebin/server.yml" <<'EOF'
server:
  rate_limit:
    enabled: true
    write:
      requests: 1000
      window: 60
    global_burst: 2000
  token: e2e-operator-token-only
  geoip:
    enabled: true
    databases:
      asn: true
      country: true
      city: true
  tor:
    binary: /__pastebin_e2e__/tor
  contact:
    admin:
      email: e2e-admin@example.test
  notifications:
    email:
      enabled: true
      smtp:
        host: pastebin-e2e-runner
        port: 64981
        tls: none
      events:
        scheduler_error: true
web:
  security:
    blocklists:
      enabled: true
      sources:
        - file: e2e-blocklist.txt
          url: http://pastebin-e2e-runner:64980/blocklist
    cve:
      enabled: true
      file: e2e-cves.json
      source: http://pastebin-e2e-runner:64980/cve
EOF
chmod 600 "${E2E_TEMP_DIR}/volumes/config/pastebin/server.yml"
__info "Temp dir: ${E2E_TEMP_DIR}"

# ── Build local image ───────────────────────────────────────────────────────
__info "Building local image ${E2E_IMAGE_NAME}..."
docker build --build-arg BUILD_TAGS=e2e -f "${E2E_PROJECT_DIR}/docker/Dockerfile" -t "${E2E_IMAGE_NAME}" "${E2E_PROJECT_DIR}"

# ── Bring up the stack from the mandated test compose file ────────────────
cp -- "${E2E_PROJECT_DIR}/docker/docker-compose.test.yml" "${E2E_TEMP_DIR}/docker-compose.yml"
sed -i.bak \
    -e "s|image: .*${E2E_PROJECT_NAME}.*|image: ${E2E_IMAGE_NAME}|" \
    -e 's|pull_policy: always|pull_policy: missing|' \
    -e '/^  pastebin:$/,/^    external: false$/ { /^    external: false$/a\
    internal: true
    }' \
    "${E2E_TEMP_DIR}/docker-compose.yml" 2>/dev/null || true
\rm -f -- "${E2E_TEMP_DIR}/docker-compose.yml.bak"

(
    cd -- "${E2E_TEMP_DIR}"
    docker compose -p "${E2E_COMPOSE_PROJECT}" up -d
)

__info "Waiting for ${E2E_BASE_URL}/server/healthz..."
E2E_READY=false
for _ in $(seq 1 30); do
    if timeout 4s docker exec "${E2E_CONTAINER_NAME}" curl -q -LSs --max-time 2 -o /dev/null -w '%{http_code}' "http://127.0.0.1:${E2E_PORT}/server/healthz" 2>/dev/null | grep -q '^200$'; then
        E2E_READY=true
        break
    fi
    sleep 2
done
if [[ "${E2E_READY}" != true ]]; then
    __fail "server did not become healthy"
    docker logs "${E2E_CONTAINER_NAME}" >"${E2E_ARTIFACT_DIR}/server.log" 2>&1 || true
    docker logs pastebin-e2e-browser >"${E2E_ARTIFACT_DIR}/browser.log" 2>&1 || true
    exit 1
fi

E2E_SERVER_IP="$(docker inspect --format '{{(index .NetworkSettings.Networks "pastebin").IPAddress}}' "${E2E_CONTAINER_NAME}")"
E2E_BROWSER_IP="$(docker inspect --format '{{(index .NetworkSettings.Networks "pastebin").IPAddress}}' pastebin-e2e-browser)"
if [[ -z "${E2E_SERVER_IP}" || -z "${E2E_BROWSER_IP}" ]]; then
    __fail "could not resolve test service addresses"
    exit 1
fi
E2E_BASE_URL="http://${E2E_SERVER_IP}:${E2E_PORT}"
E2E_BROWSER_URL="http://${E2E_BROWSER_IP}:9222"

# ── Tiers 1–3 (SSR, no-JS browser, full browser) ─────────────────────────
__info "Running all browser E2E tiers..."
if (
    cd -- "${E2E_PROJECT_DIR}"
    bash "${HOME}/.codex/hooks/test-lint-run.sh" test -- make test \
    TEST_TAGS="-tags e2e" \
    TEST_PKGS="./tests/e2e/..." \
    TEST_RUN="-count=1" \
    TEST_COVERAGE=0 \
    EXTRA_ENV="--network pastebin --network-alias=pastebin-e2e-runner -e PASTEBIN_E2E_UPDATE_API_URL=http://pastebin-e2e-runner:64980/update -e E2E_BASE_URL=${E2E_BASE_URL} -e E2E_BROWSER_URL=${E2E_BROWSER_URL} -e E2E_ARTIFACT_DIR=${E2E_ARTIFACT_DIR}"
); then
    __pass "E2E tiers 1–3"
else
    __fail "E2E tiers 1–3"
    docker logs "${E2E_CONTAINER_NAME}" >"${E2E_ARTIFACT_DIR}/server.log" 2>&1 || true
    docker logs pastebin-e2e-browser >"${E2E_ARTIFACT_DIR}/browser.log" 2>&1 || true
fi

if [[ "${E2E_ERRORS}" -gt 0 ]]; then
    __fail "${E2E_ERRORS} e2e failure(s)"
    exit 1
fi

__pass "e2e run complete"
exit 0
