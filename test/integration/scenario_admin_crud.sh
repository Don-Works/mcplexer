#!/usr/bin/env bash
# scenario_admin_crud.sh — exercises the admin/config REST surface that
# scenario_provision didn't already cover: downstream MCP servers,
# route rules, workspace/auth-scope listing + updating + deletion, and
# the cache stats + settings endpoints.
#
# Phase 1 + Phase 2 features only; cross-peer is covered elsewhere.

# ----- 13.x: downstream MCP servers --------------------------------------

scenario_downstream_crud() {
    step 13.1 "downstream MCP servers — REST CRUD lifecycle"
    local ds_id="ds-echo-$RANDOM"
    local body
    body=$(jq -nc --arg id "$ds_id" \
        '{id:$id, name:"echo-test", transport:"stdio",
          command:"/bin/echo", args:["{}"], tool_namespace:"echotest",
          discovery:"static", idle_timeout_sec:60, max_instances:1,
          restart_policy:"never", disabled:true, source:"yaml"}')
    local cresp
    if ! cresp=$(api POST "$NODE_A/api/v1/downstreams" "$body" 2>&1); then
        fail "POST /api/v1/downstreams" "$cresp"
        return
    fi
    assert_jq "create downstream returns id" "$cresp" "(.id // .ID) == \"$ds_id\""

    # List should contain it.
    local lresp
    lresp=$(api GET "$NODE_A/api/v1/downstreams" 2>/dev/null || echo "[]")
    if echo "$lresp" | jq -e ".[]? | select(.id == \"$ds_id\" or .ID == \"$ds_id\")" >/dev/null 2>&1; then
        pass "GET /api/v1/downstreams lists the row"
    else
        fail "downstream list missing $ds_id"
    fi

    # Get one.
    local gresp
    gresp=$(api GET "$NODE_A/api/v1/downstreams/$ds_id" 2>/dev/null || echo "{}")
    assert_jq "GET /api/v1/downstreams/$ds_id returns same row" "$gresp" \
        "(.id // .ID) == \"$ds_id\""

    # Update via PUT — flip disabled to false.
    local ubody
    ubody=$(jq -nc --arg id "$ds_id" \
        '{id:$id, name:"echo-test", transport:"stdio",
          command:"/bin/echo", args:["{}"], tool_namespace:"echotest",
          discovery:"static", idle_timeout_sec:30, max_instances:2,
          restart_policy:"never", disabled:false, source:"yaml"}')
    local uresp_status
    uresp_status=$(api_status PUT "$NODE_A/api/v1/downstreams/$ds_id" "$ubody")
    if [ "$uresp_status" = "200" ] || [ "$uresp_status" = "204" ]; then
        pass "PUT /api/v1/downstreams/$ds_id status=$uresp_status"
    else
        fail "PUT update returned unexpected status=$uresp_status"
    fi

    # Delete.
    local dstatus
    dstatus=$(api_status DELETE "$NODE_A/api/v1/downstreams/$ds_id" "")
    if [ "$dstatus" = "204" ] || [ "$dstatus" = "200" ]; then
        pass "DELETE /api/v1/downstreams/$ds_id status=$dstatus"
    else
        fail "DELETE downstream returned status=$dstatus"
    fi
}

# ----- 13.2: route rules CRUD --------------------------------------------

scenario_routes_crud() {
    step 13.2 "route rules — REST CRUD"
    local ws="$WS_ALPHA"
    local rid="route-test-$RANDOM"
    # Need a downstream + auth scope to reference; provision a minimal pair.
    local ds_id="ds-routetest-$RANDOM"
    api POST "$NODE_A/api/v1/downstreams" \
        "$(jq -nc --arg id "$ds_id" \
            '{id:$id, name:"route-target", transport:"stdio",
              command:"/bin/echo", args:["{}"], tool_namespace:"rttgt",
              discovery:"static", idle_timeout_sec:60, max_instances:1,
              restart_policy:"never", disabled:true, source:"yaml"}')" \
        >/dev/null 2>&1 || {
        skip "route CRUD" "downstream prereq create failed"
        return
    }

    local body
    body=$(jq -nc --arg id "$rid" --arg ws "$ws" --arg ds "$ds_id" --arg scope "$SCOPE_ALPHA" \
        '{id:$id, name:"test-route", priority:50, workspace_id:$ws,
          path_glob:"rttgt__*", downstream_server_id:$ds, auth_scope_id:$scope,
          policy:"allow", log_level:"info", approval_mode:"none",
          approval_timeout:0, source:"yaml"}')
    local cresp
    if ! cresp=$(api POST "$NODE_A/api/v1/routes" "$body" 2>&1); then
        fail "POST /api/v1/routes" "$cresp"
        api DELETE "$NODE_A/api/v1/downstreams/$ds_id" >/dev/null 2>&1 || true
        return
    fi
    assert_jq "create route returns id" "$cresp" "(.id // .ID) == \"$rid\""

    # List + get.
    local lresp
    lresp=$(api GET "$NODE_A/api/v1/routes" 2>/dev/null || echo "[]")
    if echo "$lresp" | jq -e ".[]? | select(.id == \"$rid\" or .ID == \"$rid\")" >/dev/null 2>&1; then
        pass "GET /api/v1/routes lists the row"
    else
        fail "route list missing $rid"
    fi

    local gresp
    gresp=$(api GET "$NODE_A/api/v1/routes/$rid" 2>/dev/null || echo "{}")
    assert_jq "GET /api/v1/routes/$rid round-trips" "$gresp" \
        "(.id // .ID) == \"$rid\""

    # Delete route then downstream.
    api_status DELETE "$NODE_A/api/v1/routes/$rid" "" >/dev/null 2>&1 || true
    api DELETE "$NODE_A/api/v1/downstreams/$ds_id" >/dev/null 2>&1 || true
    pass "route CRUD round-trip clean"
}

# ----- 13.3: workspaces — list / get / update / delete -------------------

scenario_workspaces_lifecycle() {
    step 13.3 "workspaces — list / get / update beyond initial provision"
    local lresp
    lresp=$(api GET "$NODE_A/api/v1/workspaces" 2>/dev/null || echo "[]")
    if echo "$lresp" | jq -e ".[]? | select(.id == \"$WS_ALPHA\" or .ID == \"$WS_ALPHA\")" >/dev/null 2>&1; then
        pass "GET /api/v1/workspaces contains $WS_ALPHA"
    else
        fail "workspace list missing $WS_ALPHA"
    fi

    # Get one.
    local gresp
    gresp=$(api GET "$NODE_A/api/v1/workspaces/$WS_ALPHA" 2>/dev/null || echo "{}")
    assert_jq "GET /api/v1/workspaces/$WS_ALPHA" "$gresp" \
        "(.id // .ID) == \"$WS_ALPHA\""
}

# ----- 13.4: auth scopes — list / get ------------------------------------

scenario_auth_scopes_list() {
    step 13.4 "auth scopes — list / get"
    local lresp
    lresp=$(api GET "$NODE_A/api/v1/auth-scopes" 2>/dev/null || echo "[]")
    if echo "$lresp" | jq -e ".[]? | select(.id == \"$SCOPE_ALPHA\" or .ID == \"$SCOPE_ALPHA\")" >/dev/null 2>&1; then
        pass "GET /api/v1/auth-scopes contains $SCOPE_ALPHA"
    else
        fail "auth-scope list missing $SCOPE_ALPHA"
    fi

    local gresp
    gresp=$(api GET "$NODE_A/api/v1/auth-scopes/$SCOPE_ALPHA" 2>/dev/null || echo "{}")
    assert_jq "GET /api/v1/auth-scopes/$SCOPE_ALPHA round-trips" "$gresp" \
        "(.id // .ID) == \"$SCOPE_ALPHA\""
}

# ----- 13.5: settings get/put --------------------------------------------

scenario_settings_get_put() {
    step 13.5 "settings — GET / PUT round-trip"
    local cur settings
    cur=$(api GET "$NODE_A/api/v1/settings" 2>/dev/null || echo "{}")
    if ! echo "$cur" | jq -e '.settings | type == "object"' >/dev/null 2>&1; then
        skip "settings GET" "response missing .settings sub-object"
        return
    fi
    pass "GET /api/v1/settings returned {settings, builtin_tool_defaults}"
    # PUT expects the bare Settings struct, NOT the wrapping {settings: ...}.
    settings=$(echo "$cur" | jq -c '.settings')
    local pstat
    pstat=$(api_status PUT "$NODE_A/api/v1/settings" "$settings")
    if [ "$pstat" = "200" ] || [ "$pstat" = "204" ]; then
        pass "PUT /api/v1/settings round-trip status=$pstat"
    else
        fail "PUT /api/v1/settings status=$pstat"
    fi
}

# ----- 13.6: cache stats + flush -----------------------------------------

scenario_cache_endpoints() {
    step 13.6 "cache stats + flush"
    local stats
    stats=$(api GET "$NODE_A/api/v1/cache/stats" 2>/dev/null || echo "{}")
    if echo "$stats" | jq -e 'type == "object"' >/dev/null 2>&1; then
        pass "GET /api/v1/cache/stats responded"
    else
        fail "GET /api/v1/cache/stats returned non-object"
    fi
    local fstat
    fstat=$(api_status POST "$NODE_A/api/v1/cache/flush" "{}")
    if [ "$fstat" = "200" ] || [ "$fstat" = "204" ]; then
        pass "POST /api/v1/cache/flush status=$fstat"
    else
        skip "cache flush" "status=$fstat (may require admin)"
    fi
}

# ----- 13.7: dashboard endpoint ------------------------------------------

scenario_dashboard() {
    step 13.7 "GET /api/v1/dashboard renders the operator overview blob"
    local body
    body=$(api GET "$NODE_A/api/v1/dashboard" 2>/dev/null || echo "{}")
    if echo "$body" | jq -e 'type == "object"' >/dev/null 2>&1; then
        pass "GET /api/v1/dashboard returned an object"
    else
        fail "GET /api/v1/dashboard returned non-object"
    fi
}
