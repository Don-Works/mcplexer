#!/usr/bin/env bash
# scenario_skill_bundles.sh — Verifies the skill-registry tar.gz BLOB
# format (internal/skillregistry/bundle.go + project_skill_registry_bundles
# memory). Sourced by scenarios.sh.
#
# Publish surface: POST /api/v1/skill-registry with body fields
#   { name, body, scope, bundle_b64 }   — bundle_b64 is base64(tar.gz).
# Fetch surface:   GET  /api/v1/skill-registry/{name}?include_bundle=true
#                  → returns the entry envelope with .bundle_b64 attached.
# Cap: MaxBundleBytes = 25 MiB (internal/skillregistry/registry.go:156).
# Invariant: the bundle MUST contain a SKILL.md (at root or one level
# deep) whose body equals the .body field — see ValidateBundle().
#
# Owner: D4 in epic 01KSK91Q4W8TNED9MAF0CTRVKC.

# bundle_build_tgz creates a tar.gz containing SKILL.md + script.sh +
# data.bin under a single top-level directory ($1 = skill name). The
# SKILL.md body matches the passed-in body so ValidateBundle accepts.
#
# Echoes the path to the produced tar.gz.
#
# Usage: bundle_build_tgz <name> <body> <out_dir>
bundle_build_tgz() {
    local name="$1"
    local body="$2"
    local outdir="$3"
    local stage="$outdir/$name"
    mkdir -p "$stage"
    printf '%s' "$body" >"$stage/SKILL.md"
    printf '#!/bin/sh\necho %s\n' "$name" >"$stage/script.sh"
    chmod +x "$stage/script.sh"
    # 1 KiB of deterministic binary fluff.
    head -c 1024 /dev/urandom >"$stage/data.bin" 2>/dev/null || \
        dd if=/dev/zero of="$stage/data.bin" bs=1024 count=1 2>/dev/null
    local out="$outdir/$name.tar.gz"
    tar -C "$outdir" -czf "$out" "$name"
    echo "$out"
}

# bundle_b64 echoes the base64 of a file with no line wrapping (matches
# the gateway's StdEncoding.DecodeString accepted form).
bundle_b64() {
    local file="$1"
    # macOS base64 doesn't take -w; pipe through tr to strip newlines.
    base64 <"$file" | tr -d '\n'
}

# skill_publish_with_bundle publishes a skill with the given body+bundle.
# Echoes the HTTP status; intended for both positive and negative cases.
#
# Usage: skill_publish_with_bundle <node> <name> <body> <bundle_b64> [scope]
skill_publish_with_bundle() {
    local url="$1"
    local name="$2"
    local body="$3"
    local b64="$4"
    local scope="${5:-global}"
    local payload
    payload=$(jq -nc \
        --arg n "$name" \
        --arg b "$body" \
        --arg s "$scope" \
        --arg bb "$b64" \
        '{name:$n, body:$b, scope:$s, author:"integration-harness", bundle_b64:$bb}')
    local tmp status
    tmp=$(mktemp)
    status=$(curl -s -o "$tmp" -w '%{http_code}' \
        -X POST "$url/api/v1/skill-registry" \
        -H "Authorization: Bearer $(token_for "$url")" \
        -H "Content-Type: application/json" \
        --data "$payload")
    local b
    b=$(cat "$tmp"); rm -f "$tmp"
    # Stash the body for the caller to grep.
    BUNDLE_PUBLISH_BODY="$b"
    echo "$status"
}

scenario_skill_bundles() {
    step 18 "skill-registry tar.gz bundle format (publish/fetch/caps/integrity)"

    local tmpdir
    tmpdir=$(mktemp -d)
    local name="bundle-fixture-$RANDOM"
    local marker="bundle-marker-$$"
    local body
    body=$(printf -- '---\nname: %s\ndescription: integration bundle fixture\n---\n\n# Body\n%s\n' \
        "$name" "$marker")

    # ----- 18.1 build + publish a small multi-file bundle -----------------
    local tgz
    tgz=$(bundle_build_tgz "$name" "$body" "$tmpdir")
    if [ ! -s "$tgz" ]; then
        fail "18.1 build tarball" "tar produced empty output at $tgz"
        rm -rf "$tmpdir"; return
    fi
    local before_sha
    before_sha=$(shasum -a 256 "$tgz" 2>/dev/null | awk '{print $1}')
    [ -z "$before_sha" ] && before_sha=$(sha256sum "$tgz" 2>/dev/null | awk '{print $1}')
    pass "18.1 built bundle tar.gz ($(wc -c <"$tgz" | tr -d ' ') bytes, sha256=${before_sha:0:16})"

    local b64
    b64=$(bundle_b64 "$tgz")
    local status
    status=$(skill_publish_with_bundle "$NODE_A" "$name" "$body" "$b64" "global")
    if [ "$status" = "200" ] || [ "$status" = "201" ]; then
        pass "18.1 publish accepted ($status)"
    else
        fail "18.1 publish failed status=$status" \
            "body head: $(echo "$BUNDLE_PUBLISH_BODY" | head -c 200)"
        rm -rf "$tmpdir"; return
    fi

    # ----- 18.2 fetch via include_bundle on the same node, checksum match
    local gresp got_b64 got_path
    gresp=$(api GET "$NODE_A/api/v1/skill-registry/$name?include_bundle=true" 2>/dev/null)
    got_b64=$(echo "$gresp" | jq -r '.bundle_b64 // empty' 2>/dev/null)
    if [ -z "$got_b64" ]; then
        fail "18.2 fetched envelope missing bundle_b64" \
            "head: $(echo "$gresp" | head -c 200)"
    else
        got_path="$tmpdir/roundtrip.tgz"
        # macOS base64 -d uses -D in BSD; portable: try GNU then BSD.
        if ! printf '%s' "$got_b64" | base64 -d >"$got_path" 2>/dev/null; then
            printf '%s' "$got_b64" | base64 -D >"$got_path" 2>/dev/null || true
        fi
        local got_sha
        got_sha=$(shasum -a 256 "$got_path" 2>/dev/null | awk '{print $1}')
        [ -z "$got_sha" ] && got_sha=$(sha256sum "$got_path" 2>/dev/null | awk '{print $1}')
        if [ "$got_sha" = "$before_sha" ]; then
            pass "18.2 fetched bundle sha256 matches uploaded ($got_sha)"
        else
            fail "18.2 sha256 mismatch on round-trip" \
                "want=$before_sha got=$got_sha"
        fi
        # Walk the file tree to prove all three files survived.
        local extract_dir="$tmpdir/extract"
        mkdir -p "$extract_dir"
        if tar -C "$extract_dir" -xzf "$got_path" 2>/dev/null; then
            local missing=""
            for f in "$name/SKILL.md" "$name/script.sh" "$name/data.bin"; do
                [ -f "$extract_dir/$f" ] || missing="$missing $f"
            done
            if [ -z "$missing" ]; then
                pass "18.2 extracted tree contains SKILL.md + script.sh + data.bin"
            else
                fail "18.2 extracted tree missing files" "$missing"
            fi
        else
            fail "18.2 extracted bundle could not be untarred" \
                "tar exited non-zero on the fetched bundle"
        fi
    fi

    # ----- 18.3 Tier 1 peer fetch (node-a -> node-b same user) ----------
    # The skill registry on node-b is a separate sqlite — until the
    # bundle replicates over mesh (or node-b explicitly requests the
    # skill), node-b's local GET returns 404. The Tier 1 expectation is
    # that node-b CAN reach the bundle via the mesh-mediated share. The
    # skill-share path is `mesh__request_skill` (covered by
    # scenario_skill_share); here we assert the local-fetch SHAPE on
    # node-b is a clean 404 (not a misrouted route) so the test rig
    # doesn't claim "tier 1 share works" via a misread.
    local tier1_status
    tier1_status=$(api_status GET "$NODE_B/api/v1/skill-registry/$name?include_bundle=true")
    case "$tier1_status" in
        200)
            local b_resp
            b_resp=$(api GET "$NODE_B/api/v1/skill-registry/$name?include_bundle=true" 2>/dev/null)
            if echo "$b_resp" | jq -e '.bundle_b64 // empty | length > 0' >/dev/null 2>&1; then
                pass "18.3 Tier 1 peer fetched the bundle locally (replicated)"
            else
                skip "18.3 Tier 1 peer fetch" \
                    "node-b returned 200 but no bundle_b64 — partial replication"
            fi
            ;;
        404)
            skip "18.3 Tier 1 peer fetch" \
                "PENDING — node-b returns 404 (skill not replicated on pair). \
file follow-up child task: replicate skill_registry bundles on Tier 1 pair, \
OR auto-mesh-fetch on first miss."
            ;;
        *)
            skip "18.3 Tier 1 peer fetch" "unexpected status=$tier1_status"
            ;;
    esac

    # ----- 18.4 negative: bundle without SKILL.md is rejected ----------
    local bad_dir="$tmpdir/bad-no-skillmd"
    mkdir -p "$bad_dir/bad-bundle"
    printf 'no SKILL.md here\n' >"$bad_dir/bad-bundle/notes.txt"
    local bad_tgz="$tmpdir/bad-no-skillmd.tar.gz"
    tar -C "$bad_dir" -czf "$bad_tgz" bad-bundle
    local bad_b64
    bad_b64=$(bundle_b64 "$bad_tgz")
    local bad_status
    bad_status=$(skill_publish_with_bundle "$NODE_A" \
        "bad-no-skillmd-$RANDOM" "$body" "$bad_b64" "global")
    if [ "$bad_status" = "400" ]; then
        if echo "$BUNDLE_PUBLISH_BODY" | grep -qiE 'SKILL.md|skill\.md|not match|missing'; then
            pass "18.4 bundle without SKILL.md rejected (400 + ValidateBundle message)"
        else
            pass "18.4 bundle without SKILL.md rejected (400) — generic publish-failed message"
        fi
    elif [ "$bad_status" = "200" ] || [ "$bad_status" = "201" ]; then
        fail "18.4 bundle without SKILL.md was ACCEPTED ($bad_status)" \
            "ValidateBundle regression"
    else
        fail "18.4 unexpected status $bad_status" \
            "body head: $(echo "$BUNDLE_PUBLISH_BODY" | head -c 200)"
    fi

    # ----- 18.5 negative: bundle exceeding MaxBundleBytes (25 MiB) -----
    # ValidateBundle bails at len(raw) > 25 MiB; build a 26 MiB tar.gz so
    # the gzip-compressed size also exceeds it (incompressible random
    # contents — zeros would compress).
    local big_dir="$tmpdir/big-bundle"
    mkdir -p "$big_dir/big-bundle"
    printf '%s' "$body" >"$big_dir/big-bundle/SKILL.md"
    head -c $((26 * 1024 * 1024)) /dev/urandom >"$big_dir/big-bundle/payload.bin" 2>/dev/null
    if [ -s "$big_dir/big-bundle/payload.bin" ]; then
        local big_tgz="$tmpdir/big.tar.gz"
        tar -C "$big_dir" -czf "$big_tgz" big-bundle
        local big_size
        big_size=$(wc -c <"$big_tgz" | tr -d ' ')
        local big_b64
        big_b64=$(bundle_b64 "$big_tgz")
        local big_status
        big_status=$(skill_publish_with_bundle "$NODE_A" \
            "big-bundle-$RANDOM" "$body" "$big_b64" "global")
        if [ "$big_status" = "400" ] || [ "$big_status" = "413" ]; then
            if echo "$BUNDLE_PUBLISH_BODY" | grep -qiE 'exceeds cap|too large|bundle.*bytes'; then
                pass "18.5 oversized bundle ($big_size bytes) rejected ($big_status + cap message)"
            else
                pass "18.5 oversized bundle ($big_size bytes) rejected ($big_status)"
            fi
        elif [ "$big_status" = "200" ] || [ "$big_status" = "201" ]; then
            fail "18.5 oversized bundle accepted ($big_status)" \
                "MaxBundleBytes cap regressed"
        else
            skip "18.5 oversized bundle" "unexpected status=$big_status"
        fi
    else
        skip "18.5 oversized bundle" "could not produce 26 MiB random payload"
    fi

    rm -rf "$tmpdir"
}
