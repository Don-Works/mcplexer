#!/usr/bin/env bash
# Write deterministic SHA-256 sums for every release payload except the sum file.
set -euo pipefail

DIST_DIR="${1:-dist}"
[[ -d "$DIST_DIR" ]] || {
  echo "release directory does not exist: $DIST_DIR" >&2
  exit 1
}

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

(
  cd "$DIST_DIR"
  find . -mindepth 1 -maxdepth 1 -type f ! -name checksums.txt -print \
    | LC_ALL=C sort \
    | while IFS= read -r file; do
        shasum -a 256 "$file"
      done
) > "$tmp"

[[ -s "$tmp" ]] || {
  echo "release directory contains no payloads: $DIST_DIR" >&2
  exit 1
}
mv "$tmp" "$DIST_DIR/checksums.txt"
chmod 0644 "$DIST_DIR/checksums.txt"
trap - EXIT
