#!/usr/bin/env bash
# citation-bench-mcp.sh — MCP stdio transport for citation-bench.sh.
#
# Sourced by citation-bench.sh; not executable on its own.
#
# WHY NOT REST: POST /api/v1/delegations rejects the payload with
# "failed to create delegation" because Delegate() requires workspace_id
# (internal/workers/admin/delegation.go:719). The MCP tool fills that in from
# the caller's session; a raw HTTP request has no session, so the field is
# empty. Rather than hardcode a workspace UUID, this drives the same MCP path
# the agent uses — which also drops the API-key dependency entirely, so the
# harness needs no credential and no ~/.mcplexer access.
#
# `mcplexer connect` bridges stdin/stdout to the daemon's local IPC socket.
# Requests must follow a completed initialize handshake, hence the sleep: the
# proxy is a pipe, not a request/response client, and firing tools/call before
# initialize returns gets "MCP session must initialize successfully".

MCP_INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"claude-code","version":"1"}}}'
MCP_INITED='{"jsonrpc":"2.0","method":"notifications/initialized"}'
MCP_BIN="${MCPLEXER_BIN:-mcplexer}"

# mcp_batch <specfile> [wait_seconds]
# specfile: one JSON object per line, {"tool":"name","args":{...}}
# Emits one "<n><TAB><payload>" line per request, n being the 1-based request
# index. Payload is the unwrapped text content, or "ERROR:<message>".
mcp_batch() {
    local specfile="$1" wait="${2:-8}" reqs i=0
    reqs=$(mktemp)
    while IFS= read -r spec; do
        [ -z "$spec" ] && continue
        i=$((i + 1))
        jq -c --argjson n "$((100 + i))" \
            '{jsonrpc:"2.0", id:$n, method:"tools/call",
              params:{name:.tool, arguments:.args}}' <<<"$spec" >> "$reqs"
    done < "$specfile"
    {
        printf '%s\n' "$MCP_INIT"
        sleep 2
        printf '%s\n' "$MCP_INITED"
        cat "$reqs"
        sleep "$wait"
    } | "$MCP_BIN" connect 2>/dev/null |
        jq -r 'select(.id != null and .id >= 101)
               | "\(.id - 100)\t\(((.result.content[0].text)? // ("ERROR:" + (.error.message // "unknown"))) | gsub("[\n\r]"; " "))"'
    rm -f "$reqs"
}

# mcp_one <tool> <args_json> [wait] -> payload text on stdout
mcp_one() {
    local spec; spec=$(mktemp)
    # -c is required: mcp_batch reads exactly one spec per line.
    jq -nc --arg t "$1" --argjson a "$2" '{tool:$t, args:$a}' > "$spec"
    mcp_batch "$spec" "${3:-8}" | head -1 | cut -f2-
    rm -f "$spec"
}

# mcp_preflight — fail fast with a clear message when the daemon is unreachable,
# instead of surfacing as "every question is missing" later.
mcp_preflight() {
    command -v "$MCP_BIN" >/dev/null || { echo "FATAL: '$MCP_BIN' not on PATH" >&2; return 1; }
    local out; out=$(mcp_one "mcpx__list_delegations" '{"limit":1}' 5)
    case "$out" in
        ERROR:*|"") echo "FATAL: cannot reach mcplexer daemon over MCP: ${out:-no response}" >&2; return 1 ;;
    esac
    return 0
}
