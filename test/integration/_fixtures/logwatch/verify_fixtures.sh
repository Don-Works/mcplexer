#!/usr/bin/env bash
# verify_fixtures.sh — self-test for the log-history fixtures.
#
# The monitoring scenarios assert that the daemon reaches a particular verdict
# on each shape. That assertion is only worth something if the shape is
# actually the shape it claims to be, so this script measures the generated
# output against the MEASURED production characteristics before any container
# is started. It needs no daemon, no docker and no network — run it whenever
# gen_logs.sh changes.
#
#   ./verify_fixtures.sh          # uses a fixed anchor, fully deterministic
#
# Exits non-zero on the first violated property.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GEN="$HERE/gen_logs.sh"
TIMELIB="$HERE/timelib.awk"
# A fixed anchor keeps every measurement below reproducible. It is a plain
# epoch second with no significance beyond being a Wednesday, so the "flat
# across weekends" property is exercised rather than assumed.
ANCHOR="${FIXTURE_ANCHOR:-1784000000}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

V_PASS=0; V_FAIL=0
check() {
    local label="$1" got="$2" lo="$3" hi="$4"
    if awk -v g="$got" -v lo="$lo" -v hi="$hi" 'BEGIN{exit !(g >= lo && g <= hi)}'; then
        V_PASS=$((V_PASS + 1)); printf '  ok   %-52s %s (want %s..%s)\n' "$label" "$got" "$lo" "$hi"
    else
        V_FAIL=$((V_FAIL + 1)); printf '  FAIL %-52s %s (want %s..%s)\n' "$label" "$got" "$lo" "$hi" >&2
    fi
}

# tawk runs a small awk program with timelib.awk's functions in scope. The
# two -f flags are required: awk treats a positional program string as a FILE
# operand once -f has been used, so the library and the program must BOTH be
# passed as program files.
tawk() {
    local prog="$1"; shift
    awk -f "$TIMELIB" -f <(printf '%s\n' "$prog") "$@"
}

# gaps prints the inter-arrival gaps in seconds for the lines matching $2.
gaps() {
    grep -F "$2" "$1" | tawk '{ s = parse_stamp_sec($1); if (NR > 1) print s - p; p = s }'
}

printf '\n--- generating shapes (anchor=%s) ---\n' "$ANCHOR"
"$GEN" healthy_bursty       --now "$ANCHOR" --from-days 9 --to-days 0 > "$WORK/healthy.log"
"$GEN" silent_from          --now "$ANCHOR" --from-days 9 --to-days 0 --silent-at-days 0.5 > "$WORK/silent.log"
"$GEN" irregular            --now "$ANCHOR" --from-days 9 --to-days 0 > "$WORK/irregular.log"
"$GEN" conditional_terminal --now "$ANCHOR" --from-days 9 --to-days 0 > "$WORK/conditional.log"
"$GEN" deploy_at            --now "$ANCHOR" --at-days 4 > "$WORK/deploy.log"
"$GEN" error_burst          --now "$ANCHOR" --at-days 0.01 --count 12 > "$WORK/errors.log"

printf '\n--- healthy_bursty: the measured steady state ---\n'
# Whole days only: the first and last calendar day of the window are partial.
totals=$(grep -F 'completed scheduled run' "$WORK/healthy.log" | cut -c1-10 | uniq -c | sed -n '2,$p' | sed '$d' | awk '{print $1}')
lo=$(echo "$totals" | sort -n | head -1); hi=$(echo "$totals" | sort -n | tail -1)
check "daily completions, lowest whole day"  "$lo" 950 960
check "daily completions, highest whole day" "$hi" 950 960

starts=$(grep -cF 'starting scheduled run' "$WORK/healthy.log")
dones=$(grep -cF 'completed scheduled run' "$WORK/healthy.log")
check "completions OUTNUMBER starts (ratio)" \
    "$(awk -v d="$dones" -v s="$starts" 'BEGIN{printf "%.3f", d/s}')" 1.20 1.30
# Never a 1:1 pairing — the detector must not assume one completion per start.
check "starts per 30min"      "$(awk -v s="$starts" 'BEGIN{printf "%.1f", s/(9*48)}')" 15.0 17.0
check "completions per 30min" "$(awk -v d="$dones" 'BEGIN{printf "%.1f", d/(9*48)}')" 19.0 21.0

# Flat 24/7: every whole hour of the span carries at least one arrival, so
# hour occupancy is 1.0 and the "sleeps overnight" rejection cannot trigger.
hours=$(cut -c1-13 "$WORK/healthy.log" | sort -u | wc -l | tr -d ' ')
check "hours occupied over a 9-day span" "$hours" 216 218
days=$(cut -c1-10 "$WORK/healthy.log" | sort -u | wc -l | tr -d ' ')
check "distinct calendar days (day-history floor is 7)" "$days" 10 10

printf '\n--- healthy_bursty: BUNCHED arrivals, not one-per-tick ---\n'
gaps "$WORK/healthy.log" 'completed scheduled run' | sort -n > "$WORK/dgaps"
med=$(awk '{a[NR]=$1} END{print a[int(NR/2)]}' "$WORK/dgaps")
p95=$(awk '{a[NR]=$1} END{print a[int(NR*0.95)]}' "$WORK/dgaps")
check "median completion gap is intra-burst (~1s)" "$med" 1 1
check "p95 completion gap is the tick (~298s)"     "$p95" 290 300
# This ratio is the whole point: on RAW gaps the most reliably periodic signal
# on the box scores p95/median ~298 against a promotion cap of 3. A detector
# that measures raw gaps rejects it as noise, which is what recall=0 looked
# like. The fixture must therefore exhibit that ratio, not avoid it.
check "raw p95/median (must be far above the cap of 3)" \
    "$(awk -v p="$p95" -v m="$med" 'BEGIN{printf "%.0f", p/m}')" 200 320
# ...and it must be BIMODAL, with an empty band wide enough for the 8x
# separation test to find, or burst-splitting is guesswork.
band=$(awk '{a[NR]=$1} END{r=0; for(i=2;i<=NR;i++) if(a[i-1]>0 && a[i]/a[i-1]>r) r=a[i]/a[i-1]; printf "%.0f", r}' "$WORK/dgaps")
check "largest step in the sorted gap sample (>= 8x)" "$band" 8 400

printf '\n--- silent_from: the silent hang (incident 1) ---\n'
cut=$(awk -v a="$ANCHOR" 'BEGIN{printf "%d", a - 0.5*86400}')
tail_starts=$(tawk '/starting scheduled run/ && parse_stamp_sec($1) >= cut' \
    -v cut="$cut" "$WORK/silent.log" | wc -l | tr -d ' ')
tail_dones=$(tawk '/completed scheduled run/ && parse_stamp_sec($1) >= cut' \
    -v cut="$cut" "$WORK/silent.log" | wc -l | tr -d ' ')
# Starts CONTINUE — the process is alive, the container is healthy, every
# liveness probe reads green. Only the completions stopped.
check "starts continue after the hang"      "$tail_starts" 300 500
check "completions stop dead after the hang" "$tail_dones" 0 0

printf '\n--- irregular: the null hypothesis (must be refusable) ---\n'
gaps "$WORK/irregular.log" 'reconciled ledger entry' | sort -n > "$WORK/igaps"
imed=$(awk '{a[NR]=$1} END{print a[int(NR/2)]}' "$WORK/igaps")
ip95=$(awk '{a[NR]=$1} END{print a[int(NR*0.95)]}' "$WORK/igaps")
# For an exponential process p95/median = ln20/ln2 = 4.32, comfortably above
# the promotion cap of 3.0 and with NO bimodal band for burst-splitting to
# rescue it — so it is rejected on the statistics alone.
check "irregular p95/median (exponential ~4.3, cap is 3.0)" \
    "$(awk -v p="$ip95" -v m="$imed" 'BEGIN{printf "%.2f", p/m}')" 3.2 6.0
iband=$(awk '{a[NR]=$1} END{r=0; n=0; for(i=61;i<=NR-60;i++) if(a[i-1]>0 && a[i]/a[i-1]>r) r=a[i]/a[i-1]; printf "%.2f", r}' "$WORK/igaps")
check "irregular has NO bimodal band (< 8x separation)" "$iband" 1.0 7.99

printf '\n--- conditional_terminal: perfect stats, wrong line ---\n'
ct_ok=$(grep -cF 'no invoices to send, nothing to do' "$WORK/conditional.log")
ct_success=$(grep -cF 'completed scheduled run' "$WORK/conditional.log" || true)
check "early-return terminal is present"      "$ct_ok" 2000 3000
# The success line has NEVER been observed. That absence is the entire hazard:
# the statistics of a reliably-idle job are indistinguishable from a working
# one, so only the TEXT carries the distinction.
check "success line never appears"            "$ct_success" 0 0

printf '\n--- deploy_at + error_burst ---\n'
check "deploy emits a version banner" \
    "$(grep -cF 'running version:' "$WORK/deploy.log")" 1 1
check "deploy restart gap in seconds" \
    "$(gaps "$WORK/deploy.log" 'order-sync' | sort -n | tail -1)" 30 40
check "error burst line count" "$(grep -cF 'send failed' "$WORK/errors.log")" 12 12
check "error burst carries an explicit error level" \
    "$(grep -cF 'level=error' "$WORK/errors.log")" 12 12

printf '\n--- composability: healthy 9d + deploy at d4 + silent from d0.5 ---\n'
"$GEN" healthy_with_churn --now "$ANCHOR" --from-days 9 --to-days 0 --at-days 4 > "$WORK/churn.log"
"$GEN" merge "$WORK/churn.log" "$WORK/deploy.log" "$WORK/errors.log" > "$WORK/combined.log"
check "merged line count" \
    "$(wc -l < "$WORK/combined.log" | tr -d ' ')" 15000 16500
# A merge that is not chronological would corrupt the collector's cursor.
unsorted=$(awk '{ if (NR > 1 && $1 < p) bad++ ; p = $1 } END { print bad + 0 }' "$WORK/combined.log")
check "merged output is chronological" "$unsorted" 0 0
# The redeploy mints a SECOND template id for the same job: same cadence,
# different wording. Grouping them is what preserves a learned baseline.
check "pre-churn completion wording present" \
    "$(grep -c 'completed scheduled run in [0-9]*ms records=[0-9]*$' "$WORK/churn.log")" 1 100000
check "post-churn completion wording present" \
    "$(grep -c 'shard=' "$WORK/churn.log")" 1 100000

printf '\n=== verify_fixtures: %d passed, %d failed ===\n' "$V_PASS" "$V_FAIL"
[ "$V_FAIL" -eq 0 ]
