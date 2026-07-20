#!/usr/bin/env bash
# scenario_logwatch_ingest.sh — steps 42.x: proof that the rig can put
# realistic log history in front of the daemon THROUGH THE PRODUCT'S OWN
# COLLECTION PATH, and that backdated history behaves like observed history.
#
# This is the foundation the detector scenarios (40.x/41.x) stand on, and it
# is asserted separately on purpose. If injection silently degrades — the
# shim's --since stops filtering, the cursor stops advancing, backdated
# timestamps stop reaching log_lines.ts — every detector assertion above it
# becomes meaningless while still reporting green. That is the exact failure
# mode this whole rig exists to prevent, so the plumbing gets its own tests.
#
# WHAT IS REAL HERE: sshd, publickey auth out of the node's own ssh-agent,
# host-key TOFU and pin persistence, sshx.CommandForSource, the byte-capped
# run, byte-zero timestamp parsing, the cursorTS+1ns exclusive window, tail
# reconciliation, the distiller, and the log_template_days trigger.
# WHAT IS SIMULATED: only the container runtime behind the `docker` CLI.
#
# ---------------------------------------------------------------------------
# KNOWN-FAILING TODAY (do not weaken these to make the suite green)
# ---------------------------------------------------------------------------
#   42.9 static stream stops producing lines — FAILS. UpdateLogSourceCursor
#   persists cursor_ts through formatTime (time.RFC3339, second precision),
#   discarding the nanoseconds that pull.go's +1ns exclusive window and
#   DockerLogsCommand's RFC3339Nano --since are both built on. The cursor
#   becomes a fixed point at the final second of the stream and re-ingests
#   that second's lines on every pull, forever. The duplicates share exact
#   timestamps, so the baseline miner reads zero-second gaps that were never
#   emitted and the cadence statistics are corrupted at source. See the step's
#   own comment for the full mechanism.
#
# Required: scenarios.sh sourced this AFTER ensure_tokens; lib_logwatch.sh
# supplies the injection helpers.

# shellcheck source=lib_logwatch.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib_logwatch.sh"

# ----- state (declared so `set -u` cannot trip a step that runs alone) -----
LWI_WS="${LWI_WS:-}"
LWI_HOST_ID="${LWI_HOST_ID:-}"
LWI_SRC_HEALTHY="${LWI_SRC_HEALTHY:-}"
LWI_SRC_ERRORS="${LWI_SRC_ERRORS:-}"
LWI_ANCHOR="${LWI_ANCHOR:-0}"
LWI_WORK="${LWI_WORK:-}"
LWI_READY="${LWI_READY:-}"

# ----- 42.1: bring up the collected host ----------------------------------

scenario_logwatch_ingest_setup() {
    step 42.1 "provision a real SSH log host and collect nine backdated days from it"
    if ! logwatch_available; then
        skip "logwatch injection rig" \
            "compose has no log-host service — rebuild with the current docker-compose.yml"
        return 0
    fi

    # One frozen anchor for every fixture below, so a run straddling a minute
    # boundary still produces identical relative geometry.
    LWI_ANCHOR=$(date -u +%s)
    LWI_WORK=$(mktemp -d)

    local stamp="$LWI_ANCHOR-$RANDOM"
    local ws="ws-logwatch-$stamp"
    if ! api POST "$NODE_A/api/v1/workspaces" \
        "$(jq -nc --arg id "$ws" --arg nm "logwatch-$stamp" \
            '{id:$id, name:$nm, root_path:"/tmp/logwatch", default_policy:"allow"}')" \
        >/dev/null 2>&1; then
        fail "create logwatch workspace" "POST /api/v1/workspaces rejected $ws"
        return 0
    fi
    LWI_WS="$ws"

    # Authorise the node's OWN agent identity on the log host. The daemon
    # dials with the credential it actually holds; no key is baked into an
    # image or committed to this public repo.
    if ! logwatch_authorize "$NODE_A" >/dev/null 2>&1; then
        fail "authorize node-a on log-host" "could not copy the node's public key"
        return 0
    fi
    pass "authorised node-a's agent identity on log-host"

    LWI_HOST_ID=$(logwatch_provision_host "$NODE_A" "$ws" "log-host-$stamp")
    if [ -z "$LWI_HOST_ID" ]; then
        fail "create remote host" "POST /api/v1/remote-hosts returned no id"
        return 0
    fi
    pass "created remote host $LWI_HOST_ID (ssh_agent scope, no stored key)"

    # Nine days against the 7-day retention default is deliberate: the
    # log_template_days rows survive (they are never pruned) while log_lines
    # is trimmed to seven. That IS the production steady state, and asserting
    # against anything else would be asserting against a fiction.
    logwatch_gen healthy_bursty --now "$LWI_ANCHOR" --from-days 9 --to-days 0 \
        > "$LWI_WORK/order-sync.log"
    logwatch_inject "order-sync" "$LWI_WORK/order-sync.log"

    LWI_SRC_HEALTHY=$(logwatch_provision_source \
        "$NODE_A" "$ws" "$LWI_HOST_ID" "order-sync-$stamp" "order-sync")
    if [ -z "$LWI_SRC_HEALTHY" ]; then
        fail "create log source" "POST /api/v1/log-sources returned no id"
        return 0
    fi
    pass "created log source $LWI_SRC_HEALTHY (docker kind, 20s cadence)"

    # Two templates minimum: starts and completions are textually distinct.
    if logwatch_wait_for_lines "$NODE_A" "$ws" "$LWI_SRC_HEALTHY" 2 150; then
        LWI_READY=1
        pass "collector pulled nine days of history over SSH"
    else
        fail "collector never ingested from log-host" \
            "no templates after 150s; check node-a logs for 'logwatch: pull failed'"
    fi
}

# ----- 42.2: the SSH path really ran --------------------------------------

scenario_logwatch_ingest_pull_path() {
    step 42.2 "the pull advanced a cursor and pinned the host key — not a store-side shortcut"
    if [ -z "$LWI_READY" ]; then
        skip "pull path assertions" "42.1 did not complete"
        return 0
    fi

    # A cursor only exists if pullSource ran to completion: dial, command,
    # parse, ingest, persist. Nothing else in the daemon writes it.
    #
    # POLLED, not sampled once. ingestPull hands lines to the distiller BEFORE
    # persisting the cursor, so there is a real window in which 42.1's
    # template check has already passed and the cursor row is still empty.
    # Sampling here raced that window and reported a missing cursor on a
    # collector that was working perfectly.
    local cursor="" i=0
    while [ $i -lt 45 ]; do
        cursor=$(logwatch_cursor "$NODE_A" "$LWI_SRC_HEALTHY")
        [ -n "$cursor" ] && break
        sleep 3; i=$((i + 3))
    done
    if [ -n "$cursor" ]; then
        pass "log source carries a pull cursor ($cursor)"
    else
        fail "no pull cursor" "cursor_ts still empty after 45s — no pull completed"
    fi

    # TOFU: the first dial records the host key fingerprint. An empty pin
    # after a successful pull would mean the dial never happened.
    local host
    host=$(api GET "$NODE_A/api/v1/remote-hosts/$LWI_HOST_ID" 2>/dev/null || echo '{}')
    assert_jq "first dial TOFU-pinned the host key" "$host" \
        '((.host_key_pin // "") | length) > 0'
    assert_jq "source has no consecutive failures" \
        "$(api GET "$NODE_A/api/v1/log-sources/$LWI_SRC_HEALTHY" 2>/dev/null || echo '{}')" \
        '(.consecutive_failures // 0) == 0'
}

# ----- 42.3: backdated history behaves like observed history --------------

scenario_logwatch_ingest_backdating() {
    step 42.3 "backdated lines land with the LINE's timestamp, spanning nine days"
    if [ -z "$LWI_READY" ]; then
        skip "backdating assertions" "42.1 did not complete"
        return 0
    fi
    local tpls
    tpls=$(logwatch_templates "$NODE_A" "$LWI_WS" \
        | jq -c --arg s "$LWI_SRC_HEALTHY" '[.templates[]? | select(.source_id == $s)]')

    # first_seen must be ~9 days back. If ingestion stamped rows with the wall
    # clock instead of the parsed line time, this collapses to minutes and the
    # promotion machinery below it can never be exercised at all.
    local oldest span_days
    oldest=$(echo "$tpls" | jq -r '[.[].first_seen] | min // empty')
    if [ -n "$oldest" ]; then
        span_days=$(python3 -c "
import datetime,sys
t=sys.argv[1].replace('Z','+00:00')
d=datetime.datetime.fromisoformat(t)
now=datetime.datetime.fromtimestamp($LWI_ANCHOR, datetime.timezone.utc)
print(round((now-d).total_seconds()/86400, 2))" "$oldest" 2>/dev/null || echo 0)
        if awk -v d="$span_days" 'BEGIN{exit !(d >= 6.0)}'; then
            pass "oldest retained template first_seen is ${span_days}d back"
        else
            fail "backdated history did not survive ingestion" \
                "oldest first_seen only ${span_days}d back — expected >= 6d (7-day retention trims 9)"
        fi
    else
        fail "no templates for the healthy source" "payload: $(echo "$tpls" | head -c 200)"
    fi

    # The measured shape: starts and completions are SEPARATE templates and
    # completions outnumber starts. A detector that assumes one completion per
    # start is wrong about this signal, so the fixture must prove they differ.
    local starts dones
    starts=$(echo "$tpls" | jq -r '[.[] | select(.masked | test("starting scheduled run")) | .count] | add // 0')
    dones=$(echo "$tpls" | jq -r '[.[] | select(.masked | test("completed scheduled run")) | .count] | add // 0')
    if [ "${starts:-0}" -gt 0 ] && [ "${dones:-0}" -gt 0 ]; then
        pass "start and completion are textually distinct templates ($starts / $dones)"
    else
        fail "expected distinct start and completion templates" \
            "starts=$starts dones=$dones"
    fi
    if [ "${dones:-0}" -gt "${starts:-0}" ]; then
        pass "completions outnumber starts ($dones > $starts) — never a 1:1 pairing"
    else
        fail "completions did not outnumber starts" "starts=$starts dones=$dones"
    fi

    # Masking must collapse the volatile duration and record count, or every
    # tick mints a new template and novelty detection drowns.
    assert_jq "volatile values are masked out of the template" "$tpls" \
        '[.[] | select(.masked | test("completed scheduled run"))] | length == 1'
}

# ----- 42.4: incremental collection, not replay ---------------------------

scenario_logwatch_ingest_incremental() {
    step 42.4 "appending to the stream ingests ONLY the new lines (cursor is exclusive)"
    if [ -z "$LWI_READY" ]; then
        skip "incremental pull assertions" "42.1 did not complete"
        return 0
    fi
    local before
    before=$(logwatch_count "$NODE_A" "$LWI_WS" "$LWI_SRC_HEALTHY")

    # A distinctly-worded batch at the anchor. Appending (not replacing) is
    # what a live container does, and the collector must pick up only the
    # tail: its cursor is already past everything before it.
    logwatch_gen error_burst --now "$LWI_ANCHOR" --at-days 0 --count 9 \
        > "$LWI_WORK/append.log"
    logwatch_append "order-sync" "$LWI_WORK/append.log"

    # Two source cadences plus slack for the collector's own 15s tick.
    local i=0 after=0
    while [ $i -lt 90 ]; do
        after=$(logwatch_count "$NODE_A" "$LWI_WS" "$LWI_SRC_HEALTHY")
        [ "${after:-0}" -gt "${before:-0}" ] && break
        sleep 5
        i=$((i + 5))
    done

    # The bulk assertion: the pull was INCREMENTAL. A cursor that failed
    # outright would replay all ~15k prior lines here.
    local delta=$((after - before))
    if [ "$delta" -gt 0 ] && [ "$delta" -lt 200 ]; then
        pass "append collected incrementally ($delta lines, not a ~15k replay)"
    else
        fail "append was not collected incrementally" \
            "count moved by $delta (before=$before after=$after)"
    fi
    # The exact assertion is deliberately separate and lives in 42.9, because
    # it is KNOWN-FAILING today for a real reason worth naming on its own.
}

# ----- 42.9: the cursor must not re-ingest its own boundary ---------------

scenario_logwatch_ingest_cursor_precision() {
    step 42.9 "a STATIC log stream must stop producing lines once fully collected"
    if [ -z "$LWI_READY" ]; then
        skip "cursor precision assertions" "42.1 did not complete"
        return 0
    fi
    # ---------------------------------------------------------------------
    # KNOWN-FAILING — do not weaken this to make the suite green.
    # ---------------------------------------------------------------------
    # Nothing appends to the fixture during this step, so a correct collector
    # reaches the end of the stream and every later pull returns nothing. The
    # observed behaviour is that the line count keeps climbing by one per pull
    # forever, and cursor_ts never advances past its first value.
    #
    # Mechanism: the pull path is built on nanosecond precision — pull.go
    # advances logSince by exactly one nanosecond to make the window
    # exclusive, and DockerLogsCommand formats --since as RFC3339Nano. But
    # UpdateLogSourceCursor persists cursor_ts through formatTime, whose
    # layout is time.RFC3339 (SECOND precision), so the fraction is dropped on
    # the way to disk. The next pull asks for <second>.000000001, which is at
    # or before every line in that second, so the tail lines come back and are
    # re-ingested — and the recomputed cursor truncates to the same second
    # again. It is a fixed point: the cursor can never advance past the final
    # second of the stream.
    #
    # Why it matters beyond duplicate rows: the re-ingested arrivals carry
    # IDENTICAL timestamps, so the baseline miner reads zero-second
    # inter-arrival gaps that were never emitted. Median, MAD and the burst
    # split are all computed over that polluted sample, which is a direct
    # corruption of the cadence evidence promotion depends on.
    #
    # This is invisible to any store-side ingest seam: it only appears when
    # real nanosecond-precision `docker logs` output round-trips through a
    # real --since window.
    local first second i=0
    first=$(logwatch_count "$NODE_A" "$LWI_WS" "$LWI_SRC_HEALTHY")
    # Two source cadences (20s) plus slack for the collector's own 15s tick,
    # so at least two further pulls have certainly run.
    sleep 60
    second=$(logwatch_count "$NODE_A" "$LWI_WS" "$LWI_SRC_HEALTHY")
    local cur
    cur=$(logwatch_cursor "$NODE_A" "$LWI_SRC_HEALTHY")

    if [ "${second:-0}" -eq "${first:-0}" ]; then
        pass "fully-collected static stream produced no further lines ($first)"
    else
        fail "cursor re-ingests its own boundary on every pull" \
            "count rose $first -> $second over ~60s with NO new lines on the host; cursor_ts=$cur is second-precision (UpdateLogSourceCursor writes through formatTime = time.RFC3339, dropping the nanoseconds pull.go's +1ns window depends on)"
    fi
}

# ----- 42.5: errors alert immediately, with no baseline -------------------

scenario_logwatch_ingest_error_alerting() {
    step 42.5 "an error-class template fires on first sight, with no learned baseline"
    if [ -z "$LWI_READY" ]; then
        skip "error alerting assertions" "42.1 did not complete"
        return 0
    fi
    # The appended burst from 42.4 carries level=error. The distiller fires an
    # anomaly for a NEW error-class template immediately — that is the
    # operator's "errors alert always, with no baseline" half of the contract,
    # and it must hold on a source whose baseline has never been learned.
    local tpls
    tpls=$(logwatch_templates "$NODE_A" "$LWI_WS" \
        | jq -c --arg s "$LWI_SRC_HEALTHY" '[.templates[]? | select(.source_id == $s)]')
    assert_jq "the failure line classed as error-or-worse" "$tpls" \
        '[.[] | select(.masked | test("send failed")) |
           select(.severity == "error" or .severity == "critical")] | length >= 1'
    # ...and the healthy steady state did NOT. A detector that classes routine
    # completions as errors alerts on everything and is switched off by day 2.
    assert_jq "routine completions stayed info-class" "$tpls" \
        '[.[] | select(.masked | test("completed scheduled run")) |
           select(.severity == "error" or .severity == "critical")] | length == 0'
}

# ----- 42.6: the shim is a faithful docker, not a permissive one ----------

scenario_logwatch_ingest_shim_fidelity() {
    step 42.6 "the log host answers only the four read-only shapes sshx can build"
    if ! logwatch_available; then
        skip "shim fidelity assertions" "compose has no log-host service"
        return 0
    fi
    # If the shim quietly accepted anything, a future collector change could
    # start issuing a mutating command and the rig would never notice.
    local out
    out=$(docker exec "$LOGHOST_CONTAINER" sh -c "docker rm -f order-sync" 2>&1 || true)
    if echo "$out" | grep -q 'unsupported command'; then
        pass "a mutating docker command is refused by the host"
    else
        fail "the log host accepted a mutating command" "output: $(echo "$out" | head -c 200)"
    fi

    # Docker's --since is INCLUSIVE, and the collector relies on that by
    # advancing its cursor a single nanosecond to get an exclusive window. If
    # the shim rounded to the second, that step would be silently untested.
    local ts next first
    ts=$(docker exec "$LOGHOST_CONTAINER" \
        sh -c "docker logs --timestamps 'order-sync' | sed -n '500p' | cut -d' ' -f1" 2>/dev/null)
    if [ -z "$ts" ]; then
        skip "nanosecond boundary check" "no fixture loaded on log-host"
        return 0
    fi
    first=$(docker exec "$LOGHOST_CONTAINER" \
        sh -c "docker logs --timestamps --since '$ts' 'order-sync' | head -1 | cut -d' ' -f1" 2>/dev/null)
    if [ "$first" = "$ts" ]; then
        pass "--since is inclusive at the exact nanosecond"
    else
        fail "--since boundary is not inclusive" "asked $ts, first returned $first"
    fi
    next=$(python3 -c "
d,f='$ts'.rstrip('Z').split('.')
print(f'{d}.{int(f)+1:09d}Z')" 2>/dev/null || echo "")
    if [ -n "$next" ]; then
        first=$(docker exec "$LOGHOST_CONTAINER" \
            sh -c "docker logs --timestamps --since '$next' 'order-sync' | head -1 | cut -d' ' -f1" 2>/dev/null)
        if [ "$first" != "$ts" ]; then
            pass "cursor+1ns excludes the boundary line — the collector's window works"
        else
            fail "+1ns did not exclude the boundary" "still returned $first"
        fi
    fi
}

# ----- 42.7: the shapes the detector must REFUSE --------------------------

scenario_logwatch_ingest_refusal_shapes() {
    step 42.7 "the refusal fixtures are collectable and reach the daemon intact"
    if [ -z "$LWI_READY" ]; then
        skip "refusal-shape assertions" "42.1 did not complete"
        return 0
    fi
    # 40.x asserts the VERDICTS on these. 42.7's job is narrower and is the
    # prerequisite for those: prove the two shapes the learner must refuse
    # actually arrive, so a later "rejected" result means the detector judged
    # them rather than never having seen them.
    local stamp="$LWI_ANCHOR-$RANDOM"
    logwatch_gen irregular --now "$LWI_ANCHOR" --from-days 9 --to-days 0 \
        > "$LWI_WORK/ledger-sync.log"
    logwatch_inject "ledger-sync" "$LWI_WORK/ledger-sync.log"
    logwatch_gen conditional_terminal --now "$LWI_ANCHOR" --from-days 9 --to-days 0 \
        > "$LWI_WORK/invoice-sync.log"
    logwatch_inject "invoice-sync" "$LWI_WORK/invoice-sync.log"

    local irr cond
    irr=$(logwatch_provision_source "$NODE_A" "$LWI_WS" "$LWI_HOST_ID" \
        "ledger-sync-$stamp" "ledger-sync")
    cond=$(logwatch_provision_source "$NODE_A" "$LWI_WS" "$LWI_HOST_ID" \
        "invoice-sync-$stamp" "invoice-sync")
    if [ -z "$irr" ] || [ -z "$cond" ]; then
        fail "create refusal-shape sources" "irregular=$irr conditional=$cond"
        return 0
    fi

    if logwatch_wait_for_lines "$NODE_A" "$LWI_WS" "$irr" 1 120; then
        pass "irregular (random-arrival) history collected"
    else
        fail "irregular history never collected" "source $irr has no templates"
    fi
    if logwatch_wait_for_lines "$NODE_A" "$LWI_WS" "$cond" 2 120; then
        pass "conditional-terminal history collected"
    else
        fail "conditional-terminal history never collected" "source $cond has no templates"
    fi

    # The hazard in its own right: the job's SUCCESS line has never been
    # emitted, so the only terminal the learner can see is the early return.
    local tpls
    tpls=$(logwatch_templates "$NODE_A" "$LWI_WS" \
        | jq -c --arg s "$cond" '[.templates[]? | select(.source_id == $s)]')
    assert_jq "the early-return terminal is present" "$tpls" \
        '[.[] | select(.masked | test("nothing to do"))] | length >= 1'
    assert_jq "the success line was NEVER observed (the whole hazard)" "$tpls" \
        '[.[] | select(.masked | test("completed scheduled run"))] | length == 0'
}

# ----- 42.8: what still cannot be driven from the rig ---------------------

scenario_logwatch_ingest_learner_gap() {
    step 42.8 "report whether a learner pass can be forced from the rig"
    if [ -z "$LWI_READY" ]; then
        skip "learner trigger probe" "42.1 did not complete"
        return 0
    fi
    # Injection is solved; DRIVING THE LEARNER is not, and that gap belongs in
    # the results rather than in a comment nobody reads. baseline.Learner has
    # a hardcoded 5-minute startup delay and an hourly ticker, and the absence
    # Evaluator a 2-minute one — none env-tunable. A rig run therefore gets at
    # most ONE learn pass and cannot re-learn after injecting, so a verdict
    # assertion would be timing-dependent rather than deterministic.
    local code
    code=$(api_status POST "$NODE_A/api/v1/monitoring/test-tick" '{"what":"learn"}')
    # The router serves an SPA catch-all, so 200 alone proves nothing; a real
    # route answers JSON.
    if [ "$code" = "200" ] && api POST "$NODE_A/api/v1/monitoring/test-tick" \
        '{"what":"learn"}' 2>/dev/null | jq -e . >/dev/null 2>&1; then
        pass "daemon exposes a learner trigger — verdict assertions can be deterministic"
    else
        skip "learner trigger" \
            "no POST /api/v1/monitoring/test-tick (HTTP $code); baseline promotion cannot be forced, so 40.x verdicts stay timing-dependent"
    fi
    [ -n "$LWI_WORK" ] && rm -rf "$LWI_WORK"
    return 0
}
