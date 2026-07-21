// Package install owns mcplexer's client installation flow: detecting
// which MCP clients are present, writing their config blocks (with
// Receipt-based reversibility), and managing the upcoming Shell Guard
// hooks + Sandbox Guard shim deployment.
//
// This file enumerates the admin MCP tool names that M1+ will expose
// for client management. They live here so cross-package callers can
// reference the canonical strings without a circular import on
// internal/gateway. The names are NOT exposed in tools/list until
// their handlers land in M1.
package install

import "sort"

const (
	ToolClientRegister     = "mcplexer__client_register"
	ToolClientInstallHooks = "mcplexer__client_install_hooks"
	ToolClientInstallShim  = "mcplexer__client_install_shim"

	ToolSandboxEnable    = "mcplexer__sandbox_enable"
	ToolSandboxDisable   = "mcplexer__sandbox_disable"
	ToolSandboxStatus    = "mcplexer__sandbox_status"
	ToolSandboxAttach    = "mcplexer__sandbox_attach"
	ToolSandboxUninstall = "mcplexer__sandbox_uninstall"

	ToolAllowlistGet = "mcplexer__allowlist_get"
	ToolAllowlistSet = "mcplexer__allowlist_set"

	ToolScheduleList   = "mcplexer__schedule_list"
	ToolScheduleCreate = "mcplexer__schedule_create"
	ToolScheduleDelete = "mcplexer__schedule_delete"

	ToolSanitizerGet  = "mcplexer__sanitizer_get"
	ToolSanitizerSet  = "mcplexer__sanitizer_set"
	ToolSanitizerTest = "mcplexer__sanitizer_test"
)

// ReservedToolNames returns the canonical list of upcoming admin MCP
// tool names, sorted lexicographically. Use this to assert uniqueness
// or to advertise the planned surface in docs. A fresh slice is
// returned on each call so callers cannot mutate the canonical order.
func ReservedToolNames() []string {
	names := []string{
		ToolClientRegister,
		ToolClientInstallHooks,
		ToolClientInstallShim,

		ToolSandboxEnable,
		ToolSandboxDisable,
		ToolSandboxStatus,
		ToolSandboxAttach,
		ToolSandboxUninstall,

		ToolAllowlistGet,
		ToolAllowlistSet,

		ToolScheduleList,
		ToolScheduleCreate,
		ToolScheduleDelete,

		ToolSanitizerGet,
		ToolSanitizerSet,
		ToolSanitizerTest,
	}
	sort.Strings(names)
	return names
}
