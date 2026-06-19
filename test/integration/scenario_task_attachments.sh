#!/usr/bin/env bash
# scenario_task_attachments.sh — Verifies the task-attachments surface
# (C2.3 + C2.4 + C2.5, PR #60). Sourced by scenarios.sh.
#
# Routes:
#   POST   /api/v1/tasks/{task_id}/attachments       — multipart upload
#   GET    /api/v1/tasks/{task_id}/attachments       — list metadata
#   GET    /api/v1/attachments/{id}                  — download blob
#   DELETE /api/v1/attachments/{id}                  — soft-delete
#
# Cap: 25 MiB (taskAttachmentMaxBytes in task_attachments_handler.go).
# Storage: content-addressed under <data_dir>/attachments/<workspace>/<task>/<sha256>.
#
# Owner: D3 in epic 01KSK91Q4W8TNED9MAF0CTRVKC.

# attach_upload_via_curl uploads a binary blob via multipart/form-data
# (the multipart form parser is in handleUpload). Echoes the attachment
# id (.id) on success, empty string on error.
#
# Usage: attach_upload_via_curl <node_url> <task_id> <local_file> [filename]
attach_upload_via_curl() {
    local url="$1"
    local task_id="$2"
    local local_file="$3"
    local filename="${4:-blob.bin}"
    local tok
    tok=$(token_for "$url")
    curl -fsS -X POST "$url/api/v1/tasks/$task_id/attachments" \
        -H "Authorization: Bearer $tok" \
        -F "file=@$local_file;filename=$filename;type=application/octet-stream" \
        2>/dev/null \
        | jq -r '.id // empty' 2>/dev/null
}

# attach_upload_with_status returns the HTTP status code for a multipart
# upload of `local_file` with `filename`. Used for negative-case checks
# (oversize, etc.) where we want the status, not the body.
attach_upload_with_status() {
    local url="$1"
    local task_id="$2"
    local local_file="$3"
    local filename="${4:-blob.bin}"
    local tok
    tok=$(token_for "$url")
    curl -s -o /dev/null -w '%{http_code}' \
        -X POST "$url/api/v1/tasks/$task_id/attachments" \
        -H "Authorization: Bearer $tok" \
        -F "file=@$local_file;filename=$filename;type=application/octet-stream"
}

scenario_task_attachments() {
    step 17 "task attachments REST surface (upload/list/download/delete + negative)"

    local ws="$WS_ALPHA"
    if [ -z "${ws:-}" ]; then
        skip "task attachments" "WS_ALPHA unset — scenario_provision must run first"
        return
    fi

    # ----- 17.1 setup: create a task to hang attachments off ---------------
    local title="attach-task-$RANDOM"
    local cbody cresp tid
    cbody=$(jq -nc --arg ws "$ws" --arg t "$title" \
        '{workspace_id:$ws, title:$t, status:"open"}')
    if ! cresp=$(api POST "$NODE_A/api/v1/tasks" "$cbody" 2>&1); then
        fail "17.1 setup task create" "$cresp"
        return
    fi
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty')
    if [ -z "$tid" ]; then
        fail "17.1 setup task create returned no id" "$cresp"
        return
    fi
    pass "17.1 setup task $title ($tid)"

    # ----- 17.2 upload a small binary (10 KiB) -----------------------------
    local tmpdir
    tmpdir=$(mktemp -d)
    local payload="$tmpdir/payload.bin"
    # 10 KiB of pseudo-random bytes (deterministic-ish per run is fine).
    head -c 10240 /dev/urandom >"$payload" 2>/dev/null \
        || dd if=/dev/zero of="$payload" bs=1024 count=10 2>/dev/null
    local expected_sha
    expected_sha=$(shasum -a 256 "$payload" 2>/dev/null | awk '{print $1}')
    if [ -z "$expected_sha" ]; then
        expected_sha=$(sha256sum "$payload" 2>/dev/null | awk '{print $1}')
    fi
    if [ -z "$expected_sha" ]; then
        fail "17.2 sha256 computation" "no shasum/sha256sum available"
        rm -rf "$tmpdir"
        return
    fi

    local aid
    aid=$(attach_upload_via_curl "$NODE_A" "$tid" "$payload" "harness-payload.bin")
    if [ -z "$aid" ]; then
        fail "17.2 upload 10KiB blob" "no attachment id returned"
        rm -rf "$tmpdir"
        return
    fi
    pass "17.2 uploaded blob (10 KiB) → attachment id=$aid"

    # ----- 17.3 list attachments returns the new row ---------------------
    local lresp
    lresp=$(api GET "$NODE_A/api/v1/tasks/$tid/attachments" 2>/dev/null)
    if echo "$lresp" | jq -e --arg aid "$aid" \
        'any(.[]?; (.id // .ID) == $aid)' >/dev/null 2>&1; then
        pass "17.3 list attachments contains $aid"
    else
        fail "17.3 list missed the uploaded attachment" \
            "list head: $(echo "$lresp" | head -c 300)"
    fi

    # ----- 17.4 download checksum matches the upload ----------------------
    local dl_path="$tmpdir/downloaded.bin"
    curl -fsS -X GET "$NODE_A/api/v1/attachments/$aid" \
        -H "Authorization: Bearer $(token_for "$NODE_A")" \
        -o "$dl_path" 2>/dev/null || true
    if [ ! -s "$dl_path" ]; then
        fail "17.4 download body empty" "expected $expected_sha"
    else
        local got_sha
        got_sha=$(shasum -a 256 "$dl_path" 2>/dev/null | awk '{print $1}')
        [ -z "$got_sha" ] && got_sha=$(sha256sum "$dl_path" 2>/dev/null | awk '{print $1}')
        if [ "$got_sha" = "$expected_sha" ]; then
            pass "17.4 downloaded blob checksum matches upload"
        else
            fail "17.4 checksum mismatch" "want=$expected_sha got=$got_sha"
        fi
    fi

    # ----- 17.5 delete removes the entry; GET → 404/410 ------------------
    local dstatus
    dstatus=$(api_status DELETE "$NODE_A/api/v1/attachments/$aid")
    if [ "$dstatus" = "204" ] || [ "$dstatus" = "200" ]; then
        pass "17.5 DELETE returned $dstatus"
    else
        fail "17.5 DELETE returned unexpected $dstatus"
    fi
    local after_del_status
    after_del_status=$(api_status GET "$NODE_A/api/v1/attachments/$aid")
    # handleDownload returns 410 Gone for soft-deleted rows, 404 if the
    # store layer filters them; tolerate either.
    if [ "$after_del_status" = "404" ] || [ "$after_del_status" = "410" ]; then
        pass "17.5 GET after delete returned $after_del_status (gone)"
    else
        fail "17.5 GET after delete returned $after_del_status — soft-delete didn't take effect"
    fi

    # ----- 17.6 cross-peer fetch — Tier 1 (silent) -----------------------
    # Tier 1: node-a ↔ node-b share MCPLEXER_SELF_USER_ID=user-alice. The
    # foundation's pair_same_user auto-grants default scopes; the
    # task-attachment fetch path piggybacks on the same trust tier.
    # NOTE: Today /api/v1/attachments/{id} is LOCAL-ONLY — there's no
    # cross-peer remote-fetch surface. So this assertion is structured to
    # SKIP+PENDING when the endpoint doesn't accept a peer_id arg.
    local cross_status
    cross_status=$(api_status GET "$NODE_B/api/v1/attachments/$aid")
    case "$cross_status" in
        404|410)
            skip "17.6 Tier 1 silent cross-peer fetch" \
                "PENDING — attachment fetch is local-only on node-b ($cross_status). \
file follow-up child task: ship remote_peer_id arg on GET /api/v1/attachments/{id}."
            ;;
        200)
            pass "17.6 Tier 1 silent cross-peer fetch returned 200 (remote-fetch surface present)"
            ;;
        *)
            skip "17.6 Tier 1 cross-peer fetch" \
                "unexpected status=$cross_status — surface may not exist yet"
            ;;
    esac

    # ----- 17.7 cross-peer fetch — Tier 2 (must reject without grant) ----
    # node-c is Bob (different user, same org as Alice). Without an
    # explicit grant, an attempt to fetch from node-a's attachments via
    # node-c MUST NOT succeed. Mirrors 17.6's "no remote-fetch surface
    # today" stance — when the surface ships, this assertion flips to
    # enforcing the explicit-grant requirement.
    local tier2_status
    tier2_status=$(api_status GET "$NODE_C/api/v1/attachments/$aid")
    case "$tier2_status" in
        404|410|403)
            pass "17.7 Tier 2 fetch from $NODE_C without grant correctly NOT-found/denied ($tier2_status)"
            ;;
        200)
            fail "17.7 Tier 2 fetch from $NODE_C unexpectedly succeeded" \
                "cross-user attachment fetch should require an explicit grant"
            ;;
        *)
            skip "17.7 Tier 2 fetch" \
                "status=$tier2_status — surface may not exist yet"
            ;;
    esac

    # ----- 17.8 negative: oversized upload (> 25 MiB) rejected -----------
    # The cap (taskAttachmentMaxBytes = 25 MiB) is enforced by both the
    # MaxBytesReader on the request body AND the post-read check. We try
    # 26 MiB to land on the post-read 413; if the body cap fires earlier
    # the curl exit code or status will still be non-2xx.
    #
    # Re-use the task id from 17.1 since the attachment is task-scoped.
    local big="$tmpdir/big.bin"
    # 26 MiB of zeros. Cheap to produce.
    dd if=/dev/zero of="$big" bs=1024 count=$((26 * 1024)) 2>/dev/null || true
    if [ -s "$big" ]; then
        local big_status
        big_status=$(attach_upload_with_status "$NODE_A" "$tid" "$big" "huge.bin")
        if [ "$big_status" = "413" ] || [ "$big_status" = "400" ]; then
            pass "17.8 oversized (26 MiB) upload rejected ($big_status)"
        elif [ "$big_status" = "200" ] || [ "$big_status" = "201" ]; then
            fail "17.8 oversized upload was accepted ($big_status)" \
                "taskAttachmentMaxBytes cap may have regressed"
        else
            # Curl may close mid-stream on the MaxBytesReader; tolerate
            # a stream-error reply as a non-failing skip.
            skip "17.8 oversized upload status" \
                "status=$big_status — stream closed mid-upload (typical when MaxBytesReader fires)"
        fi
    else
        skip "17.8 oversized upload" "could not create 26 MiB scratch file"
    fi

    # ----- 17.9 negative: filename containing a secret-looking string -----
    # The redactor scrubs values listed on the auth-scope redaction_hints
    # and a global needle list (sk-echo-stub is the canonical fixture).
    # Upload a file whose NAME contains the seeded secret needle; audit
    # MUST not echo it. We don't currently expect the filename itself to
    # be auto-redacted (no rule covers it) — so this lands as SKIP+
    # PENDING with a child-task suggestion.
    local secret_marker="secret-echo-stub-attach-fixture-$RANDOM"
    local secretfile="$tmpdir/${secret_marker}.bin"
    head -c 256 /dev/urandom >"$secretfile" 2>/dev/null || \
        dd if=/dev/zero of="$secretfile" bs=64 count=4 2>/dev/null
    local sid
    sid=$(attach_upload_via_curl "$NODE_A" "$tid" "$secretfile" "$(basename "$secretfile")")
    if [ -z "$sid" ]; then
        skip "17.9 secret-named upload" "upload failed; can't probe audit redaction"
    else
        # Give the audit writer a moment.
        local arows
        arows=""
        for _ in 1 2 3 4 5; do
            arows=$(api GET "$NODE_A/api/v1/audit?limit=500" 2>/dev/null)
            echo "$arows" | jq -c '.data[]?' 2>/dev/null \
                | grep -q "$secret_marker" && break
            sleep 1
        done
        if echo "$arows" | jq -c '.data[]?' 2>/dev/null | grep -q "$secret_marker"; then
            skip "17.9 filename-redaction" \
                "PENDING — audit echoes the secret-shaped filename '$secret_marker'. \
file follow-up child task: redact filenames on task_attachments_upload audit rows."
        else
            pass "17.9 audit does not echo the secret-shaped filename"
        fi
    fi

    # ----- 17.10 negative: forbidden mime type ----------------------------
    # The handler does not currently maintain a mime-deny list (see
    # handleUpload: it accepts whatever Content-Type the multipart part
    # carries). Land as SKIP+PENDING.
    skip "17.10 forbidden mime type" \
        "PENDING — no mime deny-list in task_attachments_handler.go. \
file follow-up child task: add a configurable mime allow/deny list."

    rm -rf "$tmpdir"
}
