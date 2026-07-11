#!/usr/bin/env bash
# Build installable release archives for GitHub Releases.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-$(git describe --tags --dirty --always --abbrev=12 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}"
DIST_DIR="${DIST_DIR:-dist}"
WORK_DIR="${WORK_DIR:-.release-work}"

if [[ "$VERSION" == *dirty* ]]; then
  echo "refusing to build release artifacts from dirty version: $VERSION" >&2
  echo "commit or stash local changes, or set VERSION explicitly for a snapshot" >&2
  exit 1
fi

rm -rf "$DIST_DIR" "$WORK_DIR"
mkdir -p "$DIST_DIR" "$WORK_DIR"

echo "==> Building web bundle"
(cd web && npm ci && npm run build)

LDFLAGS="-s -w -X main.buildVersion=${VERSION} -X main.buildCommit=${COMMIT}"

build_one() {
  local goos="$1"
  local goarch="$2"
  local ext=""
  local archive_ext="tar.gz"
  if [[ "$goos" == "windows" ]]; then
    ext=".exe"
    archive_ext="zip"
  fi

  local name="mcplexer_${VERSION}_${goos}_${goarch}"
  local pkg="$WORK_DIR/$name"
  local binary="$pkg/mcplexer${ext}"
  mkdir -p "$pkg"

  echo "==> Building ${goos}/${goarch}"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -tags p2p -ldflags "$LDFLAGS" -o "$binary" ./cmd/mcplexer

  if [[ "$goos" == "darwin" ]]; then
    sh scripts/codesign-darwin.sh "$binary"
  fi

  cp README.md LICENSE NOTICE THIRD_PARTY_NOTICES.md "$pkg/"
  cp scripts/install-release.sh scripts/install-release.ps1 "$pkg/"
  cat > "$pkg/INSTALL.txt" <<EOF
MCPlexer ${VERSION}

This archive contains a self-contained MCPlexer binary with the web UI embedded.

Quick install:

  macOS/Linux:
    ./install-release.sh --version ${VERSION}

  Windows PowerShell:
    ./install-release.ps1 -Version ${VERSION}

Manual install:
  1. Copy mcplexer${ext} to a directory on PATH, or to ~/.mcplexer/bin.
  2. Run: mcplexer${ext} version --json
  3. Run: mcplexer${ext} setup
  4. Open: http://127.0.0.1:3333

Verify checksums against checksums.txt from the GitHub Release.
EOF

  if [[ "$archive_ext" == "zip" ]]; then
    (cd "$WORK_DIR" && zip -qr "../$DIST_DIR/${name}.zip" "$name")
  else
    tar -C "$WORK_DIR" -czf "$DIST_DIR/${name}.tar.gz" "$name"
  fi
}

build_one darwin amd64
build_one darwin arm64
build_one linux amd64
build_one linux arm64
build_one windows amd64
build_one windows arm64

echo "==> Writing checksums"
(cd "$DIST_DIR" && shasum -a 256 ./* > checksums.txt)

echo "==> Release artifacts"
ls -lh "$DIST_DIR"
