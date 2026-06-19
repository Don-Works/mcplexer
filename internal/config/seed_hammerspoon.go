package config

// IDs used by the optional Hammerspoon downstream. Kept in one place so the
// catalog, auth-scope, and route seeds can refer to them by symbolic name.
const (
	hammerspoonServerID    = "hammerspoon"
	hammerspoonAuthScopeID = "hammerspoon-bridge"
	hammerspoonRouteID     = "hammerspoon-allow"
)

func init() {
	RegisterEnvFields(hammerspoonAuthScopeID, []EnvField{
		{Key: "HAMMERSPOON_BRIDGE_PASSWORD", Label: "Bridge Password", Secret: true},
		{Key: "HAMMERSPOON_BRIDGE_URL", Label: "Bridge URL (default http://127.0.0.1:27123)"},
		{Key: "HAMMERSPOON_DRIVER", Label: "Driver: http or cli (default http)"},
		{Key: "HAMMERSPOON_ALLOW_EXEC_LUA", Label: "Enable raw exec_lua (true/false, default false)"},
	})
}
