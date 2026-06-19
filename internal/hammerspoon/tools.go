package hammerspoon

// Tool schemas advertised via tools/list. Tool names omit the
// "hammerspoon__" prefix; mcplexer adds the namespace at routing time.
//
// The schema returned to clients is built by serializing the structs below
// into a JSON object. Defining them as Go literals (rather than a static
// string) lets the exec_lua gate filter the list at runtime without string
// surgery.

func alwaysOnTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_windows",
			"description": "List visible windows across all running apps on macOS via Hammerspoon. Returns one entry per window with app, pid, title, frame (x,y,w,h), frontmost flag, and a stable window_id usable by focus_app / screenshot.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "focus_app",
			"description": "Bring a macOS application to the foreground. Accepts either a bundle ID (com.apple.Safari) or a display name (Safari). Requires Accessibility permission for the Hammerspoon.app.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"app": map[string]any{"type": "string", "description": "Bundle ID or app name."},
				},
				"required": []string{"app"},
			},
		},
		{
			"name":        "screenshot",
			"description": "Capture a screenshot of the whole screen, a single window, or all windows of an app. PNG output is returned inline as base64 when under 2 MB; larger captures spill to ~/.mcplexer/hammerspoon-screenshots/<ts>.png and the response carries the path only. Note: window-level capture under Electron/Slack may be incomplete unless the app was launched with --force-renderer-accessibility.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target":        map[string]any{"type": "string", "enum": []string{"screen", "window", "app"}, "description": "Defaults to screen."},
					"app":           map[string]any{"type": "string", "description": "Required when target=app."},
					"window_id":     map[string]any{"type": "number", "description": "Required when target=window. From list_windows."},
					"save_path":     map[string]any{"type": "string", "description": "Optional absolute path to write the PNG to. Overrides the spill location."},
					"return_base64": map[string]any{"type": "boolean", "description": "Defaults to true. Set false to skip inline base64 even when under the cap."},
				},
			},
		},
		{
			"name":        "send_keys",
			"description": "Send a chord (keys + modifiers) or literal text to the frontmost app. Requires Accessibility permission. Use either keys+modifiers OR text, not both.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"keys":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Key chord, e.g. ['c']. Combined with modifiers."},
					"text":      map[string]any{"type": "string", "description": "Literal text to type. Mutually exclusive with keys."},
					"modifiers": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"cmd", "alt", "shift", "ctrl"}}},
					"window_id": map[string]any{"type": "number", "description": "Optional: focus this window first."},
				},
			},
		},
		{
			"name":        "notify",
			"description": "Post a macOS notification banner via Hammerspoon. No Accessibility needed.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":    map[string]any{"type": "string"},
					"subtitle": map[string]any{"type": "string"},
					"body":     map[string]any{"type": "string"},
					"sound":    map[string]any{"type": "string", "description": "macOS sound name, e.g. 'Glass'."},
				},
				"required": []string{"title"},
			},
		},
	}
}

func execLuaTool() map[string]any {
	return map[string]any{
		"name":        "exec_lua",
		"description": "Run an arbitrary Lua snippet inside the user's Hammerspoon runtime. RCE escape hatch — gated by HAMMERSPOON_ALLOW_EXEC_LUA=true. Returns the snippet's return value JSON-encoded.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lua":        map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "number", "description": "Per-call timeout, max 30000. Defaults to 5000."},
			},
			"required": []string{"lua"},
		},
	}
}
