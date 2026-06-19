#!/bin/sh
#
# upgrade.sh — hardened in-place daemon swap with drain-wait, readiness
# verification, and automatic rollback on failure.
#
# Usage:
#   scripts/upgrade.sh                   # normal upgrade
#   scripts/upgrade.sh --dry-run         # show what would happen, exit 0
#   scripts/upgrade.sh --binary <path>   # override source binary
#
# Lifecycle contract:
#   GET /api/v1/health returns 200 with status=ready only after migrations
#   + downstream init; 503 with status=starting|draining otherwise.
#   Legacy deployments may expose a separate readiness field — both are
#   accepted. SIGTERM triggers draining state.
#
# Semantics:
#   drain  — request drain + wait for old daemon to stop. Does NOT restart.
#   swap   — atomically install the new binary. Begins only after drain
#            confirms the old daemon is down.
#   start  — load/reload the service manager so the new binary runs.
#            Only called after swap (normal path) or during rollback.
#   rollback — halt the new daemon, restore .prev binary, start again.
#
# Environment:
#   MCPLEXER_BIN_DIR   — install directory (default ~/.mcplexer/bin)
#   MCPLEXER_ADDR      — health endpoint base URL (default derived from launchd --addr, then MCPLEXER_HTTP_ADDR, then http://127.0.0.1:3333)
#   MCPLEXER_PLIST     — launchd plist path (default ~/Library/LaunchAgents/com.mcplexer.daemon.plist)
#   MCPLEXER_API_TOKEN_PATH — API bearer token path (default ~/.mcplexer/api-key)
#   MCPLEXER_ALLOW_ACTIVE_WORKER_RESTART=1 — bypass active worker-run guard
#   MCPLEXER_WAIT_FOR_ACTIVE_WORKERS=1 — wait for running worker runs to drain instead of failing
#   ACTIVE_WORKER_TIMEOUT — seconds to wait for active worker runs (default 900)
#   ACTIVE_WORKER_POLL_INTERVAL — seconds between worker-run polls (default 5)
#   DRAIN_TIMEOUT      — seconds to wait for drain (default 30)
#   READY_TIMEOUT      — seconds to wait for ready after restart (default 60)
#   HEALTH_CURL_TIMEOUT — seconds before a health probe gives up (default 3)

set -eu

# --- config ---

BIN_DIR="${MCPLEXER_BIN_DIR:-$HOME/.mcplexer/bin}"
DAEMON="$BIN_DIR/mcplexer"
PLIST_FOR_ADDR="${MCPLEXER_PLIST:-$HOME/Library/LaunchAgents/com.mcplexer.daemon.plist}"
ADDR_SOURCE="${MCPLEXER_ADDR:-}"
if [ -z "$ADDR_SOURCE" ] && [ -f "$PLIST_FOR_ADDR" ]; then
    ADDR_SOURCE=$(sed -n 's/.*<string>--addr=\(.*\)<\/string>.*/\1/p' "$PLIST_FOR_ADDR" | head -n 1)
fi
if [ -z "$ADDR_SOURCE" ]; then
    ADDR_SOURCE="${MCPLEXER_HTTP_ADDR:-127.0.0.1:3333}"
fi
case "$ADDR_SOURCE" in
    http://*|https://*) ADDR="$ADDR_SOURCE" ;;
    0.0.0.0:*) ADDR="http://127.0.0.1:${ADDR_SOURCE#0.0.0.0:}" ;;
    :*) ADDR="http://127.0.0.1$ADDR_SOURCE" ;;
    *) ADDR="http://$ADDR_SOURCE" ;;
esac
HEALTH_URL="$ADDR/api/v1/health"
DRAIN_TIMEOUT="${DRAIN_TIMEOUT:-30}"
READY_TIMEOUT="${READY_TIMEOUT:-60}"
HEALTH_CURL_TIMEOUT="${HEALTH_CURL_TIMEOUT:-3}"
API_TOKEN_PATH="${MCPLEXER_API_TOKEN_PATH:-$HOME/.mcplexer/api-key}"
ACTIVE_WORKER_TIMEOUT="${ACTIVE_WORKER_TIMEOUT:-900}"
ACTIVE_WORKER_POLL_INTERVAL="${ACTIVE_WORKER_POLL_INTERVAL:-5}"

SRC_BINARY=""
DRY_RUN=false

# --- parse args ---

while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run)  DRY_RUN=true; shift ;;
        --binary)   SRC_BINARY="$2"; shift 2 ;;
        -h|--help)
            echo "Usage: $0 [--dry-run] [--binary <path>]"
            exit 0
            ;;
        *)
            echo "unknown argument: $1" >&2
            exit 1
            ;;
    esac
done

# Derive source binary from build output if not specified.
if [ -z "$SRC_BINARY" ]; then
    SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
    if [ -f "$SCRIPT_DIR/../bin/mcplexer-p2p" ]; then
        SRC_BINARY="$SCRIPT_DIR/../bin/mcplexer-p2p"
    elif [ -f "$SCRIPT_DIR/../bin/mcplexer" ]; then
        SRC_BINARY="$SCRIPT_DIR/../bin/mcplexer"
    else
        echo "error: no built binary found. Run 'make build' first." >&2
        exit 1
    fi
fi

if [ ! -f "$SRC_BINARY" ]; then
    echo "error: source binary not found: $SRC_BINARY" >&2
    exit 1
fi

# --- platform detection ---

launchd_user_domain() {
    echo "gui/$(id -u)"
}

launchd_service_target() {
    echo "gui/$(id -u)/com.mcplexer.daemon"
}

launchd_plist_path() {
    echo "${MCPLEXER_PLIST:-$HOME/Library/LaunchAgents/com.mcplexer.daemon.plist}"
}

is_darwin() {
    test "$(uname -s)" = "Darwin"
}

is_launchd_installed() {
    test -f "$(launchd_plist_path)"
}

is_systemd_user() {
    systemctl --user is-enabled mcplexer.service >/dev/null 2>&1
}

# --- helpers ---

log() { printf "%s\n" "$@"; }

parse_health_state() {
    _body="$1"
    _val=$(printf "%s" "$_body" | sed -n 's/.*"readiness"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    if [ -z "$_val" ]; then
        _val=$(printf "%s" "$_body" | sed -n 's/.*"status"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
        case "$_val" in
            ok) _val=ready ;;
        esac
    fi
    printf "%s" "$_val"
}

health_readiness() {
    _resp=$(curl -s --connect-timeout 1 --max-time "$HEALTH_CURL_TIMEOUT" -o - -w '\n%{http_code}' "$HEALTH_URL" 2>/dev/null) || {
        printf "down"
        return
    }
    _code=$(printf "%s" "$_resp" | tail -1)
    _body=$(printf "%s" "$_resp" | sed '$d')
    if [ "$_code" = "200" ] || [ "$_code" = "503" ]; then
        _val=$(parse_health_state "$_body")
        if [ -n "$_val" ]; then
            printf "%s" "$_val"
            return
        fi
    fi
    printf "down"
}

api_get() {
    _path="$1"
    if [ ! -f "$API_TOKEN_PATH" ]; then
        return 3
    fi
    _token=$(tr -d '\r\n' < "$API_TOKEN_PATH")
    if [ -z "$_token" ]; then
        return 3
    fi
    _cfg=$(mktemp "${TMPDIR:-/tmp}/mcplexer-upgrade-curl.XXXXXX")
    chmod 600 "$_cfg"
    printf 'header = "Authorization: Bearer %s"\n' "$_token" > "$_cfg"
    set +e
    _out=$(curl -fsS --config "$_cfg" "$ADDR$_path")
    _rc=$?
    set -e
    rm -f "$_cfg"
    printf "%s" "$_out"
    return "$_rc"
}

active_worker_run_count() {
    _workers=$(api_get "/api/v1/workers" 2>/dev/null) || return 1
    _count=$(printf "%s" "$_workers" | grep -o '"last_run_status":"running"' | wc -l | tr -d '[:space:]')
    printf "%s" "${_count:-0}"
}

ensure_no_active_worker_runs() {
    if [ "${MCPLEXER_ALLOW_ACTIVE_WORKER_RESTART:-0}" = "1" ]; then
        log "==> Active worker-run guard bypassed by MCPLEXER_ALLOW_ACTIVE_WORKER_RESTART=1."
        return 0
    fi
    _elapsed=0
    while :; do
        _count=$(active_worker_run_count) || {
            log "FAILED: could not check active worker runs via $ADDR/api/v1/workers." >&2
            log "       Refusing to restart because live delegated sessions may be running." >&2
            log "       Set MCPLEXER_ALLOW_ACTIVE_WORKER_RESTART=1 only after confirming the queue is idle." >&2
            exit 1
        }
        if [ "$_count" -eq 0 ] 2>/dev/null; then
            log "==> Active worker-run guard passed (0 running)."
            return 0
        fi
        if [ "${MCPLEXER_WAIT_FOR_ACTIVE_WORKERS:-0}" != "1" ]; then
            log "FAILED: $_count worker run(s) are currently running." >&2
            log "       Refusing to restart because this daemon owns model subprocesses today." >&2
            log "       Wait for runs to finish, cancel them explicitly, set MCPLEXER_WAIT_FOR_ACTIVE_WORKERS=1," >&2
            log "       or set MCPLEXER_ALLOW_ACTIVE_WORKER_RESTART=1 after confirming a restart is acceptable." >&2
            exit 1
        fi
        if [ "$_elapsed" -ge "$ACTIVE_WORKER_TIMEOUT" ] 2>/dev/null; then
            log "FAILED: timed out after ${ACTIVE_WORKER_TIMEOUT}s waiting for active worker runs to finish." >&2
            log "       Last observed active worker run count: $_count." >&2
            exit 1
        fi
        log "==> $_count worker run(s) active; waiting before daemon restart (${_elapsed}/${ACTIVE_WORKER_TIMEOUT}s)..."
        sleep "$ACTIVE_WORKER_POLL_INTERVAL"
        _elapsed=$((_elapsed + ACTIVE_WORKER_POLL_INTERVAL))
    done
}

# signal_drain sends SIGTERM so the daemon transitions to draining state.
# It does NOT restart; it does NOT wait. Call wait_for_drain after this.
signal_drain() {
    log "==> Sending drain signal (SIGTERM)..."
    if is_darwin && is_launchd_installed; then
        launchctl kill SIGTERM "$(launchd_service_target)" 2>/dev/null || \
            "$DAEMON" daemon stop 2>/dev/null || true
    elif is_systemd_user; then
        systemctl --user stop mcplexer.service 2>/dev/null || true
    else
        "$DAEMON" daemon stop 2>/dev/null || true
    fi
}

# halt_daemon forcefully stops the daemon and prevents service-manager
# restart (KeepAlive). Called when the drain loop detects that the service
# manager restarted the daemon before we swapped the binary.
halt_daemon() {
    log "==> Halting daemon (prevent restart)..."
    if is_darwin && is_launchd_installed; then
        launchctl bootout "$(launchd_user_domain)" "$(launchd_plist_path)" 2>/dev/null || \
            launchctl bootout "$(launchd_service_target)" 2>/dev/null || true
    elif is_systemd_user; then
        systemctl --user stop mcplexer.service 2>/dev/null || true
    else
        "$DAEMON" daemon stop 2>/dev/null || true
    fi
    # Brief settle after bootout/stop.
    sleep 1
}

# start_daemon loads and starts the daemon via the platform service manager.
# On macOS with launchd, bootout unloads the job; start_daemon re-bootstraps
# it. KeepAlive=true in the plist means launchd auto-launches after bootstrap.
start_daemon() {
    log "==> Starting daemon..."
    if is_darwin && is_launchd_installed; then
        if launchctl print "$(launchd_service_target)" >/dev/null 2>&1; then
            launchctl kickstart "$(launchd_service_target)" 2>/dev/null || true
        else
            launchctl bootstrap "$(launchd_user_domain)" "$(launchd_plist_path)" 2>/dev/null || true
        fi
    elif is_systemd_user; then
        systemctl --user start mcplexer.service 2>/dev/null || true
    else
        "$DAEMON" daemon stop 2>/dev/null || true
        "$DAEMON" daemon start 2>/dev/null || true
    fi
}

wait_for_drain() {
    # Poll health until the daemon is down (no longer responding) or
    # we timeout. If the daemon restarts (KeepAlive), halt it.
    # Returns 0 if the daemon went down, 1 on timeout.
    _timeout=$1
    _elapsed=0
    _halted=false
    log "==> Waiting for daemon to drain (timeout ${_timeout}s)..."
    while [ "$_elapsed" -lt "$_timeout" ]; do
        _state=$(health_readiness)
        case "$_state" in
            down)
                log "    Daemon is down."
                return 0
                ;;
            draining)
                printf "    draining...(%d/%d)s\r" "$_elapsed" "$_timeout"
                ;;
            ready|starting)
                if "$_halted"; then
                    printf "    Daemon restarted after halt, resending drain signal...\n"
                    signal_drain
                else
                    printf "    Daemon still live (readiness=%s), halting to prevent KeepAlive restart...\n" "$_state"
                    halt_daemon
                    _halted=true
                fi
                ;;
        esac
        sleep 1
        _elapsed=$((_elapsed + 1))
    done
    printf "\n"
    log "    Timed out waiting for drain after ${_timeout}s." >&2
    return 1
}

wait_for_ready() {
    # Poll health until readiness=ready or timeout.
    _timeout=$1
    _elapsed=0
    log "==> Waiting for daemon to become ready (timeout ${_timeout}s)..."
    while [ "$_elapsed" -lt "$_timeout" ]; do
        _state=$(health_readiness)
        case "$_state" in
            ready)
                printf "\n"
                log "    Daemon is ready."
                return 0
                ;;
            starting)
                printf "    starting...(%d/%d)s\r" "$_elapsed" "$_timeout"
                ;;
            down)
                printf "    waiting...(%d/%d)s\r" "$_elapsed" "$_timeout"
                ;;
            draining)
                printf "    draining...(%d/%d)s\r" "$_elapsed" "$_timeout"
                ;;
        esac
        sleep 1
        _elapsed=$((_elapsed + 1))
    done
    printf "\n"
    log "    Timed out waiting for ready after ${_timeout}s." >&2
    return 1
}

do_swap() {
    # Atomically swap the binary, keeping a .prev copy for rollback.
    log "==> Backing up current binary..."
    if [ -f "$DAEMON" ]; then
        # A stale .prev may still be mapped by long-lived `mcplexer connect`
        # clients. Unlink it first so Linux can keep the old inode alive for
        # those clients while the upgrade writes a fresh rollback copy.
        rm -f "$DAEMON.prev"
        cp "$DAEMON" "$DAEMON.prev"
        chmod +x "$DAEMON.prev"
    fi

    log "==> Installing new binary (atomic rename)..."
    mkdir -p "$BIN_DIR"
    cp "$SRC_BINARY" "$DAEMON.new"
    chmod +x "$DAEMON.new"
    mv "$DAEMON.new" "$DAEMON"
}

do_rollback() {
    # Restore the previous binary and restart.
    log "==> ROLLING BACK to previous binary..." >&2
    if [ -f "$DAEMON.prev" ]; then
        # Halt the running (bad) daemon first, then swap binary, then start.
        halt_daemon
        sleep 1
        mv "$DAEMON.prev" "$DAEMON"
        log "    Previous binary restored."
        start_daemon
        log "    Daemon restarted with previous binary."
    else
        log "    No previous binary to roll back to." >&2
    fi
}

# --- main ---

if [ ! -f "$DAEMON" ]; then
    log ""
    log "MCPlexer is not installed yet. Run 'make install' first."
    exit 1
fi

if $DRY_RUN; then
    log "==> DRY RUN — no changes will be made."
    log "    Source binary:  $SRC_BINARY"
    log "    Target:         $DAEMON"
    log "    Backup:         $DAEMON.prev"
    log "    Health URL:     $HEALTH_URL"
    log "    Drain timeout:  ${DRAIN_TIMEOUT}s"
    log "    Ready timeout:  ${READY_TIMEOUT}s"
    log "    Worker guard:   wait=${MCPLEXER_WAIT_FOR_ACTIVE_WORKERS:-0}, timeout=${ACTIVE_WORKER_TIMEOUT}s"
    _state=$(health_readiness)
    log "    Current state:  $_state"
    log "    Action: drain -> swap -> start -> verify-ready"
    log "==> Dry run complete."
    exit 0
fi

# 1. Signal the daemon to drain, then wait for it to go down.
#    The drain phase must NOT restart the daemon (the old binary must stop).
ensure_no_active_worker_runs
log "==> Signalling daemon to drain..."
signal_drain
if ! wait_for_drain "$DRAIN_TIMEOUT"; then
    log "FAILED: drain timeout exceeded; old daemon did not stop. Aborting before binary swap." >&2
    exit 1
fi
# Ensure the service manager is unloaded after drain so launchd/systemd picks
# up changed service definitions such as new EnvironmentVariables.
halt_daemon

# 2. Swap the binary. Only after the old daemon is confirmed down.
do_swap

# 3. Re-harden data dir file modes.
if [ -f "$(dirname "$0")/harden-data-dir.sh" ]; then
    log "==> Re-hardening data dir file modes..."
    bash "$(dirname "$0")/harden-data-dir.sh"
fi

# 4. Sync agent rules.
log "==> Syncing agent rules..."
"$DAEMON" rules sync 2>/dev/null || log "    (skipped; agent rules will sync on next setup)"

# 5. Start the daemon with the new binary.
start_daemon

# 6. Wait for readiness.
if wait_for_ready "$READY_TIMEOUT"; then
    log ""
    log "Upgrade complete."
    "$DAEMON" daemon status 2>/dev/null | head -1 || true
    # Clean up .prev on success.
    rm -f "$DAEMON.prev"
    exit 0
fi

# 7. Ready check failed — rollback.
log ""
log "FAILED: daemon did not become ready within ${READY_TIMEOUT}s." >&2
do_rollback
exit 1
