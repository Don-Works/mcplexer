package hammerspoon

// Manager carries the configured Bridge and feature flags for the in-process
// hammerspoon downstream. It's deliberately tiny: the MCPServer wrapper holds
// any tool-dispatch logic and the bridge does the transport. Adding state
// here (caches, sessions, etc.) should be a deliberate choice — most tools
// are one-shot Lua calls and need nothing persistent.
type Manager struct {
	bridge       Bridge
	allowExecLua bool
}

// NewManager constructs a Manager. A nil bridge is replaced with nullBridge
// so every tool call returns a clean "downstream not enabled" envelope rather
// than panicking.
func NewManager(bridge Bridge, allowExecLua bool) *Manager {
	if bridge == nil {
		bridge = nullBridge{}
	}
	return &Manager{bridge: bridge, allowExecLua: allowExecLua}
}

// HasBridge reports whether a real (non-null) bridge is wired. The dashboard
// uses this to decide whether to mark the server "configured" vs "needs
// setup".
func (m *Manager) HasBridge() bool {
	if m == nil {
		return false
	}
	return !isNullBridge(m.bridge)
}

// AllowExecLua reports whether the exec_lua escape-hatch tool should be
// advertised + dispatchable. Off by default; opt-in per machine via env.
func (m *Manager) AllowExecLua() bool {
	if m == nil {
		return false
	}
	return m.allowExecLua
}

// Bridge returns the underlying bridge. Exposed for the MCPServer wrapper and
// tests; not part of the public API surface.
func (m *Manager) Bridge() Bridge {
	if m == nil {
		return nullBridge{}
	}
	return m.bridge
}
