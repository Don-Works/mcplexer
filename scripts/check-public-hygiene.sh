#!/usr/bin/env bash
# Report only paths that may contain private/public-release material. Never
# print a matching line or a configured private pattern.
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "public-hygiene: run this check from a git worktree" >&2
  exit 2
}
cd "$repo_root"

failed=0
patterns_raw=$(mktemp "${TMPDIR:-/tmp}/mcplexer-public-hygiene.XXXXXX")
patterns_clean=$(mktemp "${TMPDIR:-/tmp}/mcplexer-public-hygiene.XXXXXX")
files_list=$(mktemp "${TMPDIR:-/tmp}/mcplexer-public-hygiene.XXXXXX")
trap 'rm -f "$patterns_raw" "$patterns_clean" "$files_list"' EXIT

git ls-files --cached --others --exclude-standard -z >"$files_list"

public_files() {
  cat "$files_list"
}

allowed_rule_path() {
  local rule=$1
  local path=$2
  case "$rule:$path" in
    google_chat_webhook:internal/audit/redact_test.go) return 0 ;;
    github_token:internal/compression/harness.go) return 0 ;;
  esac
  return 1
}

scan_regex() {
  local rule=$1
  local label=$2
  local pattern=$3
  local found=0
  local path

  while IFS= read -r path; do
    [[ "$path" == "scripts/check-public-hygiene.sh" ]] && continue
    allowed_rule_path "$rule" "$path" && continue
    if [[ "$found" -eq 0 ]]; then
      printf 'public-hygiene: possible %s in:\n' "$label" >&2
    fi
    printf '  %q\n' "$path" >&2
    found=1
    failed=1
  done < <(public_files | LC_ALL=C xargs -0 grep -I -l -E -- "$pattern" 2>/dev/null || true)
}

scan_sensitive_filenames() {
  local path base found=0
  while IFS= read -r -d '' path; do
    base=${path##*/}
    case "$base" in
      .env.example|.env.sample|.env.template|.env.*.example|.env.*.sample|.env.*.template) continue ;;
      *.example.pem|*.sample.pem|*.fixture.pem|*.example.p12|*.sample.p12|*.fixture.p12|*.example.pfx|*.sample.pfx|*.fixture.pfx) continue ;;
      .env|.env.*|*.pem|*.p12|*.pfx) ;;
      *) continue ;;
    esac
    if [[ "$found" -eq 0 ]]; then
      echo "public-hygiene: sensitive filename tracked or ready to add:" >&2
    fi
    printf '  %q\n' "$path" >&2
    found=1
    failed=1
  done < <(public_files)
}

scan_private_patterns() {
  local found=0 path

  if [[ -n "${MCPLEXER_PUBLIC_HYGIENE_PATTERNS:-}" ]]; then
    printf '%s\n' "$MCPLEXER_PUBLIC_HYGIENE_PATTERNS" >>"$patterns_raw"
  fi
  if [[ -n "${MCPLEXER_PUBLIC_HYGIENE_PATTERNS_FILE:-}" ]]; then
    if [[ ! -r "$MCPLEXER_PUBLIC_HYGIENE_PATTERNS_FILE" ]]; then
      echo "public-hygiene: configured private pattern file is unreadable" >&2
      failed=1
      return
    fi
    cat "$MCPLEXER_PUBLIC_HYGIENE_PATTERNS_FILE" >>"$patterns_raw"
  fi

  # Blank lines would match every file; comments let a local denylist explain
  # itself without becoming a search pattern.
  sed -e '/^[[:space:]]*$/d' -e '/^[[:space:]]*#/d' "$patterns_raw" >"$patterns_clean"
  [[ -s "$patterns_clean" ]] || return 0

  while IFS= read -r path; do
    if [[ "$found" -eq 0 ]]; then
      echo "public-hygiene: configured private identifier found in:" >&2
    fi
    printf '  %q\n' "$path" >&2
    found=1
    failed=1
  done < <(public_files | LC_ALL=C xargs -0 grep -I -l -F -f "$patterns_clean" -- 2>/dev/null || true)
}

scan_sensitive_filenames
scan_regex private_key "private-key PEM block" '^[[:space:]]*-----BEGIN (RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----'
scan_regex google_chat_webhook "Google Chat webhook" 'https://chat\.googleapis\.com/v1/spaces/[^/[:space:]]+/messages\?key=[^&[:space:]]+&token='
scan_regex slack_webhook "Slack webhook" 'https://hooks\.slack\.com/services/[[:alnum:]_/-]+'
scan_regex github_token "GitHub token" '(gh[pousr]_[[:alnum:]]{20,}|github_pat_[[:alnum:]_]{40,})'
scan_regex slack_token "Slack token" 'xox[baprs]-[[:alnum:]-]{20,}'
scan_regex telegram_bot_token "Telegram bot token" '[0-9]{6,12}:[[:alnum:]_-]{30,}'
scan_regex tailscale_key "Tailscale key" 'tskey-(auth|client|api)-[[:alnum:]_-]{16,}'
scan_regex aws_key "AWS access key" 'AKIA[0-9A-Z]{16}'
scan_private_patterns

if [[ "$failed" -ne 0 ]]; then
  echo "public-hygiene: FAILED; replace private material with neutral examples" >&2
  exit 1
fi

echo "public-hygiene: OK (path-only scan)"
