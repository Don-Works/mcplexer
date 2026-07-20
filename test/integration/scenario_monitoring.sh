#!/usr/bin/env bash
# scenario_monitoring.sh — acceptance tests for automatic log monitoring:
# what the daemon learns "normal" looks like, and what it refuses to learn.
#
# Steps 40.x. Incident detection (absence / collection / deploy tolerance /
# channel health) lives in scenario_monitoring_incidents.sh, steps 41.x.
#
# Read lib_monitoring.sh FIRST — it carries the two production incidents these
# scenarios encode, the measured signal shape the fixtures reproduce, and the
# daemon test-hook contract every step here consumes.
#
# ---------------------------------------------------------------------------
# KNOWN-FAILING TODAY (do not weaken these to make the suite green)
# ---------------------------------------------------------------------------
#   40.2 healthy bursty signal PROMOTES — expected to FAIL on a build whose
#   regularity gate treats bunched arrivals as one cadence. The measured shape
#   has a median inter-arrival of ~1s (three completions back-to-back) and a p95
#   of ~297s (the 5-minute tick), so p95/median scores 8-298 against
#   store.BaselineMaxP95Ratio = 3.0 and the real, healthy, textbook-periodic
#   order-sync signal is rejected as "irregular". A detector that refuses the
#   only signal it was built for is the bug; a green suite over it is worse.
#   store.BaselineBurstSeparation (8.0) is the intended fix — collapse each
#   burst to one arrival, then measure the tick — and 40.2 is its acceptance
#   test.
#
# Everything else here is expected to pass once the test hooks exist. Without
# the hooks every step SKIPs with the missing route named.

# shellcheck source=lib_monitoring.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib_monitoring.sh"

# ----- 40.1: rig setup ----------------------------------------------------

scenario_monitoring_setup() {
    step 40.1 "provision a monitoring workspace, host, sources and a self-contained alert sink"
    monitoring_probe_capabilities

    # One frozen "now" for every fixture in 40.x and 41.x, so a run that
    # straddles a minute boundary still produces identical relative geometry.
    MON_NOW=$(date -u +%s)

    # Workspace NAME is unique, not just the id, so a re-run against a kept
    # harness (TEST_KEEP=1) must not collide with the previous run's rows.
    local stamp="$MON_NOW-$RANDOM"
    local ws="ws-monitoring-$stamp"
    local wbody
    wbody=$(jq -nc --arg id "$ws" --arg nm "monitoring-$stamp" \
        '{id:$id, name:$nm, root_path:"/tmp/monitoring", default_policy:"allow"}')
    if ! api POST "$NODE_A/api/v1/workspaces" "$wbody" >/dev/null 2>&1; then
        fail "create monitoring workspace" "POST /api/v1/workspaces rejected $ws"
        return 0
    fi
    WS_MON="$ws"
    pass "created monitoring workspace $ws"

    # A remote host row is required as the log sources' parent, and
    # ValidateRemoteHost requires an auth scope to hold the (never-used) SSH
    # credential. Nothing ever dials it: every line arrives through the
    # test-ingest hook, so the address is a documentation placeholder
    # (RFC 5737) and no SSH connection is ever attempted.
    local sbody sresp
    sbody=$(jq -nc --arg id "scope-monitoring-$stamp" --arg nm "monitoring-ssh-$stamp" \
        '{id:$id, name:$nm, type:"ssh_key"}')
    sresp=$(api POST "$NODE_A/api/v1/auth-scopes" "$sbody" 2>/dev/null || echo '{}')
    MON_SCOPE_ID=$(echo "$sresp" | jq -r '.id // .ID // empty')
    if [ -z "$MON_SCOPE_ID" ]; then
        fail "create monitoring auth scope" "body: $(echo "$sresp" | head -c 200)"
        return 0
    fi

    local hbody hresp
    hbody=$(jq -nc --arg ws "$ws" --arg scope "$MON_SCOPE_ID" \
        '{workspace_id:$ws, name:"ops-example", ssh_user:"logwatch",
          ssh_host:"203.0.113.10", ssh_port:22, auth_scope_id:$scope, enabled:true}')
    hresp=$(api POST "$NODE_A/api/v1/remote-hosts" "$hbody" 2>/dev/null || echo '{}')
    MON_HOST_ID=$(echo "$hresp" | jq -r '.id // empty')
    if [ -z "$MON_HOST_ID" ]; then
        fail "create remote host" "body: $(echo "$hresp" | head -c 200)"
        return 0
    fi
    pass "created remote host ops-example ($MON_HOST_ID)"

    # Four sources so the scenarios never contaminate each other: each one owns
    # exactly one signal shape and one verdict.
    mon_make_source healthy   && pass "created log source 'healthy' ($MON_SRC_HEALTHY)"
    mon_make_source irregular && pass "created log source 'irregular' ($MON_SRC_IRREGULAR)"
    mon_make_source condterm  && pass "created log source 'condterm' ($MON_SRC_CONDTERM)"
    mon_make_source deploy    && pass "created log source 'deploy' ($MON_SRC_DEPLOY)"
    mon_make_source darkpull  && pass "created log source 'darkpull' ($MON_SRC_DARKPULL)"

    # The alert sink. kind=mesh keeps every dispatched alert inside this
    # container — it lands as a mesh message readable at /api/v1/mesh/status.
    # No webhook, no Google Chat, no paired peer, nothing leaves the rig.
    local cbody cresp
    cbody=$(jq -nc --arg ws "$ws" \
        '{workspace_id:$ws, name:"harness-sink", kind:"mesh",
          config_json:"{}", min_severity:"info", enabled:true}')
    cresp=$(api POST "$NODE_A/api/v1/monitoring-channels" "$cbody" 2>/dev/null || echo '{}')
    MON_CHANNEL_ID=$(echo "$cresp" | jq -r '.id // empty')
    if [ -n "$MON_CHANNEL_ID" ]; then
        pass "created in-container alert sink (kind=mesh, $MON_CHANNEL_ID)"
    else
        fail "create alert sink channel" "body: $(echo "$cresp" | head -c 200)"
    fi

    printf '  capabilities: ingest=%s learn=%s evaluate=%s baselines=%s\n' \
        "${MON_HAVE_INGEST:-no}" "${MON_HAVE_LEARN:-no}" \
        "${MON_HAVE_EVAL:-no}" "${MON_HAVE_BASELINES:-no}"
}

# mon_make_source <tag> — creates one log source and exports MON_SRC_<TAG>.
# Retention is 14 days so a 7-day backdated fixture is never pruned mid-run.
mon_make_source() {
    local tag="$1"
    local body resp id
    body=$(jq -nc --arg ws "$WS_MON" --arg host "$MON_HOST_ID" --arg nm "$tag" \
        '{workspace_id:$ws, remote_host_id:$host, name:("job-" + $nm),
          kind:"docker", selector:("job-" + $nm), schedule_spec:"*/5 * * * *",
          max_pull_bytes:8000000, retention_mb:64, retention_days:14, enabled:true}')
    resp=$(api POST "$NODE_A/api/v1/log-sources" "$body" 2>/dev/null || echo '{}')
    id=$(echo "$resp" | jq -r '.id // empty')
    if [ -z "$id" ]; then
        fail "create log source $tag" "body: $(echo "$resp" | head -c 200)"
        return 1
    fi
    eval "MON_SRC_${tag^^}=\"$id\""
    return 0
}

# mon_seed_days <source_id> <days> <generator...> — ingests a backdated fixture
# one day at a time. Day 0 is `days` days ago; the last day ends at MON_NOW.
# Chunking keeps each request body well under a megabyte.
mon_seed_days() {
    local src="$1" days="$2"; shift 2
    local gen="$1" burst="${2:-3}" tick="${3:-300}" text="${4:-}"
    local d start lines total=0 n
    for d in $(seq "$days" -1 1); do
        start=$(( MON_NOW - d * 86400 ))
        case "$gen" in
            bursty)  lines=$(mon_gen_bursty "$start" 24 "$burst" "$tick" "$text") ;;
            poisson) lines=$(mon_gen_poisson "$start" 24 "$tick" "$d" "$text") ;;
            *) return 1 ;;
        esac
        # Retry once. A batch can fail transiently when the daemon is restarted
        # under the run (another agent rebuilding the image, a compose bounce),
        # and a silently-dropped day leaves the fixture a day short of the
        # 7-day gap-free history the promotion ladder requires — which then
        # reads as "the learner refused a healthy signal" rather than "the
        # harness never sent one".
        n=$(mon_ingest "$NODE_A" "$WS_MON" "$src" "$lines" | jq -r '.ingested // 0')
        if [ "${n:-0}" -eq 0 ]; then
            sleep 2
            n=$(mon_ingest "$NODE_A" "$WS_MON" "$src" "$lines" | jq -r '.ingested // 0')
        fi
        total=$(( total + ${n:-0} ))
    done
    echo "$total"
}

# ----- 40.2: the healthy bursty signal must PROMOTE -----------------------

scenario_monitoring_bursty_promotes() {
    step 40.2 "the measured bursty order-sync signal PROMOTES a baseline rule"
    mon_require "bursty signal promotes" ingest learn baselines || return 0

    # Seven days, so the 7-day gap-free day-history floor
    # (store.BaselineMinDayHistoryDays) is satisfiable at all; 5-minute tick;
    # ~3.3 bunched completions per tick; ~40/hour; flat 24/7.
    local seeded
    seeded=$(mon_seed_days "$MON_SRC_HEALTHY" 7 bursty 3 300 "$MON_JOB_DONE")
    if [ "${seeded:-0}" -lt 5000 ]; then
        fail "seed healthy signal" "ingested only ${seeded:-0} completion lines, want >5000 over 7 days"
        return 0
    fi
    pass "seeded $seeded completions over 7 days (5m tick, ~3.3 bunched, ~40/hr)"

    # Starts are a DISTINCT template on a DIFFERENT cadence, and there are fewer
    # of them than completions. Nothing may model this job as start/finish pairs.
    local starts
    starts=$(mon_seed_days "$MON_SRC_HEALTHY" 7 poisson 0 112 "$MON_JOB_START")
    if [ "${starts:-0}" -gt 0 ] && [ "${starts:-0}" -lt "${seeded:-0}" ]; then
        pass "starts ($starts) are a distinct template and are OUTNUMBERED by completions ($seeded)"
    else
        fail "start/completion ratio wrong" "starts=$starts completions=$seeded — completions must outnumber starts"
    fi

    mon_learn "$NODE_A" "$WS_MON" >/dev/null

    local b
    b=$(mon_baseline_for "$NODE_A" "$WS_MON" "completed order")
    if [ -z "$b" ] || [ "$b" = "null" ]; then
        fail "learner produced no baseline for the completion template" \
            "the learner must record a verdict for every candidate, promotion or refusal"
        return 0
    fi

    local decision reason p95ratio relmad
    decision=$(echo "$b" | jq -r '.decision // "?"')
    reason=$(echo "$b" | jq -r '.reason // ""')
    p95ratio=$(echo "$b" | jq -r '.p95_ratio // 0')
    relmad=$(echo "$b" | jq -r '.relative_mad // 0')

    if [ "$decision" = "promoted" ]; then
        pass "healthy bursty signal PROMOTED (p95_ratio=$p95ratio relative_mad=$relmad)"
    else
        # THE KNOWN FAILURE. Do not soften this into a skip: the whole feature
        # is worthless if the only signal it was built for is refused.
        fail "healthy bursty signal was REFUSED (decision=$decision)" \
            "reason='$reason' p95_ratio=$p95ratio relative_mad=$relmad — the measured shape is bunched (median gap ~1s, p95 ~297s), so a gate that scores raw inter-arrivals against BaselineMaxP95Ratio=3.0 rejects a textbook-periodic job. Collapse bursts (BaselineBurstSeparation) before measuring cadence."
        return 0
    fi

    assert_jq "promotion produced a live rule id" "$b" '(.rule_id // "") | length > 0'
    assert_jq "promotion derived a verifiable match substring" "$b" \
        '(.match_substring // "") | length >= 12'

    # The learned window IS the time-to-detection ceiling for scenario 41.1.
    local window
    window=$(echo "$b" | jq -r '.window_seconds // 0')
    if [ "${window:-0}" -gt 0 ] && [ "${window:-0}" -le 3600 ]; then
        pass "learned absence window is ${window}s — detection ceiling well inside an hour"
    else
        fail "learned absence window is ${window}s" \
            "12h was the status quo that let 7h39m of silence pass unnoticed; a window over 3600s is not a detector"
    fi
}

# ----- 40.3: an irregular signal must be REFUSED --------------------------

scenario_monitoring_irregular_refused() {
    step 40.3 "a random (Poisson) arrival pattern is REFUSED, not promoted"
    mon_require "irregular signal refused" ingest learn baselines || return 0

    # Precision matters more than recall here. A false "your orders stopped" at
    # 3am gets the whole channel muted, after which the system is worse than
    # useless — so an arrival process the evidence cannot call periodic must be
    # recorded as a refusal with a reason, never promoted on a guess.
    local seeded
    seeded=$(mon_seed_days "$MON_SRC_IRREGULAR" 7 poisson 0 90 "$MON_JOB_DONE")
    if [ "${seeded:-0}" -lt 2000 ]; then
        fail "seed irregular signal" "ingested only ${seeded:-0} lines, want >2000"
        return 0
    fi
    pass "seeded $seeded randomly-arriving lines over 7 days (mean gap 90s)"

    mon_learn "$NODE_A" "$WS_MON" >/dev/null

    local b decision
    b=$(api GET "$NODE_A/api/v1/monitoring/baselines?source_id=$MON_SRC_IRREGULAR&limit=50" \
        2>/dev/null | jq -c 'first((.baselines // [])[])' 2>/dev/null || echo "null")
    if [ -z "$b" ] || [ "$b" = "null" ]; then
        fail "learner recorded no verdict for the irregular candidate" \
            "a refusal must be stored with the same weight as a promotion — 'why is there no rule for this job?' needs an answer"
        return 0
    fi
    decision=$(echo "$b" | jq -r '.decision // "?"')
    if [ "$decision" = "promoted" ]; then
        fail "random arrivals were PROMOTED (decision=promoted)" \
            "p95_ratio=$(echo "$b" | jq -r '.p95_ratio') relative_mad=$(echo "$b" | jq -r '.relative_mad') — an exponential process must be rejected with margin, or the first quiet stretch pages the operator falsely"
    else
        pass "random arrivals REFUSED (decision=$decision, reason='$(echo "$b" | jq -r '.reason' | head -c 90)')"
    fi
    assert_jq "the refusal carries the statistics it was made from" "$b" \
        '(.sample_count // 0) > 0 and (.relative_mad // 0) > 0'
    assert_jq "the refusal produced no live rule" "$b" '(.rule_id // "") == ""'
}

# ----- 40.4: the conditional-terminal job must be REFUSED -----------------

scenario_monitoring_conditional_terminal_refused() {
    step 40.4 "a job whose only terminal line is an early return is REFUSED"
    mon_require "conditional-terminal refused" ingest learn baselines || return 0

    # The measured second job. It ALWAYS returns early through a "no invoices to
    # send" branch, so its only terminal line is conditional on there being
    # nothing to do. Its arrival rate measures how often there was no work, not
    # how often the job completed — the wrong observable entirely.
    #
    # Promoting it looks textbook: perfectly periodic, continuous, high
    # confidence. And it inverts the alert — the rule stays green while the job
    # has nothing to do, and fires "invoice sync has stopped!" on the first day
    # the customer actually has invoices. A false alarm on the day the system
    # starts working is exactly what this whole feature is biased against.
    local seeded
    seeded=$(mon_seed_days "$MON_SRC_CONDTERM" 7 bursty 1 300 "$MON_COND_LINE")
    if [ "${seeded:-0}" -lt 1000 ]; then
        fail "seed conditional-terminal signal" "ingested only ${seeded:-0} lines"
        return 0
    fi
    pass "seeded $seeded perfectly-periodic early-return lines over 7 days"

    mon_learn "$NODE_A" "$WS_MON" >/dev/null

    local b decision
    b=$(api GET "$NODE_A/api/v1/monitoring/baselines?source_id=$MON_SRC_CONDTERM&limit=50" \
        2>/dev/null | jq -c 'first((.baselines // [])[])' 2>/dev/null || echo "null")
    if [ -z "$b" ] || [ "$b" = "null" ]; then
        fail "learner recorded no verdict for the conditional-terminal candidate" ""
        return 0
    fi
    decision=$(echo "$b" | jq -r '.decision // "?"')
    if [ "$decision" = "conditional_terminal" ]; then
        pass "early-return line refused as conditional_terminal (reason='$(echo "$b" | jq -r '.reason' | head -c 80)')"
    elif [ "$decision" = "promoted" ]; then
        fail "the early-return line was PROMOTED as the job's success signal" \
            "'no invoices to send' asserts a NULL OUTCOME — it is emitted on the branch taken when there is nothing to do, so it cannot be an unconditional terminal. Promoting it inverts the alert."
    else
        pass "early-return line refused (decision=$decision) — not modelled as healthy"
    fi
    assert_jq "the conditional-terminal refusal produced no live rule" "$b" '(.rule_id // "") == ""'
}

# ----- 40.5: template identity survives a redeploy ------------------------

scenario_monitoring_template_identity() {
    step 40.5 "a redeploy mints a new template id for identical text; the masked shape is stable"
    mon_require "template identity across redeploy" ingest || return 0

    # Line numbers shift between releases and the version string in the banner
    # changes, so nothing may fingerprint on file:line or on the literal
    # version. The masked template must be stable across the version bump.
    local t0=$(( MON_NOW - 7200 ))
    mon_ingest "$NODE_A" "$WS_MON" "$MON_SRC_DEPLOY" \
        "$(mon_gen_at "$MON_DEPLOY_BANNER v1.4.1" $t0 $((t0+1)))" >/dev/null
    mon_ingest "$NODE_A" "$WS_MON" "$MON_SRC_DEPLOY" \
        "$(mon_gen_at "$MON_DEPLOY_BANNER v1.4.2" $((t0+3600)) $((t0+3601)))" >/dev/null

    local tpls shapes
    tpls=$(api GET "$NODE_A/api/v1/monitoring/templates?workspace_id=$WS_MON&window=24h" \
        2>/dev/null || echo '{}')
    shapes=$(echo "$tpls" | jq -r \
        '[(.templates // [])[] | select(.masked | contains("running version")) | .masked] | unique | length' \
        2>/dev/null || echo 0)
    if [ "${shapes:-0}" = "1" ]; then
        pass "both releases mask to ONE stable shape — the version string is masked out"
    else
        fail "version banner produced $shapes distinct masked shapes, want 1" \
            "the version string must be masked; fingerprinting on it makes every deploy look like a new template"
    fi
}

# ----- 40.6: the baseline surface explains itself -------------------------

scenario_monitoring_baseline_explainable() {
    step 40.6 "GET /monitoring/baselines answers 'why is there no alert for this job?'"
    if [ -z "${MON_HAVE_BASELINES:-}" ]; then
        skip "baseline read surface" "GET /api/v1/monitoring/baselines not served by this daemon"
        return 0
    fi
    local body
    body=$(api GET "$NODE_A/api/v1/monitoring/baselines?workspace_id=${WS_MON:-none}&limit=100" \
        2>/dev/null || echo '{}')
    assert_jq "baselines endpoint returns a baselines array" "$body" '(.baselines // null) | type == "array"'
    assert_jq "baselines endpoint echoes the promotion thresholds" "$body" \
        '(.thresholds.max_p95_ratio // 0) > 0 and (.thresholds.max_relative_mad // 0) > 0'
    # Serving the thresholds next to the verdicts is what makes a stored
    # decision checkable rather than assertable.
    assert_jq "thresholds include the random-arrival reference values" "$body" \
        '(.thresholds.random_arrival_p95_ratio // 0) > (.thresholds.max_p95_ratio // 0)'
}
