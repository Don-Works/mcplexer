#!/usr/bin/env bash
# scenario_googlechat.sh — exercises the Google Chat bridge REST surface
# (migration 067 + internal/googlechat/).
#
# The bridge wraps a real Google Chat app, which the docker harness can't
# provision. We exercise the REST contract only — status, spaces list,
# webhook payload accept — and SKIP the live-send + JWT-verified paths
# cleanly. Mirrors scenario_tasks.sh in structure.

scenario_googlechat_status() {
    step 30.1 "GET /api/v1/googlechat/status responds with token_set/client_active=false on fresh daemon"
    local body
    body=$(api GET "$NODE_A/api/v1/googlechat/status" 2>/dev/null || echo "{}")
    # NOTE: don't use jq's `//` alternative operator here — it treats
    # `false` as falsey and would substitute null, breaking the
    # comparison. Direct field access is correct; a missing key produces
    # jq null which won't equal false either, so this still catches
    # absent fields.
    assert_jq "googlechat/status has token_set=false on fresh node-a" "$body" \
        '.token_set == false'
    assert_jq "googlechat/status has client_active=false on fresh node-a" "$body" \
        '.client_active == false'
}

scenario_googlechat_spaces_list_empty() {
    step 30.2 "GET /api/v1/googlechat/spaces returns an empty array on a fresh daemon"
    local body
    body=$(api GET "$NODE_A/api/v1/googlechat/spaces" 2>/dev/null || echo "{}")
    assert_jq "googlechat/spaces returns spaces array" "$body" \
        '(.spaces // []) | type == "array"'
    assert_jq "googlechat/spaces is empty on fresh daemon" "$body" \
        '(.spaces // []) | length == 0'
}

scenario_googlechat_token_rejected_invalid() {
    step 30.3 "POST /api/v1/googlechat/token rejects malformed JSON"
    local status
    status=$(api_status POST "$NODE_A/api/v1/googlechat/token" \
        '{"service_account_json":"not-json"}')
    if [ "$status" = "400" ]; then
        pass "rejected malformed service account JSON with 400"
    else
        fail "expected 400 on malformed service account, got $status"
    fi
}

scenario_googlechat_webhook_unbound() {
    step 30.4 "POST /api/v1/googlechat/events tolerates an unbound MESSAGE event (200 ignored or 200 ok)"
    # The webhook accepts events and drops MESSAGE for unknown spaces silently.
    # Either status=ok (parsed) or status=ignored (parse miss) is acceptable;
    # what we're checking is that the endpoint exists and returns 2xx for a
    # well-formed payload.
    local payload
    payload=$(jq -nc '{
        type: "MESSAGE",
        space: {name: "spaces/UNBOUND", spaceType: "DIRECT_MESSAGE"},
        user: {name: "users/42", displayName: "Alice", type: "HUMAN"},
        message: {
            name: "spaces/UNBOUND/messages/MSG1",
            text: "hi",
            sender: {name: "users/42", displayName: "Alice", type: "HUMAN"}
        }
    }')
    local status
    status=$(api_status POST "$NODE_A/api/v1/googlechat/events" "$payload")
    if [ "$status" = "200" ]; then
        pass "webhook accepts MESSAGE event for unbound space (status=200)"
    else
        fail "expected webhook 200 for unbound MESSAGE event, got $status"
    fi
}

scenario_googlechat_webhook_added() {
    step 30.5 "POST /api/v1/googlechat/events handles ADDED_TO_SPACE lifecycle event"
    local payload
    payload=$(jq -nc '{
        type: "ADDED_TO_SPACE",
        space: {name: "spaces/NEWROOM", displayName: "New Room", spaceType: "SPACE"},
        user: {name: "users/42", displayName: "Admin", type: "HUMAN"}
    }')
    local status
    status=$(api_status POST "$NODE_A/api/v1/googlechat/events" "$payload")
    if [ "$status" = "200" ]; then
        pass "webhook accepts ADDED_TO_SPACE event (status=200)"
    else
        fail "expected webhook 200 for ADDED_TO_SPACE, got $status"
    fi
}

scenario_googlechat_pairing_requires_workspace() {
    step 30.6 "POST /api/v1/googlechat/pairings rejects missing workspace_id"
    local status
    status=$(api_status POST "$NODE_A/api/v1/googlechat/pairings" '{}')
    if [ "$status" = "400" ]; then
        pass "rejected pairing without workspace_id (400)"
    else
        fail "expected 400 on empty pairing body, got $status"
    fi
}

scenario_googlechat_test_message_requires_client() {
    step 30.7 "POST /api/v1/googlechat/test-message returns 503 when no client configured"
    # On a fresh daemon no service account JSON is set, so the client is
    # inactive — test-message must surface 503 (not crash, not 500).
    local status
    status=$(api_status POST "$NODE_A/api/v1/googlechat/test-message" \
        '{"space_id":"space-fake","text":"hi"}')
    if [ "$status" = "503" ]; then
        pass "test-message returns 503 without an active client"
    else
        fail "expected 503 on test-message without client, got $status"
    fi
}
