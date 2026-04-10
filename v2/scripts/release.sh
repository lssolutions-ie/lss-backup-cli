#!/usr/bin/env bash
# scripts/release.sh — cross-compile lss-backup-cli for all supported platforms
# and create a GitHub release with the binaries attached.
#
# Usage (run from the v2/ directory):
#   ./scripts/release.sh
#
# Requires: go, gh (GitHub CLI, authenticated)
# The version is read from internal/version/version.go automatically.

set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=$(grep 'Current\s*=' internal/version/version.go | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+')
if [ -z "$VERSION" ]; then
  echo "ERROR: could not parse version from internal/version/version.go" >&2
  exit 1
fi

echo "Building release $VERSION"

BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT

PLATFORMS=(
  "windows amd64 .exe"
  "windows arm64 .exe"
  "darwin  amd64 "
  "darwin  arm64 "
  "linux   amd64 "
  "linux   arm64 "
)

ASSETS=()

for entry in "${PLATFORMS[@]}"; do
  read -r GOOS GOARCH EXT <<< "$entry"
  BINNAME="lss-backup-cli-${GOOS}-${GOARCH}${EXT}"
  OUT="$BUILD_DIR/$BINNAME"
  echo "  compiling $BINNAME..."
  GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags="-s -w" -o "$OUT" ./cmd/lss-backup
  ASSETS+=("$OUT")
done

echo ""
echo "Creating tag $VERSION..."
git tag "$VERSION" 2>/dev/null || echo "  tag $VERSION already exists, skipping"

echo "Pushing tag..."
git push origin "$VERSION"

echo "Creating GitHub release..."
gh release create "$VERSION" \
  --title "$VERSION" \
  --notes "See CLAUDE.md for full changelog." \
  "${ASSETS[@]}"

echo ""
echo "Release $VERSION published."
echo "Binary assets:"
for a in "${ASSETS[@]}"; do
  echo "  $(basename "$a")"
done
