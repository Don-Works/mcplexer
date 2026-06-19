#!/usr/bin/env bash
# scenario_consent_audit.sh — Group C / C2 of the bulletproof e2e suite.
#
# THE LOAD-BEARING ASSERTION: every cross-boundary audit row (kind in
# {skill_share, memory_share, task_offer, mesh_direct}) on every node
# MUST carry an `accepted_by` envelope that matches the trust tier:
#
#   Tier 1 (same user, two machines)         → accepted_by.kind = "auto_pair"
#   Tier 2 (same org, different users)       → accepted_by.kind = "human"
#                                              + non-empty user_id
#                                              + non-empty agent_id
#                                              + valid ISO-8601 timestamp
#   Tier 3 (cross-org, different users)      → same as Tier 2
#
# Any cross-boundary audit row missing `accepted_by` is a HARD FAIL —
# that's the contract that data NEVER moves across a trust boundary
# without an explicit (human) or implicit (same-user-machines)
# acknowledgement.
#
# Today's state: the audit schema (store/models.go::AuditRecord) carries
# `tool_name`, `actor_kind`, `actor_id`, `status`, `params_redacted`,
# `correlation_id`, etc — but does NOT yet expose `tier` or
# `accepted_by` as first-class fields. Per the brief: each absent field
# becomes a SKIP+PENDING note pointing at epic
# 01KSK91Q4W8TNED9MAF0CTRVKC. We don't pretend it passed.
#
# Precondition: assumes B1/B2/B3 (Group B) already ran and produced
# cross-boundary audit rows. If not, this scenario falls back to
# scanning whatever is in the audit log without seeding new rows — the
# contract check is "every cross-boundary row that EXISTS carries
# accepted_by", regardless of who produced it.

# ----- helpers -----------------------------------------------------------

# CROSS_BOUNDARY_TOOL_NAMES — the wire-layer audit kinds that count as
# cross-boundary shares. Mirrors the writers in
# cmd/mcplexer/{skill_share,memory_share_wire,task_share_wire}.go and
# internal/mesh/audit.go. mesh__send is treated as the wire identity for
# `mesh_direct` (we narrow to recipient-addressed rows below).
CROSS_BOUNDARY_TOOL_NAMES='mesh__skill_share mesh__memory_share mesh__task_share mesh__send'

# audit_kind_of normalises a row's tool_name into the brief's logical
# `kind` vocabulary (skill_share | memory_share | task_offer |
# mesh_direct | "").
audit_kind_of() {
    case "$1" in
        mesh__skill_share)  echo "skill_share"  ;;
        mesh__memory_share) echo "memory_share" ;;
        mesh__task_share)   echo "task_offer"   ;;
        mesh__send)         echo "mesh_direct"  ;;
        *)                  echo ""              ;;
    esac
}

# is_recipient_addressed_mesh returns 0 if a `mesh__send` row was
# specifically peer-addressed (recipient.kind == "peer"), not a
# broadcast or audience match. Audit rows for broadcast sends are NOT
# cross-boundary — broadcasts are public-by-design and don't need a
# per-recipient acknowledgement. We only enforce `accepted_by` on
# peer-addressed direct mesh sends.
#
# Best-effort: if the row doesn't carry enough info to decide, we skip
# (returns 1, "ambiguous").
is_recipient_addressed_mesh() {
    local row="$1"
    # params_redacted may itself be a JSON string (double-encoded) or an
    # object. Try the object path first; fall back to string-parse.
    local r1 r2
    r1=$(echo "$row" | jq -r '.params_redacted.recipient.kind // ""' 2>/dev/null)
    if [ -z "$r1" ]; then
        r2=$(echo "$row" \
            | jq -r '.params_redacted // ""' 2>/dev/null \
            | jq -r '.recipient.kind // ""' 2>/dev/null)
        r1="$r2"
    fi
    [ "$r1" = "peer" ]
}

# tier_of_audit_row returns "tier1"|"tier2"|"tier3"|"" using the audit
# row's actor_id (peer id) cross-referenced against the local
# /api/p2p/peers row's same_user flag + org metadata if present. If the
# audit row doesn't carry an actor_id we return "" (caller emits
# SKIP+PENDING — the audit schema needs ActorID populated on share
# writers).
tier_of_audit_row() {
    local node_url="$1"
    local row="$2"
    local peer_id
    peer_id=$(echo "$row" | jq -r '.actor_id // ""')
    if [ -z "$peer_id" ]; then
        echo ""
        return
    fi
    local prow
    prow=$(peers_row_for "$node_url" "$peer_id" 2>/dev/null)
    if [ -z "$prow" ] || [ "$prow" = "null" ]; then
        echo ""
        return
    fi
    local same_user
    same_user=$(echo "$prow" | jq -r '.same_user // false')
    if [ "$same_user" = "true" ]; then
        echo "tier1"; return
    fi
    # Daemon doesn't yet expose org on the peer row; the test-side
    # tier_topology.sh map is the canonical answer. node_org() maps the
    # LOCAL node URL — for the remote peer we'd need a reverse lookup
    # peer_id → node_url. That's nontrivial without an inventory, so we
    # default to tier2 (same-org) when same_user=false. Tier 3 distinction
    # is the aspirational case the brief calls out as PENDING via the
    # "if schema doesn't carry tier" branch below.
    echo "tier2"
}

# assert_accepted_by checks ONE audit row's accepted_by envelope. The
# brief mandates four sub-fields on Tier 2/3:
#   .accepted_by.kind == "human"
#   .accepted_by.user_id   non-empty
#   .accepted_by.agent_id  non-empty
#   .accepted_by.timestamp valid ISO-8601-ish
# On Tier 1 the only requirement is .accepted_by.kind == "auto_pair".
# Each absent sub-field → SKIP+PENDING (epic 01KSK91Q4W8TNED9MAF0CTRVKC),
# matching the brief.
assert_accepted_by() {
    local label="$1"
    local row="$2"
    local tier="$3"
    local epic="01KSK91Q4W8TNED9MAF0CTRVKC"

    if ! echo "$row" | jq -e 'has("accepted_by")' >/dev/null 2>&1; then
        if [ "$tier" = "" ]; then
            skip "$label accepted_by" \
                "audit schema missing accepted_by AND tier — PENDING (epic $epic)"
        else
            # tier is known on the test side but absent from the row →
            # the contract is violated. Hard FAIL per the brief.
            fail "$label accepted_by" \
                "tier=$tier row has NO accepted_by — consent contract violated (epic $epic)"
        fi
        return
    fi

    local akind
    akind=$(echo "$row" | jq -r '.accepted_by.kind // ""')

    case "$tier" in
        tier1)
            if [ "$akind" = "auto_pair" ]; then
                pass "$label tier1 accepted_by.kind=auto_pair"
            else
                fail "$label tier1 accepted_by.kind" \
                    "want auto_pair, got '$akind' (epic $epic)"
            fi
            ;;
        tier2|tier3)
            if [ "$akind" = "human" ]; then
                pass "$label $tier accepted_by.kind=human"
            else
                fail "$label $tier accepted_by.kind" \
                    "want human, got '$akind' (epic $epic)"
            fi
            local uid aid ts
            uid=$(echo "$row" | jq -r '.accepted_by.user_id // ""')
            aid=$(echo "$row" | jq -r '.accepted_by.agent_id // ""')
            ts=$(echo "$row" | jq -r '.accepted_by.timestamp // ""')
            [ -n "$uid" ] && pass "$label $tier accepted_by.user_id=$uid" \
                || fail "$label $tier accepted_by.user_id missing/empty" "(epic $epic)"
            [ -n "$aid" ] && pass "$label $tier accepted_by.agent_id=$aid" \
                || fail "$label $tier accepted_by.agent_id missing/empty" "(epic $epic)"
            # ISO-8601 sanity: 4 digits, dash, 2 digits, dash, 2 digits,
            # T, time. Don't require sub-second / Z — just a reasonable
            # timestamp shape. date(1) parse would be ideal but BSD vs
            # GNU date disagrees on flags.
            if echo "$ts" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}'; then
                pass "$label $tier accepted_by.timestamp valid"
            else
                fail "$label $tier accepted_by.timestamp invalid" \
                    "got '$ts' (want ISO-8601, epic $epic)"
            fi
            ;;
        "")
            # We couldn't determine the tier (no actor_id, or no peers
            # row). The accepted_by envelope exists — but we can't
            # validate it against the right tier rule. SKIP+PENDING.
            skip "$label accepted_by tier resolution" \
                "tier unknown for this row — actor_id or peer-row missing (epic $epic)"
            ;;
    esac
}

# walk_audit_node iterates one node's audit rows, applies the per-row
# contract, and emits PASS/FAIL/SKIP for every cross-boundary kind.
walk_audit_node() {
    local node_url="$1"
    local node_lbl="$2"
    local epic="01KSK91Q4W8TNED9MAF0CTRVKC"

    local body
    body=$(api GET "$node_url/api/v1/audit?limit=1000" 2>/dev/null || echo "{}")
    local rows
    rows=$(echo "$body" | jq -c '.data // [] | .[]' 2>/dev/null)
    if [ -z "$rows" ]; then
        skip "$node_lbl audit walk" "no audit rows returned"
        return
    fi

    local saw_any="false"
    while IFS= read -r row; do
        [ -z "$row" ] && continue
        local tn
        tn=$(echo "$row" | jq -r '.tool_name // ""' 2>/dev/null)
        local kind
        kind=$(audit_kind_of "$tn")
        if [ -z "$kind" ]; then
            continue
        fi
        # Narrow mesh__send to peer-addressed rows (mesh_direct only —
        # broadcasts are not cross-boundary).
        if [ "$tn" = "mesh__send" ] && ! is_recipient_addressed_mesh "$row"; then
            continue
        fi
        saw_any="true"

        # Tier resolution. The audit row needs actor_id (the remote
        # peer id) to look up the local /api/p2p/peers row.
        local tier
        tier=$(tier_of_audit_row "$node_url" "$row")
        local rid
        rid=$(echo "$row" | jq -r '.id // empty')
        local label="$node_lbl kind=$kind id=${rid:-?}"

        assert_accepted_by "$label" "$row" "$tier"
    done <<< "$rows"

    if [ "$saw_any" = "false" ]; then
        skip "$node_lbl cross-boundary audit walk" \
            "no rows with kind in (skill_share, memory_share, task_offer, mesh_direct) on this node — B1/B2/B3 may not have seeded yet (epic $epic)"
    fi
}

# seed_one_share is a defensive fallback when the precondition (B1/B2/B3
# ran first) doesn't hold. Fires a single best-effort cross-boundary
# share so the audit walk has at least one row to assert against. We
# DON'T fail when this produces nothing — if the wire-layer can't even
# enqueue an audit row, that's a different scenario's job.
seed_one_share() {
    if [ -z "${PID_A:-}" ] || [ -z "${PID_C:-}" ]; then
        return
    fi
    if [ -z "${WS_ALPHA:-}" ]; then
        return
    fi
    # Tasks offer A→C (Tier 2) — the cheapest cross-boundary write we
    # can do that produces an audit row on BOTH ends.
    local title="consent-audit-seed-$RANDOM"
    local cresp tid
    cresp=$(api POST "$NODE_A/api/v1/tasks" \
        "$(jq -nc --arg ws "$WS_ALPHA" --arg t "$title" \
            '{workspace_id:$ws, title:$t, status:"open"}')" 2>/dev/null) || true
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty' 2>/dev/null)
    [ -z "$tid" ] && return
    api POST "$NODE_A/api/v1/tasks/offers" \
        "$(jq -nc --arg ws "$WS_ALPHA" --arg to "$PID_C" --arg tid "$tid" \
            '{workspace_id:$ws, task_id:$tid, to_peer_id:$to,
              message:"consent audit seed"}')" >/dev/null 2>&1 || true
    sleep 3  # let the audit writer settle
}

# ----- main scenario -----------------------------------------------------

scenario_consent_audit() {
    step C2 "every cross-boundary audit row carries accepted_by"

    # Defensive: seed one share if B1/B2/B3 haven't already done so.
    # Idempotent — extra rows don't change the contract.
    seed_one_share

    # Walk every node we have a token for. Tier-1 same-user pairs (A/B,
    # D/E) and Tier-2/3 cross-user pairs (A/C, A/D, ...) all produce
    # rows we care about — the assertion is global.
    walk_audit_node "$NODE_A" "node-a"
    walk_audit_node "$NODE_B" "node-b"
    walk_audit_node "$NODE_C" "node-c"
    if [ -n "${NODE_D:-}" ] && [ -n "${TOK_D:-}" ]; then
        walk_audit_node "$NODE_D" "node-d"
    fi
    if [ -n "${NODE_E:-}" ] && [ -n "${TOK_E:-}" ]; then
        walk_audit_node "$NODE_E" "node-e"
    fi
}
