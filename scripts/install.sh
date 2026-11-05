#!/usr/bin/env bash
# install.sh — installs cairnbin binary to /usr/local/bin
# Usage: curl -sSL https://github.com/apimgr/cairnbin/raw/main/scripts/install.sh | bash
set -euo pipefail

REPO="apimgr/cairnbin"
BINARY="cairnbin"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    case "${arch}" in
        x86_64)  arch="amd64" ;;
        aarch64) arch="arm64" ;;
        arm64)   arch="arm64" ;;
        *)       echo "Unsupported architecture: ${arch}" >&2; exit 1 ;;
    esac
    echo "${os}-${arch}"
}

PLATFORM="$(detect_platform)"
VERSION="${VERSION:-$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)}"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY}-${PLATFORM}"

echo "Installing ${BINARY} ${VERSION} for ${PLATFORM}..."

# mktemp: predictable shared paths like /tmp/cairnbin are a symlink/hijack
# target when this script runs as root (pass 1 security). The mkdir -p is
# required, not decorative: mktemp creates the final file but NOT the
# intervening directory, so without it a fresh machine fails with
# "No such file or directory" before anything is downloaded. This is the
# project's documented temp-dir convention (AI.md 32750-32751).
mkdir -p "${TMPDIR:-/tmp}/apimgr"
TMP_FILE="$(mktemp "${TMPDIR:-/tmp}/apimgr/cairnbin-XXXXXX")"

curl -sSL "${URL}" -o "${TMP_FILE}"
chmod +x "${TMP_FILE}"

if [ -w "${INSTALL_DIR}" ]; then
    mv "${TMP_FILE}" "${INSTALL_DIR}/${BINARY}"
else
    sudo mv "${TMP_FILE}" "${INSTALL_DIR}/${BINARY}"
fi

echo "Installed ${BINARY} to ${INSTALL_DIR}/${BINARY}"
"${INSTALL_DIR}/${BINARY}" --version
