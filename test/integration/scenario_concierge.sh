#!/usr/bin/env bash
# scenario_concierge.sh — Verifies the concierge self-improving chat loop
# shipped in commit 4fe31b1. Sourced by scenarios.sh.
#
# The concierge subsystem (internal/concierge/) has four layers:
#   1. Signals  — concierge__record_signal MCP tool + ChatTurnSignal store
#      surfaced via GET /api/v1/chat-signals + POST /chat-signals/{id}/mark-promoted.
#   2. Classifier — rule-based, deterministic (internal/concierge/classifier.go).
#   3. A/B telemetry — AggregateArmStats + PickWinner over signals grouped
#      by prompt_version (internal/concierge/ab.go).
#   4. Lessons — PinLesson writes a `note`-kind memory under the
#      concierge.lessons:* scope (internal/concierge/lessons.go).
#
# What this scenario asserts vs. parks as PENDING:
#   23.1 — Record N=5 signals with same user_message → SAME classifier label
#          (rule-based classifier is deterministic by construction).
#   23.2 — REST GET /api/v1/chat-signals returns all 5 rows with the marker.
#   23.3 — Promotion: POST /chat-signals/{id}/mark-promoted stamps the row;
#          subsequent GET ?promoted=false hides it.
#   23.4 — A/B aggregation: seed signals at two prompt_versions, verify
#          PickWinner is reachable (the Go-side aggregator is unit-tested;
#          here we surface a PENDING for end-to-end A/B arm selection).
#   23.5 — Lesson stored back to memory: PinLesson is invoked by the
#          friction-extractor worker, not the gateway. Park as PENDING.
#
# Owner: D1 in epic 01KSK91Q4W8TNED9MAF0CTRVKC.

# concierge_record_signal calls concierge__record_signal via mcpx__execute_code
# (the tool is not in the builtin allowlist outside code-mode). Returns the
# parsed signal row JSON on stdout, empty string on error.
#
# Usage: concierge_record_signal <node_url> <worker_id> <channel> <user_message> <prompt_version> [label] [assistant_message] [user_id_external]
concierge_record_signal() {
    local url="$1"
    local worker_id="$2"
    local channel="$3"
    local user_message="$4"
    local prompt_version="$5"
    local label="${6:-}"
    local assistant_message="${7:-prior reply}"
    local user_id_external="${8:-harness-user-1}"

    # Build the JS args object via jq so quoting in user_message is safe,
    # then concatenate a short driver snippet. We use jq -nc to JSON-encode
    # the args, then drop them inline as a JS object literal (the parsed
    # JSON is a valid JS literal).
    local args_json
    args_json=$(jq -nc \
        --arg worker_id "$worker_id" \
        --arg channel "$channel" \
        --arg user_message "$user_message" \
        --arg assistant_message "$assistant_message" \
        --arg user_id_external "$user_id_external" \
        --arg label "$label" \
        --argjson prompt_version "$prompt_version" \
        '{worker_id:$worker_id,
          channel:$channel,
          user_message:$user_message,
          assistant_message:$assistant_message,
          user_id_external:$user_id_external,
          prompt_version:$prompt_version}
         + (if $label == "" then {} else {label:$label} end)')
    local snippet
    snippet="const args = $args_json;
try {
  const r = concierge.record_signal(args);
  print(JSON.stringify(r));
} catch (e) {
  print(JSON.stringify({__error: e.message}));
}"
    local args resp text
    args=$(jq -nc --arg c "$snippet" '{code:$c}')
    resp=$(mcp_call "$url" "mcpx__execute_code" "$args")
    text=$(echo "$resp" | jq -r '.result.content[0].text // ""' 2>/dev/null)
    echo "$text" | tr -d '\r' | tail -1
}

scenario_concierge_self_improving() {
    step 23 "concierge self-improving loop: signals + classifier stability + promotion"

    local worker_id="harness-concierge-wkr-$RANDOM"
    local channel="harness-channel"
    local user_msg="that's not right, I meant the green one"
    local marker
    marker="conc-marker-$$"

    # ----- 23.1 classifier stability across 5 identical inputs --------------
    # Rule-based classifier (internal/concierge/classifier.go) — "that's
    # not right" matches the Correction priority. Five identical inputs
    # must produce the SAME label every time.
    local labels=""
    local first_signal_id=""
    local i
    for i in 1 2 3 4 5; do
        local row
        row=$(concierge_record_signal \
            "$NODE_A" "$worker_id" "$channel" \
            "$user_msg ($marker iter $i)" \
            "1" "" "you said the green one" "harness-user-$marker")
        if [ -z "$row" ] || echo "$row" | grep -q '__error'; then
            fail "23.1 record_signal iter $i" "row: $row"
            return
        fi
        # The handler returns { signal: <row> }; unwrap.
        local lbl id
        lbl=$(echo "$row" | jq -r '.signal.label // .label // ""' 2>/dev/null)
        id=$(echo "$row" | jq -r '.signal.id // .id // ""' 2>/dev/null)
        if [ -z "$lbl" ]; then
            fail "23.1 record_signal iter $i — no label in response" "row=$row"
            return
        fi
        labels="$labels $lbl"
        [ -z "$first_signal_id" ] && first_signal_id="$id"
    done
    # Unique-count the labels.
    local uniq_count
    uniq_count=$(echo "$labels" | tr ' ' '\n' | sort -u | grep -c .)
    if [ "$uniq_count" = "1" ]; then
        local one_label
        one_label=$(echo "$labels" | tr ' ' '\n' | sort -u | head -1)
        pass "23.1 classifier stable across 5 identical inputs (label=$one_label)"
    else
        fail "23.1 classifier not stable: labels=$(echo "$labels" | tr ' ' ',')" \
            "uniq=$uniq_count expected=1"
    fi

    # ----- 23.2 signals readable via REST --------------------------------
    sleep 1
    local list_resp count
    list_resp=$(api GET "$NODE_A/api/v1/chat-signals?limit=200&worker_id=$worker_id" 2>/dev/null)
    count=$(echo "$list_resp" \
        | jq --arg w "$worker_id" '[.[]? | select(.worker_id == $w)] | length' \
        2>/dev/null || echo "0")
    if [ "${count:-0}" -ge 5 ]; then
        pass "23.2 GET /api/v1/chat-signals returned $count rows for worker $worker_id"
    else
        fail "23.2 expected >=5 chat-signals rows for worker $worker_id; got $count" \
            "head: $(echo "$list_resp" | head -c 200)"
    fi

    # ----- 23.3 promotion stamps the row ---------------------------------
    if [ -z "$first_signal_id" ]; then
        skip "23.3 mark-promoted" "no signal_id available from 23.1"
    else
        # Generate a fake refinement id; mark-promoted just stamps the
        # linkage, the existence of the refinement row isn't enforced.
        local refinement_id="harness-ref-$RANDOM"
        local pstatus
        pstatus=$(api_status POST \
            "$NODE_A/api/v1/chat-signals/$first_signal_id/mark-promoted" \
            "$(jq -nc --arg r "$refinement_id" '{refinement_id:$r}')")
        if [ "$pstatus" = "204" ] || [ "$pstatus" = "200" ]; then
            pass "23.3 POST mark-promoted returned $pstatus"
        else
            fail "23.3 mark-promoted returned $pstatus"
        fi
        # ?promoted=false should now hide that signal.
        local unp
        unp=$(api GET "$NODE_A/api/v1/chat-signals?worker_id=$worker_id&promoted=false&limit=200" 2>/dev/null)
        if echo "$unp" | jq -e --arg id "$first_signal_id" \
            'any(.[]?; .id == $id)' >/dev/null 2>&1; then
            fail "23.3 promoted signal still surfaces with promoted=false" \
                "first_signal_id=$first_signal_id"
        else
            pass "23.3 promoted=false filter correctly hides the marked row"
        fi
    fi

    # ----- 23.4 A/B aggregation reachable --------------------------------
    # Seed signals at two prompt_versions on the same worker. The Go-side
    # AggregateArmStats / PickWinner are reachable from the friction-
    # extractor worker via the SignalListing interface (ab.go), but there
    # is NO direct REST endpoint to surface the arm stats. Park as
    # PENDING with a child-task suggestion to ship one.
    local v2_msg="thanks, that worked"
    concierge_record_signal "$NODE_A" "$worker_id" "$channel" "$v2_msg" "2" \
        "" "here is the green one" "harness-user-$marker" >/dev/null
    concierge_record_signal "$NODE_A" "$worker_id" "$channel" "$v2_msg" "2" \
        "" "here is the green one" "harness-user-$marker" >/dev/null
    skip "23.4 A/B arm aggregation REST surface" \
        "PENDING — AggregateArmStats / PickWinner live in internal/concierge/ab.go \
but there's no GET /api/v1/concierge/ab/arms (or similar) to read them. \
file follow-up child task: surface arm stats + winner via REST so a test rig \
can verify the loop end-to-end."

    # ----- 23.5 lesson stored in memory ----------------------------------
    # PinLesson is called by the friction-extractor worker AFTER it
    # proposes a refinement — that worker isn't running in the integration
    # harness (it's an LLM-driven worker, same limitation as D2's
    # consolidator). We exercise the underlying recall path directly:
    # write a `concierge.lessons:*`-keyed memory row, then verify it
    # surfaces in memory.recall (proves the convention works).
    local scope_key="concierge.lessons:$channel:harness-user-$marker"
    local lesson_content="when user says that is not right refer back to the prior assistant_message before retrying"
    local lesson_user_tag="user:harness-user-$marker"
    local lesson_channel_tag="channel:$channel"
    local lesson_resp
    lesson_resp=$(api POST "$NODE_A/api/v1/memory" \
        "$(jq -nc \
            --arg n "$scope_key" \
            --arg c "$lesson_content" \
            --arg ct "$lesson_channel_tag" \
            --arg ut "$lesson_user_tag" \
            '{name:$n, content:$c, kind:"note",
              tags:["concierge","lesson",$ct,$ut]}')" \
        2>/dev/null)
    if echo "$lesson_resp" | jq -e '.id' >/dev/null 2>&1; then
        pass "23.5 pinned lesson-shaped memory under $scope_key"
        # Recall via the search endpoint — proves the convention is
        # readable end-to-end. The friction extractor's RecentLessonsFor
        # uses the same store query path (tag-filtered list), so this
        # exercises the same code surface.
        local search_resp
        search_resp=$(api POST "$NODE_A/api/v1/memory/search" \
            "$(jq -nc '{query:"that is not right", limit:20}')" 2>/dev/null)
        if echo "$search_resp" | jq -e \
            --arg key "$scope_key" \
            'any(.[]?; ((.entry.name // .name) == $key))' \
            >/dev/null 2>&1; then
            pass "23.5 lesson surfaces in memory.search by content"
        else
            # Tag-only lookup as a fallback (the search FTS5 floor may not
            # tokenise the apostrophe consistently).
            local list_resp
            list_resp=$(api GET "$NODE_A/api/v1/memory?tags=concierge,lesson&limit=200" 2>/dev/null)
            if echo "$list_resp" | jq -e --arg key "$scope_key" \
                'any(.[]?; .name == $key)' >/dev/null 2>&1; then
                pass "23.5 lesson surfaces in memory list (tag=concierge,lesson)"
            else
                fail "23.5 lesson did not surface in memory.search or list" \
                    "scope_key=$scope_key"
            fi
        fi
        # Full friction-extractor → PinLesson path needs the worker.
        skip "23.5 PinLesson via friction-extractor worker" \
            "PENDING — friction-extractor worker is LLM-driven; the echo-llm \
stub can't produce the refinement+pin tool calls. file follow-up: deterministic \
LLM stub or a /api/v1/concierge/lessons REST surface so an integration test \
can drive the pin path without a real LLM."
    else
        fail "23.5 lesson memory write failed" "$(echo "$lesson_resp" | head -c 200)"
    fi
}
