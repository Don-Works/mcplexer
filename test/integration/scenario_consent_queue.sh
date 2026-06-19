#!/usr/bin/env bash
# scenario_consent_queue.sh — Group C / C1 of the bulletproof e2e suite.
#
# THE LOAD-BEARING ASSERTION: every Tier 2 (same-org, different user) or
# Tier 3 (cross-org) cross-boundary share — skill_share, memory_share,
# task_offer, mesh_direct — MUST land on the recipient's
# /api/v1/approvals queue as an explicit consent prompt with:
#   - originating_workspace  (non-empty)
#   - kind                   (one of {skill_share, memory_share,
#                              task_offer, mesh_direct})
#   - summary                (human-readable, non-empty)
#
# accept (PATCH/POST → "accepted") MUST cause the underlying share to
# complete on the recipient. reject MUST leave the recipient's local
# state pristine — no skill installed, no memory row, no task row,
# no mesh message visible.
#
# That's the contract the user named: "data transfer must be
# acknowledged and explicitly allowed by them." Anything weaker is a
# silent cross-boundary leak.
#
# Today's state: the approval queue ships as a generic ToolApproval
# shape (store/models.go::ToolApproval) keyed on tool_name + arguments
# — it does NOT yet carry `originating_workspace`, `kind in
# (skill_share|memory_share|task_offer|mesh_direct)`, or `summary`
# as first-class fields. Until the consent surface lands, every
# missing field SKIPs with PENDING + pointer to epic
# 01KSK91Q4W8TNED9MAF0CTRVKC. Don't pretend it passed.
#
# UI parity: driven separately by the bulletproof skill via
# /playwright-browser; this scenario stays REST-only. See TODO below.

# ----- helpers -----------------------------------------------------------

# consent_queue_for returns the recipient's pending /api/v1/approvals
# rows as a JSON array. Empty array on error (so caller assertions are
# stable). Falls back to legacy "object with .data" shape.
consent_queue_for() {
    local node_url="$1"
    local body
    body=$(api GET "$node_url/api/v1/approvals?status=pending" 2>/dev/null \
        || echo "[]")
    # /api/v1/approvals returns a bare JSON array; defensive fallback for
    # operators who wrapped it in {data:[...]} via a reverse proxy.
    if echo "$body" | jq -e 'type == "array"' >/dev/null 2>&1; then
        echo "$body"
    elif echo "$body" | jq -e '.data | type == "array"' >/dev/null 2>&1; then
        echo "$body" | jq -c '.data'
    else
        echo "[]"
    fi
}

# consent_row_for_kind picks the FIRST queue row whose `kind` matches.
# Returns "" if absent (caller emits SKIP+PENDING).
consent_row_for_kind() {
    local queue="$1"
    local kind="$2"
    echo "$queue" \
        | jq -c --arg k "$kind" \
            '.[] | select((.kind // "") == $k)' \
        | head -1
}

# assert_consent_envelope checks one queue row carries the three
# load-bearing fields the consent contract requires. Each missing field
# becomes a SKIP+PENDING (matches the brief: don't pretend it passed
# when the schema isn't there yet).
assert_consent_envelope() {
    local label="$1"
    local row="$2"
    local epic="01KSK91Q4W8TNED9MAF0CTRVKC"

    if [ -z "$row" ] || [ "$row" = "null" ]; then
        skip "$label envelope" \
            "queue row absent — consent surface PENDING for this share kind (epic $epic)"
        return 1
    fi

    local ws kind summary
    ws=$(echo "$row" | jq -r '.originating_workspace // ""')
    kind=$(echo "$row" | jq -r '.kind // ""')
    summary=$(echo "$row" | jq -r '.summary // ""')

    if [ -n "$ws" ]; then
        pass "$label originating_workspace=$ws"
    else
        skip "$label originating_workspace" \
            "field absent on queue row — schema PENDING (epic $epic)"
    fi
    if [ -n "$kind" ]; then
        pass "$label kind=$kind"
    else
        skip "$label kind" \
            "field absent on queue row — schema PENDING (epic $epic)"
    fi
    if [ -n "$summary" ]; then
        pass "$label summary present (len=${#summary})"
    else
        skip "$label summary" \
            "field absent on queue row — schema PENDING (epic $epic)"
    fi
    return 0
}

# resolve_approval drives the accept/reject path. The brief accepts
# PATCH or POST; ToolApproval today exposes POST
# /api/v1/approvals/{id}/resolve with {approved:bool}. Returns 0 on a
# 2xx, 1 otherwise.
resolve_approval() {
    local node_url="$1"
    local approval_id="$2"
    local approved="$3"      # "true" | "false"
    local body
    body=$(jq -nc --argjson a "$approved" '{approved:$a, reason:"bulletproof e2e"}')
    local status
    status=$(api_status POST "$node_url/api/v1/approvals/$approval_id/resolve" "$body")
    case "$status" in
        2*) return 0 ;;
        *)  return 1 ;;
    esac
}

# wait_for_consent_row polls the recipient's queue for up to 12s for a
# row whose `kind` matches. Echoes the row (or "") and returns 0 on
# found, 1 on timeout.
wait_for_consent_row() {
    local node_url="$1"
    local kind="$2"
    local row=""
    local _
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
        local q
        q=$(consent_queue_for "$node_url")
        row=$(consent_row_for_kind "$q" "$kind")
        if [ -n "$row" ] && [ "$row" != "null" ]; then
            echo "$row"
            return 0
        fi
        sleep 1
    done
    echo ""
    return 1
}

# ----- per-kind probes ---------------------------------------------------
# Each probe triggers ONE cross-boundary share of its kind from $src to
# $dst with NO prior grant, then asserts the envelope on the recipient's
# queue. Probes are SKIP-tolerant — if the wire-layer share can't even
# leave the sender (no libp2p, missing scope on the negative path) we
# SKIP rather than FAIL, because the contract under test is "did the
# RECIPIENT surface a consent prompt", not "did the libp2p bridge work".

probe_consent_skill_share() {
    local src="$1"
    local dst="$2"
    local lbl="$3"

    local pid_dst
    pid_dst=$(peer_id_for "$dst")
    if [ -z "$pid_dst" ]; then
        skip "$lbl skill_share queue probe" "no peer id readback on $dst"
        return
    fi

    # Trigger: ungranted mesh__request_skill from src to dst. Today this
    # path rejects at the recipient's peer-scope gate (mesh.skill_request
    # not granted). The aspirational consent surface would instead enqueue
    # an approval on the recipient. We probe both — if the queue row
    # lands we run the envelope assertion; otherwise SKIP+PENDING.
    mcp_call "$src" "mesh__request_skill" \
        "$(jq -nc --arg pid "$pid_dst" \
            '{peer_id:$pid, skill_name:"bulletproof-consent-probe"}')" \
        >/dev/null 2>&1 || true

    local row
    row=$(wait_for_consent_row "$dst" "skill_share")
    assert_consent_envelope "$lbl skill_share" "$row" || return
}

probe_consent_memory_share() {
    local src="$1"
    local dst="$2"
    local lbl="$3"

    local pid_dst
    pid_dst=$(peer_id_for "$dst")
    if [ -z "$pid_dst" ]; then
        skip "$lbl memory_share queue probe" "no peer id readback on $dst"
        return
    fi

    # Create a memory on src so the offer references something concrete.
    local mname="consent-probe-$RANDOM"
    local cresp mid
    cresp=$(api POST "$src/api/v1/memory" \
        "$(jq -nc --arg n "$mname" \
            '{name:$n, content:"consent probe payload", kind:"fact"}')" \
        2>/dev/null) || true
    mid=$(echo "$cresp" | jq -r '.id // empty' 2>/dev/null)
    if [ -z "$mid" ]; then
        skip "$lbl memory_share queue probe" "couldn't seed src memory"
        return
    fi

    mcp_call "$src" "memory__offer_memory" \
        "$(jq -nc --arg pid "$pid_dst" --arg mid "$mid" \
            '{peer_id:$pid, memory_id:$mid}')" \
        >/dev/null 2>&1 || true

    local row
    row=$(wait_for_consent_row "$dst" "memory_share")
    assert_consent_envelope "$lbl memory_share" "$row" || return
}

probe_consent_task_offer() {
    local src="$1"
    local dst="$2"
    local lbl="$3"

    local pid_dst
    pid_dst=$(peer_id_for "$dst")
    if [ -z "$pid_dst" ]; then
        skip "$lbl task_offer queue probe" "no peer id readback on $dst"
        return
    fi

    # Seed a task on src — every workspace_* var was set by
    # scenario_provision. We deliberately do NOT pre-grant task_offer:* on
    # dst so the recipient sees a NEW unsanctioned offer.
    local ws_src
    case "$src" in
        "$NODE_A") ws_src="${WS_ALPHA:-}" ;;
        "$NODE_B") ws_src="${WS_BRAVO:-}" ;;
        "$NODE_C") ws_src="${WS_CHARLIE:-}" ;;
        *)        ws_src="" ;;
    esac
    if [ -z "$ws_src" ]; then
        skip "$lbl task_offer queue probe" \
            "no workspace id for $src (scenario_provision didn't run for this node)"
        return
    fi

    local title="consent-task-probe-$RANDOM"
    local cresp tid
    cresp=$(api POST "$src/api/v1/tasks" \
        "$(jq -nc --arg ws "$ws_src" --arg t "$title" \
            '{workspace_id:$ws, title:$t, status:"open"}')" 2>/dev/null) || true
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty' 2>/dev/null)
    if [ -z "$tid" ]; then
        skip "$lbl task_offer queue probe" "couldn't seed src task"
        return
    fi

    # Fire the offer — REST shape, fall back to MCP if the REST handler
    # is missing on this build.
    api POST "$src/api/v1/tasks/offers" \
        "$(jq -nc --arg ws "$ws_src" --arg to "$pid_dst" --arg tid "$tid" \
            '{workspace_id:$ws, task_id:$tid, to_peer_id:$to, message:"consent probe"}')" \
        >/dev/null 2>&1 \
    || mcp_call "$src" "task__offer" \
            "$(jq -nc --arg to "$pid_dst" --arg tid "$tid" \
                '{to:$to, task_id:$tid, message:"consent probe"}')" \
            >/dev/null 2>&1 || true

    local row
    row=$(wait_for_consent_row "$dst" "task_offer")
    assert_consent_envelope "$lbl task_offer" "$row" || return
}

probe_consent_mesh_direct() {
    local src="$1"
    local dst="$2"
    local lbl="$3"

    local pid_dst
    pid_dst=$(peer_id_for "$dst")
    if [ -z "$pid_dst" ]; then
        skip "$lbl mesh_direct queue probe" "no peer id readback on $dst"
        return
    fi

    # Direct-addressed mesh send (recipient = specific peer id, not a
    # broadcast). Today this lands without consent — the aspirational
    # consent surface would interpose a queue row when the recipient
    # didn't grant `mesh.message_inbound` (or whatever the dst scope
    # eventually becomes).
    local marker="consent-direct-$RANDOM"
    api POST "$src/api/v1/mesh/send" \
        "$(jq -nc --arg pid "$pid_dst" --arg c "$marker" \
            '{recipient:{kind:"peer",value:$pid},
              kind:"finding", content:$c, priority:"normal",
              agent_name:"bulletproof-consent"}')" \
        >/dev/null 2>&1 || true

    local row
    row=$(wait_for_consent_row "$dst" "mesh_direct")
    assert_consent_envelope "$lbl mesh_direct" "$row" || return
}

# ----- accept + reject paths --------------------------------------------
# Once a Tier 2/3 share has produced a queue row, accepting it MUST
# complete the share (recipient now sees the resource); rejecting MUST
# leave the recipient pristine. We model the assertion as a single
# memory_share round-trip — the cleanest read endpoint to assert
# resource presence/absence against. If the consent surface isn't
# materialising rows we SKIP+PENDING (consistent with the envelope
# probes).

probe_consent_accept_reject_memory() {
    local src="$1"
    local dst="$2"
    local lbl="$3"
    local epic="01KSK91Q4W8TNED9MAF0CTRVKC"

    local pid_dst
    pid_dst=$(peer_id_for "$dst")
    if [ -z "$pid_dst" ]; then
        skip "$lbl accept/reject" "no peer id readback on $dst"
        return
    fi

    # Accept path: seed memory on src → offer to dst → wait for queue
    # row → accept → recipient should see the memory listed.
    local accept_name="consent-accept-$RANDOM"
    local cresp aid
    cresp=$(api POST "$src/api/v1/memory" \
        "$(jq -nc --arg n "$accept_name" \
            '{name:$n, content:"accept-path payload", kind:"fact"}')" \
        2>/dev/null) || true
    aid=$(echo "$cresp" | jq -r '.id // empty' 2>/dev/null)
    if [ -z "$aid" ]; then
        skip "$lbl accept seed" "couldn't seed src memory"
        return
    fi

    mcp_call "$src" "memory__offer_memory" \
        "$(jq -nc --arg pid "$pid_dst" --arg mid "$aid" \
            '{peer_id:$pid, memory_id:$mid}')" \
        >/dev/null 2>&1 || true

    local arow
    arow=$(wait_for_consent_row "$dst" "memory_share")
    if [ -z "$arow" ]; then
        skip "$lbl accept memory_share" \
            "no consent queue row materialised on $dst — PENDING (epic $epic)"
        return
    fi
    local approval_id
    approval_id=$(echo "$arow" | jq -r '.id // ""')
    if [ -z "$approval_id" ]; then
        skip "$lbl accept resolve" "queue row missing .id"
        return
    fi

    if resolve_approval "$dst" "$approval_id" "true"; then
        pass "$lbl accept POST /resolve approved=true"
    else
        fail "$lbl accept POST /resolve" "resolve returned non-2xx"
        return
    fi

    # Recipient should now see the memory locally — give the install path
    # a few seconds.
    local installed="false"
    local _
    for _ in 1 2 3 4 5 6 7 8; do
        local lresp
        lresp=$(api GET "$dst/api/v1/memory?limit=200" 2>/dev/null || echo "[]")
        if echo "$lresp" | jq -e --arg n "$accept_name" \
                '(. // []) | any(.[]?; (.name // "") == $n)' >/dev/null 2>&1 \
           || echo "$lresp" | jq -e --arg n "$accept_name" \
                '.data[]? | select((.name // "") == $n)' >/dev/null 2>&1; then
            installed="true"; break
        fi
        sleep 1
    done
    if [ "$installed" = "true" ]; then
        pass "$lbl accept → memory $accept_name visible on $dst"
    else
        fail "$lbl accept → memory $accept_name NOT visible on $dst" \
            "consent contract violated: approved share did not complete (epic $epic)"
    fi

    # Reject path: seed a SECOND memory → offer → wait for row → reject
    # → memory MUST NOT appear on dst.
    local reject_name="consent-reject-$RANDOM"
    local rresp rid
    rresp=$(api POST "$src/api/v1/memory" \
        "$(jq -nc --arg n "$reject_name" \
            '{name:$n, content:"reject-path payload", kind:"fact"}')" \
        2>/dev/null) || true
    rid=$(echo "$rresp" | jq -r '.id // empty' 2>/dev/null)
    if [ -z "$rid" ]; then
        skip "$lbl reject seed" "couldn't seed src memory"
        return
    fi
    mcp_call "$src" "memory__offer_memory" \
        "$(jq -nc --arg pid "$pid_dst" --arg mid "$rid" \
            '{peer_id:$pid, memory_id:$mid}')" \
        >/dev/null 2>&1 || true

    local rrow rapp
    rrow=$(wait_for_consent_row "$dst" "memory_share")
    if [ -z "$rrow" ]; then
        skip "$lbl reject memory_share" \
            "no consent queue row materialised on $dst — PENDING (epic $epic)"
        return
    fi
    rapp=$(echo "$rrow" | jq -r '.id // ""')
    if [ -z "$rapp" ]; then
        skip "$lbl reject resolve" "queue row missing .id"
        return
    fi
    if resolve_approval "$dst" "$rapp" "false"; then
        pass "$lbl reject POST /resolve approved=false"
    else
        fail "$lbl reject POST /resolve" "resolve returned non-2xx"
        return
    fi

    sleep 3  # give any racing install path time to (incorrectly) land
    local leaked="false"
    local lresp2
    lresp2=$(api GET "$dst/api/v1/memory?limit=200" 2>/dev/null || echo "[]")
    if echo "$lresp2" | jq -e --arg n "$reject_name" \
            '(. // []) | any(.[]?; (.name // "") == $n)' >/dev/null 2>&1 \
       || echo "$lresp2" | jq -e --arg n "$reject_name" \
            '.data[]? | select((.name // "") == $n)' >/dev/null 2>&1; then
        leaked="true"
    fi
    if [ "$leaked" = "false" ]; then
        pass "$lbl reject → memory $reject_name absent on $dst (contract holds)"
    else
        fail "$lbl reject → memory $reject_name PRESENT on $dst" \
            "consent contract violated: rejected share completed anyway (epic $epic)"
    fi
}

# ----- main scenario -----------------------------------------------------

scenario_consent_queue() {
    step C1 "consent queue surfaces every Tier 2/3 cross-boundary share"

    if ! bulletproof_topology_ready; then
        skip "consent_queue" \
            "5-node bulletproof topology not up — Tier 3 probes need NODE_D"
        return
    fi

    # Tier 2: node-a → node-c (same org AcmeCo, different users).
    # Tier 3: node-a → node-d (cross-org, AcmeCo → BetaCo).
    # Both must be pair-handshaken first or the wire never reaches the
    # recipient. pair_basic from lib_tiers.sh — SKIPs the test cleanly if
    # the closed docker bridge prevents the handshake.
    if ! pair_basic "consent_queue T2 prep" "$NODE_C" "$NODE_A"; then
        skip "consent_queue tier2 prep" "A↔C pair handshake didn't land"
    else
        probe_consent_skill_share  "$NODE_A" "$NODE_C" "T2 A→C"
        probe_consent_memory_share "$NODE_A" "$NODE_C" "T2 A→C"
        probe_consent_task_offer   "$NODE_A" "$NODE_C" "T2 A→C"
        probe_consent_mesh_direct  "$NODE_A" "$NODE_C" "T2 A→C"
        probe_consent_accept_reject_memory "$NODE_A" "$NODE_C" "T2 A→C"
    fi

    if ! pair_basic "consent_queue T3 prep" "$NODE_D" "$NODE_A"; then
        skip "consent_queue tier3 prep" "A↔D pair handshake didn't land"
    else
        probe_consent_skill_share  "$NODE_A" "$NODE_D" "T3 A→D"
        probe_consent_memory_share "$NODE_A" "$NODE_D" "T3 A→D"
        probe_consent_task_offer   "$NODE_A" "$NODE_D" "T3 A→D"
        probe_consent_mesh_direct  "$NODE_A" "$NODE_D" "T3 A→D"
        probe_consent_accept_reject_memory "$NODE_A" "$NODE_D" "T3 A→D"
    fi

    # TODO: drive /approvals via /playwright-browser when running locally
    # with mcpx+playwright. The bulletproof skill itself runs the UI
    # parity step separately (manifest feature
    # approvals.consent_queue.tier_aware ← scenario_consent_queue is REST
    # surface only).
}
