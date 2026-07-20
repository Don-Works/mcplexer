#!/usr/bin/env bash
# citation-bench.sh — reliability harness for local-model delegation.
#
# Dispatches ground-truth-checkable questions at a delegated worker and scores
# three things independently:
#
#   success   — did mcplexer mark the run successful?
#   semantic  — is the answer actually right?
#   citation  — does the `file.go:NN` it cites point at the real line?
#
# Those three come apart in practice. On qwen3.6-35b-a3b (pi_cli/qwen-local) a
# measured run scored 4/4 success, 4/4 semantic, 1/4 citation — the model knew
# the answers and cited the wrong lines, and every run was reported clean. A
# citation the parent trusts without re-reading is the whole value of
# delegation, so citation accuracy is tracked as a first-class metric here.
#
# Ground truth is recomputed from the working tree on every run (grep -n), never
# hardcoded, so the benchmark cannot rot as the repo moves.
#
# Usage:
#   scripts/citation-bench.sh truth                  # print computed ground truth
#   scripts/citation-bench.sh run [-m MODEL] [-n N]  # dispatch + score
#   scripts/citation-bench.sh score results.json     # re-score a saved run
#
# `run` drives the local daemon over MCP stdio (`mcplexer connect`) and needs
# NO API key. `truth` and `score` are offline and need no daemon at all.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

MODEL_ID="${MODEL_ID:-qwen-local}"
MODEL_PROVIDER="${MODEL_PROVIDER:-pi_cli}"
RUNS=1
WITH_GATE=0
GATE_FILE="$REPO_ROOT/scripts/citation-gate.js"
# shellcheck source=scripts/citation-bench-questions.sh
. "$REPO_ROOT/scripts/citation-bench-questions.sh"
# shellcheck source=scripts/citation-bench-mcp.sh
. "$REPO_ROOT/scripts/citation-bench-mcp.sh"



cmd_truth() {
    printf '%-5s %-12s %-28s %s\n' ID KIND EXPECTED CITE
    questions | while IFS=$'\t' read -r id kind expected cf cl _prompt; do
        printf '%-5s %-12s %-28s %s\n' "$id" "$kind" "$expected" "${cf}:${cl:--}"
    done
}

# ---------------------------------------------------------------------------
# scoring
# ---------------------------------------------------------------------------
# semantic_ok <expected> <kind> <answer>
semantic_ok() {
    local expected="$1" kind="$2" answer="$3"
    local norm; norm=$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')
    local want; want=$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')
    case "$kind" in
        count|line_number) grep -qE "(^|[^0-9])${want}([^0-9]|$)" <<<"$norm" && return 0 ;;
        presence)
            # A model rarely answers a bare "yes"/"no" — it says "appears in two
            # places" or "not found". Score the claim, not the wording.
            if [ "$want" = "yes" ]; then
                grep -qE '\byes\b|appears|is present|was found|found [0-9]+ (match|hit)' <<<"$norm" &&
                    ! grep -qE 'does not appear|not found|no match' <<<"$norm" && return 0
            else
                grep -qE '\bno\b|does not appear|not found|no match|absent' <<<"$norm" && return 0
            fi
            ;;
        *)                 grep -qF "$want" <<<"$norm" && return 0 ;;
    esac
    return 1
}

# citation_ok <cite_file> <cite_line> <answer>
# Returns 0 = correct, 1 = wrong, 2 = no citation offered.
citation_ok() {
    local cf="$1" cl="$2" answer="$3"
    if [ -z "$cl" ] || [ "$cl" = "-" ]; then return 2; fi
    local base esc cited
    base=$(basename "$cf"); esc=${base//./\\.}
    # Accept both "file.go:NN" and "line NN of file.go" — the same two shapes
    # scripts/citation-gate.js parses, so bench and gate agree on what a
    # citation is.
    cited=$(grep -oE "${esc}:[0-9]+" <<<"$answer" | head -1 | cut -d: -f2)
    if [ -z "$cited" ]; then
        # Models wrap citations in markdown: **line 108** in `path/file.go`.
        # Without tolerating the wrappers this reads as "no citation offered"
        # and a wrong line scores NONE instead of WRONG.
        local w='[`'"'"'"*([]*'
        cited=$(grep -oiE "lines?[[:space:]]+${w}[0-9]+${w}[[:space:]]+(of|in|from|at)[[:space:]]+${w}[A-Za-z0-9_./-]*${esc}" <<<"$answer" |
            head -1 | grep -oE '[0-9]+' | head -1)
    fi
    [ -z "$cited" ] && return 2
    [ "$cited" = "$cl" ] && return 0
    return 1
}

cmd_score() {
    local file="$1"
    command -v jq >/dev/null || { echo "jq required" >&2; return 1; }
    jq -e . "$file" >/dev/null 2>&1 || { echo "FATAL: $file is not valid JSON" >&2; return 1; }
    local n_ok=0 n_sem=0 n_cit_ok=0 n_cit_bad=0 n_cit_none=0 n=0 tin=0 tout=0 tdur=0 n_missing=0
    printf '%-5s %-9s %-9s %-9s %7s %7s %6s  %s\n' \
        ID STATUS SEMANTIC CITATION IN OUT DUR EXPECTED
    # Process substitution, not a pipe: a piped `while` runs in a subshell and
    # every counter increment below would be discarded at the loop's end.
    while IFS=$'\t' read -r id kind expected cf cl _prompt; do
        local status answer inn outn dur sem cit
        status=$(jq -r --arg id "$id" '.[]|select(.id==$id)|.status // "missing"' "$file")
        answer=$(jq -r --arg id "$id" '.[]|select(.id==$id)|.output // ""' "$file")
        inn=$(jq -r --arg id "$id" '.[]|select(.id==$id)|.input_tokens // 0' "$file")
        outn=$(jq -r --arg id "$id" '.[]|select(.id==$id)|.output_tokens // 0' "$file")
        dur=$(jq -r --arg id "$id" '.[]|select(.id==$id)|.duration_s // 0' "$file")
        if semantic_ok "$expected" "$kind" "$answer"; then sem=OK; else sem=WRONG; fi
        citation_ok "$cf" "$cl" "$answer"
        case $? in
            0) cit=OK;   n_cit_ok=$((n_cit_ok + 1)) ;;
            1) cit=WRONG; n_cit_bad=$((n_cit_bad + 1)) ;;
            *) cit=NONE; n_cit_none=$((n_cit_none + 1)) ;;
        esac
        [ "$status" = "success" ] && n_ok=$((n_ok + 1))
        [ "$status" = "missing" ] && n_missing=$((n_missing + 1))
        [ "$sem" = "OK" ] && n_sem=$((n_sem + 1))
        n=$((n + 1)); tin=$((tin + inn)); tout=$((tout + outn)); tdur=$((tdur + dur))
        printf '%-5s %-9s %-9s %-9s %7s %7s %5ss  %s\n' \
            "$id" "${status:-missing}" "$sem" "$cit" "$inn" "$outn" "$dur" "$expected"
    done < <(questions)

    # Correlation failure must be an ERROR, not a table of zeros. A silent
    # "missing/WRONG/0 tokens" for every row reads as a real (catastrophic)
    # model result and is worse than no output at all.
    if [ "$n_missing" -eq "$n" ] && [ "$n" -gt 0 ]; then
        echo "" >&2
        echo "FATAL: could not correlate ANY of the $n questions to a run." >&2
        echo "  Every row is 'missing' — this is a harness/correlation failure," >&2
        echo "  NOT a model accuracy result. Do not report these numbers." >&2
        echo "  Check that $file has non-empty .status/.output per question." >&2
        return 2
    fi
    if [ "$n_missing" -gt 0 ]; then
        echo "" >&2
        echo "WARNING: $n_missing/$n questions did not correlate to a run;" >&2
        echo "  aggregate below covers only the $((n - n_missing)) that did." >&2
    fi
    local cited=$((n_cit_ok + n_cit_bad))
    printf '\n--- aggregate (n=%d) ---\n' "$n"
    printf 'reported success   : %d/%d\n' "$n_ok" "$n"
    printf 'semantically right : %d/%d\n' "$n_sem" "$n"
    printf 'citations offered  : %d  (correct %d, WRONG %d)\n' "$cited" "$n_cit_ok" "$n_cit_bad"
    printf 'no citation given  : %d\n' "$n_cit_none"
    [ "$cited" -gt 0 ] && printf 'citation error rate: %d%%\n' $((n_cit_bad * 100 / cited))
    printf 'tokens in/out      : %d / %d\n' "$tin" "$tout"
    [ "$n" -gt 0 ] && printf 'mean duration      : %ds\n' $((tdur / n))
    return 0
}

# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------
allowlist() {
    # Leading space is deliberate: coerceStringifiedArgs (args_coerce.go:50)
    # re-parses a string starting with '[' into an array, which the
    # string-typed tool_allowlist_json field then rejects.
    printf ' ["mcpx__execute_code","mcpx__search_tools","index__summary","index__symbols","index__search"]'
}

# dispatch_spec <prompt> -> one mcp_batch spec line.
# no_auto_context is set so runs are INDEPENDENT: delegations otherwise inject
# recalled memory + recent mesh, which would let an earlier question's answer
# leak into a later one and quietly invalidate the measurement.
dispatch_spec() {
    local prompt="$1" gate=""
    [ "$WITH_GATE" = "1" ] && [ -f "$GATE_FILE" ] && gate=$(cat "$GATE_FILE")
    jq -nc \
        --arg obj "$prompt" --arg mp "$MODEL_PROVIDER" --arg mi "$MODEL_ID" \
        --arg allow "$(allowlist)" --arg gate "$gate" \
        '{tool:"mcpx__delegate_worker", args:
           ({objective:$obj, model_provider:$mp, model_id:$mi,
             worker_isolation:"none", worker_mode:"review",
             tool_allowlist_json:$allow, no_auto_context:true,
             max_wall_clock_seconds:300, max_tool_calls:40}
            + (if $gate=="" then {} else {post_execute_script:$gate} end))}'
}

# settled_run <delegation_id> <list_json_file> -> latest_run object, or empty
# while still in flight. Correlates on delegation_id: the dispatch response has
# no top-level worker_id, and the run lives under .workers[].latest_run.
# Accepts both the MCP shape ({delegations:[...]}) and a bare array.
settled_run() {
    jq -c --arg did "$1" '
        [((.delegations // .)[] | select(.id==$did) | .workers[0].latest_run // empty)]
        | map(select(.status != null and .status != "running" and .status != "pending"))
        | first // empty' "$2"
}

# poll_runs <keyfile: id<TAB>delegation_id> <outfile>
# Blocks until every delegation settles or POLL_LIMIT elapses.
poll_runs() {
    local keyfile="$1" out="$2" total settled=0 tries=0
    total=$(wc -l < "$keyfile" | tr -d ' ')
    local list; list=$(mktemp)
    while [ "$tries" -lt "${POLL_LIMIT:-60}" ]; do
        mcp_one "mcpx__list_delegations" '{"limit":200}' 8 > "$list"
        settled=0
        while IFS=$'\t' read -r _id did; do
            [ -n "$(settled_run "$did" "$list")" ] && settled=$((settled + 1))
        done < "$keyfile"
        echo "  settled $settled/$total" >&2
        [ "$settled" -ge "$total" ] && break
        tries=$((tries + 1)); sleep 15
    done
    {
        echo "["
        local first=1
        while IFS=$'\t' read -r id did; do
            local run; run=$(settled_run "$did" "$list")
            [ -z "$run" ] && run='{}'
            [ $first -eq 0 ] && echo ","
            first=0
            jq -n --arg id "$id" --argjson r "$run" \
                '{id:$id, status:($r.status // "missing"), output:($r.output_text // ""),
                  input_tokens:($r.input_tokens // 0), output_tokens:($r.output_tokens // 0),
                  duration_s:(((($r.duration_ms // 0)) / 1000) | floor)}'
        done < "$keyfile"
        echo "]"
    } > "$out"
    rm -f "$list"
    [ "$settled" -ge "$total" ]
}

cmd_run() {
    command -v jq >/dev/null || { echo "jq required" >&2; return 1; }
    mcp_preflight || return 1
    local arm=ungated; [ "$WITH_GATE" = "1" ] && arm=gated
    local out="citation-bench-$arm-$(date +%s).json"
    local specs ids keyfile
    specs=$(mktemp); ids=$(mktemp); keyfile=$(mktemp)
    while IFS=$'\t' read -r id _kind _expected _cf _cl prompt; do
        printf '%s\n' "$id" >> "$ids"
        dispatch_spec "$prompt" >> "$specs"
    done < <(questions)
    echo "dispatching $(wc -l < "$ids" | tr -d ' ') delegations ($arm) ..." >&2
    while IFS=$'\t' read -r idx payload; do
        local qid did
        qid=$(sed -n "${idx}p" "$ids")
        did=$(jq -r '.delegation_id // empty' <<<"$payload" 2>/dev/null)
        # Fail loudly. A silent empty id here is what produced a full table of
        # zeros that looked like a real (terrible) result.
        if [ -z "$did" ]; then
            echo "FATAL: ${qid:-request $idx} dispatch returned no delegation_id" >&2
            echo "  response: $(head -c 400 <<<"$payload")" >&2
            rm -f "$specs" "$ids" "$keyfile"; return 1
        fi
        echo "  $qid -> $did" >&2
        printf '%s\t%s\n' "$qid" "$did" >> "$keyfile"
    done < <(mcp_batch "$specs" 20)
    if [ ! -s "$keyfile" ]; then
        echo "FATAL: no delegations were dispatched (daemon returned nothing)" >&2
        rm -f "$specs" "$ids" "$keyfile"; return 1
    fi
    echo "polling ($arm) ..." >&2
    poll_runs "$keyfile" "$out" ||
        echo "WARNING: some runs never settled; they are recorded as missing" >&2
    rm -f "$specs" "$ids" "$keyfile"
    echo "results: $out" >&2
    cmd_score "$out"
}

case "${1:-truth}" in
    truth) cmd_truth ;;
    score) shift; cmd_score "${1:?usage: score <results.json>}" ;;
    run)
        shift
        while getopts "m:n:g" opt; do
            case "$opt" in
                m) MODEL_ID="$OPTARG" ;;
                n) RUNS="$OPTARG" ;;
                g) WITH_GATE=1 ;;
                *) exit 1 ;;
            esac
        done
        cmd_run
        ;;
    *) echo "usage: $0 {truth|run|score <file>}" >&2; exit 1 ;;
esac
