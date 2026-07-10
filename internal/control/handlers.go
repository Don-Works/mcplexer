package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/store"
)

type handlerFunc func(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error)

var handlers = map[string]handlerFunc{
	// Server
	"list_servers":  handleListServers,
	"get_server":    handleGetServer,
	"create_server": handleCreateServer,
	"update_server": handleUpdateServer,
	"delete_server": handleDeleteServer,
	// Workspace
	"list_workspaces":  handleListWorkspaces,
	"get_workspace":    handleGetWorkspace,
	"create_workspace": handleCreateWorkspace,
	"update_workspace": handleUpdateWorkspace,
	"delete_workspace": handleDeleteWorkspace,
	// Route
	"list_routes":  handleListRoutes,
	"create_route": handleCreateRoute,
	"update_route": handleUpdateRoute,
	"delete_route": handleDeleteRoute,
	// Auth
	"list_auth_scopes":  handleListAuthScopes,
	"get_auth_scope":    handleGetAuthScope,
	"create_auth_scope": handleCreateAuthScope,
	"update_auth_scope": handleUpdateAuthScope,
	"delete_auth_scope": handleDeleteAuthScope,
	// Monitoring — remote hosts, log sources, alert channels (migration 128)
	"create_remote_host":        handleCreateRemoteHost,
	"list_remote_hosts":         handleListRemoteHosts,
	"get_remote_host":           handleGetRemoteHost,
	"update_remote_host":        handleUpdateRemoteHost,
	"delete_remote_host":        handleDeleteRemoteHost,
	"repin_remote_host":         handleRepinRemoteHost,
	"create_log_source":         handleCreateLogSource,
	"list_log_sources":          handleListLogSources,
	"get_log_source":            handleGetLogSource,
	"update_log_source":         handleUpdateLogSource,
	"delete_log_source":         handleDeleteLogSource,
	"create_monitoring_channel": handleCreateMonitoringChannel,
	"list_monitoring_channels":  handleListMonitoringChannels,
	"get_monitoring_channel":    handleGetMonitoringChannel,
	"update_monitoring_channel": handleUpdateMonitoringChannel,
	"delete_monitoring_channel": handleDeleteMonitoringChannel,
	// Info
	"status":      handleStatus,
	"query_audit": handleQueryAudit,
	// Skills registry admin
	"list_skill_registry":       handleListSkillRegistry,
	"get_skill_registry":        handleGetSkillRegistry,
	"delete_skill_registry":     handleDeleteSkillRegistry,
	"set_skill_registry_tag":    handleSetSkillRegistryTag,
	"delete_skill_registry_tag": handleDeleteSkillRegistryTag,
	// Tool discovery (M0.7 MCP parity)
	"list_available_tools": handleListAvailableTools,
	// Linked workspaces (cross-machine task replication; migration 088)
	"link_workspace":          handleLinkWorkspace,
	"list_workspace_links":    handleListWorkspaceLinks,
	"unlink_workspace":        handleUnlinkWorkspace,
	"suggest_workspace_links": handleSuggestWorkspaceLinks,
	// Identity — read-only views of the M7.1 users + peer_users tables
	// (whoami / list users / get one user / list a user's devices/peers).
	// Mutating peer-ownership flows remain admin-gated follow-ups.
	"whoami":            handleWhoami,
	"list_users":        handleListUsers,
	"get_user":          handleGetUser,
	"list_user_devices": handleListUserDevices,
	// AI subscription usage source configuration. Snapshot/refresh dispatch
	// through InternalBackend because they need the live usage service.
	"configure_usage_source": handleConfigureUsageSource,
	"remove_usage_source":    handleRemoveUsageSource,
}

// textResult wraps a text string in MCP CallToolResult format.
func textResult(text string) json.RawMessage {
	result := gateway.CallToolResult{
		Content: []gateway.ToolContent{{Type: "text", Text: text}},
	}
	data, _ := json.Marshal(result)
	return data
}

// jsonResult marshals v to indented JSON and wraps in MCP CallToolResult format.
func jsonResult(v any) (json.RawMessage, error) {
	text, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	return textResult(string(text)), nil
}

// errorResult wraps an error message in MCP CallToolResult format with isError=true.
func errorResult(msg string) json.RawMessage {
	result := gateway.CallToolResult{
		Content: []gateway.ToolContent{{Type: "text", Text: msg}},
		IsError: true,
	}
	data, _ := json.Marshal(result)
	return data
}

// requireID extracts and validates the "id" field from tool arguments.
func requireID(args json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.ID == "" {
		return "", fmt.Errorf("id is required")
	}
	return p.ID, nil
}
