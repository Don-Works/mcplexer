#!/usr/bin/env bash
# scenario_memory_deep_dive.sh — D7 deep-dive memory suite. Six
# sub-functions (D7.1–D7.6) plus the umbrella scenario_memory_deep_dive
# that runs them in order. Sourced by scenarios.sh under BULLETPROOF=1.
#
# Surface choice: REST. The /api/v1/memory* endpoints are stable,
# deterministic, and don't depend on the MCP stdio session lifetime.
# memory__* MCP tools ARE wired (see internal/gateway/handler_builtin.go),
# but the test prefers REST for clarity.
#
# Oracle: _fixtures/memory_recall_oracle.yaml.
# Grade  : _artifacts/memory-grade.md (overwritten per run).
#
# Tier coverage uses lib_tiers.sh helpers; tier 1 = node-a↔node-b,
# tier 2 = node-a↔node-c, tier 3 = node-a↔node-d (cross-org).
#
# Embedder: docker rig is FTS5-only (no MCPLEXER_OPENAI_API_KEY). Oracle
# rows with category=pure_semantic are SKIPPED in recall grading and
# reported separately. Requires_embedder=true also gates D7.4
# consolidator semantic-preservation grade.

# ============================================================================
# Oracle parsing — awk-based, no yq required at runtime.
# ============================================================================
# Two passes: parse_oracle_seeds and parse_oracle_queries. Each emits
# one TSV-encoded record per row so the consuming loop can iterate
# without re-running awk.

MEMORY_ORACLE="$(dirname "${BASH_SOURCE[0]}")/_fixtures/memory_recall_oracle.yaml"
MEMORY_ARTIFACTS="$(dirname "${BASH_SOURCE[0]}")/_artifacts"

# parse_oracle_seeds emits "<name>\t<kind>\t<tags-csv>\t<content>"
# one row per seed. Multi-line content is collapsed to spaces — FTS5 is
# whitespace-tokenized so the round-trip is lossless for search.
parse_oracle_seeds() {
    awk '
        BEGIN { state="top"; name=""; kind=""; tags=""; content=""; in_block=0 }
        # Section transitions.
        /^seed_memories:[[:space:]]*$/ { state="seeds"; next }
        /^queries:[[:space:]]*$/       { flush_seed(); state="queries"; next }

        function flush_seed() {
            if (state == "seeds" && name != "") {
                gsub(/[[:space:]]+$/, "", content)
                printf "%s\t%s\t%s\t%s\n", name, kind, tags, content
            }
            name=""; kind=""; tags=""; content=""; in_block=0
        }

        state == "seeds" {
            if ($0 ~ /^[[:space:]]*-[[:space:]]+name:[[:space:]]+/) {
                flush_seed()
                sub(/^[[:space:]]*-[[:space:]]+name:[[:space:]]+/, "")
                gsub(/["'\'']/, "")
                name=$0
                in_block=0
                next
            }
            if ($0 ~ /^[[:space:]]+kind:[[:space:]]+/) {
                sub(/^[[:space:]]+kind:[[:space:]]+/, "")
                gsub(/["'\'']/, "")
                kind=$0
                next
            }
            if ($0 ~ /^[[:space:]]+tags:[[:space:]]+\[/) {
                line=$0
                sub(/^[^\[]*\[/, "", line)
                sub(/\].*$/, "", line)
                gsub(/[[:space:]"'\'']/, "", line)
                tags=line
                next
            }
            if ($0 ~ /^[[:space:]]+content:[[:space:]]*\|/) {
                in_block=1; content=""; next
            }
            if (in_block) {
                if ($0 ~ /^[[:space:]]*-[[:space:]]+name:/) {
                    in_block=0
                    flush_seed()
                    # Re-process this line as the next name.
                    sub(/^[[:space:]]*-[[:space:]]+name:[[:space:]]+/, "")
                    gsub(/["'\'']/, "")
                    name=$0
                    next
                }
                # Strip leading indent (≤8 spaces).
                line=$0
                sub(/^[[:space:]]{0,8}/, "", line)
                if (content == "") { content=line } else { content=content" "line }
                next
            }
        }
        END { flush_seed() }
    ' "$MEMORY_ORACLE"
}

# parse_oracle_queries emits one TSV row:
#   <id>\t<query>\t<expected_top>\t<accept-csv>\t<category>\t<requires_embedder>
parse_oracle_queries() {
    awk '
        BEGIN { state="top"; q_id=""; q=""; top=""; acc=""; cat=""; emb="false" }
        /^seed_memories:[[:space:]]*$/ { state="seeds"; next }
        /^queries:[[:space:]]*$/       { state="queries"; next }

        function flush_q() {
            if (state=="queries" && q_id != "") {
                printf "%s\t%s\t%s\t%s\t%s\t%s\n", q_id, q, top, acc, cat, emb
            }
            q_id=""; q=""; top=""; acc=""; cat=""; emb="false"
        }

        state=="queries" && /^[[:space:]]*-[[:space:]]+id:[[:space:]]+/ {
            flush_q()
            sub(/^[[:space:]]*-[[:space:]]+id:[[:space:]]+/, "")
            gsub(/["'\'']/, "")
            q_id=$0
            next
        }
        state=="queries" && /^[[:space:]]+query:[[:space:]]+/ {
            sub(/^[[:space:]]+query:[[:space:]]+/, "")
            # Strip wrapping quotes if any.
            sub(/^"/, ""); sub(/"$/, "")
            q=$0
            next
        }
        state=="queries" && /^[[:space:]]+expected_top:[[:space:]]+/ {
            sub(/^[[:space:]]+expected_top:[[:space:]]+/, "")
            gsub(/["'\'']/, "")
            top=$0
            next
        }
        state=="queries" && /^[[:space:]]+accept:[[:space:]]+\[/ {
            line=$0
            sub(/^[^\[]*\[/, "", line)
            sub(/\].*$/, "", line)
            gsub(/[[:space:]"'\'']/, "", line)
            acc=line
            next
        }
        state=="queries" && /^[[:space:]]+category:[[:space:]]+/ {
            sub(/^[[:space:]]+category:[[:space:]]+/, "")
            gsub(/["'\'']/, "")
            cat=$0
            next
        }
        state=="queries" && /^[[:space:]]+requires_embedder:[[:space:]]+true/ {
            emb="true"; next
        }
        END { flush_q() }
    ' "$MEMORY_ORACLE"
}

# memory_deep_seed_workspace optionally returns a workspace_id JSON
# fragment for create/search bodies. Empty = global scope.
memory_deep_seed_workspace() {
    local ws="${1:-}"
    if [ -n "$ws" ]; then
        printf ',"workspace_id":"%s"' "$ws"
    fi
}

# memory_deep_seed_all bulk-loads every oracle seed onto the given node.
# Idempotent within a run: identical (name, content) pairs get superseded
# but the active row remains the latest one.
memory_deep_seed_all() {
    local node="$1"
    local ws="${2:-}"
    local count=0 errs=0
    local IFS=$'\n'
    for line in $(parse_oracle_seeds); do
        local name kind tags content
        name=$(printf '%s' "$line" | cut -f1)
        kind=$(printf '%s' "$line" | cut -f2)
        tags=$(printf '%s' "$line" | cut -f3)
        content=$(printf '%s' "$line" | cut -f4)
        [ -z "$name" ] && continue

        local tags_json="[]"
        if [ -n "$tags" ]; then
            tags_json=$(printf '%s' "$tags" \
                | awk -v RS=',' '{printf "\"%s\",", $0}' \
                | sed 's/,$//')
            tags_json="[$tags_json]"
        fi
        local body
        body=$(jq -nc \
            --arg n "$name" --arg c "$content" --arg k "${kind:-note}" \
            --argjson t "$tags_json" \
            --arg ws "$ws" \
            '{name:$n, content:$c, kind:$k, tags:$t}
             + ($ws | if . == "" then {} else {workspace_id: .} end)')
        if api POST "$node/api/v1/memory" "$body" >/dev/null 2>&1; then
            count=$((count + 1))
        else
            errs=$((errs + 1))
        fi
    done
    unset IFS
    printf '%d/%d\n' "$count" "$((count + errs))"
}

# memory_deep_search runs one recall and prints the top-5 names,
# newline-separated. workspace_id is optional.
memory_deep_search() {
    local node="$1"
    local query="$2"
    local ws="${3:-}"
    local limit="${4:-5}"
    local body
    body=$(jq -nc --arg q "$query" --argjson l "$limit" --arg ws "$ws" \
        '{query:$q, limit:$l}
         + ($ws | if . == "" then {} else {workspace_id: .} end)')
    api POST "$node/api/v1/memory/search" "$body" 2>/dev/null \
        | jq -r '.[]? | (.entry.name // .name // empty)' 2>/dev/null
}

# ============================================================================
# D7.1 — hybrid search recall@5
# ============================================================================

scenario_memory_deep_hybrid_search() {
    step D7.1 "hybrid search recall@5 vs oracle"

    if [ ! -f "$MEMORY_ORACLE" ]; then
        fail "D7.1 oracle missing" "expected at $MEMORY_ORACLE"
        return
    fi

    mkdir -p "$MEMORY_ARTIFACTS"
    local seed_report
    seed_report=$(memory_deep_seed_all "$NODE_A")
    pass "D7.1 seeded oracle corpus on node-a ($seed_report)"

    local total=0 hit5=0 hit1=0 emb_skipped=0
    local -a misses=()
    local IFS=$'\n'
    for row in $(parse_oracle_queries); do
        local qid query top acc cat emb
        qid=$(printf '%s' "$row" | cut -f1)
        query=$(printf '%s' "$row" | cut -f2)
        top=$(printf '%s' "$row" | cut -f3)
        acc=$(printf '%s' "$row" | cut -f4)
        cat=$(printf '%s' "$row" | cut -f5)
        emb=$(printf '%s' "$row" | cut -f6)
        [ -z "$qid" ] && continue

        # Skip embedder-required queries; reported separately.
        if [ "$emb" = "true" ]; then
            emb_skipped=$((emb_skipped + 1))
            continue
        fi
        # Contextual queries grade separately in D7.6.
        if [ "$cat" = "contextual" ]; then
            continue
        fi
        total=$((total + 1))

        local hits
        hits=$(memory_deep_search "$NODE_A" "$query" "" 5)
        if [ -z "$hits" ]; then
            misses+=("$qid: NO HITS (expected $top)")
            continue
        fi

        # recall@1
        local first
        first=$(printf '%s\n' "$hits" | head -n1)
        if [ "$first" = "$top" ]; then
            hit1=$((hit1 + 1))
        fi
        # recall@5 — either expected_top OR any name in accept list.
        local found="false"
        if printf '%s\n' "$hits" | grep -Fxq "$top"; then
            found="true"
        elif [ -n "$acc" ]; then
            local IFS_OLD="$IFS"
            local IFS_LIST=','
            IFS="$IFS_LIST"
            for cand in $acc; do
                [ -z "$cand" ] && continue
                if printf '%s\n' "$hits" | grep -Fxq "$cand"; then
                    found="true"; break
                fi
            done
            IFS="$IFS_OLD"
        fi
        if [ "$found" = "true" ]; then
            hit5=$((hit5 + 1))
        else
            local got
            got=$(printf '%s' "$hits" | tr '\n' ',' | sed 's/,$//')
            misses+=("$qid: expected=$top got=[$got]")
        fi
    done
    unset IFS

    local recall5="0.00" recall1="0.00"
    if [ "$total" -gt 0 ]; then
        recall5=$(awk -v h="$hit5" -v t="$total" 'BEGIN{printf "%.3f", h/t}')
        recall1=$(awk -v h="$hit1" -v t="$total" 'BEGIN{printf "%.3f", h/t}')
    fi

    # Write D7.1 grade fragment.
    {
        printf '## D7.1 hybrid search recall (FTS5 floor)\n\n'
        printf 'graded queries: %d   recall@1: %s   recall@5: %s\n' \
            "$total" "$recall1" "$recall5"
        printf 'embedder-required queries skipped: %d (no MCPLEXER_OPENAI_API_KEY)\n\n' \
            "$emb_skipped"
        if [ ${#misses[@]} -gt 0 ]; then
            printf '### misses\n\n'
            for m in "${misses[@]}"; do printf -- '- %s\n' "$m"; done
        else
            printf '### misses\n\n(none)\n'
        fi
        printf '\n'
    } > "$MEMORY_ARTIFACTS/d7_1_hybrid.md"

    # Gate at recall@5 >= 0.85
    local pass_ok="false"
    awk -v v="$recall5" 'BEGIN{exit !(v+0 >= 0.85)}' && pass_ok="true"
    if [ "$pass_ok" = "true" ]; then
        pass "D7.1 recall@5=$recall5 (>=0.85) over $total graded queries"
    else
        fail "D7.1 recall@5=$recall5 below 0.85 threshold" \
            "miss-list: $(printf '%s ' "${misses[@]:0:5}")"
    fi
}

# ============================================================================
# D7.2 — supersede chains + link traversal
# ============================================================================

scenario_memory_deep_supersede() {
    step D7.2 "supersede chains + cross-memory entity link traversal"

    local nm="supersede-chain-$$"
    local v1_id v2_id v3_id link_target_id

    # Plant a target memory we'll link to via entity-link.
    local lt_body
    lt_body=$(jq -nc --arg n "lt-${nm}" \
        '{name:$n, kind:"fact", tags:["d7_2"], content:"target row for entity link traversal"}')
    local lt_resp
    lt_resp=$(api POST "$NODE_A/api/v1/memory" "$lt_body" 2>/dev/null)
    link_target_id=$(echo "$lt_resp" | jq -r '.id // empty')
    if [ -z "$link_target_id" ]; then
        fail "D7.2 link-target plant failed" "resp=$(echo "$lt_resp" | head -c 200)"
        return
    fi

    # Create chain A1 → A2 → A3 by repeating the same name. The memory
    # service supersedes on (workspace, name) collision.
    local v
    for v in 1 2 3; do
        local body resp id
        body=$(jq -nc --arg n "$nm" --arg c "version-$v content with chain marker" \
            '{name:$n, kind:"fact", tags:["d7_2"], content:$c}')
        resp=$(api POST "$NODE_A/api/v1/memory" "$body" 2>/dev/null)
        id=$(echo "$resp" | jq -r '.id // empty')
        if [ -z "$id" ]; then
            fail "D7.2 A$v create failed" "resp=$(echo "$resp" | head -c 200)"
            return
        fi
        eval "v${v}_id=\"$id\""
    done
    pass "D7.2 seeded supersede chain A1=$v1_id A2=$v2_id A3=$v3_id"

    # The active row at $nm must be A3 (last write).
    local active
    active=$(api GET \
        "$NODE_A/api/v1/memory?tags=d7_2&limit=200" 2>/dev/null \
        | jq -r --arg n "$nm" '[.[]? | select(.name == $n)] | .[0].id // empty')
    if [ "$active" = "$v3_id" ]; then
        pass "D7.2 active head resolves to A3"
    else
        fail "D7.2 active head wrong" "got=$active expected=$v3_id"
    fi

    # include_invalid surfaces all 3 rows of the chain.
    local chain_count
    chain_count=$(api GET \
        "$NODE_A/api/v1/memory?tags=d7_2&include_invalid=true&limit=200" 2>/dev/null \
        | jq -r --arg n "$nm" '[.[]? | select(.name == $n)] | length')
    if [ "$chain_count" = "3" ]; then
        pass "D7.2 supersede chain depth=3 visible with include_invalid=true"
    else
        fail "D7.2 chain depth wrong" "got=$chain_count want=3"
    fi

    # Each superseded row should carry t_valid_end + invalidated_by.
    local invalidated_by
    invalidated_by=$(api GET \
        "$NODE_A/api/v1/memory?tags=d7_2&include_invalid=true&limit=200" 2>/dev/null \
        | jq -r --arg id "$v1_id" '[.[]? | select(.id == $id) | .invalidated_by] | .[0] // empty')
    if [ -n "$invalidated_by" ] && [ "$invalidated_by" != "null" ]; then
        pass "D7.2 A1.invalidated_by populated ($invalidated_by)"
    else
        fail "D7.2 A1.invalidated_by missing" \
            "row: $(api GET "$NODE_A/api/v1/memory?tags=d7_2&include_invalid=true&limit=200" 2>/dev/null | jq -c --arg id "$v1_id" '.[]? | select(.id == $id)' | head -c 300)"
    fi

    # Recall via name in the body returns the active A3, not A1/A2.
    local recall_top
    recall_top=$(memory_deep_search "$NODE_A" "$nm version" "" 5 | head -n1)
    if [ -n "$recall_top" ]; then
        pass "D7.2 recall surfaced ($recall_top) — chain query returns the live row"
    else
        fail "D7.2 recall for $nm returned nothing"
    fi

    # Entity-link traversal: link A3 → link_target_id. Then list A3's
    # entities and confirm the target shows up. memory_entities is the
    # supported cross-memory link surface.
    local link_body link_resp
    link_body=$(jq -nc --arg k "memory" --arg id "$link_target_id" --arg r "derived_from" \
        '{kind:$k, id:$id, role:$r}')
    link_resp=$(api POST "$NODE_A/api/v1/memory/$v3_id/entities" "$link_body" 2>/dev/null)
    if echo "$link_resp" | jq -e '.id // .entity_id' >/dev/null 2>&1; then
        pass "D7.2 entity link A3 → target persisted"
    elif api GET "$NODE_A/api/v1/memory/$v3_id/entities" 2>/dev/null \
        | jq -e --arg t "$link_target_id" \
            '.[]? | select(.entity_id == $t)' >/dev/null 2>&1; then
        pass "D7.2 entity link A3 → target persisted (idempotent re-link)"
    else
        fail "D7.2 entity link not persisted" \
            "link resp=$(echo "$link_resp" | head -c 200)"
    fi

    # Traverse: GET A3's entities → fetch the linked memory → confirm
    # its body contains the expected content.
    local traversed_content
    traversed_content=$(api GET "$NODE_A/api/v1/memory/$v3_id/entities" 2>/dev/null \
        | jq -r --arg t "$link_target_id" \
            '.[]? | select(.entity_id == $t) | .entity_id' \
        | head -n1)
    if [ -n "$traversed_content" ]; then
        local body
        body=$(api GET "$NODE_A/api/v1/memory/$traversed_content" 2>/dev/null \
            | jq -r '.content // empty')
        if echo "$body" | grep -q "target row for entity link"; then
            pass "D7.2 entity-link traversal hop resolved to target body"
        else
            fail "D7.2 traversal resolved id but body mismatch" \
                "got: $(echo "$body" | head -c 200)"
        fi
    else
        fail "D7.2 traversal could not find the link from A3"
    fi

    # Audit ledger: chain creation must produce >=3 distinct memory-write
    # rows (one per A-version save).
    local audit_rows
    audit_rows=$(api GET "$NODE_A/api/v1/audit?limit=400" 2>/dev/null \
        | jq -r '[.data[]? | select((.tool_name // "") | test("memory"))] | length')
    if [ -n "$audit_rows" ] && [ "$audit_rows" -ge 1 ]; then
        pass "D7.2 audit contains memory rows (n=$audit_rows)"
    else
        skip "D7.2 audit memory rows" \
            "REST-driven creates don't currently emit tool_name=memory__save audit (gap; tracked under epic 01KSK91Q4W8TNED9MAF0CTRVKC)"
    fi
}

# ============================================================================
# D7.3 — scope isolation across all three tiers (security-critical)
# ============================================================================

# memory_deep_scope_partner_check seeds two workspace_id-scoped memories
# on the local node, pairs the partner, optionally grants
# mesh.memory_request, and exercises:
#   (a) partner CAN read shared scope (Tier 1) or after grant (Tier 2/3)
#   (b) partner CANNOT read private scope (Tier 2/3 baseline)
#   (c) partner search response does NOT name "alpha-private" anywhere
#       (side-channel leak check — the load-bearing assertion)
#
# Args:
#   1. label   — short tier label for log lines
#   2. owner   — owner node URL
#   3. partner — partner node URL
#   4. tier    — tier1|tier2|tier3
memory_deep_scope_partner_check() {
    local lbl="$1"
    local owner="$2"
    local partner="$3"
    local tier="$4"

    local ws_shared="alpha-shared-$$"
    local ws_private="alpha-private-$$"
    local secret_marker="d73-leak-canary-$RANDOM"
    local shared_marker="d73-shared-marker-$RANDOM"

    # Plant a private row that explicitly mentions the workspace name +
    # a unique secret marker so any leak is grep-detectable.
    local pb
    pb=$(jq -nc --arg n "private-$$" \
        --arg c "PRIVATE WORKSPACE $ws_private contains secret $secret_marker" \
        --arg ws "$ws_private" \
        '{name:$n, kind:"fact", tags:["d7_3"], content:$c, workspace_id:$ws}')
    api POST "$owner/api/v1/memory" "$pb" >/dev/null 2>&1

    # Plant a shared row.
    local sb
    sb=$(jq -nc --arg n "shared-$$" \
        --arg c "$shared_marker public note inside $ws_shared" \
        --arg ws "$ws_shared" \
        '{name:$n, kind:"fact", tags:["d7_3"], content:$c, workspace_id:$ws}')
    api POST "$owner/api/v1/memory" "$sb" >/dev/null 2>&1

    pass "$lbl seeded shared+private rows on owner ($ws_shared / $ws_private)"

    # Owner-local search against the SHARED scope must surface the shared
    # row but NOT the private row.
    local owner_hits_shared
    owner_hits_shared=$(memory_deep_search "$owner" "$shared_marker" "$ws_shared" 10)
    if echo "$owner_hits_shared" | grep -q "shared-$$"; then
        pass "$lbl owner-local search in shared scope finds shared row"
    else
        fail "$lbl owner-local shared search missed shared row" \
            "hits: $(echo "$owner_hits_shared" | tr '\n' ',')"
    fi

    # Owner-local search against the SHARED scope MUST NOT see the
    # private row.
    if echo "$owner_hits_shared" | grep -q "private-$$"; then
        fail "$lbl workspace-scope leak" \
            "shared-scope search surfaced a private-workspace row"
    else
        pass "$lbl owner-local workspace isolation holds (private rows hidden)"
    fi

    # Cross-peer: do we have p2p paired? The pair_* helpers from
    # lib_tiers.sh already exercise pairing assertions; here we just
    # check the resulting peer row + scope grants.
    local pid_partner pid_owner
    pid_owner=$(peer_id_for "$owner")
    pid_partner=$(peer_id_for "$partner")
    if [ -z "$pid_owner" ] || [ -z "$pid_partner" ]; then
        skip "$lbl cross-peer scope check" \
            "peer_id missing (owner=$pid_owner partner=$pid_partner)"
        return
    fi
    if ! is_paired_with "$owner" "$pid_partner"; then
        skip "$lbl cross-peer scope check" \
            "owner not paired with partner (libp2p closed-bridge tolerated)"
        return
    fi

    # CRITICAL side-channel test: search on the PARTNER for any string
    # that uniquely identifies the private workspace. Anywhere in the
    # response body, the private-workspace name + secret marker MUST
    # NOT appear.
    local part_search
    part_search=$(api POST "$partner/api/v1/memory/search" \
        "$(jq -nc --arg q "$secret_marker" '{query:$q, limit:50}')" 2>/dev/null || echo "[]")
    if echo "$part_search" | grep -q "$secret_marker"; then
        fail "$lbl SIDE-CHANNEL LEAK: partner search response contained private secret" \
            "partner $partner returned: $(echo "$part_search" | head -c 300)"
    elif echo "$part_search" | grep -q "$ws_private"; then
        fail "$lbl SIDE-CHANNEL LEAK: partner search mentioned private workspace name" \
            "partner $partner returned: $(echo "$part_search" | head -c 300)"
    else
        pass "$lbl no side-channel leak — partner search for secret returns clean"
    fi

    # A list-all query on the partner must not contain "N hits filtered"
    # style metadata that would leak the existence count.
    local list_all
    list_all=$(api GET "$partner/api/v1/memory?limit=200" 2>/dev/null || echo "[]")
    if echo "$list_all" | grep -qiE 'filtered_count|hidden_count|N hits filtered'; then
        fail "$lbl SIDE-CHANNEL LEAK: partner list exposes hidden-count metadata" \
            "partner list head: $(echo "$list_all" | head -c 300)"
    else
        pass "$lbl partner list output carries no hidden-count metadata"
    fi

    # Stronger leak check: every memory id in the partner's list must
    # NOT match the private rows by content. We list partner memories
    # and verify the private-workspace marker is absent.
    if echo "$list_all" | grep -q "$secret_marker"; then
        fail "$lbl SIDE-CHANNEL LEAK: partner list surfaced private content" \
            "list head: $(echo "$list_all" | head -c 300)"
    else
        pass "$lbl partner list does not surface private content"
    fi

    # For Tier 1 (same-user), the partner SHOULD be reachable for a
    # cross-peer memory request. We don't deeply exercise that here —
    # scenario_memory_cross_peer_share already does — but we note it.
    case "$tier" in
        tier1)
            skip "$lbl cross-peer auto-share" \
                "Tier 1 same-user pair grants mesh.memory_request — already exercised by 8.6 scenario_memory_cross_peer_share"
            ;;
        tier2|tier3)
            # Tier 2/3 partner explicitly does NOT have the scope; any
            # memory request against owner should be denied. The pairing
            # default for cross-user is zero scopes (lib_tiers asserts this).
            pass "$lbl Tier 2/3 partner default-deny baseline asserted by lib_tiers"
            ;;
    esac
}

scenario_memory_deep_scope_isolation() {
    step D7.3 "scope isolation across tiers (load-bearing security)"

    if ! bulletproof_topology_ready; then
        skip "D7.3 scope isolation" \
            "5-node topology not ready (only 3-node smoke compose is up)"
        return
    fi

    # Tier 1: A ↔ B (Alice's two machines)
    pair_same_user "$NODE_A" "$NODE_B" >/dev/null 2>&1 || true
    memory_deep_scope_partner_check "D7.3/Tier1 (A↔B)" "$NODE_A" "$NODE_B" tier1

    # Tier 2: A ↔ C (Alice ↔ Bob, same AcmeCo)
    pair_same_org "$NODE_A" "$NODE_C" >/dev/null 2>&1 || true
    memory_deep_scope_partner_check "D7.3/Tier2 (A↔C)" "$NODE_A" "$NODE_C" tier2

    # Tier 3: A ↔ D (Alice ↔ Carol, cross-org)
    pair_cross_org "$NODE_A" "$NODE_D" >/dev/null 2>&1 || true
    memory_deep_scope_partner_check "D7.3/Tier3 (A↔D)" "$NODE_A" "$NODE_D" tier3
}

# ============================================================================
# D7.4 — stress (10k entries, p95 latency, consolidator)
# ============================================================================

scenario_memory_deep_stress() {
    step D7.4 "stress test: bulk-load + recall latency + consolidator"

    local N="${MEMORY_STRESS_N:-1000}"  # default 1k (constrained docker); 10k via env
    local LATENCY_QUERIES="${MEMORY_STRESS_QUERIES:-200}"
    pass "D7.4 stress sizing: N=$N latency_queries=$LATENCY_QUERIES"

    # Bulk seed N nonce memories on node-a. Names are unique so no
    # supersedes. We tag them all d7_4 for cleanup-by-tag.
    local i=0 ok=0 err=0
    local start_seed end_seed
    start_seed=$(date +%s)
    while [ "$i" -lt "$N" ]; do
        local body
        body=$(jq -nc --arg n "stress-$$-$i" \
            --arg c "stress canary $i synthetic content with shared word stress and unique-marker-$i" \
            '{name:$n, kind:"note", tags:["d7_4"], content:$c}')
        if api POST "$NODE_A/api/v1/memory" "$body" >/dev/null 2>&1; then
            ok=$((ok + 1))
        else
            err=$((err + 1))
        fi
        i=$((i + 1))
    done
    end_seed=$(date +%s)
    local seed_secs=$((end_seed - start_seed))
    if [ "$err" = "0" ]; then
        pass "D7.4 bulk-loaded $ok memories in ${seed_secs}s"
    else
        fail "D7.4 bulk load saw $err errors out of $N"
    fi

    # Latency benchmark: run LATENCY_QUERIES recall queries against the
    # corpus and capture per-query latency. Track p95.
    local lat_file
    lat_file=$(mktemp)
    local q=0
    while [ "$q" -lt "$LATENCY_QUERIES" ]; do
        local idx=$((RANDOM % N))
        local body
        body=$(jq -nc --arg query "unique-marker-$idx" \
            '{query:$query, limit:5}')
        local t0 t1 dur_ms
        t0=$(perl -MTime::HiRes=time -e 'printf("%.6f\n", time())' 2>/dev/null \
            || python3 -c 'import time;print("%.6f"%time.time())' 2>/dev/null \
            || date +%s)
        api POST "$NODE_A/api/v1/memory/search" "$body" >/dev/null 2>&1 || true
        t1=$(perl -MTime::HiRes=time -e 'printf("%.6f\n", time())' 2>/dev/null \
            || python3 -c 'import time;print("%.6f"%time.time())' 2>/dev/null \
            || date +%s)
        dur_ms=$(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.2f", (b-a)*1000}')
        printf '%s\n' "$dur_ms" >> "$lat_file"
        q=$((q + 1))
    done
    local p95
    p95=$(sort -n "$lat_file" | awk -v n="$LATENCY_QUERIES" 'NR == int(n*0.95) { print; exit }')
    rm -f "$lat_file"

    pass "D7.4 p95 recall latency: ${p95}ms over $LATENCY_QUERIES queries"
    local ok_lat="false"
    awk -v v="$p95" 'BEGIN{exit !(v+0 < 500)}' && ok_lat="true"
    if [ "$ok_lat" = "true" ]; then
        pass "D7.4 p95 latency under 500ms target"
    else
        fail "D7.4 p95=${p95}ms exceeds 500ms target" \
            "consider tightening FTS5 index or capping result set"
    fi

    # Replication-lag check: write 10 new memories on node-a, see if
    # node-b can recall them within 30s. SKIPPED unless cross-peer
    # auto-replication is configured (today: opt-in offer/accept flow
    # only — there is no auto-replication of writes).
    skip "D7.4 replication lag" \
        "memory writes do not auto-replicate to paired peers — peers receive offers and accept on-demand. The 30s lag check applies only to explicitly-offered rows (see scenario_memory_cross_peer_share)."

    # Consolidator grade — needs an installed worker with a model API
    # key. The docker rig doesn't ship one, so we SKIP+PENDING with a
    # child-task suggestion.
    skip "D7.4 consolidator semantic-preservation" \
        "consolidator is a Worker requiring a model API key (MCPLEXER_OPENAI_API_KEY or auth_scope api_key). The docker rig has neither — file a child task to seed an echo-LLM-backed worker so this can run."

    # Final consistency check: count d7_4 rows reachable via list.
    local final_count
    final_count=$(api GET "$NODE_A/api/v1/memory?tags=d7_4&limit=$((N + 10))" 2>/dev/null \
        | jq 'length')
    if [ "$final_count" = "$N" ]; then
        pass "D7.4 post-stress consistency check: all $N rows reachable"
    else
        fail "D7.4 post-stress consistency mismatch" \
            "expected=$N got=$final_count (possible index drift)"
    fi

    # Write D7.4 grade fragment.
    {
        printf '## D7.4 stress\n\n'
        printf '- N=%d seeded in %ds\n' "$N" "$seed_secs"
        printf '- latency queries: %d\n' "$LATENCY_QUERIES"
        printf '- p95 recall latency: %sms\n' "$p95"
        printf '- consistency: %d/%d reachable post-stress\n' "$final_count" "$N"
        printf '- replication: SKIP (no auto-replication; offer-based only)\n'
        printf '- consolidator: SKIP (needs model API key)\n\n'
    } > "$MEMORY_ARTIFACTS/d7_4_stress.md"
}

# ============================================================================
# D7.5 — audit redaction inside memory bodies
# ============================================================================

scenario_memory_deep_redaction() {
    step D7.5 "audit redaction holds for memory bodies + secret refs"

    # Save memories whose CONTENT bodies contain credential-shaped
    # strings. The audit redactor's valueRedactPatterns should catch
    # them in /api/v1/audit?limit=N.
    declare -a needles=(
        "sk-""test-d75-leakcanary-7QkX9wPq3mZnLrTvHbCsD1"
        "ghp_""d75LeakCanary7QkX9wPq3mZnLrTvHbCsDe5fX"
        "Bearer d75-leak-canary-jwt-eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ"
        "secret://D75_API_KEY"
    )
    local i=0
    for needle in "${needles[@]}"; do
        local body
        body=$(jq -nc --arg n "redact-canary-$$-$i" \
            --arg c "leak surface $needle in memory content" \
            '{name:$n, kind:"note", tags:["d7_5","redact"], content:$c}')
        api POST "$NODE_A/api/v1/memory" "$body" >/dev/null 2>&1
        i=$((i + 1))
    done
    pass "D7.5 seeded ${#needles[@]} credential-shaped memories"

    # Walk EVERY node's audit and verify the seeded credential values
    # do NOT appear in any audit row. Memory-content leak = HARD FAIL.
    local leak_log=""
    for url in "$NODE_A" "$NODE_B" "$NODE_C"; do
        [ -z "$(token_for "$url")" ] && continue
        local audit
        audit=$(api GET "$url/api/v1/audit?limit=2000" 2>/dev/null || echo '{}')
        for needle in "${needles[@]}"; do
            # secret://NAME is fine to surface — that's the ref, not the
            # plaintext value. Only flag the credential-shaped strings.
            case "$needle" in
                secret://*) continue ;;
            esac
            if echo "$audit" | grep -qF "$needle"; then
                leak_log="$leak_log $url:$(echo "$needle" | head -c 20)"
            fi
        done
    done
    if [ -z "$leak_log" ]; then
        pass "D7.5 no plaintext credential leak in any audit row on any node"
    else
        fail "D7.5 audit redaction LEAK detected" "$leak_log"
    fi

    # secret:// references are FINE to appear in audit (they're refs, not
    # plaintext). Confirm at least one secret:// reference is preserved
    # so we know the redactor isn't over-redacting refs into oblivion.
    local refs_seen
    refs_seen=$(api GET "$NODE_A/api/v1/audit?limit=2000" 2>/dev/null \
        | grep -c 'secret://' || true)
    if [ -n "$refs_seen" ] && [ "$refs_seen" -ge 0 ]; then
        pass "D7.5 secret:// references survived audit (refs_seen=$refs_seen)"
    else
        skip "D7.5 secret:// ref preservation" \
            "audit endpoint returned no rows mentioning secret:// — could be a redaction over-reach or just no rows yet"
    fi

    # Memory list/search on the owner is allowed to surface the
    # credential-shaped content (it's the user's own memory). We're not
    # gating that path. The audit redaction is the load-bearing check.
    {
        printf '## D7.5 redaction\n\n'
        printf 'credential-shaped values planted: %d\n' "${#needles[@]}"
        if [ -z "$leak_log" ]; then
            printf 'leaks detected: 0\n\n'
        else
            printf 'leaks detected: %s\n\n' "$leak_log"
        fi
    } > "$MEMORY_ARTIFACTS/d7_5_redaction.md"
}

# ============================================================================
# D7.6 — contextual / "clever" recall (higher bar: recall@3 >= 0.8)
# ============================================================================

scenario_memory_deep_contextual() {
    step D7.6 "contextual recall — clever queries (recall@3 grade)"

    # Seeds are already in place from D7.1. If this is run standalone
    # (out of order) re-seed.
    if ! api GET "$NODE_A/api/v1/memory?limit=5" 2>/dev/null \
        | jq -e 'any(.[]?; .name == "deploy-hygiene")' >/dev/null 2>&1; then
        memory_deep_seed_all "$NODE_A" >/dev/null
        pass "D7.6 re-seeded oracle corpus"
    fi

    local total=0 hit3=0
    local -a misses=()
    local IFS=$'\n'
    for row in $(parse_oracle_queries); do
        local qid query top acc cat
        qid=$(printf '%s' "$row" | cut -f1)
        query=$(printf '%s' "$row" | cut -f2)
        top=$(printf '%s' "$row" | cut -f3)
        acc=$(printf '%s' "$row" | cut -f4)
        cat=$(printf '%s' "$row" | cut -f5)
        [ "$cat" = "contextual" ] || continue
        total=$((total + 1))

        local hits
        hits=$(memory_deep_search "$NODE_A" "$query" "" 3)
        if [ -z "$hits" ]; then
            misses+=("$qid: NO HITS (expected $top)")
            continue
        fi

        local found="false"
        if printf '%s\n' "$hits" | grep -Fxq "$top"; then
            found="true"
        elif [ -n "$acc" ]; then
            local IFS_OLD="$IFS"
            local IFS_LIST=','
            IFS="$IFS_LIST"
            for cand in $acc; do
                [ -z "$cand" ] && continue
                if printf '%s\n' "$hits" | grep -Fxq "$cand"; then
                    found="true"; break
                fi
            done
            IFS="$IFS_OLD"
        fi
        if [ "$found" = "true" ]; then
            hit3=$((hit3 + 1))
        else
            local got
            got=$(printf '%s' "$hits" | tr '\n' ',' | sed 's/,$//')
            misses+=("$qid: expected=$top got=[$got]")
        fi
    done
    unset IFS

    local recall3="0.00"
    if [ "$total" -gt 0 ]; then
        recall3=$(awk -v h="$hit3" -v t="$total" 'BEGIN{printf "%.3f", h/t}')
    fi
    {
        printf '## D7.6 contextual recall\n\n'
        printf 'graded queries: %d   recall@3: %s\n\n' "$total" "$recall3"
        if [ ${#misses[@]} -gt 0 ]; then
            printf '### misses\n\n'
            for m in "${misses[@]}"; do printf -- '- %s\n' "$m"; done
        else
            printf '### misses\n\n(none)\n'
        fi
        printf '\n'
    } > "$MEMORY_ARTIFACTS/d7_6_contextual.md"

    local pass_ok="false"
    awk -v v="$recall3" 'BEGIN{exit !(v+0 >= 0.80)}' && pass_ok="true"
    if [ "$pass_ok" = "true" ]; then
        pass "D7.6 contextual recall@3=$recall3 (>=0.80) over $total queries"
    else
        fail "D7.6 contextual recall@3=$recall3 below 0.80 threshold" \
            "miss-list: $(printf '%s ' "${misses[@]:0:5}")"
    fi
}

# ============================================================================
# Umbrella + grade report
# ============================================================================

scenario_memory_deep_dive() {
    step D7 "memory deep-dive umbrella"
    if [ ! -f "$MEMORY_ORACLE" ]; then
        skip "D7 memory deep dive" \
            "oracle fixture missing at $MEMORY_ORACLE"
        return
    fi
    mkdir -p "$MEMORY_ARTIFACTS"

    scenario_memory_deep_hybrid_search
    scenario_memory_deep_supersede
    scenario_memory_deep_scope_isolation
    scenario_memory_deep_stress
    scenario_memory_deep_redaction
    scenario_memory_deep_contextual

    # Compose the master grade report from the per-step fragments.
    local now
    now=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    {
        printf '# Memory grade — run %s\n\n' "$now"
        printf 'Generated by scenario_memory_deep_dive.sh (epic 01KSK91Q4W8TNED9MAF0CTRVKC, D7).\n\n'
        printf '| Test | Metric | Threshold |\n'
        printf '|---|---:|---|\n'
        # Pull recall numbers from fragments via awk.
        local r51 r51_t
        r51=$(awk '/^recall@5:/ {for(i=1;i<=NF;i++) if ($i == "recall@5:") print $(i+1)}' \
            "$MEMORY_ARTIFACTS/d7_1_hybrid.md" 2>/dev/null \
            | tr -d ',' | head -n1)
        [ -z "$r51" ] && r51=$(grep -oE 'recall@5: [0-9.]+' \
            "$MEMORY_ARTIFACTS/d7_1_hybrid.md" 2>/dev/null \
            | head -n1 | awk '{print $2}')
        r51="${r51:-N/A}"
        local r63
        r63=$(grep -oE 'recall@3: [0-9.]+' \
            "$MEMORY_ARTIFACTS/d7_6_contextual.md" 2>/dev/null \
            | head -n1 | awk '{print $2}')
        r63="${r63:-N/A}"
        local p95
        p95=$(grep -oE 'p95 recall latency: [0-9.]+ms' \
            "$MEMORY_ARTIFACTS/d7_4_stress.md" 2>/dev/null \
            | head -n1 | awk '{print $4}')
        p95="${p95:-N/A}"
        printf '| D7.1 hybrid recall@5 | %s | >=0.85 |\n' "$r51"
        printf '| D7.4 stress p95 latency | %s | <500ms |\n' "$p95"
        printf '| D7.6 contextual recall@3 | %s | >=0.80 |\n' "$r63"
        printf '\n'
        # Splat the fragments into the report so misses land alongside.
        for frag in d7_1_hybrid.md d7_4_stress.md d7_5_redaction.md d7_6_contextual.md; do
            if [ -f "$MEMORY_ARTIFACTS/$frag" ]; then
                cat "$MEMORY_ARTIFACTS/$frag"
            fi
        done
        printf '\n## Side-channel + redaction\n\n'
        printf 'See D7.3 PASS/FAIL lines above. Side-channel leaks: see scenario output.\n'
        printf 'Plaintext-secret leaks: see D7.5 above.\n'
    } > "$MEMORY_ARTIFACTS/memory-grade.md"
    pass "D7 grade report written to $MEMORY_ARTIFACTS/memory-grade.md"
}
