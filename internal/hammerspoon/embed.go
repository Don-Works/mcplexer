package hammerspoon

import _ "embed"

// bridgeLuaSnippet is the contents of internal/hammerspoon/embed/hammerspoon-mcp.lua,
// baked into the binary at build time. The dashboard installer writes this to
// ~/.hammerspoon/hammerspoon-mcp.lua on a fresh setup, and the snippet endpoint
// serves the same bytes for copy-mode users who'd rather audit before
// installing.
//
//go:embed embed/hammerspoon-mcp.lua
var bridgeLuaSnippet string

// BridgeLuaSnippet returns the embedded Hammerspoon bridge Lua snippet. The
// snippet sets up a loopback hs.httpserver on port 27123 (overridable via
// MCPX_HS_PORT) with Bearer auth read from ~/.hammerspoon/.mcp-password and
// dispatches /exec POSTs through pcall, returning a {ok,result,err} envelope.
func BridgeLuaSnippet() string { return bridgeLuaSnippet }

// BridgeLuaFilename returns the canonical filename ("hammerspoon-mcp.lua") the
// snippet is shipped under inside ~/.hammerspoon/. Centralised so the installer
// handler, the snippet endpoint, and tests all agree on the name.
func BridgeLuaFilename() string { return "hammerspoon-mcp.lua" }

// BuildListWindowsLua exposes the list_windows Lua template for use by the
// probe handler's smoke-test step. Keeping a single source of truth means a
// change to the template flows through both the MCP tool and the probe.
func BuildListWindowsLua() string { return buildListWindowsLua() }
