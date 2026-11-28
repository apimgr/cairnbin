#!/bin/sh
# @@License : WTFPL
# Verify the shipped Docker image contract and Tor bootstrap health.
# Usage: sh tests/image_check.sh IMAGE
set -eu

IMAGE_CHECK_PROJECT_DIR="$(cd -- "$(dirname -- "$0")/.." && pwd -P)"
IMAGE_CHECK_PROJECT_NAME="$(awk -F': *' '$1 == "project_name" { print $2; exit }' "${IMAGE_CHECK_PROJECT_DIR}/IDEA.md")"
IMAGE_CHECK_TIMEOUT="${TOR_BOOTSTRAP_TIMEOUT:-180}"
IMAGE_CHECK_CONTAINER=""
IMAGE_CHECK_NETWORK=""
IMAGE_CHECK_SUFFIX="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
IMAGE_CHECK_NAME="${IMAGE_CHECK_PROJECT_NAME}-${IMAGE_CHECK_SUFFIX}"
IMAGE_CHECK_CONTRACT_NAME="${IMAGE_CHECK_PROJECT_NAME}-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
case "${IMAGE_CHECK_TIMEOUT}" in
  '' | *[!0-9]*)
    printf 'TOR_BOOTSTRAP_TIMEOUT must be an integer number of seconds\n' >&2
    exit 2
    ;;
esac
[ "${IMAGE_CHECK_TIMEOUT}" -gt 0 ] || {
  printf 'TOR_BOOTSTRAP_TIMEOUT must be greater than zero\n' >&2
  exit 2
}

# shellcheck disable=SC2329 # Invoked by the EXIT and signal traps below.
__cleanup() {
  if [ -n "${IMAGE_CHECK_CONTAINER}" ]; then
    docker rm -f "${IMAGE_CHECK_CONTAINER}" >/dev/null 2>&1 || true
    IMAGE_CHECK_CONTAINER=""
  fi
  if [ -n "${IMAGE_CHECK_NETWORK}" ]; then
    docker network rm "${IMAGE_CHECK_NETWORK}" >/dev/null 2>&1 || true
    IMAGE_CHECK_NETWORK=""
  fi
}
trap __cleanup EXIT
trap '__cleanup; exit 129' HUP
trap '__cleanup; exit 130' INT
trap '__cleanup; exit 143' TERM

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  printf 'Usage: sh tests/image_check.sh IMAGE\n' >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1; then
  printf 'docker is required for the image check\n' >&2
  exit 69
fi
if ! docker info >/dev/null 2>&1; then
  printf 'Docker daemon is unavailable for the image check\n' >&2
  exit 69
fi

IMAGE_CHECK_IMAGE="$1"
IMAGE_CHECK_NETWORK="${IMAGE_CHECK_PROJECT_NAME}-${IMAGE_CHECK_SUFFIX}"
docker network create "${IMAGE_CHECK_NETWORK}" >/dev/null
docker run --rm --name "${IMAGE_CHECK_CONTRACT_NAME}" --network "${IMAGE_CHECK_NETWORK}" \
  --entrypoint sh "${IMAGE_CHECK_IMAGE}" -c \
  'for c in bash curl tor i2pd tini; do command -v "$c" || exit 1; done'

IMAGE_CHECK_CONTAINER="${IMAGE_CHECK_NAME}"
docker run --detach --name "${IMAGE_CHECK_CONTAINER}" --network "${IMAGE_CHECK_NETWORK}" \
  "${IMAGE_CHECK_IMAGE}" >/dev/null
IMAGE_CHECK_STARTED="$(date +%s)"
IMAGE_CHECK_DEADLINE=$((IMAGE_CHECK_STARTED + IMAGE_CHECK_TIMEOUT))
IMAGE_CHECK_TOR_STATUS="starting"

while [ "$(date +%s)" -lt "${IMAGE_CHECK_DEADLINE}" ]; do
  if ! docker inspect -f '{{.State.Running}}' "${IMAGE_CHECK_CONTAINER}" 2>/dev/null | grep -qx true; then
    docker logs "${IMAGE_CHECK_CONTAINER}" >&2 || true
    printf 'image container exited before health became available\n' >&2
    exit 1
  fi
  IMAGE_CHECK_HEALTH="$(docker exec "${IMAGE_CHECK_CONTAINER}" curl -q -LSsf -H 'Accept: text/plain' http://127.0.0.1/server/healthz 2>/dev/null || true)"
  if [ -n "${IMAGE_CHECK_HEALTH}" ]; then
    IMAGE_CHECK_TOR_STATUS="$(printf '%s\n' "${IMAGE_CHECK_HEALTH}" | awk -F': ' '$1 == "features.tor.status" {print $2; exit}')"
    [ -n "${IMAGE_CHECK_TOR_STATUS}" ] || IMAGE_CHECK_TOR_STATUS="error"
    case "${IMAGE_CHECK_TOR_STATUS}" in
      healthy)
        printf 'Image contract and Tor health check passed for %s\n' "${IMAGE_CHECK_IMAGE}"
        exit 0
        ;;
      error)
        printf 'Tor health returned an error for %s\n' "${IMAGE_CHECK_IMAGE}" >&2
        exit 1
        ;;
    esac
  fi
  sleep 2
done

if [ "${REQUIRE_OVERLAY:-0}" = "1" ]; then
  printf 'Tor did not become healthy within %s seconds (last status: %s)\n' "${IMAGE_CHECK_TIMEOUT}" "${IMAGE_CHECK_TOR_STATUS}" >&2
  exit 1
fi
case "${IMAGE_CHECK_TOR_STATUS}" in
  starting | unavailable)
    printf 'SKIP: no outbound network (Tor status: %s)\n' "${IMAGE_CHECK_TOR_STATUS}" >&2
    exit 0
    ;;
  *)
    printf 'Tor did not reach an acceptable health status (last status: %s)\n' "${IMAGE_CHECK_TOR_STATUS}" >&2
    exit 1
    ;;
esac
