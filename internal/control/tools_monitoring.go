// tools_monitoring.go — MCP tool defs for the Monitoring feature
// (remote hosts, log sources, alert channels; migration 128). All
// CWD-gated like every mcplexer__* tool. The collector only ever runs
// fixed read-only argv templates on these hosts — see ADR 0007.
package control

import (
	"github.com/don-works/mcplexer/internal/gateway"
)

// monitoringToolDefs returns every Monitoring admin tool, appended into
// allTools().
func monitoringToolDefs() []gateway.Tool {
	return []gateway.Tool{
		// --- remote hosts ---
		{
			Name:        "create_remote_host",
			Description: "Create a Monitoring remote host — an SSH target the log collector pulls docker logs from (read-only by construction, ADR 0007). auth_scope_id points at the AuthScope holding the ssh_key material or ssh_agent socket ref. The host-key pin is TOFU-recorded on first dial.",
			InputSchema: schema(props{
				"workspace_id":  propStr("Workspace this host belongs to (required)."),
				"name":          propStr("Host name, unique per workspace (required)."),
				"ssh_user":      propStr("SSH user, e.g. logwatch (required)."),
				"ssh_host":      propStr("Hostname or IPv4 address (required)."),
				"ssh_port":      propInt("SSH port. Default 22."),
				"auth_scope_id": propStr("AuthScope id holding the SSH credential (required)."),
				"enabled":       map[string]any{"type": "boolean", "description": "Default true."},
			}, []string{"workspace_id", "name", "ssh_user", "ssh_host", "auth_scope_id"}),
		},
		{
			Name:        "list_remote_hosts",
			Description: "List Monitoring remote hosts in a workspace, including disabled rows and host-key pin state.",
			InputSchema: schema(props{
				"workspace_id": propStr("Workspace ID (required)."),
			}, []string{"workspace_id"}),
		},
		{
			Name:        "get_remote_host",
			Description: "Get one Monitoring remote host by ID.",
			InputSchema: schema(props{"id": propStr("Remote host ID (required).")}, []string{"id"}),
		},
		{
			Name:        "update_remote_host",
			Description: "Update a Monitoring remote host (partial update — only provided fields change). Host-key pin is NOT editable here; use repin_remote_host after a deliberate host rebuild.",
			InputSchema: schema(props{
				"id":            propStr("Remote host ID (required)."),
				"name":          propStr("New name."),
				"ssh_user":      propStr("New SSH user."),
				"ssh_host":      propStr("New hostname/IPv4."),
				"ssh_port":      propInt("New port."),
				"auth_scope_id": propStr("New AuthScope id."),
				"enabled":       map[string]any{"type": "boolean", "description": "Enable/disable collection from this host."},
			}, []string{"id"}),
		},
		{
			Name:        "delete_remote_host",
			Description: "Delete a Monitoring remote host. Its log sources cascade-delete.",
			InputSchema: schema(props{"id": propStr("Remote host ID (required).")}, []string{"id"}),
		},
		{
			Name:        "repin_remote_host",
			Description: "Clear a remote host's TOFU host-key pin so the next successful dial re-records it. Explicit operator action for legitimate host rebuilds (ADR 0007 §3) — a pin MISMATCH always hard-fails and is never resolved automatically.",
			InputSchema: schema(props{"id": propStr("Remote host ID (required).")}, []string{"id"}),
		},

		// --- log sources ---
		{
			Name:        "create_log_source",
			Description: "Create a Monitoring log source — one docker container's logs on a remote host, pulled incrementally on a schedule and distilled into templates. Selector is the container name (strict charset, no shell metacharacters).",
			InputSchema: schema(props{
				"workspace_id":   propStr("Workspace (required)."),
				"remote_host_id": propStr("Remote host this source lives on (required)."),
				"name":           propStr("Source name, unique per workspace (required)."),
				"kind":           propStr("docker (default; the v1 collection contract) | compose | journald | file."),
				"selector":       propStr("Docker container name, ^[A-Za-z0-9._/-]+$ (required)."),
				"schedule_spec":  propStr("Pull cadence: Go duration ('2m' default) or cron expression."),
				"max_pull_bytes": propInt("Per-pull byte cap. Default 4194304 (4 MiB)."),
				"retention_mb":   propInt("Raw ring-buffer cap in MB. Default 50."),
				"retention_days": propInt("Raw ring-buffer age cap in days. Default 7."),
				"enabled":        map[string]any{"type": "boolean", "description": "Default true."},
			}, []string{"workspace_id", "remote_host_id", "name", "selector"}),
		},
		{
			Name:        "list_log_sources",
			Description: "List Monitoring log sources in a workspace with cursor + health (consecutive_failures) state.",
			InputSchema: schema(props{
				"workspace_id": propStr("Workspace ID (required)."),
			}, []string{"workspace_id"}),
		},
		{
			Name:        "get_log_source",
			Description: "Get one Monitoring log source by ID.",
			InputSchema: schema(props{"id": propStr("Log source ID (required).")}, []string{"id"}),
		},
		{
			Name:        "update_log_source",
			Description: "Update a Monitoring log source (partial update — only provided fields change). Cursor state is collector-owned and not editable.",
			InputSchema: schema(props{
				"id":             propStr("Log source ID (required)."),
				"remote_host_id": propStr("Move the source to another host."),
				"name":           propStr("New name."),
				"kind":           propStr("docker | compose | journald | file."),
				"selector":       propStr("New selector, ^[A-Za-z0-9._/-]+$."),
				"schedule_spec":  propStr("New pull cadence."),
				"max_pull_bytes": propInt("New per-pull byte cap."),
				"retention_mb":   propInt("New ring-buffer MB cap."),
				"retention_days": propInt("New ring-buffer age cap."),
				"enabled":        map[string]any{"type": "boolean", "description": "Enable/disable this source."},
			}, []string{"id"}),
		},
		{
			Name:        "delete_log_source",
			Description: "Delete a Monitoring log source. Its templates + raw lines cascade-delete.",
			InputSchema: schema(props{"id": propStr("Log source ID (required).")}, []string{"id"}),
		},

		// --- alert channels ---
		{
			Name:        "create_monitoring_channel",
			Description: "Create a Monitoring alert channel — one output the daemon-side dispatcher fans incidents to when severity >= min_severity. config_json carries secret:// refs ONLY (e.g. {\"webhook_ref\":\"secret://GCHAT_WEBHOOK_INCIDENTS\"}); plaintext URLs/credentials are rejected. Every message carries the deterministic envelope [workspace · via gateway-host] SEVERITY · remote-host.",
			InputSchema: schema(props{
				"workspace_id": propStr("Workspace (required)."),
				"name":         propStr("Channel name, unique per workspace (required)."),
				"kind":         propStr("gchat_webhook | telegram | whatsapp | mesh (required)."),
				"config_json":  propStr("JSON object. gchat_webhook needs webhook_ref (secret:// ref); whatsapp needs to_ref (secret:// ref); telegram takes chat_id; mesh needs nothing."),
				"min_severity": propStr("Severity floor: info|warn|error|critical. Default error."),
				"enabled":      map[string]any{"type": "boolean", "description": "Default true."},
			}, []string{"workspace_id", "name", "kind"}),
		},
		{
			Name:        "list_monitoring_channels",
			Description: "List Monitoring alert channels in a workspace, including disabled rows.",
			InputSchema: schema(props{
				"workspace_id": propStr("Workspace ID (required)."),
			}, []string{"workspace_id"}),
		},
		{
			Name:        "get_monitoring_channel",
			Description: "Get one Monitoring alert channel by ID.",
			InputSchema: schema(props{"id": propStr("Channel ID (required).")}, []string{"id"}),
		},
		{
			Name:        "update_monitoring_channel",
			Description: "Update a Monitoring alert channel (partial update — only provided fields change). The secrets rule still applies: config values must be secret:// refs, never plaintext.",
			InputSchema: schema(props{
				"id":           propStr("Channel ID (required)."),
				"name":         propStr("New name."),
				"kind":         propStr("gchat_webhook | telegram | whatsapp | mesh."),
				"config_json":  propStr("New config JSON (secret:// refs only)."),
				"min_severity": propStr("New severity floor: info|warn|error|critical."),
				"enabled":      map[string]any{"type": "boolean", "description": "Enable/disable this channel."},
			}, []string{"id"}),
		},
		{
			Name:        "delete_monitoring_channel",
			Description: "Delete a Monitoring alert channel.",
			InputSchema: schema(props{"id": propStr("Channel ID (required).")}, []string{"id"}),
		},
	}
}
