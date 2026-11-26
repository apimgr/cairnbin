#!/bin/bash
# Verify shipped dependencies use compatible licenses.
# Requires go-licenses, pre-installed in casjaysdev/go:latest; never install inline.
# Run inside Docker: docker run --rm -v "$PWD":/app -w /app casjaysdev/go:latest bash scripts/verify-licenses.sh

set -eo pipefail

echo "Checking for incompatible licenses..."

command -v go-licenses >/dev/null 2>&1 || {
    echo "ERROR: go-licenses not found — run inside casjaysdev/go:latest" >&2
    exit 69
}

echo "Scanning shipped dependencies..."
mapfile -t IGNORE_ARGS < <(awk 'NF && $1 !~ /^#/ { printf "--ignore\n%s\n", $1 }' .github/license-overrides.txt 2>/dev/null || true)
licenses=$(go-licenses csv ./src/... "${IGNORE_ARGS[@]}")
OVERRIDE_REPORT=$(awk 'NF && $1 !~ /^#/ { print $1 "," $2 }' .github/license-overrides.txt 2>/dev/null || true)
if [ -n "$OVERRIDE_REPORT" ]; then
    licenses+=$'\n'"$OVERRIDE_REPORT"
fi
if echo "$licenses" | grep -iE 'GPL|AGPL|LGPL'; then
    echo "ERROR: Copyleft license detected!" >&2
    echo "Remove the dependency or find an alternative." >&2
    exit 1
fi

echo "All licenses are compatible"

PROJECT_ORG=$(sed -n 's/^project_org:[[:space:]]*//p' IDEA.md | head -n1 | tr -d '[:space:]')
INTERNAL_NAME=$(sed -n 's/^internal_name:[[:space:]]*//p' IDEA.md | head -n1 | tr -d '[:space:]')
mkdir -p "${TMPDIR:-/tmp}/${PROJECT_ORG}"
LICDIR=$(mktemp -d "${TMPDIR:-/tmp}/${PROJECT_ORG}/${INTERNAL_NAME}-XXXXXX")
trap 'rm -rf "$LICDIR"' EXIT
go-licenses csv ./src/... "${IGNORE_ARGS[@]}" > "$LICDIR/licenses.csv"
if [ -n "$OVERRIDE_REPORT" ]; then
    printf '%s\n' "$OVERRIDE_REPORT" >> "$LICDIR/licenses.csv"
fi
go-licenses save ./src/... "${IGNORE_ARGS[@]}" --save_path="$LICDIR/third_party_licenses"

echo "License report generated in a temporary directory; temporary files are removed on exit."
