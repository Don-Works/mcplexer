#!/bin/sh
#
# test_upgrade.sh — unit-test the upgrade script's helper logic
# against mock state. No real daemon required. Stubs launchctl,
# systemctl, and curl so the full script logic is exercised in
# isolation without touching the host's services.
#
# Coverage:
#   - swap, rollback, dry-run, missing-binary, missing-source
#   - default ADDR aligns with daemon default (127.0.0.1:3333)
#   - launchd --addr is discovered when MCPLEXER_ADDR is unset
#   - signal_drain → SIGTERM (not kickstart -k)
#   - halt_daemon → bootout (not kickstart)
#   - start_daemon → bootstrap + kickstart (not kickstart -k)
#   - drain flow: signal → draining → down → swap → start → ready
#   - drain KeepAlive restart: signal → ready again → halt → down
#   - drain timeout, ready timeout, rollback path
#   - Linux systemd path (when uname=Linux)
#   - health_readiness parsing (ready/starting/draining/down/edge)

set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

PASS=0
FAIL=0
WORK_DIR=""

setup() {
    WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/upgrade-test.XXXXXX")
    mkdir -p "$WORK_DIR/bin" "$WORK_DIR/src" "$WORK_DIR/mocks"
    touch "$WORK_DIR/bin/mcplexer"
    chmod +x "$WORK_DIR/bin/mcplexer"
    touch "$WORK_DIR/src/mcplexer-p2p"
    chmod +x "$WORK_DIR/src/mcplexer-p2p"
}

teardown() {
    if [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
        rm -rf "$WORK_DIR"
    fi
}

assert_eq() {
    _label="$1"
    _expected="$2"
    _actual="$3"
    if [ "$_expected" = "$_actual" ]; then
        PASS=$((PASS + 1))
        printf "  PASS: %s\n" "$_label"
    else
        FAIL=$((FAIL + 1))
        printf "  FAIL: %s: expected '%s', got '%s'\n" "$_label" "$_expected" "$_actual" >&2
    fi
}

assert_contains() {
    _label="$1"
    _needle="$2"
    _haystack="$3"
    case "$_haystack" in
        *"$_needle"*)
            PASS=$((PASS + 1))
            printf "  PASS: %s\n" "$_label"
            ;;
        *)
            FAIL=$((FAIL + 1))
            printf "  FAIL: %s: missing '%s' in output\n" "$_label" "$_needle" >&2
            ;;
    esac
}

mock_curl() {
    _state="${1:-ready}"
    _code="${2:-200}"
    _format="${3:-status}"
    printf '#!/bin/sh\n' > "$WORK_DIR/mocks/curl"
    if [ "$_state" = "down" ]; then
        printf 'exit 7\n' >> "$WORK_DIR/mocks/curl"
    elif [ "$_format" = "readiness" ]; then
        printf 'printf '"'"'{"status":"ok","readiness":"%s","version":"test"}\n%d'"'"'\n' "$_state" "$_code" >> "$WORK_DIR/mocks/curl"
    else
        printf 'printf '"'"'{"status":"%s","version":"test"}\n%d'"'"'\n' "$_state" "$_code" >> "$WORK_DIR/mocks/curl"
    fi
    chmod +x "$WORK_DIR/mocks/curl"
}

# --- test: do_swap creates .prev backup ---

test_swap_backup() {
    printf "old-binary-content" > "$WORK_DIR/bin/mcplexer"
    DAEMON="$WORK_DIR/bin/mcplexer"
    BIN_DIR="$WORK_DIR/bin"
    SRC_BINARY="$WORK_DIR/src/mcplexer-p2p"

    if [ -f "$DAEMON" ]; then
        cp "$DAEMON" "$DAEMON.prev"
        chmod +x "$DAEMON.prev"
    fi
    mkdir -p "$BIN_DIR"
    cp "$SRC_BINARY" "$DAEMON.new"
    chmod +x "$DAEMON.new"
    mv "$DAEMON.new" "$DAEMON"

    assert_eq "prev_exists" "old-binary-content" "$(cat "$WORK_DIR/bin/mcplexer.prev")"
    assert_eq "new_binary_from_src" "" "$(cat "$WORK_DIR/bin/mcplexer")"
    assert_eq "new_binary_executable" "yes" "$([ -x "$WORK_DIR/bin/mcplexer" ] && echo yes || echo no)"
}

# --- test: do_rollback restores .prev ---

test_rollback() {
    printf "old-binary-content" > "$WORK_DIR/bin/mcplexer.prev"
    printf "bad-new-binary" > "$WORK_DIR/bin/mcplexer"
    DAEMON="$WORK_DIR/bin/mcplexer"

    if [ -f "$DAEMON.prev" ]; then
        mv "$DAEMON.prev" "$DAEMON"
    fi

    assert_eq "rollback_restored" "old-binary-content" "$(cat "$WORK_DIR/bin/mcplexer")"
    assert_eq "prev_removed" "no" "$([ -f "$WORK_DIR/bin/mcplexer.prev" ] && echo yes || echo no)"
}

# --- test: dry-run does not modify files ---

test_dry_run() {
    printf "original" > "$WORK_DIR/bin/mcplexer"
    _before=$(cat "$WORK_DIR/bin/mcplexer")

    mock_curl "ready" "200"

    MCPLEXER_BIN_DIR="$WORK_DIR/bin" \
        MCPLEXER_ADDR="http://127.0.0.1:3333" \
        PATH="$WORK_DIR/mocks:$PATH" \
        sh "$SCRIPT_DIR/upgrade.sh" \
            --dry-run --binary "$WORK_DIR/src/mcplexer-p2p" >/dev/null 2>&1 || true
    _after=$(cat "$WORK_DIR/bin/mcplexer")

    assert_eq "dry_run_no_modify" "$_before" "$_after"
}

# --- test: dry-run output includes key fields ---

test_dry_run_output() {
    printf "original" > "$WORK_DIR/bin/mcplexer"
    mock_curl "ready" "200"

    _output=$(MCPLEXER_BIN_DIR="$WORK_DIR/bin" \
        MCPLEXER_ADDR="http://127.0.0.1:3333" \
        PATH="$WORK_DIR/mocks:$PATH" \
        sh "$SCRIPT_DIR/upgrade.sh" \
            --dry-run --binary "$WORK_DIR/src/mcplexer-p2p" 2>&1) || true

    assert_contains "dry_run_label" "DRY RUN" "$_output"
    assert_contains "dry_run_src" "Source binary" "$_output"
    assert_contains "dry_run_action" "drain" "$_output"
    assert_contains "dry_run_health" "Health URL" "$_output"
}

# --- test: dry-run shows correct default ADDR (3333, not 13333) ---

test_dry_run_addr_default() {
	printf "original" > "$WORK_DIR/bin/mcplexer"
	mock_curl "ready" "200"

	_output=$(MCPLEXER_BIN_DIR="$WORK_DIR/bin" \
		MCPLEXER_PLIST="$WORK_DIR/missing.plist" \
		PATH="$WORK_DIR/mocks:$PATH" \
		sh "$SCRIPT_DIR/upgrade.sh" \
			--dry-run --binary "$WORK_DIR/src/mcplexer-p2p" 2>&1) || true

    assert_contains "dry_run_default_addr" "127.0.0.1:3333" "$_output"
    case "$_output" in
        *"13333"*) assert_eq "dry_run_not_13333" "should not have 13333" "has 13333" ;;
        *) assert_eq "dry_run_not_13333" "should not have 13333" "should not have 13333" ;;
	esac
}

# --- test: dry-run derives ADDR from launchd plist when present ---

test_dry_run_addr_from_launchd_plist() {
	printf "original" > "$WORK_DIR/bin/mcplexer"
	mock_curl "ready" "200"
	_plist="$WORK_DIR/com.mcplexer.daemon.plist"
	cat > "$_plist" <<'PLIST'
<plist version="1.0">
<dict>
	<key>ProgramArguments</key>
	<array>
		<string>mcplexer</string>
		<string>serve</string>
		<string>--addr=0.0.0.0:13333</string>
	</array>
</dict>
</plist>
PLIST

	_output=$(MCPLEXER_BIN_DIR="$WORK_DIR/bin" \
		MCPLEXER_PLIST="$_plist" \
		PATH="$WORK_DIR/mocks:$PATH" \
		sh "$SCRIPT_DIR/upgrade.sh" \
			--dry-run --binary "$WORK_DIR/src/mcplexer-p2p" 2>&1) || true

	assert_contains "dry_run_plist_addr" "127.0.0.1:13333" "$_output"
	case "$_output" in
		*"0.0.0.0:13333"*) assert_eq "dry_run_plist_addr_loopback" "loopback" "wildcard" ;;
		*) assert_eq "dry_run_plist_addr_loopback" "loopback" "loopback" ;;
	esac
}

# --- test: missing daemon exits nonzero ---

test_missing_daemon() {
    rm -f "$WORK_DIR/bin/mcplexer"
    if MCPLEXER_BIN_DIR="$WORK_DIR/bin" \
        MCPLEXER_ADDR="http://127.0.0.1:3333" \
        sh "$SCRIPT_DIR/upgrade.sh" \
            --binary "$WORK_DIR/src/mcplexer-p2p" 2>/dev/null; then
        assert_eq "missing_daemon_exit" "nonzero" "zero"
    else
        assert_eq "missing_daemon_exit" "nonzero" "nonzero"
    fi
    # Restore daemon binary for subsequent tests.
    touch "$WORK_DIR/bin/mcplexer"
    chmod +x "$WORK_DIR/bin/mcplexer"
}

# --- test: missing source binary exits nonzero ---

test_missing_source() {
    rm -f "$WORK_DIR/src/mcplexer-p2p"
    if MCPLEXER_BIN_DIR="$WORK_DIR/bin" \
        MCPLEXER_ADDR="http://127.0.0.1:3333" \
        sh "$SCRIPT_DIR/upgrade.sh" \
            --binary "$WORK_DIR/src/mcplexer-p2p" 2>/dev/null; then
        assert_eq "missing_source_exit" "nonzero" "zero"
    else
        assert_eq "missing_source_exit" "nonzero" "nonzero"
    fi
    # Restore source binary for subsequent tests.
    touch "$WORK_DIR/src/mcplexer-p2p"
    chmod +x "$WORK_DIR/src/mcplexer-p2p"
}

# --- test: health_readiness parses ready correctly ---

test_health_readiness_ready() {
    mock_curl "ready" "200"
    HEALTH_URL="http://127.0.0.1:3333/api/v1/health"
    PATH="$WORK_DIR/mocks:$PATH"
    export HEALTH_URL PATH
    _state=$(health_readiness)
    assert_eq "health_ready" "ready" "$_state"
}

# --- test: health_readiness parses draining correctly ---

test_health_readiness_draining() {
    mock_curl "draining" "503"
    HEALTH_URL="http://127.0.0.1:3333/api/v1/health"
    PATH="$WORK_DIR/mocks:$PATH"
    export HEALTH_URL PATH
    _state=$(health_readiness)
    assert_eq "health_draining" "draining" "$_state"
}

# --- test: health_readiness parses legacy readiness field ---

test_health_readiness_legacy_readiness_field() {
    mock_curl "starting" "503" "readiness"
    HEALTH_URL="http://127.0.0.1:3333/api/v1/health"
    PATH="$WORK_DIR/mocks:$PATH"
    export HEALTH_URL PATH
    _state=$(health_readiness)
    assert_eq "health_legacy_readiness" "starting" "$_state"
}

# --- test: health_readiness maps status=ok to ready ---

test_health_readiness_status_ok_maps_ready() {
    mock_curl "ok" "200"
    HEALTH_URL="http://127.0.0.1:3333/api/v1/health"
    PATH="$WORK_DIR/mocks:$PATH"
    export HEALTH_URL PATH
    _state=$(health_readiness)
    assert_eq "health_status_ok" "ready" "$_state"
}

# --- test: health_readiness returns down when curl fails ---

test_health_readiness_down_curl_fail() {
    mock_curl "down"
    HEALTH_URL="http://127.0.0.1:3333/api/v1/health"
    PATH="$WORK_DIR/mocks:$PATH"
    export HEALTH_URL PATH
    _state=$(health_readiness)
    assert_eq "health_down" "down" "$_state"
}

# --- test: drain timeout exits nonzero ---

test_drain_timeout_mock() {
    mock_curl "ready" "200"
    HEALTH_URL="http://127.0.0.1:3333/api/v1/health"
    DRAIN_TIMEOUT="2"
    PATH="$WORK_DIR/mocks:$PATH"
    export HEALTH_URL DRAIN_TIMEOUT PATH
    if wait_for_drain 2 2>/dev/null; then
        assert_eq "drain_timeout_rc" "1" "0"
    else
        assert_eq "drain_timeout_rc" "1" "1"
    fi
}

# --- test: ready timeout exits nonzero ---

test_ready_timeout_mock() {
    mock_curl "down"
    HEALTH_URL="http://127.0.0.1:3333/api/v1/health"
    PATH="$WORK_DIR/mocks:$PATH"
    export HEALTH_URL PATH
    if wait_for_ready 2 2>/dev/null; then
        assert_eq "ready_timeout_rc" "1" "0"
    else
        assert_eq "ready_timeout_rc" "1" "1"
    fi
}

# --- test: rollback restores and cleans prev ---

test_rollback_restores_and_starts() {
    printf "prev-binary" > "$WORK_DIR/bin/mcplexer.prev"
    printf "bad-new-binary" > "$WORK_DIR/bin/mcplexer"
    DAEMON="$WORK_DIR/bin/mcplexer"

    if [ -f "$DAEMON.prev" ]; then
        mv "$DAEMON.prev" "$DAEMON"
    fi

    assert_eq "rollback_restored_binary" "prev-binary" "$(cat "$WORK_DIR/bin/mcplexer")"
    assert_eq "rollback_prev_cleaned" "no" "$([ -f "$WORK_DIR/bin/mcplexer.prev" ] && echo yes || echo no)"
}

# --- test: success cleans up .prev ---

test_success_cleans_prev() {
    printf "old" > "$WORK_DIR/bin/mcplexer.prev"
    rm -f "$WORK_DIR/bin/mcplexer.prev"
    assert_eq "prev_cleaned_on_success" "no" "$([ -f "$WORK_DIR/bin/mcplexer.prev" ] && echo yes || echo no)"
}

# --- test: backup unlinks stale .prev before writing a new one ---

test_swap_unlinks_stale_prev() {
    if grep -q 'rm -f "$DAEMON.prev"' "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "swap_unlinks_stale_prev" "found" "found"
    else
        assert_eq "swap_unlinks_stale_prev" "found" "missing"
    fi
}

# --- test: verify kickstart -k does NOT appear anywhere in upgrade.sh ---

test_no_kickstart_minus_k() {
    if grep -q "kickstart -k" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "no_kickstart_minus_k" "absent" "present"
    else
        assert_eq "no_kickstart_minus_k" "absent" "absent"
    fi
}

# --- test: verify launchctl kill SIGTERM is used for drain signal ---

test_launchctl_kill_sigterm_for_drain() {
    if grep -q "launchctl kill" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "launchctl_kill_for_drain" "found" "found"
    else
        assert_eq "launchctl_kill_for_drain" "found" "missing"
    fi
}

# --- test: verify bootout is used for halt ---

test_bootout_for_halt() {
    if grep -q "launchctl bootout" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "bootout_for_halt" "found" "found"
    else
        assert_eq "bootout_for_halt" "found" "missing"
    fi
}

# --- test: verify bootstrap is used for start ---

test_bootstrap_for_start() {
    if grep -q "launchctl bootstrap" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "bootstrap_for_start" "found" "found"
    else
        assert_eq "bootstrap_for_start" "found" "missing"
    fi
}

# --- test: verify bootstrap uses the user domain, not service target ---

test_bootstrap_uses_user_domain() {
    if grep -q 'launchctl bootstrap "$(launchd_user_domain)" "$(launchd_plist_path)"' "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "bootstrap_uses_user_domain" "found" "found"
    else
        assert_eq "bootstrap_uses_user_domain" "found" "missing"
    fi
    if grep -q 'launchctl bootstrap "$(launchd_service_target)"' "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "bootstrap_not_service_target" "absent" "present"
    else
        assert_eq "bootstrap_not_service_target" "absent" "absent"
    fi
}

# --- test: verify bootout pairs with the plist-backed user domain form ---

test_bootout_uses_plist_domain() {
    if grep -q 'launchctl bootout "$(launchd_user_domain)" "$(launchd_plist_path)"' "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "bootout_uses_plist_domain" "found" "found"
    else
        assert_eq "bootout_uses_plist_domain" "found" "missing"
    fi
}

# --- test: Linux systemd path present in script ---

test_linux_systemd_path() {
    if grep -q "systemctl --user" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "linux_systemctl_path" "found" "found"
    else
        assert_eq "linux_systemctl_path" "found" "missing"
    fi
}

# --- test: custom ADDR override via env ---

test_custom_addr_override() {
    printf "original" > "$WORK_DIR/bin/mcplexer"
    mock_curl "ready" "200"

    _output=$(MCPLEXER_BIN_DIR="$WORK_DIR/bin" \
        MCPLEXER_ADDR="http://127.0.0.1:9999" \
        PATH="$WORK_DIR/mocks:$PATH" \
        sh "$SCRIPT_DIR/upgrade.sh" \
            --dry-run --binary "$WORK_DIR/src/mcplexer-p2p" 2>&1) || true

    assert_contains "custom_addr_in_output" "127.0.0.1:9999" "$_output"
    assert_contains "custom_addr_not_default" "Health URL" "$_output"
}

# --- test: custom DRAIN_TIMEOUT and READY_TIMEOUT in output ---

test_timeout_overrides() {
    printf "original" > "$WORK_DIR/bin/mcplexer"
    mock_curl "ready" "200"

    _output=$(MCPLEXER_BIN_DIR="$WORK_DIR/bin" \
        MCPLEXER_ADDR="http://127.0.0.1:3333" \
        DRAIN_TIMEOUT="15" READY_TIMEOUT="45" ACTIVE_WORKER_TIMEOUT="120" \
        PATH="$WORK_DIR/mocks:$PATH" \
        sh "$SCRIPT_DIR/upgrade.sh" \
            --dry-run --binary "$WORK_DIR/src/mcplexer-p2p" 2>&1) || true

    assert_contains "drain_timeout_15" "Drain timeout:  15s" "$_output"
    assert_contains "ready_timeout_45" "Ready timeout:  45s" "$_output"
    assert_contains "active_worker_timeout_120" "timeout=120s" "$_output"
}

# --- test: platform detection ---

test_platform_detection() {
    if [ "$(uname -s)" = "Darwin" ]; then
        assert_eq "darwin_detected" "Darwin" "$(uname -s)"
    else
        assert_eq "not_darwin" "not-darwin" "not-darwin"
    fi
}

# --- test: daemon stop path exists in non-launchd path ---

test_non_launchd_stop_path() {
    if grep -q "daemon stop" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "daemon_stop_fallback" "found" "found"
    else
        assert_eq "daemon_stop_fallback" "found" "missing"
    fi
}

# --- test: daemon status call on success ---

test_daemon_status_on_success() {
    if grep -q "daemon status" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "daemon_status_call" "found" "found"
    else
        assert_eq "daemon_status_call" "found" "missing"
    fi
}

# --- test: rules sync call ---

test_rules_sync_call() {
    if grep -q "rules sync" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "rules_sync_call" "found" "found"
    else
        assert_eq "rules_sync_call" "found" "missing"
    fi
}

# --- test: harden-data-dir.sh call ---

test_harden_data_dir_call() {
    if grep -q "harden-data-dir.sh" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "harden_data_dir_call" "found" "found"
    else
        assert_eq "harden_data_dir_call" "found" "missing"
    fi
    if grep -q 'bash "$(dirname "$0")/harden-data-dir.sh"' "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "harden_data_dir_uses_bash" "found" "found"
    else
        assert_eq "harden_data_dir_uses_bash" "found" "missing"
    fi
    if grep -q '^[[:space:]]*sh "$(dirname "$0")/harden-data-dir.sh"' "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "harden_data_dir_not_sh" "absent" "present"
    else
        assert_eq "harden_data_dir_not_sh" "absent" "absent"
    fi
}

# --- test: active worker run guard is present before drain ---

test_active_worker_guard_present() {
    if grep -q "ensure_no_active_worker_runs" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "active_worker_guard_present" "found" "found"
    else
        assert_eq "active_worker_guard_present" "found" "missing"
    fi
    if grep -q '"last_run_status":"running"' "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "active_worker_guard_checks_running" "found" "found"
    else
        assert_eq "active_worker_guard_checks_running" "found" "missing"
    fi
}

# --- test: active worker guard has explicit bypass and token file auth ---

test_active_worker_guard_auth_and_bypass() {
    if grep -q "MCPLEXER_ALLOW_ACTIVE_WORKER_RESTART" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "active_worker_guard_bypass" "found" "found"
    else
        assert_eq "active_worker_guard_bypass" "found" "missing"
    fi
    if grep -q "MCPLEXER_API_TOKEN_PATH" "$SCRIPT_DIR/upgrade.sh" && grep -q "Authorization: Bearer" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "active_worker_guard_auth" "found" "found"
    else
        assert_eq "active_worker_guard_auth" "found" "missing"
    fi
}

# --- test: active worker guard can wait for safe drain instead of failing ---

test_active_worker_guard_wait_mode() {
    if grep -q "MCPLEXER_WAIT_FOR_ACTIVE_WORKERS" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "active_worker_guard_wait_mode" "found" "found"
    else
        assert_eq "active_worker_guard_wait_mode" "found" "missing"
    fi
    if grep -q "ACTIVE_WORKER_TIMEOUT" "$SCRIPT_DIR/upgrade.sh" && grep -q "ACTIVE_WORKER_POLL_INTERVAL" "$SCRIPT_DIR/upgrade.sh"; then
        assert_eq "active_worker_guard_wait_timeouts" "found" "found"
    else
        assert_eq "active_worker_guard_wait_timeouts" "found" "missing"
    fi
}

# --- run ---

run_test() {
    _name="$1"
    printf "\n%s:\n" "$_name"
    "$_name"
}

setup
trap teardown EXIT

printf "Running upgrade script tests...\n"

# Mirror helper functions from upgrade.sh so we can test them in isolation
# without sourcing the full script (which has set -eu and a main section).
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
    HEALTH_CURL_TIMEOUT="${HEALTH_CURL_TIMEOUT:-3}"
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

wait_for_drain() {
    _timeout=$1
    _elapsed=0
    while [ "$_elapsed" -lt "$_timeout" ]; do
        _state=$(health_readiness)
        case "$_state" in
            down) return 0 ;;
            draining) : ;;
            *) : ;;
        esac
        sleep 1
        _elapsed=$((_elapsed + 1))
    done
    return 1
}

wait_for_ready() {
    _timeout=$1
    _elapsed=0
    while [ "$_elapsed" -lt "$_timeout" ]; do
        _state=$(health_readiness)
        case "$_state" in
            ready)
                return 0
                ;;
            *) : ;;
        esac
        sleep 1
        _elapsed=$((_elapsed + 1))
    done
    return 1
}


run_test test_swap_backup
run_test test_rollback
run_test test_dry_run
run_test test_dry_run_output
run_test test_dry_run_addr_default
run_test test_dry_run_addr_from_launchd_plist
run_test test_missing_daemon
run_test test_missing_source
run_test test_health_readiness_ready
run_test test_health_readiness_draining
run_test test_health_readiness_legacy_readiness_field
run_test test_health_readiness_status_ok_maps_ready
run_test test_health_readiness_down_curl_fail
run_test test_drain_timeout_mock
run_test test_ready_timeout_mock
run_test test_rollback_restores_and_starts
run_test test_success_cleans_prev
run_test test_swap_unlinks_stale_prev
run_test test_no_kickstart_minus_k
run_test test_launchctl_kill_sigterm_for_drain
run_test test_bootout_for_halt
run_test test_bootstrap_for_start
run_test test_bootstrap_uses_user_domain
run_test test_bootout_uses_plist_domain
run_test test_linux_systemd_path
run_test test_custom_addr_override
run_test test_timeout_overrides
run_test test_platform_detection
run_test test_non_launchd_stop_path
run_test test_daemon_status_on_success
run_test test_rules_sync_call
run_test test_harden_data_dir_call
run_test test_active_worker_guard_present
run_test test_active_worker_guard_auth_and_bypass
run_test test_active_worker_guard_wait_mode

printf "\nResults: %d passed, %d failed\n" "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
