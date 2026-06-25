// audit_filter_map.go — convert a JSON filter object (the deep-link /
// saved-search filter shape) into a store.AuditFilter. Keeps the
// saved-search evaluator and any future map-driven query on the same
// exact-match dimensions QueryAuditRecords honours.
package sqlite

import "github.com/don-works/mcplexer/internal/store"

// filterMapToAuditFilter maps the JSON keys used in saved-search
// filter_json / alert deep-links onto AuditFilter pointer fields. Only
// string + bool + numeric scalars are recognised; unknown keys are
// ignored. Time-range + q are layered on by the caller.
func filterMapToAuditFilter(m map[string]any) store.AuditFilter {
	var f store.AuditFilter
	str := func(key string) *string {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return &s
			}
		}
		return nil
	}
	f.WorkspaceID = str("workspace_id")
	f.ToolName = str("tool_name")
	f.Status = str("status")
	f.SessionID = str("session_id")
	f.ExecutionID = str("execution_id")
	f.ActorKind = str("actor_kind")
	f.ActorID = str("actor_id")
	f.DownstreamServerID = str("downstream_server_id")
	f.RouteRuleID = str("route_rule_id")
	f.ClientType = str("client_type")
	f.ErrorCode = str("error_code")
	f.Tier = str("tier")
	if v, ok := m["cache_hit"]; ok {
		if b, ok := v.(bool); ok {
			cb := b
			f.CacheHit = &cb
		}
	}
	if v, ok := m["min_latency_ms"]; ok {
		// JSON numbers decode to float64.
		if n, ok := v.(float64); ok {
			ni := int(n)
			f.MinLatencyMs = &ni
		}
	}
	return f
}
