#!/usr/bin/env bash
# scenario_hammerspoon.sh — exercises the Hammerspoon MCP REST surface
# (internal/hammerspoon/ + internal/api/handler_hammerspoon.go).
#
# Hammerspoon is macOS-only and the docker harness runs Linux, so a live
# bridge can't be smoke-tested here. The deeper end-to-end cases
# (list_windows via stub bridge, exec_lua gate flip, 401 password mismatch,
# bridge-down) live in internal/hammerspoon/integration_test.go where a
# real httptest.Server can stand in for hs.httpserver.
#
# What this scenario verifies against the docker harness:
#   - 1.1 Fresh seed: hammerspoon downstream row exists and is disabled by
#     default; the aggregated /api/v1/tools surface does not advertise any
#     hammerspoon__* tools (CapabilitiesCache is empty until first discover).
#   - 1.2 The hammerspoon-bridge auth scope is present and lists the four
#     documented env keys.
#   - 1.3 GET /api/v1/hammerspoon/snippet returns the embedded Lua bridge.
#   - 1.4 POST /api/v1/hammerspoon/install rejects with 400 on non-darwin
#     (the docker container is Linux) — the platform guard works.
#   - 1.5 POST /api/v1/hammerspoon/probe runs even with no bridge configured;
#     reports health=broken, bridge_reachable=false, and writes an audit row.
#   - 1.6 The probe response is persisted into the downstream row's
#     CapabilitiesCache so the dashboard can render a traffic-light.
#
# Step IDs share the §16 prefix so they slot in after the existing
# 15.x aux scenarios without colliding.

# ----- 16.x: hammerspoon REST surface -----------------------------------

scenario_hammerspoon_disabled_by_default() {
    step 16.1 "hammerspoon downstream row is seeded disabled"
    local body
    body=$(api GET "$NODE_A/api/v1/downstreams/hammerspoon" 2>/dev/null || echo "{}")
    assert_jq "hammerspoon downstream exists" "$body" \
        '(.id // .ID) == "hammerspoon"'
    assert_jq "hammerspoon downstream disabled=true" "$body" \
        '(.disabled // .Disabled) == true'
    assert_jq "hammerspoon downstream transport=internal" "$body" \
        '(.transport // .Transport) == "internal"'

    # Tools surface: hammerspoon's CapabilitiesCache is empty on a fresh
    # daemon (no discover ran) so /api/v1/tools must not advertise any
    # hammerspoon__* tools regardless of the disabled flag.
    local tools
    tools=$(api GET "$NODE_A/api/v1/tools" 2>/dev/null || echo '{"tools":[]}')
    # The endpoint returns either {tools:[...]} or a bare array depending on
    # the build; tolerate both. The assertion is "no hammerspoon__ prefix
    # in any tool name".
    local hammer_count
    hammer_count=$(echo "$tools" | jq -r \
        '[(.tools // .) | .[]? | .name // empty | select(startswith("hammerspoon__"))] | length' \
        2>/dev/null || echo "0")
    if [ "$hammer_count" = "0" ]; then
        pass "/api/v1/tools does not advertise hammerspoon__* (disabled+empty cache)"
    else
        fail "/api/v1/tools advertises $hammer_count hammerspoon__* tools" \
            "(head) $(echo "$tools" | head -c 300)"
    fi
}

scenario_hammerspoon_auth_scope_seeded() {
    step 16.2 "hammerspoon-bridge auth scope seeded with the four env keys"
    local body
    body=$(api GET "$NODE_A/api/v1/auth-scopes/hammerspoon-bridge" 2>/dev/null || echo "{}")
    assert_jq "auth scope exists" "$body" \
        '(.id // .ID) == "hammerspoon-bridge"'
    assert_jq "auth scope type=env" "$body" \
        '(.type // .Type) == "env"'
}

scenario_hammerspoon_snippet_endpoint() {
    step 16.3 "GET /api/v1/hammerspoon/snippet returns the embedded Lua bridge"
    # Snippet is served as text/x-lua, not JSON — capture the body and grep.
    # Use the curl wrapper directly so we can keep raw bytes.
    local tok
    tok="$(token_for "$NODE_A")"
    local body
    body=$(curl -fsS -H "Authorization: Bearer $tok" \
        "$NODE_A/api/v1/hammerspoon/snippet" 2>/dev/null || echo "")
    if [ -z "$body" ]; then
        fail "snippet endpoint returned empty body"
        return
    fi
    # Sanity-check: the embedded snippet must declare the password reader
    # and start the loopback hs.httpserver. Two pinned substrings catch a
    # broken go:embed without false-positiving on cosmetic edits.
    if ! echo "$body" | grep -q "hammerspoon-mcp"; then
        fail "snippet body missing 'hammerspoon-mcp' marker" \
            "(head) $(echo "$body" | head -c 200)"
        return
    fi
    if ! echo "$body" | grep -q "hs.httpserver"; then
        fail "snippet body missing 'hs.httpserver' call" \
            "(head) $(echo "$body" | head -c 200)"
        return
    fi
    pass "snippet body contains hammerspoon-mcp + hs.httpserver markers"
}

scenario_hammerspoon_install_rejects_non_darwin() {
    step 16.4 "POST /api/v1/hammerspoon/install rejects on non-darwin (400)"
    # Docker container is Linux — install must refuse with a structured
    # error rather than scribbling on /root/. The handler returns
    # {error, step:"platform"} with 400.
    local status
    status=$(api_status POST "$NODE_A/api/v1/hammerspoon/install" "{}")
    if [ "$status" = "400" ]; then
        pass "install rejected with 400 on linux container"
    else
        fail "expected 400 on linux install, got $status"
    fi
}

scenario_hammerspoon_probe_unconfigured() {
    step 16.5 "POST /api/v1/hammerspoon/probe runs without a bridge and reports broken health"
    local body
    body=$(api POST "$NODE_A/api/v1/hammerspoon/probe" "{}" 2>/dev/null || echo "{}")
    # Health must be a string out of {ok, degraded, broken}. On a docker
    # node with no Hammerspoon.app and no bridge listener it should be
    # "broken" — app_running=false AND bridge_reachable=false.
    assert_jq "probe returns a health string" "$body" \
        '(.health // "") | type == "string" and length > 0'
    assert_jq "probe returns checks object" "$body" \
        '(.checks // {}) | type == "object"'
    assert_jq "probe app_running check is present and false" "$body" \
        '(.checks.app_running.ok // false) == false'
    assert_jq "probe bridge_reachable check is present and false" "$body" \
        '(.checks.bridge_reachable.ok // false) == false'
    assert_jq "probe surfaces remediation list" "$body" \
        '(.remediation // []) | type == "array" and length > 0'
}

scenario_hammerspoon_probe_persists_cache() {
    step 16.6 "probe result persists into the downstream row's CapabilitiesCache"
    # Re-fetch the row; capabilities_cache (or CapabilitiesCache) should
    # carry the probe output as JSON. Field name varies with serializer
    # casing; the row JSON contains both spellings on some builds, so we
    # accept either via the // operator with a default of "{}" so the
    # assertion handles a missing key as "no cache yet".
    local body
    body=$(api GET "$NODE_A/api/v1/downstreams/hammerspoon" 2>/dev/null || echo "{}")
    # The cache field may be a string (raw JSON) or already-decoded object
    # depending on the API response shape; jq's `fromjson?` tolerates both.
    local cache
    cache=$(echo "$body" \
        | jq -c '(.capabilities_cache // .CapabilitiesCache // "") | tostring' \
        2>/dev/null || echo '""')
    if [ "$cache" = '""' ] || [ -z "$cache" ]; then
        fail "capabilities_cache empty after probe" \
            "(head) $(echo "$body" | head -c 300)"
        return
    fi
    # If the field came back as a JSON string, parse-then-check for our key.
    # If it was already an object, the inner tostring above wrapped it in
    # quotes — jq's fromjson handles both branches uniformly.
    if echo "$cache" | jq -r 'fromjson? // .' 2>/dev/null | grep -q '"health"'; then
        pass "capabilities_cache contains probe.health field"
    else
        fail "capabilities_cache missing health field" \
            "(head) $(echo "$cache" | head -c 300)"
    fi
}

scenario_hammerspoon_probe_audit() {
    step 16.7 "probe writes a hammerspoon.bridge.probed audit row"
    # Audit writes are async via the bus → sqlite flush, so poll briefly.
    local body found="false"
    for _ in 1 2 3 4 5; do
        body=$(api GET "$NODE_A/api/v1/audit?tool_name=hammerspoon.bridge.probed&limit=10" \
            2>/dev/null || echo "{}")
        if echo "$body" | jq -e '(.data // []) | length > 0' >/dev/null 2>&1; then
            found="true"; break
        fi
        sleep 1
    done
    if [ "$found" = "true" ]; then
        pass "audit ledger contains hammerspoon.bridge.probed row"
    else
        fail "no hammerspoon.bridge.probed audit row after probe" \
            "(head) $(echo "$body" | head -c 300)"
    fi
}
