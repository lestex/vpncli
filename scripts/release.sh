#!/usr/bin/env bash
#
# Cross-compile release archives into dist/.
#
# Used by both `make dist` and the release workflow, so what CI publishes can
# be reproduced locally before a tag is ever pushed.
#
# Usage: VERSION=v0.1.0 scripts/release.sh

set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
PLATFORMS="${PLATFORMS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"
DIST="${DIST:-dist}"

# The pure-Go SQLite driver is the reason this can cross-compile at all; if
# cgo creeps back in, these builds fail rather than silently going dynamic.
export CGO_ENABLED=0

LDFLAGS="-s -w -X github.com/lestex/vpncli/internal/cli.version=${VERSION}"

rm -rf "$DIST"
mkdir -p "$DIST"

for platform in $PLATFORMS; do
    GOOS="${platform%/*}"
    GOARCH="${platform#*/}"
    export GOOS GOARCH

    name="vpncli_${VERSION}_${GOOS}_${GOARCH}"
    staging="${DIST}/${name}"

    echo "building ${GOOS}/${GOARCH}"
    mkdir -p "$staging"
    # -trimpath keeps absolute build paths out of the binary.
    go build -trimpath -ldflags "$LDFLAGS" -o "${staging}/vpncli" .
    cp README.md LICENSE "$staging/"

    tar -czf "${staging}.tar.gz" -C "$DIST" "$name"
    rm -rf "$staging"
done

# Checksums, for verifying a download.
(
    cd "$DIST"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum ./*.tar.gz >SHA256SUMS
    else
        shasum -a 256 ./*.tar.gz >SHA256SUMS
    fi
)

echo
echo "${VERSION} artifacts in ${DIST}/:"
ls -1 "$DIST"
