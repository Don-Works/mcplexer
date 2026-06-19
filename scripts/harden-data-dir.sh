#!/usr/bin/env bash
#
# harden-data-dir.sh — apply tight file modes to ~/.mcplexer.
#
# Idempotent. Safe to re-run. Called from `make setup` so fresh installs
# land hardened; can also be run by hand at any time.
#
# Rationale: mcplexer is the user's MCP gateway + audit/security tool.
# Its on-disk state contains DB rows that an AI must NOT mutate directly
# (auth/audit/redaction live in the gateway), age-encrypted secrets, the
# API bearer token, libp2p peer keys, and backup snapshots. The hook
# layer (~/.claude/hooks/block-mcplexer-db.sh) blocks AI Bash/Read/Edit
# attempts at the harness level; this script is the filesystem-mode
# belt-and-braces in case the harness is bypassed.

set -euo pipefail

DATA_DIR="${MCPLEXER_DATA_DIR:-$HOME/.mcplexer}"

if [ ! -d "$DATA_DIR" ]; then
    echo "harden: $DATA_DIR does not exist; nothing to do."
    exit 0
fi

# Per-file 0600 (owner read/write only). Skip files that don't exist
# rather than fail — fresh installs only have a subset.
for f in "$DATA_DIR/mcplexer.db" \
         "$DATA_DIR/mcplexer.db-shm" \
         "$DATA_DIR/mcplexer.db-wal" \
         "$DATA_DIR/mcplexer.db.age" \
         "$DATA_DIR/api-key" \
         "$DATA_DIR/mcplexer.log" \
         "$DATA_DIR/mcplexer.log.1"; do
    if [ -f "$f" ]; then
        chmod 600 "$f" 2>/dev/null && echo "harden: 0600 $(basename "$f")"
    fi
done

# Lumberjack rotation drops backups as `mcplexer-<timestamp>.log[.gz]`
# alongside the active file. Glob-tighten any present so older rotated
# segments never end up world-readable.
shopt -s nullglob
for f in "$DATA_DIR"/mcplexer-*.log "$DATA_DIR"/mcplexer-*.log.gz; do
    if [ -f "$f" ]; then
        chmod 600 "$f" 2>/dev/null && echo "harden: 0600 $(basename "$f")"
    fi
done
shopt -u nullglob

# Per-dir 0700 (owner-only entry) for credential-bearing subdirs.
for d in "$DATA_DIR/secrets" \
         "$DATA_DIR/p2p" \
         "$DATA_DIR/backups" \
         "$DATA_DIR/api-key.d"; do
    if [ -d "$d" ]; then
        chmod 700 "$d" 2>/dev/null && echo "harden: 0700 $(basename "$d")/"
    fi
done

# Data dir itself: 0700 so a curious peer can't list the contents.
chmod 700 "$DATA_DIR" && echo "harden: 0700 $(basename "$DATA_DIR")/"

echo "harden: done."
