#!/usr/bin/env bash
# tier_topology.sh — defines the trust-tier matrix the bulletproof e2e
# suite operates on. Sourced by scenarios.sh after lib.sh. Existing 3-node
# smoke tests don't read this file — they keep using NODE_A/B/C/CONT_A/B/C
# from scenarios.sh's own defaults.
#
# Tier model (see epic 01KSK91Q4W8TNED9MAF0CTRVKC):
#
#   Tier 1: same user, multiple machines     — auto-grant default scopes
#                                              (MCPLEXER_SELF_USER_ID equal)
#   Tier 2: same org, different users        — explicit auth_scope required
#                                              (different SELF_USER_ID,
#                                              same org label)
#   Tier 3: different orgs, different users  — explicit auth_scope required
#                                              PLUS org-boundary check
#                                              (which is currently aspirational
#                                              — the daemon doesn't model orgs;
#                                              B3/D6 file bugs against this)
#
# Org labels are TEST-SIDE only — the daemon has no "org" concept yet. We
# attach them via this static map so tier scenarios can assert behavior
# the way users will perceive it.

# ----- node URLs ---------------------------------------------------------
# Existing scenarios.sh already exports NODE_A/B/C with these values; we
# re-export anyway so tier_topology.sh is self-contained when sourced
# standalone (e.g. from a one-off scenario script).
export NODE_A="${NODE_A:-http://localhost:23333}"
export NODE_B="${NODE_B:-http://localhost:23334}"
export NODE_C="${NODE_C:-http://localhost:23335}"
export NODE_D="${NODE_D:-http://localhost:13336}"
export NODE_E="${NODE_E:-http://localhost:13337}"

export CONT_A="${CONT_A:-mcplexer-test-node-a}"
export CONT_B="${CONT_B:-mcplexer-test-node-b}"
export CONT_C="${CONT_C:-mcplexer-test-node-c}"
export CONT_D="${CONT_D:-mcplexer-test-node-d}"
export CONT_E="${CONT_E:-mcplexer-test-node-e}"

# ----- identity map ------------------------------------------------------
# Mirrors docker-compose.yml. Editing one without the other will break
# the assertions in pair_same_user / pair_same_org / pair_cross_org.
node_user() {
    case "$1" in
        "$NODE_A"|"$NODE_B")    echo "user-alice" ;;
        "$NODE_C")              echo "user-bob"   ;;
        "$NODE_D"|"$NODE_E")    echo "user-carol" ;;
        *)                      echo "" ;;
    esac
}

node_org() {
    case "$1" in
        "$NODE_A"|"$NODE_B"|"$NODE_C")    echo "AcmeCo" ;;
        "$NODE_D"|"$NODE_E")              echo "BetaCo" ;;
        *)                                 echo "" ;;
    esac
}

node_display() {
    case "$1" in
        "$NODE_A") echo "Alice (AcmeCo, machine 1)" ;;
        "$NODE_B") echo "Alice (AcmeCo, machine 2)" ;;
        "$NODE_C") echo "Bob (AcmeCo)"              ;;
        "$NODE_D") echo "Carol (BetaCo, machine 1)" ;;
        "$NODE_E") echo "Carol (BetaCo, machine 2)" ;;
        *)         echo "" ;;
    esac
}

# tier_of returns the trust tier between two node URLs, or "self" if both
# are the same node. Output is one of: self | tier1 | tier2 | tier3.
tier_of() {
    local a="$1"
    local b="$2"
    if [ "$a" = "$b" ]; then
        echo "self"
        return
    fi
    local ua ub oa ob
    ua=$(node_user "$a")
    ub=$(node_user "$b")
    oa=$(node_org "$a")
    ob=$(node_org "$b")
    if [ "$ua" = "$ub" ]; then
        echo "tier1"
    elif [ "$oa" = "$ob" ]; then
        echo "tier2"
    else
        echo "tier3"
    fi
}

# ----- canonical pair lists ----------------------------------------------
# Used by orchestrators that want to iterate "all tier-N pairs" without
# re-deriving the matrix each time. Space-separated pairs, "|" delimited.
T1_PAIRS="${NODE_A}|${NODE_B} ${NODE_D}|${NODE_E}"
T2_PAIRS="${NODE_A}|${NODE_C} ${NODE_B}|${NODE_C}"
T3_PAIRS="${NODE_A}|${NODE_D} ${NODE_A}|${NODE_E} ${NODE_C}|${NODE_D} ${NODE_C}|${NODE_E}"
export T1_PAIRS T2_PAIRS T3_PAIRS

# bulletproof_topology_ready returns 0 if all 5 nodes are reachable. Used
# by scenarios that should SKIP when only the 3-node smoke topology is up.
bulletproof_topology_ready() {
    for u in "$NODE_A" "$NODE_B" "$NODE_C" "$NODE_D" "$NODE_E"; do
        curl -sf -o /dev/null --max-time 2 "$u/api/v1/health" 2>/dev/null || return 1
    done
    return 0
}
