#!/usr/bin/env bash
# worktrees-gc.sh — remove orphan agent worktrees whose owner pid is
# no longer alive. Branches are PRESERVED (only the worktree dir +
# lockfile go); recover one with `git worktree add <new-path> <branch>`.
# Sweeps `.claude/worktrees/agent-*` only — never touches manually-
# created worktrees or the main checkout.
#
# Usage:  scripts/worktrees-gc.sh            # dry-run, prints what would go
#         scripts/worktrees-gc.sh --yes      # actually remove
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
DRY=true
[[ "${1:-}" == "--yes" ]] && DRY=false

# Pattern: lines like
#   /path/to/.claude/worktrees/agent-<hex> <sha> [<branch>] locked
removed=0
kept=0
skipped=0

while IFS= read -r line; do
  path="$(awk '{print $1}' <<<"$line")"
  case "$path" in
    "$REPO_ROOT"/.claude/worktrees/agent-*) ;;
    *) skipped=$((skipped+1)); continue ;;
  esac

  # Locked-only candidates (active worktrees are not "locked" — they
  # show empty trailing field).
  if ! grep -q '\[.*\] locked$' <<<"$line"; then
    kept=$((kept+1))
    continue
  fi

  # Reason field is "claude agent agent-<id> (pid N)" — yank the pid.
  reason="$(git worktree list --porcelain | awk -v p="$path" '
    $1=="worktree" && $2==p {found=1}
    found && $1=="locked" {sub(/^locked /, ""); print; exit}
  ')"
  pid="$(grep -oE '\(pid [0-9]+\)' <<<"$reason" | grep -oE '[0-9]+' || true)"

  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    echo "alive  pid=$pid     $path  ($reason)"
    kept=$((kept+1))
    continue
  fi

  if $DRY; then
    echo "REMOVE pid=${pid:-?}  $path  ($reason)"
  else
    git worktree remove -f -f "$path" 2>&1 | sed 's/^/  /' || true
  fi
  removed=$((removed+1))
done < <(git worktree list)

echo
if $DRY; then
  echo "Dry run. $removed orphans would be removed, $kept kept (alive or unlocked), $skipped non-agent paths skipped."
  echo "Re-run with --yes to actually remove."
else
  echo "Removed $removed orphans. Kept $kept. Skipped $skipped non-agent paths."
  git worktree prune
fi
