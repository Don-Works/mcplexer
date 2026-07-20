#!/usr/bin/env bash
# lib_logwatch.sh — realistic log-history injection for the monitoring
# scenarios.
#
# ---------------------------------------------------------------------------
# WHY THERE IS NO test-ingest ENDPOINT HERE
# ---------------------------------------------------------------------------
# The collector has exactly ONE ingest path. `internal/logwatch/collect` dials
# a host over SSH and runs a fixed read-only command; `validateCollectedKind`
# explicitly REFUSES the `file` source kind, and no REST route or MCP tool
# appends log lines. So there was a choice: add a test-only ingest seam, or
# give the rig a host worth collecting from.
#
# A seam would prove the seam works. Everything the 2026-07-20 incident turned
# on lives BELOW it — the --since window, the inclusive/exclusive cursor
# boundary, timestamp parsing at byte zero, tail reconciliation, the byte cap,
# the day-history trigger. A fixture posted straight into the store skips all
# of it and the test passes on a daemon that could never collect a line.
#
# So the rig runs a REAL sshd (`log-host`, Dockerfile.loghost) whose `docker`
# CLI reads fixtures instead of a container runtime. Everything above the
# container runtime is the shipping code path.
#
# ---------------------------------------------------------------------------
# HOW THE TIME PROBLEM IS SOLVED
# ---------------------------------------------------------------------------
# Promotion needs 72h of arrivals and 7 gap-free days of history against a
# 7-day retention. Waiting is impossible; the daemon's clock is not injectable
# and moving it would desynchronise the whole 5-node rig.
#
# So the LINES are backdated, not the clock. This is sound here for one
# specific, checked reason: nothing downstream of the parser reads a wall
# clock for the evidence.
#
#   * collect.Line.TS is the timestamp parsed off the line, so log_lines.ts is
#     the LINE's time.
#   * log_template_days — the day history that outlives raw lines and gates
#     promotion — is filled by a SQL TRIGGER on INSERT INTO log_lines keyed on
#     substr(NEW.ts, 1, 10), i.e. that same value (migration 140).
#   * MineBaselineCandidates reads arrivals from log_lines.ts and day coverage
#     from log_template_days.
#
# Nine backdated days therefore produce byte-identical rows to nine observed
# ones, through the same code, with no special case anywhere.
#
# WHAT BACKDATING DOES NOT REACH, stated plainly so no scenario asserts on it:
#   * Retention pruning uses now()-RetentionDays, so a fixture must sit inside
#     the retention window or its oldest day is pruned in the same Ingest call
#     that wrote it. Nine days against the 7-day default is deliberate: the
#     day-history rows survive (they are never pruned) while log_lines is
#     trimmed to 7 — which is exactly the production steady state.
#   * The rate-spike detector compares windows measured from now(), so a
#     backdated error burst will NOT trip it. Errors that must fire have to be
#     injected at or near the anchor.
#
# ---------------------------------------------------------------------------
# Exposes:
#   logwatch_available          capability probe — is log-host in this compose?
#   logwatch_gen                fixture generator wrapper (see gen_logs.sh)
#   logwatch_authorize          install a node's agent key on log-host
#   logwatch_inject / _append   stream a fixture onto log-host
#   logwatch_set_state          override `docker inspect` (restart count etc.)
#   logwatch_set_events         supply `docker events` restart records
#   logwatch_provision_host     auth scope + remote_host row via REST
#   logwatch_provision_source   log_source row via REST
#   logwatch_wait_for_lines     poll until the collector has actually pulled
#
# Sourced by scenarios.sh after lib.sh; do not invoke directly.

LOGHOST_CONTAINER="${LOGHOST_CONTAINER:-mcplexer-test-log-host}"
LOGHOST_SSH_HOST="${LOGHOST_SSH_HOST:-log-host}"
LOGHOST_SSH_USER="${LOGHOST_SSH_USER:-logwatch}"
LOGWATCH_GEN="$(dirname "${BASH_SOURCE[0]}")/_fixtures/logwatch/gen_logs.sh"

# ----- capability probe ---------------------------------------------------
# Every helper below is a no-op-with-reason when the log-host service is not
# part of the running compose, so an older compose file SKIPs the monitoring
# scenarios instead of failing them.
logwatch_available() {
    docker inspect -f '{{.State.Running}}' "$LOGHOST_CONTAINER" 2>/dev/null \
        | grep -q true
}

# ----- fixture generation -------------------------------------------------
# logwatch_gen <shape> [flags...] — writes a fixture to stdout.
logwatch_gen() {
    "$LOGWATCH_GEN" "$@"
}

# ----- key + fixture plumbing --------------------------------------------
# logwatch_authorize <node_url> — authorise a node's own agent identity on
# log-host. The key is the one the container's entrypoint generated and loaded
# into its ssh-agent, so the daemon dials with the credential it actually
# holds; nothing is baked into an image or committed.
logwatch_authorize() {
    local container
    container=$(container_for "$1")
    [ -n "$container" ] || return 1
    docker exec "$container" cat /data/identity_ed25519.pub 2>/dev/null \
        | docker exec -i "$LOGHOST_CONTAINER" \
            sh -c 'cat >> /etc/loghost/authorized_keys && chmod 644 /etc/loghost/authorized_keys'
}

# logwatch_inject <selector> <file> — replace a container's log stream.
logwatch_inject() {
    docker exec -i "$LOGHOST_CONTAINER" \
        sh -c "cat > /fixtures/$1.log && chown logwatch:logwatch /fixtures/$1.log" < "$2"
}

# logwatch_append <selector> <file> — extend a stream in place. Used to make a
# signal go silent (or start erroring) DURING a run: the collector's next pull
# picks up only the new lines, because its cursor is already past the old
# ones. That is the collector's real incremental behaviour, not a reset.
logwatch_append() {
    docker exec -i "$LOGHOST_CONTAINER" sh -c "cat >> /fixtures/$1.log" < "$2"
}

# logwatch_set_state <selector> <container_id> <restart_count> <started_at>
# Overrides what `docker inspect` reports. A deploy scenario bumps the restart
# count and StartedAt so the collector observes a REAL lifecycle transition
# rather than inferring one from a gap in the lines.
logwatch_set_state() {
    docker exec "$LOGHOST_CONTAINER" \
        sh -c "printf '%s|%s|%s\n' '$2' '$3' '$4' > /fixtures/$1.state"
}

# logwatch_set_events <selector> <file> — supply `docker events` restart
# records, one "<stamp>|<action>|<actor_id>" per line.
logwatch_set_events() {
    docker exec -i "$LOGHOST_CONTAINER" sh -c "cat > /fixtures/$1.events" < "$2"
}

# logwatch_clear <selector> — drop every fixture artefact for one selector.
logwatch_clear() {
    docker exec "$LOGHOST_CONTAINER" \
        sh -c "rm -f /fixtures/$1.log /fixtures/$1.state /fixtures/$1.events"
}

# ----- REST provisioning --------------------------------------------------
# logwatch_provision_host <node_url> <workspace_id> <name> — creates the
# ssh_agent auth scope and the remote_host row, echoing the host id.
#
# The scope deliberately holds NO secret. sshx falls back to SSH_AUTH_SOCK
# when an ssh_agent scope has no socket_path, and the node's entrypoint
# exports exactly that before exec'ing the daemon — so this is the same
# credential resolution a real agent-based deployment uses.
logwatch_provision_host() {
    local node="$1" ws="$2" name="$3"
    local sresp scope_id hresp
    sresp=$(api POST "$node/api/v1/auth-scopes" \
        "$(jq -nc --arg n "logwatch-agent-$name" \
            '{name:$n, display_name:"logwatch ssh agent", type:"ssh_agent"}')" \
        2>/dev/null || echo '{}')
    scope_id=$(echo "$sresp" | jq -r '.id // empty')
    [ -n "$scope_id" ] || return 1
    hresp=$(api POST "$node/api/v1/remote-hosts" \
        "$(jq -nc --arg ws "$ws" --arg n "$name" --arg u "$LOGHOST_SSH_USER" \
             --arg h "$LOGHOST_SSH_HOST" --arg s "$scope_id" \
            '{workspace_id:$ws, name:$n, ssh_user:$u, ssh_host:$h,
              ssh_port:22, auth_scope_id:$s, enabled:true}')" \
        2>/dev/null || echo '{}')
    echo "$hresp" | jq -r '.id // empty'
}

# logwatch_provision_source <node> <ws> <host_id> <name> <selector> [spec] [retention_days]
#
# The schedule spec defaults to 20s. The collector's own tick is 15s, so that
# is about as fast as a source can legitimately be driven — the cadence being
# compressed is the COLLECTOR's, never the fixture's, whose geometry stays at
# the measured 5-minute production tick.
logwatch_provision_source() {
    local node="$1" ws="$2" host="$3" name="$4" selector="$5"
    local spec="${6:-20s}" days="${7:-7}"
    api POST "$node/api/v1/log-sources" \
        "$(jq -nc --arg ws "$ws" --arg h "$host" --arg n "$name" \
             --arg sel "$selector" --arg spec "$spec" --argjson days "$days" \
            '{workspace_id:$ws, remote_host_id:$h, name:$n, kind:"docker",
              selector:$sel, schedule_spec:$spec, retention_days:$days,
              enabled:true}')" \
        2>/dev/null | jq -r '.id // empty'
}

# ----- waiting ------------------------------------------------------------
# logwatch_templates <node> <workspace_id> [window] — the templates read
# surface, filtered to one source by the caller.
#
# The endpoint is workspace-scoped and filters on last_seen >= now-window, so
# the window must span the backdated fixture's TAIL, not its whole history:
# every shape here ends at the anchor, and 240h simply keeps a long-running
# suite from aging its own evidence out mid-run.
#
# Never propagates a non-zero status. scenarios.sh runs under `set -euo
# pipefail`, so a helper whose curl blips would abort the WHOLE suite from
# inside a single assertion — the same reason every lib.sh helper ends in
# `|| echo '{}'`.
logwatch_templates() {
    api GET "$1/api/v1/monitoring/templates?workspace_id=$2&window=${3:-240h}" \
        2>/dev/null || echo '{"templates":[]}'
}

# logwatch_count <node> <workspace_id> <source_id> — total lifetime lines
# across every template on one source. Echoes 0 rather than failing.
logwatch_count() {
    logwatch_templates "$1" "$2" \
        | jq -r --arg s "$3" '[.templates[]? | select(.source_id == $s) | .count] | add // 0' \
          2>/dev/null || echo 0
}

# logwatch_wait_for_lines <node> <workspace_id> <source_id> <min> [timeout_s]
#
# Polls the shipped read surface rather than the store, so a pass means the
# lines are queryable the way an operator would query them — and, more to the
# point, that the SSH pull really ran. Returns 0 once the source owns at least
# <min> distinct templates.
logwatch_wait_for_lines() {
    local node="$1" ws="$2" src="$3" want="$4" timeout="${5:-120}"
    local i=0 n
    while [ "$i" -lt "$timeout" ]; do
        n=$(logwatch_templates "$node" "$ws" \
            | jq -r --arg s "$src" '[.templates[]? | select(.source_id == $s)] | length' \
              2>/dev/null || echo 0)
        # jq can emit an empty string on a malformed payload; normalise before
        # the numeric comparison so `set -e` never trips on `[: -ge: ...`.
        case "$n" in ''|*[!0-9]*) n=0 ;; esac
        [ -n "$n" ] || n=0
        if [ "$n" -ge "$want" ]; then
            return 0
        fi
        sleep 3
        i=$((i + 3))
    done
    return 1
}

# logwatch_cursor <node> <source_id> — the source's persisted pull cursor, or
# empty when it has never successfully pulled. The cheapest proof that the SSH
# path ran at all.
logwatch_cursor() {
    api GET "$1/api/v1/log-sources/$2" 2>/dev/null \
        | jq -r '.cursor_ts // empty' 2>/dev/null || echo ""
}
