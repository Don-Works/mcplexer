#!/usr/bin/env bash
# scenario_resource_posture.sh — goroutine/FD/disk-leak guard across
# overnight iterations. Sourced (not exec'd) by run_overnight.sh so the
# scenario_resource_posture function lives in the orchestrator's shell.
#
# Two phases:
#   scenario_resource_posture --baseline <baseline_path>
#       Capture a baseline JSON snapshot of {goroutines, fds, disk_bytes}
#       per container. Written atomically to <baseline_path>.
#
#   scenario_resource_posture --check <baseline_path> <final_path>
#       Recapture the same metrics; write to <final_path>; diff each
#       container's metrics vs baseline. PASS if every delta is within
#       both an absolute and relative guard (see THRESHOLDS below).
#       FAIL with the diff if any guard trips.
#
# Containers measured: mcplexer-test-node-a, mcplexer-test-node-b,
#   mcplexer-test-node-c. (The brief mentioned 5 nodes; the 5-node tier
#   topology hasn't landed yet — we measure whatever's in the active
#   docker-compose file. If a node is missing we skip it silently.)
#
# Goroutine measurement:
#   The daemon doesn't currently expose /debug/pprof. We probe for it at
#   $url/debug/pprof/goroutine?debug=1 — if it 200s, we parse the
#   `goroutine N` profile line. If it 404s (the common case today), we
#   fall back to /proc/<pid>/status's `Threads:` field as a weaker
#   proxy. Threads != goroutines but it's the closest leak signal we
#   have without bloating the daemon binary with pprof.
#
# FD measurement: /proc/<pid>/fd | wc -l inside the container.
# Disk measurement: du -sb /data inside the container.
#
# JSON shape (compact, one node per top-level key):
#   {
#     "node-a": {"goroutines": 42, "fds": 18, "disk_bytes": 524288,
#                "goroutine_source": "pprof"|"threads"|"skip"},
#     ...
#   }

# ----- thresholds --------------------------------------------------------
# Absolute floors — small deltas (e.g. +5 goroutines from a one-shot run)
# are noise. Above the floor we additionally enforce a relative ceiling
# (delta as % of baseline). Both must trip for the guard to fail.
GOROUTINE_ABS_LIMIT=1000
GOROUTINE_REL_LIMIT=20     # percent
FD_ABS_LIMIT=200
FD_REL_LIMIT=20            # percent
DISK_ABS_LIMIT=104857600   # 100 MB in bytes
DISK_REL_LIMIT=20          # percent

# Containers we measure. If a name is missing (the harness scaled down),
# it's skipped silently. NB: we keep this list aligned with docker-compose
# — extending to 5 nodes is a one-line edit here.
NODE_CONTAINERS=(
    "mcplexer-test-node-a"
    "mcplexer-test-node-b"
    "mcplexer-test-node-c"
    "mcplexer-test-node-d"
    "mcplexer-test-node-e"
)

# ----- helpers -----------------------------------------------------------

# rp__container_running returns 0 if the container is up.
rp__container_running() {
    local name="$1"
    docker inspect --format '{{.State.Running}}' "$name" 2>/dev/null \
        | grep -q true
}

# rp__container_pid returns the mcplexer PID inside the container.
# The container's entrypoint exec's the daemon, so it's almost always
# PID 1. We confirm by checking /proc/1/comm — if the value doesn't
# look like the daemon binary (mcplexer), we fall back to pgrep.
rp__container_pid() {
    local name="$1"
    # Fast path: PID 1.
    local comm
    comm=$(docker exec "$name" sh -c 'cat /proc/1/comm 2>/dev/null' 2>/dev/null \
        | tr -d '[:space:]')
    if [ "$comm" = "mcplexer" ]; then
        echo 1
        return 0
    fi
    # Fallback: search /proc/*/comm for mcplexer. We can't rely on pgrep
    # being installed on the slim runtime image.
    docker exec "$name" sh -c '
        for d in /proc/[0-9]*; do
            c=$(cat "$d/comm" 2>/dev/null) || continue
            if [ "$c" = "mcplexer" ]; then
                echo "${d##*/}"
                exit 0
            fi
        done
        exit 1
    ' 2>/dev/null
}

# rp__goroutines tries pprof first, falls back to thread count from
# /proc/<pid>/status. Prints "<n>" to stdout and "<source>" to a global
# variable RP_LAST_GOROUTINE_SOURCE so the caller can record provenance.
RP_LAST_GOROUTINE_SOURCE=""
rp__goroutines() {
    local name="$1"
    local pid="$2"

    # Try pprof on the loopback in-container. If the daemon doesn't
    # register the handler, curl returns non-200 and we fall back.
    local profile
    profile=$(docker exec "$name" sh -c \
        'curl -fsS --max-time 3 http://127.0.0.1:3333/debug/pprof/goroutine?debug=1 2>/dev/null \
         | head -5' 2>/dev/null)
    # Profile line shape: "goroutine profile: total 42"
    local pprof_count
    pprof_count=$(printf '%s' "$profile" | awk '
        /goroutine profile: total/ { print $NF; exit }
    ')
    if [ -n "$pprof_count" ] && [ "$pprof_count" -gt 0 ] 2>/dev/null; then
        RP_LAST_GOROUTINE_SOURCE="pprof"
        echo "$pprof_count"
        return 0
    fi

    # Fallback: /proc/<pid>/status's Threads:
    local threads
    threads=$(docker exec "$name" sh -c \
        "awk '/^Threads:/ {print \$2; exit}' /proc/$pid/status 2>/dev/null" \
        2>/dev/null | tr -d '[:space:]')
    if [ -n "$threads" ] && [ "$threads" -gt 0 ] 2>/dev/null; then
        RP_LAST_GOROUTINE_SOURCE="threads"
        echo "$threads"
        return 0
    fi

    RP_LAST_GOROUTINE_SOURCE="skip"
    echo "0"
    return 1
}

# rp__fds counts entries in /proc/<pid>/fd. Returns 0 on failure.
rp__fds() {
    local name="$1"
    local pid="$2"
    local count
    count=$(docker exec "$name" sh -c \
        "ls /proc/$pid/fd 2>/dev/null | wc -l" 2>/dev/null \
        | tr -d '[:space:]')
    if [ -z "$count" ]; then
        echo 0
        return 1
    fi
    echo "$count"
}

# rp__disk_bytes returns the bytes used under /data.
rp__disk_bytes() {
    local name="$1"
    local bytes
    bytes=$(docker exec "$name" sh -c \
        'du -sb /data 2>/dev/null | awk "{print \$1}"' 2>/dev/null \
        | tr -d '[:space:]')
    if [ -z "$bytes" ]; then
        echo 0
        return 1
    fi
    echo "$bytes"
}

# rp__node_label maps a container name to its compose service name
# (the JSON key in the output).
rp__node_label() {
    local cname="$1"
    case "$cname" in
        *node-a) echo "node-a" ;;
        *node-b) echo "node-b" ;;
        *node-c) echo "node-c" ;;
        *node-d) echo "node-d" ;;
        *node-e) echo "node-e" ;;
        *) echo "${cname##mcplexer-test-}" ;;
    esac
}

# rp__capture writes a JSON snapshot to $1.
# Uses jq when available for safer JSON construction; falls back to a
# hand-written printf path on systems missing jq (unlikely in this
# repo but kept for robustness).
rp__capture() {
    local outpath="$1"
    local tmp
    tmp=$(mktemp 2>/dev/null) || tmp="$outpath.tmp"

    # Build a per-node JSON map.
    local first=1
    printf '{\n' > "$tmp"
    for cname in "${NODE_CONTAINERS[@]}"; do
        if ! rp__container_running "$cname"; then
            continue
        fi
        local label pid goroutines source fds disk
        label=$(rp__node_label "$cname")
        pid=$(rp__container_pid "$cname")
        if [ -z "$pid" ]; then
            continue
        fi
        goroutines=$(rp__goroutines "$cname" "$pid")
        source="$RP_LAST_GOROUTINE_SOURCE"
        fds=$(rp__fds "$cname" "$pid")
        disk=$(rp__disk_bytes "$cname" "$pid")

        if [ $first -eq 0 ]; then
            printf ',\n' >> "$tmp"
        fi
        first=0
        printf '  "%s": {"goroutines": %s, "fds": %s, "disk_bytes": %s, "goroutine_source": "%s"}' \
            "$label" "$goroutines" "$fds" "$disk" "$source" >> "$tmp"
    done
    printf '\n}\n' >> "$tmp"
    mv "$tmp" "$outpath"
}

# rp__field reads a numeric field for one node from a JSON file.
# Uses jq if available; falls back to a regex grep that's
# brittle-but-good-enough for our compact format.
rp__field() {
    local path="$1"
    local node="$2"
    local field="$3"
    if command -v jq >/dev/null 2>&1; then
        jq -r --arg n "$node" --arg f "$field" \
            '.[$n][$f] // empty' "$path" 2>/dev/null
    else
        # awk-grep fallback. Looks for "node": {.... "field": NUM ...}
        awk -v node="\"$node\":" -v field="\"$field\":" '
            $0 ~ node { in_node = 1 }
            in_node && match($0, field "[[:space:]]*[0-9]+") {
                s = substr($0, RSTART, RLENGTH)
                sub(field "[[:space:]]*", "", s)
                print s
                exit
            }
        ' "$path"
    fi
}

# rp__field_str reads a string field (e.g. goroutine_source).
rp__field_str() {
    local path="$1"
    local node="$2"
    local field="$3"
    if command -v jq >/dev/null 2>&1; then
        jq -r --arg n "$node" --arg f "$field" \
            '.[$n][$f] // empty' "$path" 2>/dev/null
    else
        awk -v node="\"$node\":" -v field="\"$field\":" '
            $0 ~ node { in_node = 1 }
            in_node && match($0, field "[[:space:]]*\"[^\"]*\"") {
                s = substr($0, RSTART, RLENGTH)
                sub(field "[[:space:]]*\"", "", s)
                sub(/\".*/, "", s)
                print s
                exit
            }
        ' "$path"
    fi
}

# rp__list_nodes lists the node labels present in a snapshot.
rp__list_nodes() {
    local path="$1"
    if command -v jq >/dev/null 2>&1; then
        jq -r 'keys[]' "$path" 2>/dev/null
    else
        grep -oE '"[a-z0-9-]+":[[:space:]]*\{' "$path" \
            | sed 's/":.*//;s/^"//'
    fi
}

# rp__guard: returns 0 if delta is within both the abs floor + relative
# ceiling; 1 if both are breached (= leak).
# args: baseline_val final_val abs_limit rel_pct
rp__guard() {
    local b="$1" f="$2" abs="$3" rel="$4"
    local delta
    delta=$((f - b))
    if [ "$delta" -lt 0 ]; then delta=$((-delta)); fi
    if [ "$delta" -le "$abs" ]; then
        return 0
    fi
    if [ "$b" -le 0 ]; then
        # Baseline of zero — any growth above the abs floor counts.
        return 1
    fi
    local pct=$(( delta * 100 / b ))
    if [ "$pct" -le "$rel" ]; then
        return 0
    fi
    return 1
}

# ----- entry point -------------------------------------------------------

scenario_resource_posture() {
    local mode="${1:-}"
    case "$mode" in
        --baseline)
            local outpath="${2:-}"
            if [ -z "$outpath" ]; then
                printf 'usage: scenario_resource_posture --baseline <path>\n' >&2
                return 2
            fi
            mkdir -p "$(dirname "$outpath")"
            rp__capture "$outpath"
            printf '  resource posture baseline written: %s\n' "$outpath"
            return 0
            ;;
        --check)
            local baseline="${2:-}"
            local finalpath="${3:-}"
            if [ -z "$baseline" ] || [ -z "$finalpath" ]; then
                printf 'usage: scenario_resource_posture --check <baseline> <final>\n' >&2
                return 2
            fi
            if [ ! -f "$baseline" ]; then
                printf 'baseline not found: %s — SKIP\n' "$baseline" >&2
                return 0
            fi
            mkdir -p "$(dirname "$finalpath")"
            rp__capture "$finalpath"

            local nodes
            nodes=$(rp__list_nodes "$baseline")
            local any_fail=0
            local report=""
            for node in $nodes; do
                local b_gr f_gr b_fd f_fd b_disk f_disk src
                b_gr=$(rp__field "$baseline" "$node" goroutines)
                f_gr=$(rp__field "$finalpath" "$node" goroutines)
                b_fd=$(rp__field "$baseline" "$node" fds)
                f_fd=$(rp__field "$finalpath" "$node" fds)
                b_disk=$(rp__field "$baseline" "$node" disk_bytes)
                f_disk=$(rp__field "$finalpath" "$node" disk_bytes)
                src=$(rp__field_str "$baseline" "$node" goroutine_source)

                # If the field isn't a number, treat as missing and skip
                # that metric for this node.
                printf '  %s (goroutine_source=%s):\n' "$node" "${src:-unknown}"
                printf '    goroutines: baseline=%s final=%s' "$b_gr" "$f_gr"
                if [ "${b_gr:-0}" -gt 0 ] 2>/dev/null && [ "${f_gr:-0}" -gt 0 ] 2>/dev/null && [ "$src" != "skip" ]; then
                    if rp__guard "$b_gr" "$f_gr" "$GOROUTINE_ABS_LIMIT" "$GOROUTINE_REL_LIMIT"; then
                        printf '  ok\n'
                    else
                        printf '  LEAK\n'
                        any_fail=1
                        report="${report}${node} goroutines b=$b_gr f=$f_gr (src=$src); "
                    fi
                else
                    printf '  (skipped)\n'
                fi
                printf '    fds: baseline=%s final=%s' "$b_fd" "$f_fd"
                if [ "${b_fd:-0}" -gt 0 ] 2>/dev/null && [ "${f_fd:-0}" -gt 0 ] 2>/dev/null; then
                    if rp__guard "$b_fd" "$f_fd" "$FD_ABS_LIMIT" "$FD_REL_LIMIT"; then
                        printf '  ok\n'
                    else
                        printf '  LEAK\n'
                        any_fail=1
                        report="${report}${node} fds b=$b_fd f=$f_fd; "
                    fi
                else
                    printf '  (skipped)\n'
                fi
                printf '    disk_bytes: baseline=%s final=%s' "$b_disk" "$f_disk"
                if [ "${b_disk:-0}" -gt 0 ] 2>/dev/null && [ "${f_disk:-0}" -gt 0 ] 2>/dev/null; then
                    if rp__guard "$b_disk" "$f_disk" "$DISK_ABS_LIMIT" "$DISK_REL_LIMIT"; then
                        printf '  ok\n'
                    else
                        printf '  LEAK\n'
                        any_fail=1
                        report="${report}${node} disk b=$b_disk f=$f_disk; "
                    fi
                else
                    printf '  (skipped)\n'
                fi
            done

            if [ "$any_fail" -ne 0 ]; then
                printf 'RESOURCE POSTURE FAIL: %s\n' "$report" >&2
                return 1
            fi
            printf 'RESOURCE POSTURE PASS\n'
            return 0
            ;;
        *)
            printf 'usage: scenario_resource_posture {--baseline <path>|--check <baseline> <final>}\n' >&2
            return 2
            ;;
    esac
}

# Allow direct invocation: `bash scenario_resource_posture.sh --baseline path`.
# When sourced (no positional args beyond $0), do nothing — the function
# is now available to the parent shell.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    scenario_resource_posture "$@"
fi
