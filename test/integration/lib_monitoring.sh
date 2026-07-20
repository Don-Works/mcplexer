#!/usr/bin/env bash
# lib_monitoring.sh — fixtures + helpers for the monitoring/logwatch scenarios.
#
# Sourced by scenario_monitoring.sh; do not invoke directly. Mirrors lib.sh /
# lib_tiers.sh: the scenario files hold the assertions, this file holds the
# plumbing and the SIGNAL FIXTURES.
#
# ---------------------------------------------------------------------------
# WHY THESE SCENARIOS EXIST
# ---------------------------------------------------------------------------
# Two production incidents, both invisible to a suite where every unit test
# passed while the detector could not fire:
#
#   2026-07-20 — a recurring order-sync job HUNG. The process stayed alive and
#   emitted ZERO log lines for 7h39m, so the process table, the unit status and
#   the container healthcheck all read green. Orders fell to zero. Monitoring
#   fired ZERO alerts, because the existing detector wakes on NEW TEMPLATES and
#   a hung job emits nothing at all. STARTS continued while COMPLETIONS stopped.
#
#   2026-07-14 — a webhook returned HTTP 400 once, logged "send failed" once,
#   and then the workspace hourly cap MASKED it. The route was dead for six days
#   in silence: the failure happened once, the suppression happened 191 times,
#   and only the suppression was visible.
#
# ---------------------------------------------------------------------------
# THE MEASURED SIGNAL SHAPE (the fixtures below reproduce it exactly)
# ---------------------------------------------------------------------------
#   * 5-minute tick, ~3.3 completions BUNCHED back-to-back inside each tick,
#     ~40 completions/hour. Flat 24/7 — no overnight dip, no weekend dip.
#     Daily totals measured at 954 / 957 / 958.
#   * START and SUCCESS lines are textually DISTINCT.
#   * Completions OUTNUMBER starts (~16 starts vs ~20 completions per 30 min).
#     They never pair 1:1, so nothing may assume a start/finish correlation.
#   * A second job has NO usable completion line: it always returns early via a
#     "no invoices to send" branch, so its only terminal line is conditional on
#     there being nothing to do. It MUST be refused for promotion.
#   * Line numbers shift between releases — fingerprint message TEXT, never
#     file:line.
#   * Deploys emit "running version: vX.Y.Z"; the version string changes while
#     the masked template is stable, and a redeploy MINTS A NEW TEMPLATE ID for
#     identical text.
#   * Raw-line retention is 7 days; template rows outlive raw lines.
#
# The consequence for the promotion gate is the whole point of scenario 1: the
# bunched shape produces a median inter-arrival of ~1s and a p95 of ~297s, so
# p95/median lands between 8 and 298 against store.BaselineMaxP95Ratio of 3.0.
# A learner that treats bunched arrivals as one cadence rejects the real signal
# as "irregular" and the detector can never fire.
#
# ---------------------------------------------------------------------------
# THE TEST-HOOK INTERFACE THESE SCENARIOS CONSUME
# ---------------------------------------------------------------------------
# Every scenario needs to seed BACKDATED history (7+ days) and drive the learner
# and the absence evaluator on demand — the learner runs on a 1h ticker after a
# 5m startup delay (internal/logwatch/baseline/learner.go) and the evaluator on
# a 2m ticker, neither of which a bounded e2e run can wait for.
#
# The interface is gated behind MCPLEXER_ALLOW_TEST_INGEST=1, set for the rig's
# nodes in docker-compose.yml and nowhere else. A client that can assert
# arbitrary historical timestamps can poison every learned baseline and
# manufacture or erase an absence at will, so this must never be on by default.
#
#   POST /api/v1/monitoring/test-ingest
#     {workspace_id, source_id, lines:[{ts, message}]}
#       -> {ingested, first_ts, last_ts, days_spanned, retention_cutoff,
#           lines_below_retention}
#     `ts` is RFC3339 and honoured VERBATIM — backdating is the entire point,
#     since none of the shapes above are observable over less than days. Max
#     5000 lines per call, which is why mon_seed_days chunks by day. Lines enter
#     at distill.Distiller.Ingest, the exact seam collect uses, so masking,
#     classification, template upsert, rate-spike evaluation and retention
#     pruning all run as they do in production.
#
#   POST /api/v1/monitoring/test-tick
#     {learn: bool, evaluate: bool}  (both default true)  -> {learned, evaluated}
#     Forces one baseline learner pass and/or one absence-evaluator tick, so a
#     bounded run does not have to wait out the 5m+1h learner ticker or the 2m
#     evaluator ticker.
#
# RETENTION TRAP, worth knowing before writing a new fixture: Ingest prunes
# below now-RetentionDays as its last act, so lines backdated past that boundary
# are written and deleted in the same call. Their log_template_days rows survive
# (pruning never touches that table) so cadence still learns from them, but an
# assertion on RETAINED RAW LINES that far back is quietly wrong. The response's
# lines_below_retention field reports exactly how many were affected. Our sources
# are created with retention_days=14 against 7-day fixtures for this reason.
#
# When the hooks are absent every scenario SKIPs with the reason, rather than
# failing — see monitoring_probe_capabilities. That skip is NOT a pass: a suite
# that is green over a detector which cannot fire is the precise failure mode
# this rig exists to prevent.

# ----- state (declared so `set -u` cannot trip a step that runs out of order)
WS_MON="${WS_MON:-}"
MON_NOW="${MON_NOW:-0}"
MON_SCOPE_ID="${MON_SCOPE_ID:-}"
MON_HOST_ID="${MON_HOST_ID:-}"
MON_CHANNEL_ID="${MON_CHANNEL_ID:-}"
MON_SRC_HEALTHY="${MON_SRC_HEALTHY:-}"
MON_SRC_IRREGULAR="${MON_SRC_IRREGULAR:-}"
MON_SRC_CONDTERM="${MON_SRC_CONDTERM:-}"
MON_SRC_DEPLOY="${MON_SRC_DEPLOY:-}"
MON_SRC_DARKPULL="${MON_SRC_DARKPULL:-}"
MON_RULE_ID="${MON_RULE_ID:-}"

# ----- fixture identifiers (neutral; public repo) -------------------------
MON_JOB_START="order-sync: starting run for batch"
MON_JOB_DONE="order-sync: completed order"
MON_COND_LINE="invoice-dispatch: no invoices to send"
MON_DEPLOY_BANNER="order-sync: running version:"
MON_ERROR_LINE="order-sync: send failed: upstream returned status 400"

# ----- capability probe ---------------------------------------------------
# Sets MON_HAVE_INGEST / MON_HAVE_LEARN / MON_HAVE_EVAL to "1" or "".
#
# STATUS CODE ALONE IS NOT A PROBE ON THIS DAEMON. The router mounts an SPA
# catch-all, so an unknown /api/v1/... path is answered by index.html with
# HTTP 200 — a probe that accepts 200 reports every route in existence as
# present, then every scenario "runs" against a web page and fails on the
# assertions instead of skipping honestly. The probe therefore requires the
# response to be JSON: HTML means no such handler.
mon_probe_hook() {
    local url="$1" path="$2" method="${3:-POST}"
    local tok body
    tok="$(token_for "$url")"
    body=$(curl -s -X "$method" "$url/api/v1/monitoring/$path" \
        -H "Authorization: Bearer $tok" \
        -H 'Content-Type: application/json' \
        --data '{}' --max-time 10 2>/dev/null || echo "")
    # An HTML body is the SPA fallback: the route does not exist.
    case "$body" in
        *"<!doctype"*|*"<!DOCTYPE"*|"") echo ""; return ;;
    esac
    # A JSON body — even an error one — proves a real handler answered. The
    # exceptions are the two "route exists but the feature is off" answers:
    # test-ingest/test-tick 404 when MCPLEXER_ALLOW_TEST_INGEST is unset, and
    # several read surfaces 501 when the store lacks support. Treat both as
    # absent so scenarios skip rather than assert against a dead surface.
    if echo "$body" | jq -e . >/dev/null 2>&1; then
        if echo "$body" | jq -e \
            '(.error // "") | test("not enabled|not available|not implemented|is disabled"; "i")' \
            >/dev/null 2>&1; then
            echo ""; return
        fi
        echo "1"; return
    fi
    echo ""
}

monitoring_probe_capabilities() {
    MON_HAVE_INGEST=$(mon_probe_hook "$NODE_A" "test-ingest")
    # Learning and evaluation are one route (test-tick) taking two flags, so
    # they share a probe. Kept as two capability names because a scenario cares
    # which pass it needs, and the route may yet be split.
    local tick
    tick=$(mon_probe_hook "$NODE_A" "test-tick")
    MON_HAVE_LEARN="$tick"
    MON_HAVE_EVAL="$tick"
    # Baselines are a shipped read surface; the same JSON check applies.
    MON_HAVE_BASELINES=$(mon_probe_hook "$NODE_A" "baselines?workspace_id=probe" GET)
    export MON_HAVE_INGEST MON_HAVE_LEARN MON_HAVE_EVAL MON_HAVE_BASELINES
}

# mon_require <label> <cap...> — skip helper. Returns 1 (and records a SKIP)
# when any named capability is missing, so a scenario body can `|| return`.
mon_require() {
    local label="$1"; shift
    # Setup is a hard prerequisite for every step: without the workspace and
    # sources there is nothing to assert against, and reporting the missing
    # setup once beats five confusing failures downstream.
    if [ -z "${WS_MON:-}" ] || [ -z "${MON_SRC_HEALTHY:-}" ]; then
        skip "$label" "monitoring rig not provisioned — step 40.1 failed or did not run"
        return 1
    fi
    local missing=""
    local cap
    for cap in "$@"; do
        case "$cap" in
            ingest)    [ -z "${MON_HAVE_INGEST:-}" ] && missing="$missing test-ingest" ;;
            learn)     [ -z "${MON_HAVE_LEARN:-}" ] && missing="$missing test-learn" ;;
            eval)      [ -z "${MON_HAVE_EVAL:-}" ] && missing="$missing test-evaluate" ;;
            baselines) [ -z "${MON_HAVE_BASELINES:-}" ] && missing="$missing GET /monitoring/baselines" ;;
        esac
    done
    if [ -n "$missing" ]; then
        skip "$label" "missing daemon test hook(s):$missing — see lib_monitoring.sh header for the contract"
        return 1
    fi
    return 0
}

# ----- fixture generation -------------------------------------------------
# mon_civil <epoch_days> — RFC3339 date part. Hand-rolled (Hinnant
# civil_from_days) so the generator needs neither gawk's strftime nor perl and
# produces byte-identical output on macOS and Linux.
#
# mon_gen_bursty <start_epoch> <hours> <burst> <tick_s> <text>
#   The measured shape: `burst` lines back-to-back at 1s spacing at the top of
#   every `tick_s` window, for `hours` hours. burst=3 with a 4th line on every
#   third tick averages the measured 3.3/tick.
mon_gen_bursty() {
    awk -v start="$1" -v hours="$2" -v burst="$3" -v tick="$4" -v text="$5" '
    function civil(z,   era, doe, yoe, y, doy, mp, d, m) {
        z += 719468
        era = int((z >= 0 ? z : z - 146096) / 146097)
        doe = z - era * 146097
        yoe = int((doe - int(doe/1460) + int(doe/36524) - int(doe/146096)) / 365)
        y = yoe + era * 400
        doy = doe - (365*yoe + int(yoe/4) - int(yoe/100))
        mp = int((5*doy + 2)/153)
        d = doy - int((153*mp+2)/5) + 1
        m = mp + (mp < 10 ? 3 : -9)
        if (m <= 2) y += 1
        return sprintf("%04d-%02d-%02d", y, m, d)
    }
    function rfc(t,   days, secs) {
        days = int(t / 86400); secs = t - days * 86400
        return sprintf("%sT%02d:%02d:%02dZ", civil(days),
                       int(secs/3600), int((secs%3600)/60), secs%60)
    }
    BEGIN {
        printf "["
        n = 0; ticks = int(hours * 3600 / tick)
        for (i = 0; i < ticks; i++) {
            b = burst + ((i % 3 == 0) ? 1 : 0)   # averages 3.3 per tick
            for (j = 0; j < b; j++) {
                t = start + i * tick + j          # BUNCHED: 1s apart
                if (n++) printf ","
                printf "{\"ts\":\"%s\",\"message\":\"%s %d\"}", rfc(t), text, n
            }
        }
        printf "]"
    }'
}

# mon_gen_poisson <start_epoch> <hours> <mean_gap_s> <seed> <text>
#   Random (exponential) inter-arrivals — the null hypothesis the promotion gate
#   must reject. Seeded, so the run is reproducible.
mon_gen_poisson() {
    awk -v start="$1" -v hours="$2" -v mean="$3" -v seed="$4" -v text="$5" '
    function civil(z,   era, doe, yoe, y, doy, mp, d, m) {
        z += 719468
        era = int((z >= 0 ? z : z - 146096) / 146097)
        doe = z - era * 146097
        yoe = int((doe - int(doe/1460) + int(doe/36524) - int(doe/146096)) / 365)
        y = yoe + era * 400
        doy = doe - (365*yoe + int(yoe/4) - int(yoe/100))
        mp = int((5*doy + 2)/153)
        d = doy - int((153*mp+2)/5) + 1
        m = mp + (mp < 10 ? 3 : -9)
        if (m <= 2) y += 1
        return sprintf("%04d-%02d-%02d", y, m, d)
    }
    function rfc(t,   days, secs) {
        days = int(t / 86400); secs = t - days * 86400
        return sprintf("%sT%02d:%02d:%02dZ", civil(days),
                       int(secs/3600), int((secs%3600)/60), secs%60)
    }
    BEGIN {
        srand(seed); printf "["
        n = 0; t = start; endt = start + hours * 3600
        while (t < endt) {
            u = rand(); if (u <= 0) u = 0.000001
            t += int(-mean * log(u)) + 1
            if (t >= endt) break
            if (n++) printf ","
            printf "{\"ts\":\"%s\",\"message\":\"%s %d\"}", rfc(t), text, n
        }
        printf "]"
    }'
}

# mon_seq <from> <step> <to> — an integer range, printed exactly.
#
# NOT `seq`. BSD seq (macOS, the primary dev host) formats with %g, so any value
# past ~7 significant figures collapses: `seq 1784571626 600 1784577026` prints
# "1.78457e+09" three times. Every epoch timestamp in this file is ten digits,
# so seq silently turns a 90-minute fixture into three identical unparseable
# stamps — the ingest rejects them, the scenario reports "no incident was
# raised", and the harness blames the daemon for a defect in the harness. That
# is the worst failure a test rig can have, so this loop exists.
mon_seq() {
    local i="$1" step="$2" to="$3"
    while [ "$i" -le "$to" ]; do
        printf '%d ' "$i"
        i=$(( i + step ))
    done
}

# mon_gen_at <text> <epoch...> — a handful of lines at explicit instants. Used
# for deploy banners, error lines, and the tail-only start traffic.
mon_gen_at() {
    local text="$1"; shift
    awk -v text="$text" -v stamps="$*" '
    function civil(z,   era, doe, yoe, y, doy, mp, d, m) {
        z += 719468
        era = int((z >= 0 ? z : z - 146096) / 146097)
        doe = z - era * 146097
        yoe = int((doe - int(doe/1460) + int(doe/36524) - int(doe/146096)) / 365)
        y = yoe + era * 400
        doy = doe - (365*yoe + int(yoe/4) - int(yoe/100))
        mp = int((5*doy + 2)/153)
        d = doy - int((153*mp+2)/5) + 1
        m = mp + (mp < 10 ? 3 : -9)
        if (m <= 2) y += 1
        return sprintf("%04d-%02d-%02d", y, m, d)
    }
    function rfc(t,   days, secs) {
        days = int(t / 86400); secs = t - days * 86400
        return sprintf("%sT%02d:%02d:%02dZ", civil(days),
                       int(secs/3600), int((secs%3600)/60), secs%60)
    }
    BEGIN {
        n = split(stamps, a, " "); printf "["
        for (i = 1; i <= n; i++) {
            if (i > 1) printf ","
            printf "{\"ts\":\"%s\",\"message\":\"%s\"}", rfc(a[i]), text
        }
        printf "]"
    }'
}

# ----- daemon drivers -----------------------------------------------------
# mon_ingest <node> <ws> <source_id> <lines_json> — POSTs one batch. Chunked by
# the caller: a 7-day bursty fixture is ~6.7k lines and is sent a day at a time
# so no single request carries a multi-megabyte body.

# mon_json_post — POST and guarantee a JSON scalar back. A non-JSON body (the
# SPA fallback, a proxy error page) becomes {} so callers can pipe into jq
# without spraying parse errors over the run output.
mon_json_post() {
    local url="$1" body="$2"
    local out
    out=$(api POST "$url" "$body" 2>/dev/null || echo "")
    if echo "$out" | jq -e . >/dev/null 2>&1; then
        echo "$out"
    else
        echo '{}'
    fi
}

mon_ingest() {
    local url="$1" ws="$2" src="$3" lines="$4"
    local body
    body=$(jq -nc --arg ws "$ws" --arg src "$src" --argjson lines "$lines" \
        '{workspace_id:$ws, source_id:$src, lines:$lines}')
    mon_json_post "$url/api/v1/monitoring/test-ingest" "$body"
}

# mon_learn / mon_evaluate — one forced pass each. Both hit test-tick with the
# other half switched off, so a scenario that only wants to re-evaluate cannot
# accidentally re-learn a baseline out from under its own assertion.
mon_learn() {
    mon_json_post "$1/api/v1/monitoring/test-tick" '{"learn":true,"evaluate":false}'
}

mon_evaluate() {
    mon_json_post "$1/api/v1/monitoring/test-tick" '{"learn":false,"evaluate":true}'
}

# mon_baseline_for <node> <ws> <substring> — the learner's verdict for the
# template whose masked text contains `substring`. Emits the baseline object.
mon_baseline_for() {
    api GET "$1/api/v1/monitoring/baselines?workspace_id=$2&limit=500" 2>/dev/null \
        | jq -c --arg m "$3" 'first((.baselines // [])[] | select(.masked | contains($m)))' \
        2>/dev/null || echo "null"
}

# mon_expected_signal <node> <ws> <substring> — the learned rule whose matcher
# contains `substring`, with the evaluator's latest verdict attached.
#
# Scoped by SOURCE, not just by matcher text: several sources in this rig carry
# the same completion line on purpose (the healthy signal and the darkpull
# signal are deliberately identical, so that only their collection health
# differs). Matching on text alone returns whichever rule sorts first and
# silently answers a question about the wrong source.
mon_expected_signal() {
    local node="$1" ws="$2" substring="$3" source_id="${4:-}"
    api GET "$node/api/v1/monitoring/expected-signals?workspace_id=$ws" 2>/dev/null \
        | jq -c --arg m "$substring" --arg src "$source_id" \
            'first((.expected_signals // [])[]
               | select((.match_substring // "") | contains($m))
               | select($src == "" or .source_id == $src))' \
        2>/dev/null || echo "null"
}

# mon_rule_warming_up <node> <ws> <substring> — echoes the warming-up detail
# when the rule is younger than one window, else "".
#
# WHY THIS GUARD EXISTS. EvaluateExpectedSignal refuses to raise until a full
# window has elapsed since the rule's created_at, and the learner stamps
# created_at = now. A rule promoted during this run is therefore structurally
# incapable of raising for its whole window (30m for the measured signal), no
# matter how absent the signal is. That guard is CORRECT — a fresh install must
# not alert on the history it has just finished reading — but it means absence
# detection cannot be observed end-to-end in a bounded run without an injected
# clock. Scenarios distinguish that from a real detector failure rather than
# reporting a defect the daemon does not have.
#
# THE CONTRACT EXTENSION THAT CLOSES IT: accept an optional RFC3339 `now` on
# POST /api/v1/monitoring/test-tick and thread it into
# ExpectedSignalInput.Now. The evaluator is already a pure function of an
# injected clock, so this is a parameter, not a redesign.
# Echoes "no rule was learned for this source" when the rule is absent
# entirely, so a caller can tell "the learner declined" apart from "the rule
# exists and is ready" — two states that both used to look like the empty
# string and made a scenario assert against a rule that was never created.
mon_rule_warming_up() {
    local rule
    rule=$(mon_expected_signal "$1" "$2" "$3" "${4:-}")
    if [ -z "$rule" ] || [ "$rule" = "null" ]; then
        echo "no rule was learned for this source"
        return
    fi
    # Computed from created_at + window against now, NOT from last_outcome.
    # last_outcome is empty until the rule has been evaluated once, so a guard
    # that reads it lets the FIRST step through (which then fails for a reason
    # it cannot explain) and correctly skips every step after it — two
    # scenarios disagreeing about the same rule. The arithmetic is always true.
    echo "$rule" | jq -r --argjson now "$(date -u +%s)" '
        ((.created_at // "") | if . == "" then 0 else (fromdateiso8601? // 0) end) as $born
        | (.window_seconds // 0) as $w
        | if $born > 0 and ($now - $born) < $w
          then "rule created \(.created_at); one full \(.window // ($w|tostring)) window has not yet elapsed"
          else "" end' 2>/dev/null || echo ""
}

# ----- notification-budget isolation --------------------------------------
# mon_isolated_ws <tag> — provisions a workspace with its own auth scope, host,
# ONE log source and its own mesh sink, and echoes "<ws_id> <source_id>".
#
# WHY EVERY NOTIFY-SENSITIVE STEP NEEDS ITS OWN: the dispatcher's throttle is
# per workspace and maxNotifiesPerHour is SIX. Steps sharing a workspace
# therefore compete for one budget, and a step that runs late finds it already
# spent — reporting "the error did not alert" when the truth is "an earlier
# step in this same file used up the allowance". That is a harness defect that
# reads exactly like a product defect, which makes it the most expensive kind.
mon_isolated_ws() {
    local tag="$1"
    local stamp="${MON_NOW}-$RANDOM"
    local ws="ws-mon-${tag}-${stamp}"
    api POST "$NODE_A/api/v1/workspaces" \
        "$(jq -nc --arg id "$ws" --arg nm "mon-${tag}-${stamp}" \
            '{id:$id, name:$nm, root_path:"/tmp/monitoring", default_policy:"allow"}')" \
        >/dev/null 2>&1 || { echo ""; return 1; }
    local scope="scope-mon-${tag}-${stamp}"
    api POST "$NODE_A/api/v1/auth-scopes" \
        "$(jq -nc --arg id "$scope" --arg nm "mon-ssh-${tag}-${stamp}" \
            '{id:$id, name:$nm, type:"ssh_key"}')" >/dev/null 2>&1 || true
    local host
    host=$(api POST "$NODE_A/api/v1/remote-hosts" \
        "$(jq -nc --arg ws "$ws" --arg scope "$scope" \
            '{workspace_id:$ws, name:"ops-example", ssh_user:"logwatch",
              ssh_host:"203.0.113.10", ssh_port:22, auth_scope_id:$scope, enabled:true}')" \
        2>/dev/null | jq -r '.id // empty')
    [ -z "$host" ] && { echo ""; return 1; }
    local src
    src=$(api POST "$NODE_A/api/v1/log-sources" \
        "$(jq -nc --arg ws "$ws" --arg host "$host" --arg nm "job-$tag" \
            '{workspace_id:$ws, remote_host_id:$host, name:$nm, kind:"docker",
              selector:$nm, schedule_spec:"*/5 * * * *", max_pull_bytes:8000000,
              retention_mb:64, retention_days:14, enabled:true}')" \
        2>/dev/null | jq -r '.id // empty')
    [ -z "$src" ] && { echo ""; return 1; }
    api POST "$NODE_A/api/v1/monitoring-channels" \
        "$(jq -nc --arg ws "$ws" --arg nm "sink-$tag" \
            '{workspace_id:$ws, name:$nm, kind:"mesh", config_json:"{}",
              min_severity:"info", enabled:true}')" >/dev/null 2>&1 || true
    # Third field is the workspace NAME: the rendered mesh alert is prefixed
    # "[<workspace name> · via <host>]", so the name is what lets a scenario
    # count only its OWN deliveries.
    echo "$ws $src mon-${tag}-${stamp}"
}

# ----- incident observation ----------------------------------------------
# Absence/collection incidents surface as the canonical TASK the evaluator
# elects (cmd/mcplexer/monitoring_baseline_wire.go:baselineTaskEnsurer): tagged
# logwatch+incident+expected-signal, carrying meta.logwatch_class of
# "absence:<rule>" or "absence-collection:<rule>". Asserting on the task row is
# deliberate — it is durable, workspace-scoped observable state, unlike the
# daemon's own stdout.
mon_incident_tasks() {
    local url="$1" ws="$2" prefix="$3"
    api GET "$url/api/v1/tasks?workspace_id=$ws&limit=200" 2>/dev/null \
        | jq -c --arg p "\"logwatch_class\":\"$prefix" \
            "[.[]? | select((.meta // \"\") | contains(\$p))]" 2>/dev/null \
        || echo "[]"
}

mon_incident_count() {
    mon_incident_tasks "$1" "$2" "$3" | jq -r 'length' 2>/dev/null || echo "0"
}

# mon_alerts_for <node> <ws_name> <needle> — alerts delivered through the rig's
# self-contained sink. The monitoring channel is kind=mesh, so a dispatched
# alert lands as a mesh message on THIS node and nothing leaves the container:
# no webhook, no gchat, no paired peer.
#
# Filtering on the WORKSPACE NAME as well as the needle is load-bearing. The
# rendered alert opens "[<workspace name> · via <host>]", and the rig generates
# alerts from several workspaces at once; matching on the needle alone counts
# other scenarios' deliveries and turns "did MY alert arrive?" into "did ANY
# alert arrive?". Pass "" as ws_name to count across all workspaces knowingly.
#
# KNOWN WEAKNESS, and a gap in the product rather than in this helper:
# /api/v1/mesh/status is a LIVE FEED, hardcoded to the newest 50 messages
# within a 2-hour window (internal/api/mesh_handler.go). It is the closest
# thing the daemon has to a delivery ledger, and it is not one — a burst of
# alerts pushes earlier deliveries out of the window, so "was anyone actually
# told?" has no durable answer. See step 41.8.
mon_alerts_for() {
    local node="$1" ws_name="$2" needle="$3"
    api GET "$node/api/v1/mesh/status" 2>/dev/null \
        | jq -c --arg n "$needle" --arg ws "$ws_name" \
            '[.messages[]?
               | select((.content // "") | contains($n))
               | select($ws == "" or ((.content // "") | contains($ws)))]' \
        2>/dev/null || echo "[]"
}

mon_alert_count() {
    mon_alerts_for "$1" "$2" "$3" | jq -r 'length' 2>/dev/null || echo "0"
}
