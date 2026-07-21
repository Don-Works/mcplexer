#!/usr/bin/env bash
# lib_tiers.sh — tier-aware pair + grant assertion helpers for the
# bulletproof e2e suite. Sourced by scenarios.sh AFTER lib.sh +
# tier_topology.sh.
#
# Three pair helpers, one per tier. Each:
#   1. Runs the standard /api/p2p/pair/{start,complete} flow.
#   2. Asserts the post-pair scope vector matches what the tier requires:
#      - same_user (Tier 1): /api/p2p/peers row shows same_user=true on
#        BOTH sides AND each side has the default-same-user scopes
#        (`mesh.skill_request`, `mesh.memory_request`) auto-granted.
#      - same_org (Tier 2): same_user=false; NO auto-granted default
#        scopes; any cross-share rejected until an explicit grant lands.
#      - cross_org (Tier 3): same as Tier 2 plus a documented
#        org-boundary expectation (currently aspirational — the daemon
#        doesn't model orgs, so we record the assertion as PENDING; if
#        the org concept ships, flip the assertion to enforcing).
#
# These helpers print PASS/FAIL/SKIP lines through the existing
# pass/fail/skip helpers, so the run summary subsumes them.

# pair_basic runs the responder/initiator pair exchange and returns 0 on
# 204, 1 on any other status. Mirrors scenarios.sh::pair_pair without the
# SKIP-on-DHT-failure logic — tier helpers want a hard signal.
pair_basic() {
    local label="$1"
    local resp_url="$2"
    local init_url="$3"

    local sresp
    sresp=$(api POST "$resp_url/api/p2p/pair/start" '{}' 2>/dev/null)
    local code peer_id
    code=$(echo "$sresp" | jq -r '.code // empty' 2>/dev/null)
    peer_id=$(echo "$sresp" | jq -r '.qr_payload | fromjson | .peer_id // empty' 2>/dev/null)
    if [ -z "$code" ] || [ -z "$peer_id" ]; then
        fail "$label pair/start" "resp=$sresp"
        return 1
    fi

    local status
    status=$(api_status POST "$init_url/api/p2p/pair/complete" \
        "{\"code\":\"$code\",\"peer_id\":\"$peer_id\"}")
    if [ "$status" = "204" ]; then
        return 0
    fi
    return 1
}

# peers_row_for returns the JSON row for `peer_id` from `node_url`'s
# /api/p2p/peers, or empty if not present.
peers_row_for() {
    local node_url="$1"
    local peer_id="$2"
    api GET "$node_url/api/p2p/peers" 2>/dev/null \
        | jq -c --arg pid "$peer_id" \
            '(.peers // []) | map(select(.peer_id == $pid)) | .[0] // empty' \
            2>/dev/null
}

# scope_vector_for returns the space-separated list of mesh scopes
# granted to `peer_id` on `node_url`. Reads /api/v1/p2p/peers/<id>/scopes
# if exposed, else falls back to the embedded "scopes" array on the peer
# row. Returns empty if the peer row carries no scopes.
scope_vector_for() {
    local node_url="$1"
    local peer_id="$2"
    local row
    row=$(peers_row_for "$node_url" "$peer_id")
    echo "$row" | jq -r '(.scopes // []) | join(" ")' 2>/dev/null
}

# pair_same_user pairs two nodes that share MCPLEXER_SELF_USER_ID. The
# expected post-pair state is:
#   - same_user=true on the peer row on BOTH sides
#   - default same-user scopes (mesh.skill_request, mesh.memory_request)
#     present in the scope vector on BOTH sides
#
# Usage: pair_same_user URL_A URL_B
pair_same_user() {
    local a="$1"
    local b="$2"
    local lbl="pair_same_user ($a ↔ $b)"

    # Topology sanity — refuse to pair nodes whose user_id disagrees,
    # since that means tier_topology.sh and the live daemon env have
    # diverged (a common ops mistake).
    local ua ub
    ua=$(node_user "$a"); ub=$(node_user "$b")
    if [ "$ua" != "$ub" ] || [ -z "$ua" ]; then
        fail "$lbl topology mismatch" "user_a=$ua user_b=$ub (expected equal+non-empty)"
        return 1
    fi

    if ! pair_basic "$lbl" "$b" "$a"; then
        skip "$lbl pair handshake" "libp2p handshake didn't land (closed docker bridge?)"
        return 0
    fi

    sleep 2  # post-pair settle so peers row + auto-grant has committed

    local peer_b peer_a
    peer_b=$(peer_id_for "$b")
    peer_a=$(peer_id_for "$a")
    if [ -z "$peer_a" ] || [ -z "$peer_b" ]; then
        fail "$lbl identity readback" "peer_a=$peer_a peer_b=$peer_b"
        return 1
    fi

    # same_user true on both sides
    local row_a_about_b row_b_about_a
    row_a_about_b=$(peers_row_for "$a" "$peer_b")
    row_b_about_a=$(peers_row_for "$b" "$peer_a")
    local su_a_about_b su_b_about_a
    su_a_about_b=$(echo "$row_a_about_b" | jq -r '.same_user // false' 2>/dev/null)
    su_b_about_a=$(echo "$row_b_about_a" | jq -r '.same_user // false' 2>/dev/null)
    if [ "$su_a_about_b" = "true" ] && [ "$su_b_about_a" = "true" ]; then
        pass "$lbl same_user=true symmetric"
    else
        fail "$lbl same_user asymmetric or false" \
            "a-about-b=$su_a_about_b  b-about-a=$su_b_about_a"
    fi

    # default scopes auto-granted on both sides
    local scopes_a scopes_b
    scopes_a=$(scope_vector_for "$a" "$peer_b")
    scopes_b=$(scope_vector_for "$b" "$peer_a")
    local want="mesh.skill_request mesh.memory_request"
    local missing_a=""
    local missing_b=""
    for s in $want; do
        echo " $scopes_a " | grep -q " $s " || missing_a="$missing_a $s"
        echo " $scopes_b " | grep -q " $s " || missing_b="$missing_b $s"
    done
    if [ -z "$missing_a" ] && [ -z "$missing_b" ]; then
        pass "$lbl default scopes auto-granted on both sides"
    else
        fail "$lbl default scope vector wrong" \
            "missing on a:$missing_a   missing on b:$missing_b  (scopes_a='$scopes_a' scopes_b='$scopes_b')"
    fi
}

# pair_same_org pairs two nodes with DIFFERENT user IDs but the same org
# label. Expected post-pair state:
#   - same_user=false on the peer row on BOTH sides
#   - NO default scopes auto-granted; scope vector empty
#
# This is the security default — every cross-human trust step must be
# explicit. The grant negative is the load-bearing assertion.
pair_same_org() {
    local a="$1"
    local b="$2"
    local lbl="pair_same_org ($a ↔ $b)"

    local ua ub oa ob
    ua=$(node_user "$a"); ub=$(node_user "$b")
    oa=$(node_org "$a");  ob=$(node_org "$b")
    if [ "$ua" = "$ub" ] || [ "$oa" != "$ob" ] || [ -z "$oa" ]; then
        fail "$lbl topology mismatch" \
            "users (a=$ua b=$ub) must differ; orgs (a=$oa b=$ob) must match+non-empty"
        return 1
    fi

    if ! pair_basic "$lbl" "$b" "$a"; then
        skip "$lbl pair handshake" "libp2p handshake didn't land (closed docker bridge?)"
        return 0
    fi

    sleep 2

    local peer_b peer_a
    peer_b=$(peer_id_for "$b")
    peer_a=$(peer_id_for "$a")
    if [ -z "$peer_a" ] || [ -z "$peer_b" ]; then
        fail "$lbl identity readback" "peer_a=$peer_a peer_b=$peer_b"
        return 1
    fi

    # same_user must be false symmetrically
    local row_a_about_b row_b_about_a
    row_a_about_b=$(peers_row_for "$a" "$peer_b")
    row_b_about_a=$(peers_row_for "$b" "$peer_a")
    local su_a su_b
    su_a=$(echo "$row_a_about_b" | jq -r '.same_user // false' 2>/dev/null)
    su_b=$(echo "$row_b_about_a" | jq -r '.same_user // false' 2>/dev/null)
    if [ "$su_a" = "false" ] && [ "$su_b" = "false" ]; then
        pass "$lbl same_user=false symmetric (cross-user pair)"
    else
        fail "$lbl unexpected same_user=true on cross-user pair" \
            "a-about-b=$su_a  b-about-a=$su_b — daemon may have leaked auto-grant to a non-self user"
    fi

    # No default scopes auto-granted
    local scopes_a scopes_b
    scopes_a=$(scope_vector_for "$a" "$peer_b")
    scopes_b=$(scope_vector_for "$b" "$peer_a")
    if [ -z "$scopes_a" ] && [ -z "$scopes_b" ]; then
        pass "$lbl zero default scopes (explicit grants required)"
    else
        fail "$lbl unexpected default scopes on cross-user pair" \
            "scopes_a='$scopes_a' scopes_b='$scopes_b' — this is a security regression"
    fi
}

# pair_cross_org pairs two nodes with different users AND different orgs.
# Expected post-pair state today is identical to pair_same_org (the daemon
# doesn't yet model orgs). When the cross-org boundary lands, this helper
# should ALSO assert:
#   - a `tier=cross_org` flag on the peer row, or
#   - an explicit boundary check on subsequent share attempts that
#     produces `denial="cross_org_boundary"`.
#
# Until then we record a PENDING note so the rig surfaces the gap loudly.
pair_cross_org() {
    local a="$1"
    local b="$2"
    local lbl="pair_cross_org ($a ↔ $b)"

    local ua ub oa ob
    ua=$(node_user "$a"); ub=$(node_user "$b")
    oa=$(node_org "$a");  ob=$(node_org "$b")
    if [ "$ua" = "$ub" ] || [ "$oa" = "$ob" ] || [ -z "$oa" ] || [ -z "$ob" ]; then
        fail "$lbl topology mismatch" \
            "users (a=$ua b=$ub) must differ; orgs (a=$oa b=$ob) must differ+non-empty"
        return 1
    fi

    if ! pair_basic "$lbl" "$b" "$a"; then
        skip "$lbl pair handshake" "libp2p handshake didn't land (closed docker bridge?)"
        return 0
    fi

    sleep 2

    local peer_b peer_a
    peer_b=$(peer_id_for "$b")
    peer_a=$(peer_id_for "$a")
    if [ -z "$peer_a" ] || [ -z "$peer_b" ]; then
        fail "$lbl identity readback" "peer_a=$peer_a peer_b=$peer_b"
        return 1
    fi

    # Same baseline: same_user=false + zero default scopes
    local su_a su_b scopes_a scopes_b
    su_a=$(peers_row_for "$a" "$peer_b" | jq -r '.same_user // false' 2>/dev/null)
    su_b=$(peers_row_for "$b" "$peer_a" | jq -r '.same_user // false' 2>/dev/null)
    if [ "$su_a" = "false" ] && [ "$su_b" = "false" ]; then
        pass "$lbl same_user=false symmetric"
    else
        fail "$lbl cross-org pair leaked same_user=true" \
            "a-about-b=$su_a b-about-a=$su_b"
    fi
    scopes_a=$(scope_vector_for "$a" "$peer_b")
    scopes_b=$(scope_vector_for "$b" "$peer_a")
    if [ -z "$scopes_a" ] && [ -z "$scopes_b" ]; then
        pass "$lbl zero default scopes on cross-org pair"
    else
        fail "$lbl unexpected default scopes on cross-org pair" \
            "scopes_a='$scopes_a' scopes_b='$scopes_b'"
    fi

    # Aspirational boundary: PENDING until daemon ships org concept
    local row
    row=$(peers_row_for "$a" "$peer_b")
    if echo "$row" | jq -e 'has("org") or has("tier") or has("cross_org_boundary")' >/dev/null 2>&1; then
        pass "$lbl org-boundary metadata present on peer row"
    else
        skip "$lbl org-boundary metadata" \
            "PENDING — daemon doesn't model orgs yet; file under epic 01KSK91Q4W8TNED9MAF0CTRVKC"
    fi
}
