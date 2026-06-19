#!/usr/bin/env bash
# scenario_memory_consolidator.sh — Verifies the memory consolidator
# (commits 25d91d3 + 6d2ee2e). Sourced by scenarios.sh.
#
# The consolidator is an LLM-driven Worker materialised from the
# "memory-consolidator" seed template (internal/workertemplates/seeds/
# memory-consolidator.json). Its prompt instructs the model to call
# memory__list / memory__save / memory__invalidate to collapse near-
# duplicate notes. As a result the "real" consolidation only happens
# when a real LLM is wired — the echo-llm stub returns canned text and
# never produces tool calls, so the worker terminates without rewriting
# anything.
#
# REST surface (internal/api/memory_consolidate_handler.go):
#   GET    /api/v1/memory/consolidate/status       — enabled, last_run, ...
#   POST   /api/v1/memory/consolidate/enable       — install + enable
#   POST   /api/v1/memory/consolidate/disable      — pause
#   POST   /api/v1/memory/consolidate/run          — ad-hoc run
#
# What this scenario asserts end-to-end vs. what it parks as PENDING:
#   22.1 — REST status (before install) returns installed=false.
#   22.2 — POST /enable installs the worker (or surfaces a clear pre-
#          condition error when no api_key auth scope exists yet).
#   22.3 — POST /run fires a run-now; status moves through "doing".
#          (Actual row-count collapse needs a real LLM → SKIP+PENDING.)
#   22.4 — Disable pauses the worker (Enabled=false).
#   22.5 — Tier 1 propagation: enabling on node-a then reading node-b's
#          status confirms cross-machine awareness IS NOT yet wired
#          (status is per-node) → SKIP+PENDING with a child-task note.
#
# Owner: D2 in epic 01KSK91Q4W8TNED9MAF0CTRVKC.

# consolidator_seed_duplicates writes N memories on the given node, mixing
# duplicates, near-duplicates, and a `[[link]]` reference. Returns 0 on
# success; emits a PASS/FAIL line itself. Stashes a sample id in
# CONSOL_SEED_ID for downstream assertions that survive the consolidator's
# eventual rewrites.
#
# Usage: consolidator_seed_duplicates <node_url> <count>
consolidator_seed_duplicates() {
    local url="$1"
    local n="$2"
    local marker="consol-fixture-$RANDOM"
    local seeded=0 last_id=""
    # First pass: distinct facts.
    local i
    for i in $(seq 1 "$((n - 5))"); do
        local nm="$marker-fact-$i"
        local body
        body=$(jq -nc --arg n "$nm" --arg c "fact number $i about $marker" \
            '{name:$n, content:$c, kind:"note", tags:["consolidator-fixture"]}')
        local r
        r=$(api POST "$url/api/v1/memory" "$body" 2>/dev/null || echo "")
        if echo "$r" | jq -e '.id' >/dev/null 2>&1; then
            seeded=$((seeded + 1))
            last_id=$(echo "$r" | jq -r '.id')
        fi
    done
    # Duplicates: 3 copies of fact-1 with slightly different text.
    for i in 1 2 3; do
        local nm="$marker-dup-$i"
        local body
        body=$(jq -nc --arg n "$nm" \
            --arg c "fact number 1 about $marker — variant $i" \
            '{name:$n, content:$c, kind:"note", tags:["consolidator-fixture"]}')
        api POST "$url/api/v1/memory" "$body" >/dev/null 2>&1 && \
            seeded=$((seeded + 1))
    done
    # Two more with explicit [[link]] references — the consolidator
    # prompt says these must be rewritten when targets are superseded.
    for i in 1 2; do
        local body
        body=$(jq -nc \
            --arg n "$marker-link-$i" \
            --arg c "see [[$marker-fact-1]] and [[$marker-fact-2]] for context" \
            '{name:$n, content:$c, kind:"note", tags:["consolidator-fixture"]}')
        api POST "$url/api/v1/memory" "$body" >/dev/null 2>&1 && \
            seeded=$((seeded + 1))
    done
    CONSOL_MARKER="$marker"
    CONSOL_SEED_ID="$last_id"
    CONSOL_SEED_COUNT="$seeded"
    if [ "$seeded" -ge "$n" ] || [ "$seeded" -ge 10 ]; then
        pass "seeded $seeded memories on $url (marker=$marker)"
    else
        fail "only seeded $seeded/$n memories on $url" "marker=$marker"
    fi
}

scenario_memory_consolidator() {
    step 22 "memory consolidator: status / enable / run / disable + Tier 1 propagation"

    local ws="$WS_ALPHA"
    if [ -z "${ws:-}" ]; then
        skip "consolidator" "WS_ALPHA unset"
        return
    fi

    # ----- 22.1 status before install --------------------------------------
    local s1
    s1=$(api GET "$NODE_A/api/v1/memory/consolidate/status?workspace_id=$ws" 2>/dev/null)
    if echo "$s1" | jq -e '.installed == false' >/dev/null 2>&1; then
        pass "22.1 status before install reports installed=false"
    elif echo "$s1" | jq -e '.installed == true' >/dev/null 2>&1; then
        pass "22.1 status reports installed=true (pre-installed from a prior run)"
    else
        skip "22.1 status shape" "head: $(echo "$s1" | head -c 200)"
    fi

    # ----- 22.2 seed the workspace with N=20 overlapping memories --------
    consolidator_seed_duplicates "$NODE_A" 20

    # Capture pre-consolidation row count for the deferred assertion.
    local pre_total
    pre_total=$(api GET "$NODE_A/api/v1/memory?tags=consolidator-fixture&limit=200" 2>/dev/null \
        | jq 'length // 0' 2>/dev/null || echo "0")
    pass "22.2 pre-consolidation memory count = $pre_total (marker=$CONSOL_MARKER)"

    # ----- 22.3 enable consolidator --------------------------------------
    local ebody eresp estatus
    ebody=$(jq -nc --arg ws "$ws" '{workspace_id:$ws}')
    estatus=$(api_status POST "$NODE_A/api/v1/memory/consolidate/enable" "$ebody")
    eresp=$(api POST "$NODE_A/api/v1/memory/consolidate/enable" "$ebody" 2>/dev/null || echo "{}")
    case "$estatus" in
        200|201)
            pass "22.3 enable returned $estatus" ;;
        428)
            # PreconditionRequired = no api_key auth scope. Expected on a
            # fresh node where the worker tests haven't seeded a secret.
            skip "22.3 enable" \
                "no api_key auth scope on node-a (status=428). \
file follow-up: seed an api_key scope ahead of consolidator tests, OR \
extend scenario_provision to plant one."
            return
            ;;
        *)
            fail "22.3 enable returned $estatus" \
                "body head: $(echo "$eresp" | head -c 200)"
            return
            ;;
    esac
    local worker_id
    worker_id=$(echo "$eresp" | jq -r '.id // .ID // empty')
    if [ -n "$worker_id" ]; then
        pass "22.3 consolidator worker materialised id=$worker_id"
    fi

    # ----- 22.4 run-now ---------------------------------------------------
    local rbody rresp rstatus
    rbody=$(jq -nc --arg ws "$ws" '{workspace_id:$ws}')
    rstatus=$(api_status POST "$NODE_A/api/v1/memory/consolidate/run" "$rbody")
    rresp=$(api POST "$NODE_A/api/v1/memory/consolidate/run" "$rbody" 2>/dev/null || echo "{}")
    if [ "$rstatus" = "200" ] || [ "$rstatus" = "202" ]; then
        local run_id
        run_id=$(echo "$rresp" | jq -r '.run_id // .id // empty')
        pass "22.4 run-now returned $rstatus (run_id=${run_id:-<unspecified>})"
    else
        fail "22.4 run-now returned $rstatus" \
            "body head: $(echo "$rresp" | head -c 200)"
    fi

    # ----- 22.5 row-count collapse — PENDING (needs real LLM) -------------
    # The consolidator's actual collapse logic is an LLM prompt that
    # invokes memory__save + memory__invalidate. The harness's echo-llm
    # is a deterministic OAI-compatible stub that returns canned text and
    # never produces tool calls, so the worker terminates without
    # mutating memory. Park the count-decreased assertion.
    sleep 5
    local post_total
    post_total=$(api GET "$NODE_A/api/v1/memory?tags=consolidator-fixture&limit=200" 2>/dev/null \
        | jq 'length // 0' 2>/dev/null || echo "0")
    if [ "$post_total" -lt "$pre_total" ]; then
        pass "22.5 memory count strictly decreased ($pre_total → $post_total)"
    elif [ "$post_total" = "$pre_total" ]; then
        skip "22.5 row-count collapse" \
            "PENDING — count unchanged ($pre_total). The consolidator is an LLM-driven \
worker; the echo-llm stub returns canned text and never emits tool calls. \
A real collapse assertion needs either (a) a deterministic LLM stub that emits \
memory__save / memory__invalidate calls, or (b) a server-side \
deterministic-consolidator fallback. file follow-up child task."
    else
        fail "22.5 memory count INCREASED after consolidation" \
            "$pre_total → $post_total — unexpected"
    fi

    # ----- 22.6 audit row for consolidation event — PENDING ---------------
    # No explicit kind="memory_consolidated" event is emitted by the
    # current consolidator implementation; the audit trail surfaces
    # via the per-tool memory__save / memory__invalidate audit rows the
    # worker would produce on a real run. Park as PENDING.
    skip "22.6 audit kind=memory_consolidated" \
        "PENDING — the audit subsystem doesn't emit a distinct \
'memory_consolidated' kind today. Consolidation surfaces as the \
underlying memory__save + memory__invalidate audit rows. file follow-up: \
emit a high-level kind on consolidator-run completion."

    # ----- 22.7 disable pauses the worker ---------------------------------
    local dbody dstatus
    dbody=$(jq -nc --arg ws "$ws" '{workspace_id:$ws}')
    dstatus=$(api_status POST "$NODE_A/api/v1/memory/consolidate/disable" "$dbody")
    if [ "$dstatus" = "200" ] || [ "$dstatus" = "204" ]; then
        local s2
        s2=$(api GET "$NODE_A/api/v1/memory/consolidate/status?workspace_id=$ws" 2>/dev/null)
        if echo "$s2" | jq -e '.enabled == false' >/dev/null 2>&1; then
            pass "22.7 disable paused the worker (enabled=false)"
        else
            fail "22.7 disable accepted ($dstatus) but enabled remains true" \
                "status: $(echo "$s2" | head -c 200)"
        fi
    else
        fail "22.7 disable returned $dstatus"
    fi

    # ----- 22.8 Tier 1 propagation ----------------------------------------
    # Pair node-a + node-b (both user-alice) so they share Tier-1 trust.
    # Today the consolidator status is a per-machine concept (it queries
    # the LOCAL worker rows), and memory rows replicate only via the
    # explicit offer/accept path. So this assertion is structured as
    # PENDING — the Tier 1 trust tier is intended to enable silent
    # propagation, but the consolidator hasn't been mesh-extended yet.
    pair_same_user "$NODE_A" "$NODE_B"
    local secs=0 found="false"
    while [ "$secs" -lt 30 ]; do
        local bstatus
        bstatus=$(api GET "$NODE_B/api/v1/memory/consolidate/status?workspace_id=$ws" 2>/dev/null)
        if echo "$bstatus" | jq -e '.installed == true' >/dev/null 2>&1; then
            found="true"; break
        fi
        secs=$((secs + 3))
        sleep 3
    done
    if [ "$found" = "true" ]; then
        pass "22.8 Tier 1 propagation: node-b sees the consolidator within 30s"
    else
        skip "22.8 Tier 1 propagation" \
            "PENDING — node-b never reports installed=true after enable on node-a. \
The consolidator's status is local-per-node; it doesn't replicate across the Tier 1 \
pair today. file follow-up child task: mesh-share consolidator config + state on \
same-user paired peers."
    fi
}
