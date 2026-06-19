#!/usr/bin/env bash
# preflight.sh — validate Go 1.25+ and Node 20+ before build.
# Silent on success; prints actionable guidance on failure.
set -euo pipefail

errors=0

# --- Go >= 1.25 ---
if ! command -v go &>/dev/null; then
  echo "✗ Go not found. Install Go 1.25+:"
  echo "  https://go.dev/dl/"
  errors=1
else
  go_ver=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | head -1 | sed 's/go//')
  go_major=${go_ver%%.*}
  go_minor=${go_ver##*.}
  if [ "$go_major" -lt 1 ] || { [ "$go_major" -eq 1 ] && [ "$go_minor" -lt 25 ]; }; then
    echo "✗ Go $go_ver found; Go 1.25+ required."
    echo "  Upgrade: https://go.dev/dl/"
    errors=1
  fi
fi

# --- Node >= 20 ---
if ! command -v node &>/dev/null; then
  echo "✗ Node.js not found. Install Node 20+:"
  echo "  https://nodejs.org/"
  errors=1
else
  node_ver=$(node -v | sed 's/v//')
  node_major=${node_ver%%.*}
  if [ "$node_major" -lt 20 ]; then
    echo "✗ Node.js v$node_ver found; Node 20+ required."
    echo "  Upgrade: https://nodejs.org/ (or use nvm: nvm install 20)"
    errors=1
  fi
fi

if [ "$errors" -ne 0 ]; then
  exit 1
fi
