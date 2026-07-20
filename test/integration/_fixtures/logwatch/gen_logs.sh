#!/usr/bin/env bash
# gen_logs.sh — realistic log-history fixture generator for the monitoring
# scenarios.
#
# Every shape below reproduces a MEASURED characteristic of the production
# signal the monitoring feature exists for. They are deliberately not "some
# periodic lines": the detector was blind to the real shape, so a fixture that
# is merely periodic would pass while the product stayed broken.
#
#   healthy_bursty       5-minute tick; completions arrive BUNCHED ~1s apart,
#                        ~3.3 per tick (~40/hour), flat 24/7 with no overnight
#                        or weekend dip. Starts are a SEPARATE, textually
#                        distinct line arriving ~2.7 per tick, so completions
#                        OUTNUMBER starts and the two never pair 1:1.
#   silent_from          healthy up to T, then completions stop dead while
#                        STARTS CONTINUE — incident 1 (the silent hang). The
#                        process stayed alive and emitted nothing terminal, so
#                        liveness and healthchecks all read green.
#   irregular            genuinely random (exponential) arrivals. MUST be
#                        refused for promotion; it is the null hypothesis the
#                        promotion thresholds are calibrated against.
#   conditional_terminal the early-return job whose only terminal line is
#                        "no invoices to send". Statistically flawless and
#                        MUST be refused: promoting it inverts the alert.
#   deploy_at            version banner + restart + template-id churn + a rate
#                        dip and recovery. An info-class deploy must not read
#                        as unusual activity; errors during it must still fire.
#   error_burst          error-severity lines, for the "errors alert always,
#                        immediately, with no baseline" half of the contract.
#
# Output is exactly what `docker logs --timestamps` emits: one line per
# arrival, fixed-width RFC3339Nano UTC at byte zero. That is not a convenience
# — it is the point. The fixtures are replayed through a real sshd by the
# loghost `docker` shim, so the lines enter the daemon through the SAME pull,
# parse, cursor, distill and day-history path as a production host.
#
# TIME. Detection spans days and the test must run in seconds, so history is
# BACKDATED rather than waited for: --now anchors the fixture and every shape
# is expressed in days-before-anchor. Backdating is safe here precisely
# because nothing downstream of the parser reads a wall clock — log_lines.ts
# is the LINE's timestamp, and log_template_days is populated by a SQL trigger
# keyed on that same value, so nine backdated days produce byte-identical
# rows to nine observed ones. See the report for what this does NOT cover.
#
# Usage:
#   gen_logs.sh healthy_bursty       --now EPOCH --from-days 9 --to-days 0 [--job order-sync]
#   gen_logs.sh silent_from          --now EPOCH --from-days 9 --to-days 0 --silent-at-days 0.5
#   gen_logs.sh irregular            --now EPOCH --from-days 9 --to-days 0 [--mean-seconds 300]
#   gen_logs.sh conditional_terminal --now EPOCH --from-days 9 --to-days 0 [--job invoice-sync]
#   gen_logs.sh deploy_at            --now EPOCH --at-days 4 [--version v1.4.2]
#   gen_logs.sh error_burst          --now EPOCH --at-days 0.01 [--count 12]
#   gen_logs.sh merge FILE...        chronological merge of any of the above
#
# All shapes write to stdout and are COMPOSABLE — "healthy for 5 days, deploy
# at day 5, silent from day 5.5" is three calls and a merge.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TIMELIB="$HERE/timelib.awk"

# Measured cadence constants. Changing one invalidates every assertion that
# quotes a rate, so they live here and nowhere else.
TICK_SECONDS=300        # 5-minute scheduler tick
START_OFFSETS="0 1 2"   # intra-burst spacing for starts, seconds
DONE_OFFSETS="4 5 6 7"  # intra-burst spacing for completions, seconds

die() { printf 'gen_logs.sh: %s\n' "$1" >&2; exit 2; }

# ---- argument plumbing ---------------------------------------------------
NOW=""; FROM_DAYS=""; TO_DAYS="0"; SILENT_AT_DAYS=""; AT_DAYS=""
JOB="order-sync"; VERSION="v1.4.2"; COUNT="12"; MEAN_SECONDS="300"; SEED="1"

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --now)            NOW="$2"; shift 2 ;;
            --from-days)      FROM_DAYS="$2"; shift 2 ;;
            --to-days)        TO_DAYS="$2"; shift 2 ;;
            --silent-at-days) SILENT_AT_DAYS="$2"; shift 2 ;;
            --at-days)        AT_DAYS="$2"; shift 2 ;;
            --job)            JOB="$2"; shift 2 ;;
            --version)        VERSION="$2"; shift 2 ;;
            --count)          COUNT="$2"; shift 2 ;;
            --mean-seconds)   MEAN_SECONDS="$2"; shift 2 ;;
            --seed)           SEED="$2"; shift 2 ;;
            *) die "unknown flag $1" ;;
        esac
    done
    [ -n "$NOW" ] || NOW="$(date -u +%s)"
}

# ---- shapes --------------------------------------------------------------

# healthy_bursty emits the measured steady state. `silent_at` (epoch seconds,
# or 0 for never) is where COMPLETIONS stop while starts continue — the whole
# of silent_from is this one parameter, because the incident is a change to an
# otherwise unchanged healthy signal and the fixture should say so.
emit_bursty() {
    local start_sec="$1" end_sec="$2" job="$3" silent_at="$4" churn_at="$5"
    awk -v start="$start_sec" -v end="$end_sec" -v job="$job" \
        -v silent_at="$silent_at" -v churn_at="$churn_at" \
        -v tick="$TICK_SECONDS" -v seed="$SEED" \
        -v starts="$START_OFFSETS" -v dones="$DONE_OFFSETS" \
        -f "$TIMELIB" -f <(cat <<'AWKPROG'
    BEGIN {
        ns = split(starts, so, " "); nd = split(dones, dof, " ")
        rng = seed * 7919 + 13
        # Align the first tick to a clean 300s boundary so the cadence is a
        # property of the schedule, not of when the fixture was generated.
        t = start - (start % tick)
        if (t < start) t += tick
        n = 0
        for (; t <= end; t += tick) {
            n++
            # Start burst: 3,3,2 rotating — 2.67/tick, ~32/hour.
            nstart = (n % 3 == 0) ? 2 : 3
            for (i = 1; i <= nstart; i++) {
                rng = lcg(rng); jit = rng % 400000000
                printf "%s %s: starting scheduled run batch=%d\n",
                       stamp(t + so[i], jit), job, 4100 + n
            }
            if (silent_at > 0 && t >= silent_at) continue
            # Completion burst: 3,3,4 rotating — 3.33/tick, ~40/hour, 960/day
            # before the deterministic short tick below trims it into the
            # measured 954..958 band.
            ndone = (n % 3 == 0) ? 4 : 3
            rng = lcg(rng)
            if (rng % 70 == 0 && ndone > 2) ndone--
            for (i = 1; i <= ndone; i++) {
                rng = lcg(rng); jit = rng % 400000000
                rng = lcg(rng); ms = 600 + (rng % 900)
                rng = lcg(rng); rec = 20 + (rng % 60)
                # A redeploy changes the wording, so the SAME job owns a new
                # masked template (and therefore a new template id) after
                # churn_at. Grouping those two ids by cadence is what lets a
                # freshly redeployed job keep its learned baseline.
                if (churn_at > 0 && t >= churn_at)
                    printf "%s %s: completed scheduled run in %dms records=%d shard=%d\n",
                           stamp(t + dof[i], jit), job, ms, rec, (n % 4)
                else
                    printf "%s %s: completed scheduled run in %dms records=%d\n",
                           stamp(t + dof[i], jit), job, ms, rec
            }
        }
    }
AWKPROG
)
}

# irregular is the null hypothesis: exponential inter-arrival gaps with the
# same MEAN as the healthy tick. It looks busy and regular-ish to a human and
# must be REFUSED, which is what proves the thresholds are doing work.
emit_irregular() {
    local start_sec="$1" end_sec="$2" job="$3" mean="$4"
    awk -v start="$start_sec" -v end="$end_sec" -v job="$job" -v mean="$mean" \
        -v seed="$SEED" -f "$TIMELIB" -f <(cat <<'AWKPROG'
    BEGIN {
        rng = seed * 104729 + 7
        t = start
        n = 0
        while (t <= end) {
            rng = lcg(rng); u = (rng % 1000000 + 1) / 1000001.0
            gap = int(-mean * log(u)) + 1
            t += gap
            if (t > end) break
            n++
            rng = lcg(rng); jit = rng % 999000000
            rng = lcg(rng)
            printf "%s %s: reconciled ledger entry seq=%d rows=%d\n",
                   stamp(t, jit), job, n, (rng % 90) + 1
        }
    }
AWKPROG
)
}

# conditional_terminal is the invoice-sync job. Its only terminal line is the
# early return, and it is textbook periodic — which is exactly why refusing it
# has to be a text decision rather than a statistical one.
emit_conditional_terminal() {
    local start_sec="$1" end_sec="$2" job="$3"
    awk -v start="$start_sec" -v end="$end_sec" -v job="$job" \
        -v tick="$TICK_SECONDS" -v seed="$SEED" -f "$TIMELIB" -f <(cat <<'AWKPROG'
    BEGIN {
        rng = seed * 15485863 + 3
        t = start - (start % tick)
        if (t < start) t += tick
        n = 0
        for (; t <= end; t += tick) {
            n++
            rng = lcg(rng); jit = rng % 400000000
            printf "%s %s: starting scheduled run batch=%d\n", stamp(t, jit), job, 900 + n
            rng = lcg(rng); jit = rng % 400000000
            # The success line ("completed scheduled run") is NEVER emitted:
            # in seven days of retention this branch has never been taken.
            printf "%s %s: no invoices to send, nothing to do\n", stamp(t + 1, jit), job
        }
    }
AWKPROG
)
}

# deploy_at emits the release signature: a version banner whose masked shape
# is stable while the version string changes, a restart gap, and a short rate
# dip either side. Template-id churn is driven separately by emit_bursty's
# churn_at so the deploy and the wording change stay one event.
emit_deploy() {
    local at_sec="$1" job="$2" version="$3"
    awk -v at="$at_sec" -v job="$job" -v version="$version" -v seed="$SEED" \
        -f "$TIMELIB" -f <(cat <<'AWKPROG'
    BEGIN {
        rng = seed * 2654435761 % 2147483648
        rng = lcg(rng)
        printf "%s %s: received shutdown signal, draining scheduled runs\n", stamp(at, 120000000), job
        printf "%s %s: shutdown complete, 0 runs in flight\n", stamp(at + 2, 480000000), job
        # ~34s restart gap: the container is genuinely absent, which is what
        # produces the rate dip an anomaly detector must tolerate.
        printf "%s %s: running version: %s\n", stamp(at + 36, 90000000), job, version
        printf "%s %s: scheduler armed, tick interval 300s\n", stamp(at + 36, 310000000), job
        printf "%s %s: connected to upstream, pool size 8\n", stamp(at + 37, 20000000), job
    }
AWKPROG
)
}

# error_burst is the always-alert half of the contract: error-severity lines
# that must fire immediately, with no baseline, including during a deploy.
emit_error_burst() {
    local at_sec="$1" job="$2" count="$3"
    awk -v at="$at_sec" -v job="$job" -v count="$count" -v seed="$SEED" \
        -f "$TIMELIB" -f <(cat <<'AWKPROG'
    BEGIN {
        rng = seed * 40503 + 11
        for (i = 0; i < count; i++) {
            rng = lcg(rng); jit = rng % 900000000
            rng = lcg(rng)
            printf "%s %s: level=error send failed: upstream returned HTTP 400 attempt=%d\n",
                   stamp(at + i * 3, jit), job, (i % 3) + 1
        }
    }
AWKPROG
)
}

# ---- days-before-anchor helpers -----------------------------------------
# Fractional days are supported (--silent-at-days 0.5) because the incidents
# being reproduced do not begin on midnight boundaries.
days_to_epoch() {
    awk -v now="$1" -v days="$2" 'BEGIN { printf "%d\n", now - (days * 86400) }'
}

# ---- dispatch ------------------------------------------------------------
cmd="${1:-}"; shift || true
case "$cmd" in
    healthy_bursty)
        parse_args "$@"
        [ -n "$FROM_DAYS" ] || die "healthy_bursty needs --from-days"
        emit_bursty "$(days_to_epoch "$NOW" "$FROM_DAYS")" \
                    "$(days_to_epoch "$NOW" "$TO_DAYS")" "$JOB" 0 0
        ;;
    silent_from)
        parse_args "$@"
        [ -n "$FROM_DAYS" ] || die "silent_from needs --from-days"
        [ -n "$SILENT_AT_DAYS" ] || die "silent_from needs --silent-at-days"
        emit_bursty "$(days_to_epoch "$NOW" "$FROM_DAYS")" \
                    "$(days_to_epoch "$NOW" "$TO_DAYS")" "$JOB" \
                    "$(days_to_epoch "$NOW" "$SILENT_AT_DAYS")" 0
        ;;
    healthy_with_churn)
        # healthy_bursty whose completion wording changes at --at-days, i.e.
        # the log-side half of a redeploy. Pairs with deploy_at.
        parse_args "$@"
        [ -n "$FROM_DAYS" ] || die "healthy_with_churn needs --from-days"
        [ -n "$AT_DAYS" ] || die "healthy_with_churn needs --at-days"
        emit_bursty "$(days_to_epoch "$NOW" "$FROM_DAYS")" \
                    "$(days_to_epoch "$NOW" "$TO_DAYS")" "$JOB" 0 \
                    "$(days_to_epoch "$NOW" "$AT_DAYS")"
        ;;
    irregular)
        parse_args "$@"
        [ -n "$FROM_DAYS" ] || die "irregular needs --from-days"
        emit_irregular "$(days_to_epoch "$NOW" "$FROM_DAYS")" \
                       "$(days_to_epoch "$NOW" "$TO_DAYS")" "$JOB" "$MEAN_SECONDS"
        ;;
    conditional_terminal)
        parse_args "$@"
        [ -n "$FROM_DAYS" ] || die "conditional_terminal needs --from-days"
        [ "$JOB" != "order-sync" ] || JOB="invoice-sync"
        emit_conditional_terminal "$(days_to_epoch "$NOW" "$FROM_DAYS")" \
                                  "$(days_to_epoch "$NOW" "$TO_DAYS")" "$JOB"
        ;;
    deploy_at)
        parse_args "$@"
        [ -n "$AT_DAYS" ] || die "deploy_at needs --at-days"
        emit_deploy "$(days_to_epoch "$NOW" "$AT_DAYS")" "$JOB" "$VERSION"
        ;;
    error_burst)
        parse_args "$@"
        [ -n "$AT_DAYS" ] || die "error_burst needs --at-days"
        emit_error_burst "$(days_to_epoch "$NOW" "$AT_DAYS")" "$JOB" "$COUNT"
        ;;
    merge)
        [ $# -gt 0 ] || die "merge needs at least one file"
        # Fixed-width stamps make a lexicographic sort chronological; -s keeps
        # equal-stamp lines in argument order, which is how a real stream
        # preserves the start-before-completion relation inside one second.
        LC_ALL=C sort -s -k1,1 "$@"
        ;;
    *)
        die "usage: gen_logs.sh {healthy_bursty|silent_from|healthy_with_churn|irregular|conditional_terminal|deploy_at|error_burst|merge} ..."
        ;;
esac
