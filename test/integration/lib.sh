#!/usr/bin/env bash
# lib.sh — shared helpers for scenarios.sh.
#
# Sourced once at the top of scenarios.sh; do not invoke directly. Exposes:
#   step / pass / fail / skip — output + counter helpers
#   ensure_tokens / token_for — api-key bootstrap (read out of containers)
#   api / api_status          — curl wrappers that inject the right Bearer
#   assert_jq                 — assert a jq filter against a JSON payload
#   node_var                  — convert a node URL to A/B/C tag

# ----- counters + result log ---------------------------------------------
PASS=0
FAIL=0
SKIP=0
declare -a RESULTS=()

# ----- output helpers ----------------------------------------------------
step() {
    printf '\n=== STEP %s: %s ===\n' "$1" "$2"
}

pass() {
    PASS=$((PASS + 1))
    RESULTS+=("PASS: $1")
    printf '  PASS: %s\n' "$1"
}

fail() {
    FAIL=$((FAIL + 1))
    RESULTS+=("FAIL: $1")
    printf '  FAIL: %s\n' "$1" >&2
    if [ -n "${2:-}" ]; then
        printf '    detail: %s\n' "$2" >&2
    fi
}

skip() {
    SKIP=$((SKIP + 1))
    RESULTS+=("SKIP: $1")
    printf '  SKIP: %s — %s\n' "$1" "$2"
}

# ----- token bootstrap ---------------------------------------------------
fetch_token() {
    local container="$1"
    docker exec "$container" cat /data/api-key 2>/dev/null | tr -d '[:space:]' || true
}

ensure_tokens() {
    # api-key bootstraps on first /health hit, but docker-compose's
    # healthcheck on node-a alone doesn't poke node-b + node-c — they
    # come up healthy but with no api-key file written yet. Nudge each
    # node first, retry the fetch a few times so we tolerate the race.
    #
    # NODE_D + NODE_E are the bulletproof-tier topology (5-node compose).
    # When the 3-node smoke compose is up they're unreachable; we soft-skip
    # them so ensure_tokens stays compatible with both topologies.
    curl -sf -o /dev/null "$NODE_A/api/v1/health" 2>/dev/null || true
    curl -sf -o /dev/null "$NODE_B/api/v1/health" 2>/dev/null || true
    curl -sf -o /dev/null "$NODE_C/api/v1/health" 2>/dev/null || true
    [ -n "${NODE_D:-}" ] && curl -sf -o /dev/null "$NODE_D/api/v1/health" 2>/dev/null || true
    [ -n "${NODE_E:-}" ] && curl -sf -o /dev/null "$NODE_E/api/v1/health" 2>/dev/null || true
    local i
    for i in 1 2 3 4 5; do
        TOK_A="$(fetch_token "$CONT_A")"
        TOK_B="$(fetch_token "$CONT_B")"
        TOK_C="$(fetch_token "$CONT_C")"
        TOK_D="$(fetch_token "${CONT_D:-}")"
        TOK_E="$(fetch_token "${CONT_E:-}")"
        if [ -n "$TOK_A" ] && [ -n "$TOK_B" ] && [ -n "$TOK_C" ]; then
            return 0
        fi
        sleep 1
    done
    printf 'failed to read api-key from one or more nodes\n' >&2
    printf '  TOK_A length=%d TOK_B length=%d TOK_C length=%d TOK_D length=%d TOK_E length=%d\n' \
        ${#TOK_A} ${#TOK_B} ${#TOK_C} ${#TOK_D} ${#TOK_E} >&2
    return 1
}

token_for() {
    case "$1" in
        "$NODE_A") echo "$TOK_A" ;;
        "$NODE_B") echo "$TOK_B" ;;
        "$NODE_C") echo "$TOK_C" ;;
        "${NODE_D:-__none__}") echo "${TOK_D:-}" ;;
        "${NODE_E:-__none__}") echo "${TOK_E:-}" ;;
        *) echo "" ;;
    esac
}

# ----- HTTP helpers ------------------------------------------------------
# api METHOD URL [BODY] — fails non-zero on HTTP >= 400. Returns body.
api() {
    local method="$1"
    local url="$2"
    local body="${3:-}"
    local base="${url%%/api/*}"
    local tok
    tok="$(token_for "$base")"

    if [ -n "$body" ]; then
        curl -fsS -X "$method" "$url" \
            -H "Authorization: Bearer $tok" \
            -H 'Content-Type: application/json' \
            --data "$body"
    else
        curl -fsS -X "$method" "$url" \
            -H "Authorization: Bearer $tok"
    fi
}

# api_status METHOD URL [BODY] — returns HTTP status code instead of body.
api_status() {
    local method="$1"
    local url="$2"
    local body="${3:-}"
    local base="${url%%/api/*}"
    local tok
    tok="$(token_for "$base")"

    if [ -n "$body" ]; then
        curl -s -o /dev/null -w '%{http_code}' -X "$method" "$url" \
            -H "Authorization: Bearer $tok" \
            -H 'Content-Type: application/json' \
            --data "$body"
    else
        curl -s -o /dev/null -w '%{http_code}' -X "$method" "$url" \
            -H "Authorization: Bearer $tok"
    fi
}

# ----- assertions --------------------------------------------------------
assert_jq() {
    local label="$1"
    local payload="$2"
    local filter="$3"
    local got
    got=$(echo "$payload" | jq -r "$filter" 2>/dev/null || echo "<jq error>")
    if [ "$got" = "true" ] || [ "$got" = "1" ]; then
        pass "$label"
    else
        fail "$label" "got: $got; payload head: $(echo "$payload" | head -c 200)"
    fi
}

# ----- misc helpers ------------------------------------------------------
# node_var converts a node URL to a tag suitable for a shell var suffix.
node_var() {
    case "$1" in
        "$NODE_A") echo "A" ;;
        "$NODE_B") echo "B" ;;
        "$NODE_C") echo "C" ;;
        "${NODE_D:-__none__}") echo "D" ;;
        "${NODE_E:-__none__}") echo "E" ;;
        *) echo "X" ;;
    esac
}

# container_for maps a node URL to the docker container name. Lets the
# MCP helpers below address the right daemon by URL (matches how every
# other helper here keys off the node URL).
container_for() {
    case "$1" in
        "$NODE_A") echo "$CONT_A" ;;
        "$NODE_B") echo "$CONT_B" ;;
        "$NODE_C") echo "$CONT_C" ;;
        "${NODE_D:-__none__}") echo "${CONT_D:-}" ;;
        "${NODE_E:-__none__}") echo "${CONT_E:-}" ;;
        *) echo "" ;;
    esac
}

# peer_id_for fetches the libp2p PeerID for a node URL by GET-ing
# /api/p2p/identity. Returns the empty string on error so callers can
# detect missing identity.
peer_id_for() {
    local url="$1"
    api GET "$url/api/p2p/identity" 2>/dev/null | jq -r '.peer_id // empty' 2>/dev/null || echo ""
}

# is_paired_with returns 0 if `node_url` reports `peer_id` as a paired
# peer, 1 otherwise. Used by mesh-trigger + broadcast scenarios to SKIP
# (rather than FAIL) when the closed docker bridge prevented one of the
# pair handshakes from completing. The scenarios that depend on
# end-to-end libp2p propagation should pre-check pairing this way.
is_paired_with() {
    local node="$1"
    local peer_id="$2"
    api GET "$node/api/p2p/peers" 2>/dev/null \
        | jq -e --arg pid "$peer_id" \
            '(.peers // []) | any(.peer_id == $pid)' >/dev/null 2>&1
}

# mcp_call runs a single MCP tools/call against the daemon on `node_url`
# by piping a 3-line JSON-RPC conversation (initialize, initialized
# notification, tools/call) into `mcplexer connect --socket=...` inside
# the container. Returns the raw response payload (one JSON line, id=2).
#
# Usage: mcp_call <node_url> <tool_name> <args_json>
#
# Notes:
#   - Mesh tools (mesh__send/receive/grant_peer_scope/offer_skill/
#     request_skill/...) are universal — not CWD-gated — so this works
#     from any cwd inside the container. Admin tools (mcplexer__*) are
#     CWD-gated and intentionally NOT exercised through this helper.
#   - The daemon listens on /data/mcplexer.sock; the Dockerfile sets
#     MCPLEXER_SOCKET_PATH so runServer enables the socket listener
#     alongside HTTP.
#   - 5s timeout per call so a hung daemon doesn't block the suite.
mcp_call() {
    local url="$1"
    local tool="$2"
    local args="$3"
    local container
    container=$(container_for "$url")
    if [ -z "$container" ]; then
        echo '{"error":"unknown node url"}'
        return 1
    fi

    local init_req
    init_req=$(jq -nc '{jsonrpc:"2.0",id:1,method:"initialize",params:{protocolVersion:"2024-11-05",capabilities:{},clientInfo:{name:"integration",version:"1"}}}')
    local init_note='{"jsonrpc":"2.0","method":"notifications/initialized"}'
    local call_req
    call_req=$(jq -nc \
        --arg name "$tool" \
        --argjson args "$args" \
        '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:$name,arguments:$args}}')

    # The daemon dispatches each stdin line in its own goroutine, so we
    # must serialise the conversation manually: write init → sleep so
    # the daemon's handleInitialize completes (and the session's
    # workspace chain populates) → write the initialized notification →
    # write the call. The trailing sleep gives the response time to
    # land on stdout before stdin EOF tears the socket down. Empirically
    # 250ms is enough on a warm container; bumped to 500ms for slack.
    (
        printf '%s\n' "$init_req"
        sleep 0.5
        printf '%s\n' "$init_note"
        sleep 0.3
        printf '%s\n' "$call_req"
        sleep 0.8
    ) | docker exec -i -w / "$container" \
            /usr/local/bin/mcplexer connect --socket=/data/mcplexer.sock 2>/dev/null \
        | grep -E '^\{' \
        | jq -c 'select(.id == 2)' 2>/dev/null \
        | head -1
}

# mcp_call_ok runs mcp_call and asserts the response is a successful
# tools/call (no `error` field, `result.isError != true`). Echoes the
# decoded text content or the raw result on success; emits a fail line
# and returns 1 on failure.
#
# Usage: mcp_call_ok <label> <node_url> <tool> <args_json>
mcp_call_ok() {
    local label="$1"
    local url="$2"
    local tool="$3"
    local args="$4"
    local resp
    resp=$(mcp_call "$url" "$tool" "$args")
    if [ -z "$resp" ]; then
        fail "$label" "no response from $tool on $url"
        return 0
    fi
    # JSON-RPC error envelope.
    if echo "$resp" | jq -e '.error != null' >/dev/null 2>&1; then
        local emsg
        emsg=$(echo "$resp" | jq -c '.error')
        fail "$label" "$tool returned error: $emsg"
        return 0
    fi
    # Tool-level isError flag (handler returned a CallToolResult with isError=true).
    if echo "$resp" | jq -e '.result.isError == true' >/dev/null 2>&1; then
        local detail
        detail=$(echo "$resp" | jq -c '.result.content[0].text? // .result')
        fail "$label" "$tool result.isError=true: $detail"
        return 0
    fi
    pass "$label"
    echo "$resp"
}

# mcp_call_err is the inverse — asserts the call fails OR returns
# isError=true, surfacing a fail line if the call actually succeeded.
# Used for negative scenarios (e.g. unauthorized skill request).
#
# Usage: mcp_call_err <label> <node_url> <tool> <args_json> [expected_substring]
mcp_call_err() {
    local label="$1"
    local url="$2"
    local tool="$3"
    local args="$4"
    local want="${5:-}"
    local resp
    resp=$(mcp_call "$url" "$tool" "$args")
    if [ -z "$resp" ]; then
        fail "$label" "no response from $tool on $url"
        return 0
    fi
    local is_err=""
    if echo "$resp" | jq -e '.error != null' >/dev/null 2>&1; then
        is_err="true"
    elif echo "$resp" | jq -e '.result.isError == true' >/dev/null 2>&1; then
        is_err="true"
    fi
    if [ -z "$is_err" ]; then
        fail "$label" "expected $tool to fail, got success: $(echo "$resp" | jq -c '.result')"
        return 0
    fi
    if [ -n "$want" ]; then
        if ! echo "$resp" | grep -q "$want"; then
            fail "$label" "expected substring $want; got: $(echo "$resp" | head -c 300)"
            return 0
        fi
    fi
    pass "$label"
    echo "$resp"
}
