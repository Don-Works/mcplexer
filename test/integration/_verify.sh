#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")"
NODE_A="${NODE_A:-http://localhost:23333}"
NODE_B=""; NODE_C=""; CONT_A="mcplexer-test-node-a"
TOK_A=""; TOK_B=""; TOK_C=""; TOK_D=""; TOK_E=""
. ./lib.sh
. ./lib_monitoring.sh
. ./scenario_monitoring.sh
. ./scenario_monitoring_incidents.sh
TOK_A="$(fetch_token "$CONT_A")"
scenario_monitoring_setup >/dev/null 2>&1
scenario_monitoring_dead_channel_surfaced
echo
echo "--- LIVE SHAPE CHECK (targeting ledger + REST boundary) ---"
prov=$(mon_isolated_ws "verify"); ws=$(echo "$prov" | awk '{print $1}')
cbody=$(jq -nc --arg ws "$ws" \
  '{workspace_id:$ws, name:"verify-dead-route", kind:"gchat_webhook",
    config_json:"{\"auth_scope_id\":\"missing-scope\",\"webhook_ref\":\"secret://HARNESS_DEAD_ROUTE\"}",
    min_severity:"info", enabled:true}')
id=$(api POST "$NODE_A/api/v1/monitoring-channels" "$cbody" | jq -r '.id')
echo "== notify HTTP statuses + bodies over 9 sends =="
for i in $(seq 1 9); do
  resp=$(curl -sS -o /tmp/nb.json -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOK_A" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg ws "$ws" --arg t "verify probe $i" \
       '{workspace_id:$ws, severity:"error", title:$t, body:"probe", new_incident:true}')" \
    "$NODE_A/api/v1/monitoring/notify")
  printf '  send %d -> HTTP %s  %s\n' "$i" "$resp" "$(jq -c '{status,dispatched,delivered,attempted}' /tmp/nb.json 2>/dev/null)"
done
echo "== channel row =="
api GET "$NODE_A/api/v1/monitoring-channels/$id" | jq '{health,broken,consecutive_failures,targeted_since_success,last_success_at,last_targeted_at}'
echo "== status summary =="
api GET "$NODE_A/api/v1/monitoring/status?workspace_id=$ws" | jq '.channels'
