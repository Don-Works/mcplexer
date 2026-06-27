#!/usr/bin/env bash
# Install MCPlexer from GitHub Release artifacts on macOS/Linux.
set -euo pipefail

REPO="${MCPLEXER_REPO:-Don-Works/mcplexer}"
VERSION="latest"
BIN_DIR="${MCPLEXER_BIN_DIR:-$HOME/.mcplexer/bin}"
RUN_SETUP=1

usage() {
  cat <<'EOF'
Usage: install-release.sh [--version vX.Y.Z] [--bin-dir PATH] [--no-setup]

Downloads the matching MCPlexer release archive, verifies its checksum,
installs mcplexer to ~/.mcplexer/bin by default, and runs mcplexer setup.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    --no-setup) RUN_SETUP=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "required command not found: $1" >&2
    exit 1
  fi
}
need curl
need tar
need shasum

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin|linux) ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [[ "$VERSION" == "latest" ]]; then
  VERSION="$(
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" |
      sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
      head -n 1
  )"
fi
if [[ -z "$VERSION" ]]; then
  echo "could not determine release version" >&2
  exit 1
fi

asset="mcplexer_${VERSION}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${VERSION}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "==> Downloading ${asset}"
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

echo "==> Verifying checksum"
(cd "$tmp" && grep "  ./${asset}$" checksums.txt | shasum -a 256 -c -)

echo "==> Installing to ${BIN_DIR}"
tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$BIN_DIR"
cp "$tmp/mcplexer_${VERSION}_${os}_${arch}/mcplexer" "$BIN_DIR/mcplexer"
chmod 0755 "$BIN_DIR/mcplexer"

echo "==> Installed $("$BIN_DIR/mcplexer" version)"
if [[ ":$PATH:" != *":$BIN_DIR:"* ]]; then
  echo "Add this to PATH if needed: $BIN_DIR"
fi

if [[ "$RUN_SETUP" -eq 1 ]]; then
  exec "$BIN_DIR/mcplexer" setup
fi
