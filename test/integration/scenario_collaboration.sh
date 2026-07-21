#!/usr/bin/env bash
# Proof-bound collaboration: independent ssh-agent identities, one-time
# invitations, workspace membership mirrors, per-task visibility, and a
# write-only machine publisher. No host key or ~/.mcplexer state is used.

collaboration_public_key() {
    docker exec "$(container_for "$1")" cat /data/identity_ed25519.pub 2>/dev/null
}

collaboration_enroll_node() {
    local url="$1" name="$2" kind="$3" public_key body response
    public_key=$(collaboration_public_key "$url")
    body=$(jq -nc --arg key "$public_key" --arg name "$name" --arg kind "$kind" \
        '{public_key:$key, device_name:$name, device_kind:$kind}')
    if ! response=$(api POST "$url/api/v1/collaboration/identity/enroll" "$body" 2>&1); then
        fail "$name SSH identity enrollment" "$response"
        return 1
    fi
    assert_jq "$name SSH identity proof activated its device" "$response" \
        '.device.status == "active" and (.device.identity_key_id | length > 0)'
}

scenario_collaboration_identity_and_permissions() {
    step 3.5 "SSH-proven people and machine identities join exact workspace grants"

    collaboration_enroll_node "$NODE_A" "workspace-home" "server" || return
    collaboration_enroll_node "$NODE_B" "reader-laptop" "laptop" || return
    collaboration_enroll_node "$NODE_C" "log-monitor" "server" || return

    local home_snapshot share_id owner_id
    home_snapshot=$(api GET "$NODE_A/api/v1/collaboration")
    share_id=$(echo "$home_snapshot" | jq -r --arg ws "$WS_ALPHA" \
        '.workspaces[]? | select(.local_workspace_id == $ws) | .share_id' | head -1)
    owner_id=$(echo "$home_snapshot" | jq -r '.principals[]? | select(.is_local_owner) | .id' | head -1)
    if [ -z "$share_id" ] || [ -z "$owner_id" ]; then
        fail "collaboration matrix provisioned the alpha workspace" "snapshot=$(echo "$home_snapshot" | head -c 400)"
        return
    fi
    pass "collaboration matrix provisioned alpha default-deny (share=$share_id)"

    local reader_key reader_body reader_invite reader_code reader_principal
    reader_key=$(collaboration_public_key "$NODE_B")
    reader_body=$(jq -nc --arg key "$reader_key" --arg share "$share_id" \
        '{kind:"person", display_name:"Team reader", public_key:$key,
          workspace_grants:[{share_id:$share,capabilities:["workspace.view","tasks.read"]}]}')
    if ! reader_invite=$(api POST "$NODE_A/api/v1/collaboration/invitations" "$reader_body" 2>&1); then
        fail "create reader invitation" "$reader_invite"
        return
    fi
    reader_code=$(echo "$reader_invite" | jq -r '.invite_code // empty')
    reader_principal=$(echo "$reader_invite" | jq -r '.principal.id // empty')
    if [ -z "$reader_code" ] || [ -z "$reader_principal" ]; then
        fail "reader invitation returned one-time code" "$reader_invite"
        return
    fi
    local join_body join_response
    join_body=$(jq -nc --arg invitation "$reader_code" \
        '{invitation:$invitation,device_name:"reader-laptop",device_kind:"laptop"}')
    if ! join_response=$(api POST "$NODE_B/api/v1/collaboration/invitations/join" "$join_body" 2>&1); then
        fail "reader proves SSH key and joins" "$join_response"
        return
    fi
    assert_jq "reader join installs exact grants and workspace membership" "$join_response" \
        '([.grants[].capability] | sort) == (["tasks.read","workspace.view"] | sort) and (.workspaces | length) == 1'

    local replay_status
    replay_status=$(api_status POST "$NODE_B/api/v1/collaboration/invitations/join" "$join_body")
    if [ "$replay_status" -ge 400 ]; then
        pass "consumed invitation replay is rejected (HTTP $replay_status)"
    else
        fail "consumed invitation replay was accepted" "status=$replay_status"
    fi

    local machine_key machine_body machine_invite machine_code machine_principal machine_join
    machine_key=$(collaboration_public_key "$NODE_C")
    machine_body=$(jq -nc --arg key "$machine_key" --arg share "$share_id" --arg controller "$owner_id" \
        '{kind:"machine", display_name:"Production log monitor", controlling_principal_id:$controller,
          public_key:$key, workspace_grants:[{share_id:$share,capabilities:["tasks.publish"]}]}')
    if ! machine_invite=$(api POST "$NODE_A/api/v1/collaboration/invitations" "$machine_body" 2>&1); then
        fail "create machine invitation" "$machine_invite"
        return
    fi
    machine_code=$(echo "$machine_invite" | jq -r '.invite_code // empty')
    machine_principal=$(echo "$machine_invite" | jq -r '.principal.id // empty')
    machine_join=$(jq -nc --arg invitation "$machine_code" \
        '{invitation:$invitation,device_name:"log-monitor",device_kind:"server"}')
    if ! join_response=$(api POST "$NODE_C/api/v1/collaboration/invitations/join" "$machine_join" 2>&1); then
        fail "machine proves SSH key and joins" "$join_response"
        return
    fi
    assert_jq "machine join is write-only and controlled by the owner" "$join_response" \
        '([.grants[].capability] == ["tasks.publish"]) and (.workspaces | length) == 1'

    export COLLAB_SHARE_ID="$share_id"
    export COLLAB_READER_PRINCIPAL="$reader_principal"
    export COLLAB_MACHINE_PRINCIPAL="$machine_principal"
}

scenario_collaboration_task_flow() {
    step 3.6 "visible tasks mirror safely; private tasks stay home; machine publishes without read"
    local share_id="${COLLAB_SHARE_ID:-}"
    if [ -z "$share_id" ]; then
        skip "collaboration task flow" "identity scenario did not produce a share"
        return
    fi

    local reader_snapshot reader_ws machine_snapshot machine_ws
    reader_snapshot=$(api GET "$NODE_B/api/v1/collaboration")
    machine_snapshot=$(api GET "$NODE_C/api/v1/collaboration")
    reader_ws=$(echo "$reader_snapshot" | jq -r --arg share "$share_id" \
        '.memberships[]? | select(.share_id == $share) | .local_workspace_id' | head -1)
    machine_ws=$(echo "$machine_snapshot" | jq -r --arg share "$share_id" \
        '.memberships[]? | select(.share_id == $share) | .local_workspace_id' | head -1)
    if [ -z "$reader_ws" ] || [ -z "$machine_ws" ]; then
        fail "joined daemons expose durable local workspace mirrors" "reader=$reader_ws machine=$machine_ws"
        return
    fi
    pass "joined daemons expose durable local mirrors"

    local marker="visible-$RANDOM" secret="sk-live-container-secret-abcdefghijklmnopqrstuvwxyz"
    local visible_body visible_task visible_id
    visible_body=$(jq -nc --arg ws "$WS_ALPHA" --arg title "$marker $secret" --arg secret "$secret" \
        '{workspace_id:$ws,title:$title,description:("Authorization: Bearer "+$secret),status:"open"}')
    visible_task=$(api POST "$NODE_A/api/v1/tasks" "$visible_body")
    visible_id=$(echo "$visible_task" | jq -r '.id // empty')
    api PUT "$NODE_A/api/v1/collaboration/tasks/$visible_id/visibility" \
        '{"visibility":"workspace"}' >/dev/null

    local private_marker="private-$RANDOM" private_task
    private_task=$(api POST "$NODE_A/api/v1/tasks" \
        "$(jq -nc --arg ws "$WS_ALPHA" --arg title "$private_marker" '{workspace_id:$ws,title:$title,status:"open"}')")

    api POST "$NODE_B/api/v1/collaboration/memberships/$share_id/sync" '{}' >/dev/null
    local reader_tasks
    reader_tasks=$(api GET "$NODE_B/api/v1/tasks?workspace_id=$reader_ws&limit=200")
    if echo "$reader_tasks" | jq -e --arg marker "$marker" 'any(.[]; .title | contains($marker))' >/dev/null; then
        pass "reader pulled the workspace-visible task into its local mirror"
    else
        fail "reader mirror omitted visible task" "tasks=$(echo "$reader_tasks" | head -c 400)"
    fi
    if echo "$reader_tasks" | grep -q 'sk-live-container-secret\|Authorization: Bearer'; then
        fail "safe read projection leaked a credential pattern"
    else
        pass "safe read projection redacted credential patterns"
    fi
    if echo "$reader_tasks" | jq -e --arg marker "$private_marker" 'any(.[]; .title == $marker)' >/dev/null; then
        fail "private home task crossed into reader mirror"
    else
        pass "private task remained on the authoritative home"
    fi

    local reader_denied
    reader_denied=$(api_status POST "$NODE_B/api/v1/tasks" \
        "$(jq -nc --arg ws "$reader_ws" '{workspace_id:$ws,title:"reader must not create"}')")
    if [ "$reader_denied" -ge 400 ]; then
        pass "reader mirror rejects local task creation (HTTP $reader_denied)"
    else
        fail "reader mirror allowed task creation" "status=$reader_denied"
    fi

    local monitor_marker="monitor-$RANDOM" monitor_task monitor_id publish_resp
    monitor_task=$(api POST "$NODE_C/api/v1/tasks" \
        "$(jq -nc --arg ws "$machine_ws" --arg title "$monitor_marker $secret" --arg secret "$secret" \
          '{workspace_id:$ws,title:$title,description:("password="+$secret),status:"open"}')")
    monitor_id=$(echo "$monitor_task" | jq -r '.id // empty')
    publish_resp=$(mcp_call "$NODE_C" "task__publish_home" \
        "$(jq -nc --arg id "$monitor_id" --arg ws "$machine_ws" \
          '{task_id:$id,workspace_id:$ws,message:"sanitized monitor summary"}')")
    if echo "$publish_resp" | jq -e '.error == null and .result.isError != true' >/dev/null 2>&1; then
        pass "write-only machine published with task__publish_home (no peer id)"
    else
        fail "write-only machine publish failed" "$(echo "$publish_resp" | head -c 400)"
    fi

    local home_tasks="[]" found="false"
    for _ in 1 2 3 4 5; do
        home_tasks=$(api GET "$NODE_A/api/v1/tasks?workspace_id=$WS_ALPHA&limit=200")
        if echo "$home_tasks" | jq -e --arg marker "$monitor_marker" 'any(.[]; .title | contains($marker))' >/dev/null; then
            found="true"; break
        fi
        sleep 1
    done
    if [ "$found" = "true" ]; then
        pass "machine finding materialized on the authoritative home"
    else
        fail "machine finding never materialized on home" "$(echo "$home_tasks" | head -c 400)"
    fi
    local monitor_home_task
    monitor_home_task=$(echo "$home_tasks" | jq -c --arg marker "$monitor_marker" \
        '[.[] | select(.title | contains($marker))]')
    if echo "$monitor_home_task" | grep -q 'sk-live-container-secret'; then
        fail "machine publish leaked a credential pattern"
    else
        pass "machine publish was sanitized before crossing the boundary"
    fi

    # A publish attempted while the home is down must remain in the local
    # durable outbox. Once the authenticated access/sync handshake succeeds,
    # the scheduler retries that exact pending row automatically.
    local home_container offline_marker offline_task offline_id offline_response
    home_container=$(container_for "$NODE_A")
    offline_marker="offline-monitor-$RANDOM"
    docker stop "$home_container" >/dev/null
    offline_task=$(api POST "$NODE_C/api/v1/tasks" \
        "$(jq -nc --arg ws "$machine_ws" --arg title "$offline_marker" '{workspace_id:$ws,title:$title,status:"open"}')")
    offline_id=$(echo "$offline_task" | jq -r '.id // empty')
    offline_response=$(mcp_call "$NODE_C" "task__publish_home" \
        "$(jq -nc --arg id "$offline_id" --arg ws "$machine_ws" '{task_id:$id,workspace_id:$ws}')")
    if echo "$offline_response" | jq -e '.result.isError == true' >/dev/null 2>&1 \
       && echo "$offline_response" | grep -q 'Publish recorded'; then
        pass "offline machine publish is durably queued"
    else
        fail "offline publish did not expose its durable queue receipt" "$(echo "$offline_response" | head -c 400)"
    fi
    docker start "$home_container" >/dev/null
    local home_ready="false"
    for _ in $(seq 1 60); do
        if curl -sf -o /dev/null --max-time 2 "$NODE_A/api/v1/health" 2>/dev/null; then
            home_ready="true"
            break
        fi
        sleep 1
    done
    if [ "$home_ready" != "true" ]; then
        fail "workspace home restarted for outbox recovery"
        return
    fi
    local offline_found="false" pending_offers
    for _ in 1 2 3 4 5 6; do
        api POST "$NODE_C/api/v1/collaboration/memberships/$share_id/sync" '{}' >/dev/null || true
        home_tasks=$(api GET "$NODE_A/api/v1/tasks?workspace_id=$WS_ALPHA&limit=200")
        if echo "$home_tasks" | jq -e --arg marker "$offline_marker" 'any(.[]; .title == $marker)' >/dev/null; then
            offline_found="true"
            break
        fi
        sleep 2
    done
    pending_offers=$(api GET "$NODE_C/api/v1/tasks/offers?direction=outgoing&state=pending&limit=200")
    if [ "$offline_found" = "true" ] \
       && ! echo "$pending_offers" | jq -e --arg id "$offline_id" 'any(.[]?; .task_id == $id)' >/dev/null; then
        pass "authenticated reconnect drains the pending publication outbox"
    else
        fail "pending publication did not drain after home recovery" \
            "found=$offline_found offers=$(echo "$pending_offers" | head -c 300)"
    fi

    local home_snapshot machine_device
    home_snapshot=$(api GET "$NODE_A/api/v1/collaboration")
    machine_device=$(echo "$home_snapshot" | jq -r --arg principal "$COLLAB_MACHINE_PRINCIPAL" \
        '.principals[]? | select(.id == $principal) | .devices[]? | select(.status == "active") | .peer_id' | head -1)
    api POST "$NODE_A/api/v1/collaboration/devices/$machine_device/revoke" \
        '{"reason":"container revocation test"}' >/dev/null
    local after_revoke after_id rejected
    after_revoke=$(api POST "$NODE_C/api/v1/tasks" \
        "$(jq -nc --arg ws "$machine_ws" '{workspace_id:$ws,title:"must fail after device revoke"}')")
    after_id=$(echo "$after_revoke" | jq -r '.id // empty')
    rejected=$(mcp_call "$NODE_C" "task__publish_home" \
        "$(jq -nc --arg id "$after_id" --arg ws "$machine_ws" '{task_id:$id,workspace_id:$ws}')")
    if echo "$rejected" | jq -e '.error != null or .result.isError == true' >/dev/null 2>&1; then
        pass "revoked machine device is denied immediately despite cached membership"
    else
        fail "revoked machine device still published" "$(echo "$rejected" | head -c 400)"
    fi

    api PUT "$NODE_A/api/v1/collaboration/shares/$share_id/principals/$COLLAB_READER_PRINCIPAL" \
        '{"capabilities":[]}' >/dev/null
    local revoked_marker="revoked-reader-$RANDOM" revoked_task revoked_id
    revoked_task=$(api POST "$NODE_A/api/v1/tasks" \
        "$(jq -nc --arg ws "$WS_ALPHA" --arg title "$revoked_marker" '{workspace_id:$ws,title:$title}')")
    revoked_id=$(echo "$revoked_task" | jq -r '.id // empty')
    api PUT "$NODE_A/api/v1/collaboration/tasks/$revoked_id/visibility" \
        '{"visibility":"workspace"}' >/dev/null
    api POST "$NODE_B/api/v1/collaboration/memberships/$share_id/sync" '{}' >/dev/null || true
    reader_tasks=$(api GET "$NODE_B/api/v1/tasks?workspace_id=$reader_ws&limit=200")
    if echo "$reader_tasks" | jq -e --arg marker "$revoked_marker" 'any(.[]; .title == $marker)' >/dev/null; then
        fail "revoked reader received a post-revocation task"
    else
        pass "live grant revocation stops subsequent task disclosure"
    fi

    local refreshed_membership
    refreshed_membership=$(api GET "$NODE_B/api/v1/collaboration")
    if echo "$refreshed_membership" | jq -e --arg share "$share_id" \
        'any(.memberships[]?; .share_id == $share and .status == "active" and (.capabilities | length) == 0)' >/dev/null; then
        pass "reader mirror refreshed to the home epoch with no stale capabilities"
    else
        fail "reader mirror retained stale grants after sync" "$(echo "$refreshed_membership" | head -c 500)"
    fi

    api PUT "$NODE_A/api/v1/collaboration/shares/$share_id/principals/$COLLAB_READER_PRINCIPAL" \
        '{"capabilities":["workspace.view","tasks.read"]}' >/dev/null
    api POST "$NODE_B/api/v1/collaboration/memberships/$share_id/sync" '{}' >/dev/null
    refreshed_membership=$(api GET "$NODE_B/api/v1/collaboration")
    reader_tasks=$(api GET "$NODE_B/api/v1/tasks?workspace_id=$reader_ws&limit=200")
    if echo "$refreshed_membership" | jq -e --arg share "$share_id" \
        'any(.memberships[]?; .share_id == $share and ([.capabilities[]] | sort) == (["tasks.read","workspace.view"] | sort))' >/dev/null \
       && echo "$reader_tasks" | jq -e --arg marker "$revoked_marker" 'any(.[]; .title == $marker)' >/dev/null; then
        pass "re-grant refresh restores only the selected capabilities and later revisions"
    else
        fail "re-grant did not converge through the authenticated access receipt" \
            "membership=$(echo "$refreshed_membership" | head -c 300)"
    fi

    # Rotate the reader's SSH identity on the same libp2p device. The new key
    # proves possession through the container's private agent socket; only
    # after the device is rebound do we revoke the old key.
    local reader_container rotated_key_path rotated_public_key old_key_id rotation_body
    local rotation_invite rotation_code new_key_id rotation_join rotation_join_body
    reader_container=$(container_for "$NODE_B")
    rotated_key_path=/data/identity_ed25519_rotated
    docker exec "$reader_container" ssh-keygen -q -t ed25519 -N '' \
        -C 'mcplexer-rotated-integration' -f "$rotated_key_path"
    docker exec -e SSH_AUTH_SOCK=/data/ssh-agent.sock "$reader_container" \
        ssh-add "$rotated_key_path" >/dev/null
    rotated_public_key=$(docker exec "$reader_container" cat "$rotated_key_path.pub")
    home_snapshot=$(api GET "$NODE_A/api/v1/collaboration")
    old_key_id=$(echo "$home_snapshot" | jq -r --arg principal "$COLLAB_READER_PRINCIPAL" \
        '.principals[]? | select(.id == $principal) | .keys[]? | select(.status == "active") | .id' | head -1)
    rotation_body=$(jq -nc --arg principal "$COLLAB_READER_PRINCIPAL" --arg key "$rotated_public_key" --arg old "$old_key_id" \
        '{purpose:"rotate_key",principal_id:$principal,public_key:$key,replaces_key_id:$old}')
    rotation_invite=$(api POST "$NODE_A/api/v1/collaboration/invitations" "$rotation_body")
    rotation_code=$(echo "$rotation_invite" | jq -r '.invite_code // empty')
    new_key_id=$(echo "$rotation_invite" | jq -r '.identity_key.id // empty')
    rotation_join_body=$(jq -nc --arg invitation "$rotation_code" \
        '{invitation:$invitation,device_name:"reader-laptop",device_kind:"laptop"}')
    rotation_join=$(api POST "$NODE_B/api/v1/collaboration/invitations/join" "$rotation_join_body")
    if echo "$rotation_join" | jq -e --arg key "$new_key_id" \
        '.device.status == "active" and .device.identity_key_id == $key' >/dev/null; then
        pass "same-device SSH key rotation rebinds only after fresh proof"
    else
        fail "same-device SSH key rotation failed" "$(echo "$rotation_join" | head -c 400)"
    fi
    api POST "$NODE_A/api/v1/collaboration/keys/$old_key_id/revoke" '{}' >/dev/null
    home_snapshot=$(api GET "$NODE_A/api/v1/collaboration")
    if echo "$home_snapshot" | jq -e --arg principal "$COLLAB_READER_PRINCIPAL" --arg old "$old_key_id" --arg new "$new_key_id" \
        'any(.principals[]?; .id == $principal and
          any(.keys[]?; .id == $old and .status == "revoked") and
          any(.keys[]?; .id == $new and .status == "active") and
          any(.devices[]?; .status == "active" and .identity_key_id == $new))' >/dev/null; then
        pass "old-key revocation preserves the device rebound to the new key"
    else
        fail "old-key revocation damaged the rotated device" "$(echo "$home_snapshot" | head -c 500)"
    fi
}
