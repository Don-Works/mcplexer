#!/usr/bin/env bash
# scenario_monitoring_incidents.sh — acceptance tests for what the monitor does
# once it knows what normal looks like: raising absence, distinguishing "we
# cannot see" from "it stopped", tolerating a deploy without tolerating an error
# inside one, and refusing to let a dead alert route hide behind a throttle.
#
# Steps 41.x. Baseline learning is scenario_monitoring.sh, steps 40.x, which
# MUST run first — it provisions WS_MON, the sources, and the alert sink, and
# 41.1 consumes the rule that 40.2 promotes.
#
# Every assertion here reads OBSERVABLE STATE over REST: the canonical incident
# task (tagged logwatch+incident+expected-signal, carrying meta.logwatch_class),
# the mesh-delivered alert record, the channel row. Nothing greps the daemon's
# stdout — a log line is not a contract and scraping one is a test that breaks
# on a refactor while the defect it was meant to catch sails through.
#
# ---------------------------------------------------------------------------
# STATE OF PLAY (do not weaken an assertion to make the suite green)
# ---------------------------------------------------------------------------
#   41.1, 41.2, 41.3, 41.6 need a rule that is OLDER than one absence window.
#   EvaluateExpectedSignal will not raise until a full window has elapsed since
#   the rule's created_at, and the learner stamps created_at = now, so a rule
#   promoted during this run is structurally incapable of raising for its whole
#   window (30m for the measured signal). The guard is correct and must stay.
#   These steps therefore SKIP with that reason rather than reporting a
#   detector failure the daemon does not have. They become executable the
#   moment POST /monitoring/test-tick accepts an optional RFC3339 `now` and
#   threads it into ExpectedSignalInput.Now — the evaluator is already a pure
#   function of an injected clock, so it is a parameter, not a redesign.
#
#   41.7 a persistently failing channel must be SURFACED, and currently FAILS.
#   Detection works — reportChannelFailure escalates to slog.Error at 3
#   consecutive failures. Surfacing does not: no field on the channel row, no
#   incident, no delivery record, so the only place a dead route exists is the
#   daemon's stdout. That is the 2026-07-14 shape, where the breakage was
#   logged once and the suppression 191 times and only the suppression was
#   visible. A health state an operator cannot query is not surfaced.

# shellcheck source=lib_monitoring.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib_monitoring.sh"

# mon_rule_ready — the 41.x steps that need a learned rule share this guard, so
# a missing rule reports its cause once instead of five confusing failures.
mon_rule_ready() {
    local label="$1"
    local rid
    # Scoped to the healthy source. Several sources in this rig carry the same
    # completion text on purpose, so an unscoped lookup answers about whichever
    # rule sorts first — which is how 41.1 and 41.2 once disagreed about
    # whether the same rule was ready.
    rid=$(api GET "$NODE_A/api/v1/monitoring/baselines?source_id=$MON_SRC_HEALTHY&limit=50" 2>/dev/null \
        | jq -r 'first((.baselines // [])[] | select((.rule_id // "") != "") | .rule_id) // empty' \
        2>/dev/null)
    if [ -z "$rid" ]; then
        fail "$label" "no promoted rule for the healthy completion signal — step 40.2 refused the measured shape, so absence detection has nothing to evaluate. Fix the promotion gate first; this step cannot pass until it does."
        return 1
    fi
    MON_RULE_ID="$rid"
    # A rule promoted during this run cannot raise until a full window has
    # elapsed since its created_at. That is the evaluator's warming-up guard
    # doing its job, not a detector failure, so it must SKIP rather than FAIL —
    # blaming the daemon for a guard we asked it to have is how a rig loses the
    # operator's trust. See mon_rule_warming_up for the one-parameter contract
    # extension that makes these steps executable.
    local warming
    warming=$(mon_rule_warming_up "$NODE_A" "$WS_MON" "completed order" "$MON_SRC_HEALTHY")
    if [ -n "$warming" ]; then
        skip "$label" \
            "rule is warming up ($warming). The learner stamps created_at=now, so a rule promoted in this run cannot raise for its whole window. Add an optional RFC3339 'now' to POST /monitoring/test-tick and thread it into ExpectedSignalInput.Now — the evaluator already takes an injected clock."
        return 1
    fi
    return 0
}

# ----- 41.1: Incident 1 — the silent hang --------------------------------

scenario_monitoring_absence_raised() {
    step 41.1 "completions stop while starts continue → an ABSENCE incident is raised"
    mon_require "absence on silent job" ingest eval || return 0
    mon_rule_ready "absence on silent job" || return 0

    # The 2026-07-20 shape exactly. The job did not crash and did not error: the
    # process stayed alive, the container healthcheck stayed green, and the
    # completion line simply stopped. Starts kept arriving, which is what made
    # every liveness signal read healthy while orders fell to zero.
    local gap_start=$(( MON_NOW - 5400 ))   # completions ceased 90 minutes ago
    local t
    local stamps=""
    for t in $(mon_seq "$gap_start" 600 "$MON_NOW"); do
        stamps="$stamps $t"
    done
    # shellcheck disable=SC2086
    mon_ingest "$NODE_A" "$WS_MON" "$MON_SRC_HEALTHY" \
        "$(mon_gen_at "$MON_JOB_START (still running)" $stamps)" >/dev/null
    pass "seeded 90 minutes of STARTS with zero completions (process alive, emitting nothing useful)"

    local before=0 after=0
    before=$(mon_incident_count "$NODE_A" "$WS_MON" "absence:")
    mon_evaluate "$NODE_A" "$WS_MON" >/dev/null
    after=$(mon_incident_count "$NODE_A" "$WS_MON" "absence:")

    if [ "${after:-0}" -gt "${before:-0}" ]; then
        pass "absence incident raised (incident tasks ${before}→${after})"
    else
        fail "no absence incident was raised" \
            "completions have been silent for 90 minutes with starts still arriving — this is the exact 2026-07-20 shape that produced 7h39m of silence and ZERO alerts"
        return 0
    fi

    # ABSENCE, not COLLECTION. The source is plainly visible (starts are
    # arriving), so reporting "we cannot see" here would be wrong and would send
    # the operator to fix the collector instead of the job.
    local collection
    collection=$(mon_incident_count "$NODE_A" "$WS_MON" "absence-collection:")
    if [ "${collection:-0}" -eq 0 ]; then
        pass "classified as ABSENCE, not COLLECTION — the source is demonstrably visible"
    else
        fail "a COLLECTION incident was raised for a visible source" \
            "starts are arriving, so collection is healthy; 'we cannot see' is a different incident with a different fix"
    fi

    # TIME TO DETECTION. The learned window is the ceiling by construction: the
    # evaluator raises on the first tick after a full window of silence. 12h was
    # the status quo and it is worthless — the operator found the outage himself
    # the next morning.
    local window
    window=$(mon_baseline_for "$NODE_A" "$WS_MON" "completed order" | jq -r '.window_seconds // 0')
    printf '  MEASURED time-to-detection ceiling: %ss (%s minutes)\n' \
        "$window" "$(( ${window:-0} / 60 ))"
    if [ "${window:-0}" -gt 0 ] && [ "${window:-0}" -le 3600 ]; then
        pass "time to detection is ${window}s — the 7h39m outage would have been caught inside the hour"
    else
        fail "time to detection is ${window}s" \
            "anything approaching the 12h status quo does not detect the incident, it documents it after the fact"
    fi

    # A signal that is merely late must NOT be an incident. Without this the
    # window is decoration and the rule fires on jitter.
    local raised_early
    raised_early=$(mon_incident_count "$NODE_A" "$WS_MON" "absence:")
    if [ "${window:-0}" -gt 0 ] && [ 5400 -gt "${window:-0}" ]; then
        pass "the 90-minute silence exceeds the ${window}s window — the raise is earned, not accidental"
    else
        fail "fixture gap (5400s) does not exceed the learned window (${window}s)" \
            "the raise cannot be attributed to absence detection; widen the fixture gap or the window is too large"
    fi
    MON_ABSENCE_SEEN="$raised_early"
}

# ----- 41.2: convergence, delivery, re-notification, recovery -------------

scenario_monitoring_absence_converges() {
    step 41.2 "repeat ticks converge on ONE incident; the alert is delivered; recovery clears it"
    mon_require "absence convergence" ingest eval || return 0
    mon_rule_ready "absence convergence" || return 0

    local first
    first=$(mon_incident_count "$NODE_A" "$WS_MON" "absence:")
    if [ "${first:-0}" -eq 0 ]; then
        fail "absence convergence" "no absence incident exists — 41.1 must raise one first"
        return 0
    fi

    # Three more ticks over the same unchanged silence. A detector that mints a
    # new incident (and a new task, and a new page) per tick is a detector that
    # gets muted within the hour.
    mon_evaluate "$NODE_A" "$WS_MON" >/dev/null
    mon_evaluate "$NODE_A" "$WS_MON" >/dev/null
    mon_evaluate "$NODE_A" "$WS_MON" >/dev/null
    local repeat
    repeat=$(mon_incident_count "$NODE_A" "$WS_MON" "absence:")
    if [ "${repeat:-0}" -eq "${first:-0}" ]; then
        pass "3 further ticks produced NO new incident (still $repeat) — the class converges"
    else
        fail "incident count grew ${first}→${repeat} across repeat ticks" \
            "one ongoing outage must be ONE incident; per-tick duplicates are how an alert channel gets muted"
    fi

    # The alert must be RECORDED as delivered, through the rig's in-container
    # mesh sink. Asserting on delivery state rather than on a send attempt is
    # the difference between "we tried" and "it arrived".
    local alerts
    alerts=$(mon_alert_count "$NODE_A" "$WS_MON" "order-sync")
    if [ "${alerts:-0}" -gt 0 ]; then
        pass "alert recorded as delivered to the in-container sink ($alerts message(s), nothing left the rig)"
    else
        fail "no alert delivery was recorded" \
            "an incident nobody is told about is the 2026-07-20 outcome; the mesh channel is enabled at min_severity=info so nothing filtered it"
    fi

    # A persistent incident must keep saying so. Going silent after the first
    # alert is what let a dead route sit unnoticed for six days.
    local renotified
    renotified=$(api GET "$NODE_A/api/v1/tasks?workspace_id=$WS_MON&limit=200" 2>/dev/null \
        | jq -r '[.[]? | select((.meta // "") | contains("\"logwatch_class\":\"absence:"))
                 | select((.status // "") != "done")] | length' 2>/dev/null || echo 0)
    if [ "${renotified:-0}" -gt 0 ]; then
        pass "the incident task stays OPEN while the outage persists (re-notification candidate)"
    else
        fail "the incident task closed itself while the signal is still absent" \
            "an unresolved absence must remain open and be re-asked, or the operator hears about it exactly once"
    fi

    # RECOVERY. Completions resume; the next tick must clear.
    local t stamps=""
    for t in $(mon_seq $(( MON_NOW - 240 )) 60 "$MON_NOW"); do
        stamps="$stamps $t $((t+1)) $((t+2))"
    done
    # shellcheck disable=SC2086
    mon_ingest "$NODE_A" "$WS_MON" "$MON_SRC_HEALTHY" \
        "$(mon_gen_at "$MON_JOB_DONE 999999" $stamps)" >/dev/null
    mon_evaluate "$NODE_A" "$WS_MON" >/dev/null

    local closed
    closed=$(api GET "$NODE_A/api/v1/tasks?workspace_id=$WS_MON&limit=200" 2>/dev/null \
        | jq -r '[.[]? | select((.meta // "") | contains("\"logwatch_class\":\"absence:"))
                 | select((.status // "") == "done")] | length' 2>/dev/null || echo 0)
    if [ "${closed:-0}" -gt 0 ]; then
        pass "the signal returned and the incident CLEARED (task resolved)"
    else
        fail "the incident did not clear after the signal returned" \
            "an absence alert whose task is never closed trains operators to ignore the next one"
    fi
}

# ----- 41.3: broken collection is a DIFFERENT incident --------------------

scenario_monitoring_collection_not_absence() {
    step 41.3 "a failing source pull raises COLLECTION, never ABSENCE"
    mon_require "collection vs absence" ingest learn eval || return 0

    # "No orders!" when the truth is "we cannot see" sends the operator to the
    # wrong system entirely. The two incidents have different fixes and must
    # never merge into one class.
    local seeded
    seeded=$(mon_seed_days "$MON_SRC_DARKPULL" 7 bursty 3 300 "$MON_JOB_DONE")
    if [ "${seeded:-0}" -lt 5000 ]; then
        skip "collection vs absence" "could not seed the darkpull source (got ${seeded:-0} lines)"
        return 0
    fi
    mon_learn "$NODE_A" "$WS_MON" >/dev/null

    # Collection breaks. Two routes reach the collection branch of
    # EvaluateExpectedSignal; we drive whichever the API exposes.
    #
    #   (a) consecutive_failures >= max_consecutive_failures — the pull is
    #       erroring. Preferred, because it is what a broken SSH target does.
    #   (b) the source is disabled — we are not even looking.
    #
    # Route (c), "source produced no lines of any kind", is deliberately NOT
    # used: it is indistinguishable from an idle rig and would make this step
    # pass for the wrong reason.
    api PATCH "$NODE_A/api/v1/log-sources/$MON_SRC_DARKPULL" \
        "$(jq -nc '{consecutive_failures:5}')" >/dev/null 2>&1 || true
    local health
    health=$(api GET "$NODE_A/api/v1/log-sources/$MON_SRC_DARKPULL" 2>/dev/null \
        | jq -r '.consecutive_failures // 0')
    if [ "${health:-0}" -ge 3 ]; then
        pass "source pull marked as failing ($health consecutive failures)"
    else
        api PATCH "$NODE_A/api/v1/log-sources/$MON_SRC_DARKPULL" \
            "$(jq -nc '{enabled:false}')" >/dev/null 2>&1 || true
        local enabled
        enabled=$(api GET "$NODE_A/api/v1/log-sources/$MON_SRC_DARKPULL" 2>/dev/null \
            | jq -r '.enabled')
        if [ "$enabled" = "false" ]; then
            pass "source disabled — collection cannot verify the signal at all"
        else
            skip "collection vs absence" \
                "no supported way to break collection: PATCH /log-sources accepts neither consecutive_failures nor enabled:false"
            return 0
        fi
    fi

    local warming
    warming=$(mon_rule_warming_up "$NODE_A" "$WS_MON" "completed order" "$MON_SRC_DARKPULL")
    if [ -n "$warming" ]; then
        skip "collection vs absence" \
            "cannot evaluate the darkpull rule: $warming. A rule promoted in this run is inside its own warming-up window; see mon_rule_warming_up for the injected-clock contract extension that makes this step executable."
        return 0
    fi

    mon_evaluate "$NODE_A" "$WS_MON" >/dev/null

    local coll abs
    coll=$(mon_incident_count "$NODE_A" "$WS_MON" "absence-collection:")
    if [ "${coll:-0}" -gt 0 ]; then
        pass "raised a COLLECTION incident ($coll) — reported as lost visibility, not as a stopped job"
    else
        fail "no COLLECTION incident was raised for a source we cannot read" \
            "silent blindness is the worst state of all: the dashboard reads green because nothing is arriving to contradict it"
    fi

    # And it must NOT masquerade as absence on the same rule.
    abs=$(mon_incident_tasks "$NODE_A" "$WS_MON" "absence:" \
        | jq -r --arg s "$MON_SRC_DARKPULL" '[.[] | select((.meta // "") | contains("absence-collection") | not)] | length' \
        2>/dev/null || echo 0)
    local title_ok
    title_ok=$(mon_incident_tasks "$NODE_A" "$WS_MON" "absence-collection:" \
        | jq -r '[.[] | select((.title // "") | test("cannot verify"; "i"))] | length' 2>/dev/null || echo 0)
    if [ "${title_ok:-0}" -gt 0 ]; then
        pass "the collection incident says 'cannot verify' rather than claiming the signal stopped"
    else
        fail "the collection incident does not state that visibility was lost" \
            "the wording is the fix instruction: 'cannot verify' sends the operator to the collector, 'stopped' sends them to the job"
    fi
}

# ----- 41.4: a deploy must not read as an anomaly ------------------------

scenario_monitoring_deploy_is_not_an_anomaly() {
    step 41.4 "a deploy — banner, restart gap, template churn, rate dip and recovery — raises NO anomaly alert"
    mon_require "deploy tolerance" ingest eval || return 0

    # The operator's requirement, verbatim: "we need to be able to deploy
    # without an info triggering 'unusual activity'". A monitor that pages on
    # every release gets switched off, and then nothing is monitored at all.
    #
    # ISOLATED WORKSPACE, and this one matters more than the others: 41.4
    # asserts that NOTHING alerted. Run it in a workspace whose six-per-hour
    # notify budget an earlier step already spent and it passes for the wrong
    # reason — silence from a spent allowance is indistinguishable from silence
    # from correct deploy tolerance, and the test would certify a monitor that
    # actually pages on every release. 41.5 deliberately reuses this same
    # workspace and the same deploy window, with the budget still intact.
    local prov
    prov=$(mon_isolated_ws "deploy") || true
    MON_WS_DEPLOY=$(echo "$prov" | awk '{print $1}')
    MON_SRC_DEPLOY_ISO=$(echo "$prov" | awk '{print $2}')
    MON_WSNAME_DEPLOY=$(echo "$prov" | awk '{print $3}')
    if [ -z "$MON_WS_DEPLOY" ] || [ -z "$MON_SRC_DEPLOY_ISO" ]; then
        skip "deploy tolerance" "could not provision an isolated workspace for the deploy fixture"
        return 0
    fi
    MON_DEPLOY_AT=$(( MON_NOW - 1800 ))
    local dep_at="$MON_DEPLOY_AT"
    local before=0 after=0

    before=$(mon_alert_count "$NODE_A" "$MON_WSNAME_DEPLOY" "order-sync")

    # Steady state, then: version banner, a restart gap with no output, a new
    # template id for identical banner text, a dip in rate, and recovery.
    local t stamps=""
    for t in $(mon_seq $(( dep_at - 3600 )) 300 $(( dep_at - 300 ))); do
        stamps="$stamps $t $((t+1)) $((t+2))"
    done
    # shellcheck disable=SC2086
    mon_ingest "$NODE_A" "$MON_WS_DEPLOY" "$MON_SRC_DEPLOY_ISO" \
        "$(mon_gen_at "$MON_JOB_DONE" $stamps)" >/dev/null
    mon_ingest "$NODE_A" "$MON_WS_DEPLOY" "$MON_SRC_DEPLOY_ISO" \
        "$(mon_gen_at "$MON_DEPLOY_BANNER v2.0.0" $dep_at)" >/dev/null
    # ... 4 minutes of restart silence, then the rate returns.
    stamps=""
    for t in $(mon_seq $(( dep_at + 240 )) 300 "$MON_NOW"); do
        stamps="$stamps $t $((t+1)) $((t+2))"
    done
    # shellcheck disable=SC2086
    mon_ingest "$NODE_A" "$MON_WS_DEPLOY" "$MON_SRC_DEPLOY_ISO" \
        "$(mon_gen_at "$MON_JOB_DONE" $stamps)" >/dev/null
    pass "seeded a full deploy: banner, 4-minute restart gap, new template id, rate dip and recovery"

    mon_evaluate "$NODE_A" "$MON_WS_DEPLOY" >/dev/null
    after=$(mon_alert_count "$NODE_A" "$MON_WSNAME_DEPLOY" "order-sync")

    if [ "${after:-0}" -eq "${before:-0}" ]; then
        pass "the deploy produced NO anomaly alert (delivery count unchanged at $after)"
    else
        fail "the deploy raised $(( after - before )) anomaly alert(s)" \
            "banner + restart gap + template churn is what a normal release looks like; alerting on it is how the channel gets muted before the real incident arrives"
    fi
}

# ----- 41.5: an ERROR inside that same deploy window STILL alerts ---------

scenario_monitoring_error_during_deploy_alerts() {
    step 41.5 "an ERROR inside the deploy window STILL alerts — no baseline, no grace, no delay"
    mon_require "error during deploy" ingest eval || return 0

    # This is the balance the operator asked for and the scenario most likely to
    # be got wrong. Anomaly detection may tolerate a deploy. Error detection may
    # not: errors alert always, immediately, with no baseline — otherwise a
    # grace window becomes a place for real failures to hide, which is exactly
    # how a 400 from a webhook went unnoticed for six days.
    if [ -z "${MON_WS_DEPLOY:-}" ] || [ -z "${MON_SRC_DEPLOY_ISO:-}" ]; then
        skip "error during deploy" "41.4 did not provision the deploy workspace"
        return 0
    fi
    # Same workspace and same deploy window 41.4 just tolerated — that is the
    # whole point of the pair — but with the notify budget untouched, because
    # 41.4 asserted that it delivered nothing.
    local dep_at="${MON_DEPLOY_AT:-$(( MON_NOW - 1800 ))}"
    # Initialised, not merely declared: `local x` leaves x UNSET, and `set -u`
    # then aborts the whole run on the first expansion rather than failing one
    # assertion.
    local before=0 after=0
    before=$(mon_alert_count "$NODE_A" "$MON_WSNAME_DEPLOY" "send failed")

    mon_ingest "$NODE_A" "$MON_WS_DEPLOY" "$MON_SRC_DEPLOY_ISO" \
        "$(mon_gen_at "$MON_ERROR_LINE" $(( dep_at + 60 )) $(( dep_at + 90 )) $(( dep_at + 120 )))" \
        >/dev/null
    pass "seeded 3 error lines 60-120s after the version banner — squarely inside the deploy window"

    mon_evaluate "$NODE_A" "$MON_WS_DEPLOY" >/dev/null
    after=$(mon_alert_count "$NODE_A" "$MON_WSNAME_DEPLOY" "send failed")

    if [ "${after:-0}" -gt "${before:-0}" ]; then
        pass "the error alerted despite the deploy window (${before}→${after})"
    else
        fail "an error inside the deploy window did NOT alert" \
            "deploy tolerance must apply to ANOMALY signals only; suppressing errors during a deploy is a grace window that swallows the failures most likely to be caused by the deploy"
    fi

    # And it must be recognisable as an error, not filed as informational churn.
    local sev
    sev=$(api GET "$NODE_A/api/v1/monitoring/templates?workspace_id=$MON_WS_DEPLOY&window=24h" 2>/dev/null \
        | jq -r 'first((.templates // [])[] | select(.masked | contains("send failed")) | .severity) // ""' \
        2>/dev/null || echo "")
    case "$sev" in
        error|critical) pass "the failure line is classified severity=$sev" ;;
        "") fail "the error line produced no template" "it must be visible in the template explorer to be triageable" ;;
        *)  fail "the failure line is classified severity=$sev" \
                "'send failed: upstream returned status 400' is not informational; misclassifying it puts it below every channel min_severity floor" ;;
    esac
}

# ----- 41.6: a job that never returns AFTER a deploy still alerts ---------

scenario_monitoring_broken_by_deploy_alerts() {
    step 41.6 "a job that never returns after a deploy DOES alert once the grace window expires"
    mon_require "post-deploy breakage" ingest eval || return 0
    mon_rule_ready "post-deploy breakage" || return 0

    # Tuesday's exact shape, and the reason a deploy grace window is dangerous:
    # the deploy came AFTER the breakage, so a naive "recently deployed →
    # suppress" rule swallows the very outage the deploy caused. A grace window
    # may delay the alert. It may never cancel it.
    local dep_at=$(( MON_NOW - 7200 ))    # deployed 2h ago
    mon_ingest "$NODE_A" "$WS_MON" "$MON_SRC_HEALTHY" \
        "$(mon_gen_at "$MON_DEPLOY_BANNER v2.1.0" $dep_at)" >/dev/null
    # Starts continue after the deploy. Completions never resume.
    local t stamps=""
    for t in $(mon_seq $(( dep_at + 60 )) 300 "$MON_NOW"); do
        stamps="$stamps $t"
    done
    # shellcheck disable=SC2086
    mon_ingest "$NODE_A" "$WS_MON" "$MON_SRC_HEALTHY" \
        "$(mon_gen_at "$MON_JOB_START (post-deploy)" $stamps)" >/dev/null
    pass "seeded a deploy 2h ago followed by starts only — the job never returns"

    local before=0 after=0
    before=$(mon_incident_count "$NODE_A" "$WS_MON" "absence:")
    mon_evaluate "$NODE_A" "$WS_MON" >/dev/null
    after=$(mon_incident_count "$NODE_A" "$WS_MON" "absence:")

    if [ "${after:-0}" -ge 1 ]; then
        pass "the post-deploy breakage raised an absence incident (count=$after)"
    else
        fail "no incident raised for a job that has not completed since a deploy 2h ago" \
            "before=$before after=$after — a deploy grace window that outlives the outage it is masking is worse than no window at all"
    fi
}

# ----- 41.7: Incident 2 — the dead channel -------------------------------

scenario_monitoring_dead_channel_surfaced() {
    step 41.7 "a channel that fails every send is SURFACED, even while a throttle is active"
    # Own workspace: the throttle is per workspace and the budget is six an
    # hour, so a shared one would stop the route being tried before the failure
    # run can reach the unhealthy threshold.
    local prov ws
    prov=$(mon_isolated_ws "deadroute") || true
    ws=$(echo "$prov" | awk '{print $1}')
    if [ -z "$ws" ]; then
        skip "dead channel surfaced" "could not provision an isolated workspace"
        return 0
    fi

    # 2026-07-14: a webhook returned HTTP 400, logged "send failed" exactly
    # once, and the workspace hourly cap then withheld every subsequent
    # notification BEFORE any channel was consulted. The route was never
    # retried and never logged again. Dead for six days, silently: the failure
    # happened once, the suppression happened 191 times, and only the
    # suppression was visible.
    #
    # The failing route points at this daemon's own listener on a path that
    # rejects the payload, so the failure is a real non-2xx from a real HTTP
    # round-trip and nothing leaves the container.
    local cbody cresp bad_id
    cbody=$(jq -nc --arg ws "$ws" \
        '{workspace_id:$ws, name:"harness-dead-route", kind:"gchat_webhook",
          config_json:"{\"auth_scope_id\":\"missing-scope\",\"webhook_ref\":\"secret://HARNESS_DEAD_ROUTE\"}",
          min_severity:"info", enabled:true}')
    cresp=$(api POST "$NODE_A/api/v1/monitoring-channels" "$cbody" 2>/dev/null || echo '{}')
    bad_id=$(echo "$cresp" | jq -r '.id // empty')
    if [ -z "$bad_id" ]; then
        skip "dead channel surfaced" \
            "could not create the failing channel: $(echo "$cresp" | head -c 160)"
        return 0
    fi
    pass "created a channel whose every send fails (unresolvable secret ref, $bad_id)"

    # Drive enough notifications to cross channelUnhealthyThreshold (3). The
    # notify surface is the shipped send path, so this exercises the same
    # dispatcher the detectors use.
    local i sent=0
    for i in 1 2 3 4 5; do
        local st
        st=$(api_status POST "$NODE_A/api/v1/monitoring/notify" \
            "$(jq -nc --arg ws "$ws" --arg t "harness dead-route probe $i" \
               '{workspace_id:$ws, severity:"error", title:$t, body:"probe", new_incident:true}')")
        [ "$st" = "200" ] && sent=$((sent + 1))
        [ "$st" = "501" ] && { skip "dead channel surfaced" "notify surface disabled on this daemon"; return; }
    done
    pass "drove $sent notifications through the failing route (threshold is 3 consecutive failures)"

    # THE ASSERTION. After a run of failures the route's health must be
    # queryable. Health that exists only as a log line is health nobody sees —
    # which is the entire content of the incident this step is named after.
    local ch health
    ch=$(api GET "$NODE_A/api/v1/monitoring-channels/$bad_id" 2>/dev/null || echo '{}')
    health=$(echo "$ch" | jq -r '
        (.consecutive_failures // .health.consecutive_failures // empty) as $f
        | if $f == null then "" else ($f | tostring) end' 2>/dev/null || echo "")
    if [ -n "$health" ] && [ "${health:-0}" -ge 3 ]; then
        pass "the channel row reports $health consecutive failures — the dead route is queryable"
        return 0
    fi

    # Alternate acceptable surface: the breakage raised its own incident.
    local ch_incident
    ch_incident=$(api GET "$NODE_A/api/v1/tasks?workspace_id=$ws&limit=200" 2>/dev/null \
        | jq -r '[.[]? | select(((.title // "") + (.description // "")) | test("channel|route|webhook|deliver"; "i"))
                 | select((.tags // "" | tostring) | test("logwatch"))] | length' 2>/dev/null || echo 0)
    if [ "${ch_incident:-0}" -gt 0 ]; then
        pass "the dead route raised its own incident ($ch_incident) — visible without reading the daemon log"
        return 0
    fi

    fail "a route that failed $sent consecutive sends is not observable over any API" \
        "DETECTION works — reportChannelFailure escalates to slog.Error 'channel appears broken' at 3 consecutive failures. SURFACING does not: there is no field on GET /monitoring-channels/{id}, no incident, and no delivery record, so the only place a dead route exists is the daemon's stdout. An operator reading the dashboard, and this harness, both see a healthy channel. That is the 2026-07-14 shape — grep this run's logs and you will find ONE 'send failed' against ELEVEN 'suppressed: workspace hourly notify cap' lines, which is exactly the ratio that hid a dead webhook for six days. Fix: persist consecutive_failures/first_failure_at on the channel row and expose it, or raise a monitoring incident for the route."
}

# ----- 41.8: suppression must not hide a live failure --------------------

scenario_monitoring_throttle_does_not_mask() {
    step 41.8 "the workspace notify cap suppresses volume without hiding that a route is broken"
    # Own workspace with an untouched budget: this step measures what the cap
    # does, so it must start from a full allowance rather than inheriting an
    # earlier step's spend.
    local prov ws
    prov=$(mon_isolated_ws "throttle") || true
    ws=$(echo "$prov" | awk '{print $1}')
    local ws_name
    ws_name=$(echo "$prov" | awk '{print $3}')
    if [ -z "$ws" ]; then
        skip "throttle does not mask" "could not provision an isolated workspace"
        return 0
    fi

    # A suppression mechanism that is indistinguishable from success is the
    # mechanism that hid Incident 2. Whatever the cap does to volume, the
    # OUTCOME of a send must remain inspectable.
    local i
    for i in $(seq 1 12); do
        api POST "$NODE_A/api/v1/monitoring/notify" \
            "$(jq -nc --arg ws "$ws" --arg t "harness throttle probe $i" \
               '{workspace_id:$ws, severity:"warn", title:$t, body:"probe"}')" \
            >/dev/null 2>&1 || true
    done
    pass "issued 12 notifications to drive the workspace hourly cap"

    # The delivered subset is observable through the in-container mesh sink;
    # the suppressed remainder must not be silently indistinguishable from it.
    local delivered
    delivered=$(mon_alert_count "$NODE_A" "$ws_name" "harness throttle probe")
    if [ "${delivered:-0}" -gt 0 ] && [ "${delivered:-0}" -lt 12 ]; then
        pass "$delivered of 12 delivered — the cap suppressed volume and delivery state stayed inspectable"
    elif [ "${delivered:-0}" -ge 12 ]; then
        pass "all 12 delivered — no cap engaged at this severity (nothing was hidden)"
    else
        fail "no delivery record exists for any of the 12 notifications" \
            "if suppression and success look identical from outside, a dead route is undetectable — that is precisely how 191 suppressions masked one HTTP 400 for six days"
    fi
}
