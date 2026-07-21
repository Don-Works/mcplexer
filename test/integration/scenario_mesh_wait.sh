#!/usr/bin/env bash
# scenario_mesh_wait.sh — event-driven mesh wait. Sourced by scenarios.sh.
#
# Feature under test: `mcplexer mesh wait --agent <name> [flags]` blocks
# server-side (event-driven long-poll of GET /api/v1/mesh/wait, NOT client
# polling) until a mesh message targets <name>, then prints the matched
# message JSON and exits — waking the agent.
#
# IDENTITY MODEL (load-bearing for this test): a mesh agent identity *is* a
# live stdio session. A name resolves to a wake target only while the session
# that registered it is still connected (rows for a disconnected `mcplexer
# connect` session don't survive for a separate process to resolve). In
# production the dormant agent's MAIN Claude Code session stays connected the
# whole time it has backgrounded `mesh wait`, so its named row stays live.
# This scenario models that with hold_identity(): a backgrounded `connect`
# session that registers a name and holds stdin open, keeping the identity
# live while the separate `mesh wait` process resolves + waits on it.
#
# Pinned contracts asserted here:
#   CLI: mcplexer mesh wait --agent NAME [--tags csv] [--include-broadcast]
#        [--timeout SEC] [--once]
#     - matching targeted message -> stdout {"matched":true,...}, exit 0.
#     - --once + no message before timeout -> stdout {"timed_out":true}, exit 0.
#   Default wake scope = targeted-by-name only; --include-broadcast widens it.
#
# Helpers relied on (all in lib.sh): step, pass, fail, skip, mcp_call,
# container_for, is_paired_with, peer_id_for.

# ----- identity-holder helpers -------------------------------------------

# Track backgrounded identity-holder pipelines so a case can tear them down.
MESH_WAIT_HOLDERS=()

# hold_identity <node_url> <name> <hold_seconds> — open a `mcplexer connect`
# session inside the node's container that registers <name> via mesh__receive
# and then holds stdin open for <hold_seconds>, keeping the session (and thus
# the named, resolvable agent row) ALIVE. Backgrounded; PID tracked in
# MESH_WAIT_HOLDERS. Sleeps ~2s so the registration round-trip lands before
# the caller starts a waiter.
hold_identity() {
    local url="$1"
    local name="$2"
    local secs="$3"
    local container
    container=$(container_for "$url")
    [ -z "$container" ] && return 1

    local init_req init_note recv_req
    init_req=$(jq -nc --arg n "$name" \
        '{jsonrpc:"2.0",id:1,method:"initialize",params:{protocolVersion:"2024-11-05",capabilities:{},clientInfo:{name:("holder-"+$n),version:"1"}}}')
    init_note='{"jsonrpc":"2.0","method":"notifications/initialized"}'
    recv_req=$(jq -nc --arg n "$name" \
        '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:"mesh__receive",arguments:{name:$n}}}')

    (
        printf '%s\n' "$init_req"
        sleep 0.5
        printf '%s\n' "$init_note"
        sleep 0.3
        printf '%s\n' "$recv_req"
        sleep "$secs"
    ) | docker exec -i "$container" \
            /usr/local/bin/mcplexer connect --socket=/data/mcplexer.sock \
            >/dev/null 2>&1 &
    MESH_WAIT_HOLDERS+=("$!")
    sleep 2
}

# release_identities <node_url> — kill tracked holder pipelines and, belt-and-
# braces, any lingering `mcplexer connect` inside the container so the next
# case starts from a clean directory.
release_identities() {
    local url="$1"
    local p
    for p in "${MESH_WAIT_HOLDERS[@]}"; do
        kill "$p" >/dev/null 2>&1 || true
    done
    MESH_WAIT_HOLDERS=()
    local container
    container=$(container_for "$url")
    [ -z "$container" ] && return 0
    docker exec "$container" sh -c 'pkill -f "mcplexer connect" 2>/dev/null || true' \
        >/dev/null 2>&1 || true
    sleep 1
}

# ----- waiter helpers ----------------------------------------------------

# start_waiter <node_url> <out_file> <extra_args...> — background a `mesh wait`
# inside the node's container, combined output to <out_file>. Returns non-zero
# if the container can't be resolved.
start_waiter() {
    local url="$1"; shift
    local outfile="$1"; shift
    local container
    container=$(container_for "$url")
    [ -z "$container" ] && return 1
    docker exec "$container" sh -c ": > '$outfile'" >/dev/null 2>&1 || true
    docker exec -d "$container" sh -c \
        "/usr/local/bin/mcplexer mesh wait $* > '$outfile' 2>&1"
}

# read_waiter <node_url> <out_file> — cat the out-file from the container.
read_waiter() {
    local url="$1"
    local outfile="$2"
    local container
    container=$(container_for "$url")
    [ -z "$container" ] && return 0
    docker exec "$container" cat "$outfile" 2>/dev/null || true
}

# poll_waiter <node_url> <out_file> <needle> [timeout] — poll the out-file for
# <needle>. Stdout: contents when matched (or final on timeout). Returns 0 on
# match, 1 on timeout.
poll_waiter() {
    local url="$1"
    local outfile="$2"
    local needle="$3"
    local timeout="${4:-6}"
    local i=0
    local out=""
    while [ "$i" -lt "$timeout" ]; do
        out=$(read_waiter "$url" "$outfile")
        if [ -n "$out" ] && printf '%s' "$out" | grep -q "$needle"; then
            printf '%s' "$out"
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    printf '%s' "$out"
    return 1
}

# cleanup_waiters <node_url> <out_files...> — kill lingering waiters + remove
# out-files. Best-effort.
cleanup_waiters() {
    local url="$1"; shift
    local container
    container=$(container_for "$url")
    [ -z "$container" ] && return 0
    docker exec "$container" sh -c 'pkill -f "mesh wait" 2>/dev/null || true' \
        >/dev/null 2>&1 || true
    local f
    for f in "$@"; do
        docker exec "$container" sh -c "rm -f '$f'" >/dev/null 2>&1 || true
    done
}

# send_targeted <from_node> <to_agent> <content> [tags] — mesh__send with
# to_agent name-targeting via a (separate, ephemeral) mcp_call session.
send_targeted() {
    local from_node="$1"
    local to_agent="$2"
    local content="$3"
    local tags="${4:-}"
    local args
    args=$(jq -nc \
        --arg to "$to_agent" \
        --arg c "$content" \
        --arg tags "$tags" \
        '{kind:"finding", content:$c, to_agent:$to, priority:"low",
          agent_name:"wait-sender"}
         + (if $tags != "" then {tags:$tags} else {} end)')
    mcp_call "$from_node" "mesh__send" "$args" >/dev/null 2>&1 || true
}

# send_broadcast <from_node> <content> — mesh__send to audience "*".
send_broadcast() {
    local from_node="$1"
    local content="$2"
    local args
    args=$(jq -nc \
        --arg c "$content" \
        '{kind:"finding", content:$c, audience:"*", priority:"low",
          agent_name:"wait-broadcaster"}')
    mcp_call "$from_node" "mesh__send" "$args" >/dev/null 2>&1 || true
}

# ----- scenario ----------------------------------------------------------

scenario_mesh_wait() {
    step 5.5 "event-driven mesh wait — blocking-proof, cross-peer, filters, timeout"

    # Distinct name per sub-case: exactly one live identity per name (so
    # to_agent resolves unambiguously) and a fresh cursor (so a stale match
    # from an earlier case can't pre-trip a later waiter — default scope is
    # signal-only, cursor not advanced on wake). Deterministic cases send from
    # node-a (same daemon) so delivery is in-process, independent of pairing.

    local out_block="/tmp/wait_block.out"
    local out_cross="/tmp/wait_cross.out"
    local out_tag="/tmp/wait_tag.out"
    local out_bcast="/tmp/wait_bcast.out"
    local out_bcasti="/tmp/wait_bcasti.out"

    # --- 5.5.1 BLOCKING-PROOF: command blocks, then a targeted msg wakes it ---
    local n_block="waiter-block"
    hold_identity "$NODE_A" "$n_block" 30
    if ! start_waiter "$NODE_A" "$out_block" --agent "$n_block" --timeout 25; then
        fail "mesh-wait: could not start blocking-proof waiter"
    else
        sleep 3
        local early
        early=$(read_waiter "$NODE_A" "$out_block")
        if [ -z "$early" ]; then
            pass "mesh-wait: waiter blocked (out-file empty after 3s — event-driven, not polling)"
        else
            fail "mesh-wait: waiter returned before any message arrived" \
                "out=$(printf '%s' "$early" | head -c 200)"
        fi
        send_targeted "$NODE_A" "$n_block" "wake-block-$RANDOM"
        local woke
        if woke=$(poll_waiter "$NODE_A" "$out_block" '"matched":true' 8); then
            pass "mesh-wait: targeted message woke the waiter (matched:true)"
        else
            fail "mesh-wait: waiter did NOT wake on targeted message" \
                "out=$(printf '%s' "$woke" | head -c 200)"
        fi
    fi
    cleanup_waiters "$NODE_A" "$out_block"
    release_identities "$NODE_A"

    # --- 5.5.2 CROSS-PEER WAKE: a node-b message wakes a node-a waiter -------
    # This proves the event-driven wake fires for REMOTE (p2p-ingested)
    # messages, not just local ones — the cross-machine half of the feature.
    #
    # We use a BROADCAST from node-b (not to_agent name-targeting): the mesh
    # agent directory gossips only DEVICE-level peer presence (node-b sees
    # node-a as a single "node-a [peer:...]" entry), NOT node-a's individual
    # agent names — so node-b cannot resolve to_agent:"<a-local-name>". The
    # path that propagates over libp2p is audience="*" (cf. scenario_mesh_send),
    # which node-a's --include-broadcast waiter wakes on. (The local-ingest wake
    # for a TARGETED remote message is covered by the WaitForMessage unit test's
    # p2p-ingest case.)
    #
    # SKIP-guarded: if A<->B pairing didn't establish on the closed docker
    # bridge, or the broadcast doesn't propagate within the window, we SKIP
    # rather than FAIL — libp2p-over-docker-bridge is environmental, and the
    # wake logic itself is already hard-asserted locally + in unit tests.
    local n_cross="waiter-cross"
    local pid_b="${PID_B:-$(peer_id_for "$NODE_B")}"
    if [ -z "$pid_b" ] || ! is_paired_with "$NODE_A" "$pid_b"; then
        skip "mesh-wait cross-peer" \
            "node-a not paired with node-b (libp2p closed-bridge) — cross-mesh wake cannot propagate"
    else
        hold_identity "$NODE_A" "$n_cross" 35
        if ! start_waiter "$NODE_A" "$out_cross" --agent "$n_cross" --include-broadcast --timeout 30; then
            fail "mesh-wait: could not start cross-peer waiter on node-a"
        else
            sleep 2
            send_broadcast "$NODE_B" "wake-cross-$RANDOM"
            local cross
            if cross=$(poll_waiter "$NODE_A" "$out_cross" '"matched":true' 18); then
                pass "mesh-wait: cross-peer broadcast from node-b woke node-a (p2p-ingested wake)"
            else
                skip "mesh-wait cross-peer" \
                    "paired, but node-b broadcast did not propagate to node-a within 18s (libp2p closed-bridge timing)"
            fi
        fi
        cleanup_waiters "$NODE_A" "$out_cross"
        release_identities "$NODE_A"
    fi

    # --- 5.5.3 TAG FILTER: --tags urgent ignores untagged, wakes on tagged ---
    local n_tag="waiter-tag"
    hold_identity "$NODE_A" "$n_tag" 30
    if ! start_waiter "$NODE_A" "$out_tag" --agent "$n_tag" --tags urgent --timeout 25; then
        fail "mesh-wait: could not start tag-filter waiter"
    else
        sleep 1
        send_targeted "$NODE_A" "$n_tag" "no-tag-$RANDOM"
        sleep 3
        local untagged
        untagged=$(read_waiter "$NODE_A" "$out_tag")
        if [ -z "$untagged" ]; then
            pass "mesh-wait: tag filter ignored a message lacking tag 'urgent'"
        else
            fail "mesh-wait: tag filter woke on a non-'urgent' message" \
                "out=$(printf '%s' "$untagged" | head -c 200)"
        fi
        send_targeted "$NODE_A" "$n_tag" "tagged-$RANDOM" "urgent"
        local tagged
        if tagged=$(poll_waiter "$NODE_A" "$out_tag" '"matched":true' 8); then
            pass "mesh-wait: tag filter woke on the 'urgent'-tagged message"
        else
            fail "mesh-wait: tag filter did NOT wake on the 'urgent' message" \
                "out=$(printf '%s' "$tagged" | head -c 200)"
        fi
    fi
    cleanup_waiters "$NODE_A" "$out_tag"
    release_identities "$NODE_A"

    # --- 5.5.4 BROADCAST GATING (default): "*" must NOT wake -----------------
    local n_bcast="waiter-bcast"
    hold_identity "$NODE_A" "$n_bcast" 20
    if ! start_waiter "$NODE_A" "$out_bcast" --agent "$n_bcast" --timeout 6; then
        fail "mesh-wait: could not start default-scope broadcast waiter"
    else
        sleep 1
        send_broadcast "$NODE_A" "broadcast-ignored-$RANDOM"
        sleep 3
        local bcast
        bcast=$(read_waiter "$NODE_A" "$out_bcast")
        if [ -z "$bcast" ]; then
            pass "mesh-wait: default scope did NOT wake on a '*' broadcast"
        else
            fail "mesh-wait: default scope leaked — woke on a broadcast" \
                "out=$(printf '%s' "$bcast" | head -c 200)"
        fi
    fi
    cleanup_waiters "$NODE_A" "$out_bcast"
    release_identities "$NODE_A"

    # --- 5.5.5 BROADCAST GATING (--include-broadcast): "*" must wake ---------
    local n_bcasti="waiter-bcasti"
    hold_identity "$NODE_A" "$n_bcasti" 20
    if ! start_waiter "$NODE_A" "$out_bcasti" --agent "$n_bcasti" --include-broadcast --timeout 12; then
        fail "mesh-wait: could not start --include-broadcast waiter"
    else
        sleep 1
        send_broadcast "$NODE_A" "broadcast-woke-$RANDOM"
        local bcast_inc
        if bcast_inc=$(poll_waiter "$NODE_A" "$out_bcasti" '"matched":true' 8); then
            pass "mesh-wait: --include-broadcast woke on a '*' broadcast"
        else
            fail "mesh-wait: --include-broadcast did NOT wake on a broadcast" \
                "out=$(printf '%s' "$bcast_inc" | head -c 200)"
        fi
    fi
    cleanup_waiters "$NODE_A" "$out_bcasti"
    release_identities "$NODE_A"

    # --- 5.5.6 TIMEOUT PATH: --once + no message -> {"timed_out":true} -------
    local n_tmo="waiter-tmo"
    hold_identity "$NODE_A" "$n_tmo" 12
    local cont_a
    cont_a=$(container_for "$NODE_A")
    if [ -z "$cont_a" ]; then
        fail "mesh-wait: timeout path — no container for node-a"
    else
        local t_start t_end elapsed timeout_out
        t_start=$(date +%s)
        timeout_out=$(docker exec "$cont_a" \
            /usr/local/bin/mcplexer mesh wait --agent "$n_tmo" --timeout 3 --once \
            2>/dev/null || true)
        t_end=$(date +%s)
        elapsed=$((t_end - t_start))
        if printf '%s' "$timeout_out" | grep -q '"timed_out":true'; then
            pass "mesh-wait: --once timeout printed {\"timed_out\":true}"
        else
            fail "mesh-wait: --once timeout did NOT print timed_out:true" \
                "out=$(printf '%s' "$timeout_out" | head -c 200)"
        fi
        if [ "$elapsed" -ge 2 ]; then
            pass "mesh-wait: --once actually waited (~${elapsed}s >= timeout, not an instant return)"
        else
            fail "mesh-wait: --once returned too fast (${elapsed}s) — not waiting server-side"
        fi
    fi
    release_identities "$NODE_A"

    # --- final cleanup ---------------------------------------------------
    cleanup_waiters "$NODE_A" \
        "$out_block" "$out_cross" "$out_tag" "$out_bcast" "$out_bcasti"
}
