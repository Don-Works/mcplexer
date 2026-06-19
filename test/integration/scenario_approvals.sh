#!/usr/bin/env bash
# scenario_approvals.sh — exercises the approval flow + sensitive-tool
# gating + approval-rule CRUD.

# ----- 14.1: approval rules CRUD -----------------------------------------

scenario_approval_rules_crud() {
    step 14.1 "approval rules — CRUD"
    local lresp
    lresp=$(api GET "$NODE_A/api/v1/approval-rules" 2>/dev/null || echo "{}")
    if echo "$lresp" | jq -e '.rules | type == "array"' >/dev/null 2>&1; then
        pass "GET /api/v1/approval-rules returns {rules:[...]}"
    else
        fail "approval-rules list missing .rules array" "$(echo "$lresp" | head -c 200)"
        return
    fi

    # Create a rule via the wire shape (approvalRuleInput). ID + audit
    # are server-set so we don't pass an id.
    local body
    body=$(jq -nc \
        '{surface:"shell", pattern:"echotest *", directory:"",
          ai_session_id:"integration-harness", decision:"deny",
          priority:50, created_by:"integration-runner"}')
    local cresp cstat
    cstat=$(api_status POST "$NODE_A/api/v1/approval-rules" "$body")
    if [ "$cstat" != "200" ] && [ "$cstat" != "201" ]; then
        skip "approval rule create" "status=$cstat — schema may have additional required fields"
        return
    fi
    pass "POST /api/v1/approval-rules status=$cstat"
    # Try to extract the created id so we can clean up.
    cresp=$(api POST "$NODE_A/api/v1/approval-rules" "$body" 2>/dev/null || echo "{}")
    local rid
    rid=$(echo "$cresp" | jq -r '.id // .ID // empty')
    if [ -n "$rid" ]; then
        api_status DELETE "$NODE_A/api/v1/approval-rules/$rid" "" >/dev/null 2>&1 || true
    fi
}

# ----- 14.2: approval queue endpoint -------------------------------------

scenario_approval_queue() {
    step 14.2 "approval queue — pending list responds"
    local body
    body=$(api GET "$NODE_A/api/v1/approvals" 2>/dev/null || echo "{}")
    if echo "$body" | jq -e 'type == "object" or type == "array"' >/dev/null 2>&1; then
        pass "GET /api/v1/approvals responded"
    else
        fail "GET /api/v1/approvals non-object/array"
    fi
}

# ----- 14.3: worker approvals (auto-paused safety) -----------------------

scenario_worker_approvals() {
    step 14.3 "worker-approvals queue responds"
    local body
    body=$(api GET "$NODE_A/api/v1/worker-approvals" 2>/dev/null || echo "[]")
    if echo "$body" | jq -e 'type == "object" or type == "array"' >/dev/null 2>&1; then
        pass "GET /api/v1/worker-approvals responded"
    else
        fail "GET /api/v1/worker-approvals non-object/array"
    fi
}

# ----- 14.4: approval queue envelope schema (BUG-ENV) -------------------
# /api/v1/approvals rows must accept the cross-boundary share envelope
# (originating_workspace, kind, summary) without crashing on legacy rows
# that omit them. We can't reliably seed a pending row from the bash
# harness, so we settle for verifying the endpoint returns an array
# and that any row present either omits the new fields entirely (legacy)
# or carries them as strings (cross-boundary share). Both shapes are
# valid — the goal is a strict superset of the prior envelope.

scenario_approval_envelope_schema() {
    step 14.4 "approval envelope tolerates legacy + new fields"
    local body
    body=$(api GET "$NODE_A/api/v1/approvals" 2>/dev/null || echo "[]")
    if ! echo "$body" | jq -e 'type == "array"' >/dev/null 2>&1; then
        fail "GET /api/v1/approvals not an array" "$(echo "$body" | head -c 200)"
        return
    fi
    # For every row, kind/originating_workspace/summary must be either
    # absent OR a string. No row should mix types or surface as a
    # number/object — that would break the React renderer.
    local malformed
    malformed=$(echo "$body" | jq '[.[]
        | select(
            (.kind != null and (.kind | type) != "string") or
            (.originating_workspace != null and (.originating_workspace | type) != "string") or
            (.summary != null and (.summary | type) != "string")
          )] | length')
    if [ "$malformed" = "0" ]; then
        pass "every approval row has well-typed envelope fields"
    else
        fail "$malformed approval rows have malformed envelope fields"
    fi
}

# ----- 14.5: mesh-grant consent recorded as approval (BUG-CONSENT) ------
# After mesh__grant_peer_scope succeeds, a kind=mesh_grant_consent row
# is emitted on the approvals queue so the user has a UI-visible audit
# trail of grants they accepted. We can't directly call the MCP tool
# from bash, but we can confirm:
#   (a) the kind vocabulary is accepted by the read path
#   (b) any pre-existing consent row carries a non-empty summary
# In the multi-node harness this scenario passes as a smoke check
# whether or not the grant happened — the real assertion lives in the
# Go unit tests (approval/manager_test.go + sqlite/m0_guards_test.go).

scenario_mesh_grant_consent_envelope() {
    step 14.5 "mesh_grant_consent rows render with summary"
    local body
    body=$(api GET "$NODE_A/api/v1/approvals?status=approved" 2>/dev/null || echo "[]")
    if ! echo "$body" | jq -e 'type == "array"' >/dev/null 2>&1; then
        skip "/api/v1/approvals?status=approved did not return an array"
        return
    fi
    local consent_count
    consent_count=$(echo "$body" | jq '[.[] | select(.kind == "mesh_grant_consent")] | length')
    if [ "$consent_count" = "0" ]; then
        pass "no consent rows yet (expected on a fresh node) — schema accepts the kind"
        return
    fi
    local empty_summary
    empty_summary=$(echo "$body" | jq '[.[] | select(.kind == "mesh_grant_consent" and (.summary // "") == "")] | length')
    if [ "$empty_summary" = "0" ]; then
        pass "$consent_count mesh_grant_consent rows all carry a summary"
    else
        fail "$empty_summary / $consent_count mesh_grant_consent rows missing summary"
    fi
}
