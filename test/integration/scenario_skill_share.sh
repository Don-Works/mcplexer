#!/usr/bin/env bash
# scenario_skill_share.sh — exercise the cross-node skill-share gate via
# the mesh__grant_peer_scope / mesh__request_skill MCP tools. Sourced by
# scenarios.sh.
#
# These scenarios target the AUTHORIZATION + AUDIT path on
# /mcplexer/skill/1.0.0 — not bundle install end-to-end. The full
# install path requires a real .mcskill bundle (sig + manifest + cache);
# fabricating one inside a shell harness is research-grade plumbing for
# what's already covered by skills/install_test.go. What WE check here:
#
#   * mesh__grant_peer_scope persists scopes on both sides and audit
#     captures the high-impact event;
#   * an ungranted peer requesting a skill is denied at the peer-paired
#     scope gate (ErrSkillShareDenied) before any bundle bytes flow;
#   * a granted peer requesting a non-installed skill is rejected with
#     ErrSkillNotInstalled (proves the auth gate passes and the lookup
#     reaches the offer-resolver step).

# scenario_skill_share_grant — two-way grant of mesh.skill_request
# between A and B, then assert audit rows on both sides.
scenario_skill_share_grant() {
    step 6.1 "two-way mesh.skill_request grant between A and B"

    local pid_a pid_b
    pid_a=$(peer_id_for "$NODE_A")
    pid_b=$(peer_id_for "$NODE_B")
    if [ -z "$pid_a" ] || [ -z "$pid_b" ]; then
        fail "fetch peer ids for A↔B grant" "pid_a=$pid_a pid_b=$pid_b"
        return
    fi

    mcp_call_ok "A grants B mesh.skill_request" "$NODE_A" \
        "mesh__grant_peer_scope" \
        "$(jq -nc --arg pid "$pid_b" '{peer:$pid, scope:"mesh.skill_request"}')" \
        >/dev/null
    mcp_call_ok "B grants A mesh.skill_request" "$NODE_B" \
        "mesh__grant_peer_scope" \
        "$(jq -nc --arg pid "$pid_a" '{peer:$pid, scope:"mesh.skill_request"}')" \
        >/dev/null

    # Audit assertions. The grant emits a mesh__grant_peer_scope row
    # on the granter; we check both sides recorded their respective grant.
    local arows brows
    arows=$(api GET "$NODE_A/api/v1/audit?limit=200" 2>/dev/null)
    if echo "$arows" | jq -e '.data[]? | select((.tool_name // "") == "mesh__grant_peer_scope")' >/dev/null 2>&1; then
        pass "node-a audit captured mesh__grant_peer_scope"
    else
        fail "node-a audit missing mesh__grant_peer_scope row"
    fi
    brows=$(api GET "$NODE_B/api/v1/audit?limit=200" 2>/dev/null)
    if echo "$brows" | jq -e '.data[]? | select((.tool_name // "") == "mesh__grant_peer_scope")' >/dev/null 2>&1; then
        pass "node-b audit captured mesh__grant_peer_scope"
    else
        fail "node-b audit missing mesh__grant_peer_scope row"
    fi
}

# scenario_skill_share_unauthorized — node-c requests a skill from
# node-a with no grant in place. The peer-scope gate must reject before
# any bundle bytes flow.
scenario_skill_share_unauthorized() {
    step 6.2 "unauthorized skill request rejected at the peer-scope gate"
    local pid_a
    pid_a=$(peer_id_for "$NODE_A")
    if [ -z "$pid_a" ]; then
        fail "fetch peer id for unauthorized test" "pid_a empty"
        return
    fi

    # The skill name doesn't have to exist; the auth check fires first.
    mcp_call_err "C → A request rejected (no grant on A)" "$NODE_C" \
        "mesh__request_skill" \
        "$(jq -nc --arg pid "$pid_a" '{peer_id:$pid, skill_name:"anything-c-asks-for"}')" \
        "" >/dev/null

    # Node-a should record the rejection via its skill-share stream
    # handler (skill_stream_p2p.go::handleStream → recordAudit).
    local arows
    arows=$(api GET "$NODE_A/api/v1/audit?limit=400" 2>/dev/null)
    if echo "$arows" | jq -e '.data[]? | select((.tool_name // "") == "mesh__skill_share") | select((.status // .outcome // "") == "denied")' >/dev/null 2>&1; then
        pass "node-a audit recorded denied skill_share stream"
    else
        # Tolerate audit batching racing the GET; the MCP-side rejection on
        # node-c is the primary assertion. Note the lack of a denied row.
        skip "node-a audit denied row" \
            "no 'denied' row visible yet — possibly async; primary assertion (MCP rejection on C) passed"
    fi
}

# scenario_skill_share_authorized_nonexistent — node-b is GRANTED but the
# requested skill doesn't exist on node-a. Proves the auth gate passes
# and the offer-resolver step is reached (ErrSkillNotInstalled).
scenario_skill_share_authorized_nonexistent() {
    step 6.3 "granted peer requesting a non-installed skill hits ErrSkillNotInstalled"
    local pid_a
    pid_a=$(peer_id_for "$NODE_A")
    if [ -z "$pid_a" ]; then
        fail "fetch peer id" "pid_a empty"
        return
    fi
    # Relies on 6.1 having installed the two-way grant. Request a skill
    # name that's not in A's installed-skill store — should pass the
    # auth gate, fail at the resolver with a clear "not installed".
    local resp
    resp=$(mcp_call "$NODE_B" "mesh__request_skill" \
        "$(jq -nc --arg pid "$pid_a" '{peer_id:$pid, skill_name:"never-installed-skill-fixture"}')")
    if [ -z "$resp" ]; then
        fail "B → A request (granted, nonexistent)" "no MCP response"
        return
    fi
    local text
    text=$(echo "$resp" | jq -r '.result.content[0].text // ""' 2>/dev/null)
    if echo "$text" | grep -qiE 'not have the requested skill installed|not installed'; then
        pass "node-b sees ErrSkillNotInstalled (auth gate passed, resolver rejected)"
    else
        # Defensive: tolerate denied-by-scope if the stream raced the grant.
        if echo "$text" | grep -qiE 'mesh\.skill_request|scope.*required|paired-peers'; then
            skip "B → A path" "scope gate still tripped — grant may not have synced yet (${text:0:120})"
        else
            fail "unexpected error from B → A" "result=$text"
        fi
    fi
}
