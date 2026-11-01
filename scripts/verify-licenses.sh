#!/bin/bash
# scripts/verify-licenses.sh
# Verify all dependencies use compatible licenses (MIT/Apache/BSD/ISC/Public Domain).
# Requires go-licenses — pre-installed in casjaysdev/go:latest; never install inline.
# Run inside Docker: docker run --rm -v $PWD:/app -w /app casjaysdev/go:latest bash scripts/verify-licenses.sh

set -eo pipefail

echo "Checking for incompatible licenses..."

command -v go-licenses >/dev/null 2>&1 || {
    echo "ERROR: go-licenses not found — run inside casjaysdev/go:latest"
    exit 1
}

# Capture first - a scanner failure must abort (set -e), never read as "no match"
echo "Scanning dependencies..."
licenses=$(go-licenses csv ./...)
if echo "$licenses" | grep -iE 'GPL|AGPL|LGPL'; then
    echo "ERROR: Copyleft license detected!"
    echo "Remove the dependency or find an alternative."
    exit 1
fi

echo "All licenses are compatible"

echo "Generating license report..."
go-licenses csv ./... > licenses.csv
# modernc.org/mathutil is BSD-3-Clause but its license text is not classifiable by go-licenses; it is attributed in LICENSE.md
go-licenses save ./... --force --ignore modernc.org/mathutil --save_path=third_party_licenses

echo "License report saved to licenses.csv and third_party_licenses/"
echo ""
echo "Next steps:"
echo "1. Review licenses.csv"
echo "2. Update LICENSE.md with any new dependencies"
echo "3. Commit the changes"
