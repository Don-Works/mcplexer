package hammerspoon

import (
	"strings"
	"testing"
)

// String-shape assertions for the Lua templates. These don't run the Lua —
// that's the integration scenario's job — but they catch regressions in the
// JSON-safe escaping and the envelope-encoding contract.

func TestLuaQuote_BasicEscaping(t *testing.T) {
	got := luaQuote(`hello "world"`)
	// JSON-marshalled string — \"-escaped quotes, double-quoted boundary.
	want := `"hello \"world\""`
	if got != want {
		t.Errorf("luaQuote: want %q got %q", want, got)
	}
}

func TestLuaQuote_Newline(t *testing.T) {
	got := luaQuote("line1\nline2")
	if got != `"line1\nline2"` {
		t.Errorf("luaQuote newline: got %s", got)
	}
}

func TestLuaQuoteList_Empty(t *testing.T) {
	if got := luaQuoteList(nil); got != "{}" {
		t.Errorf("empty: got %s", got)
	}
}

func TestLuaQuoteList_Values(t *testing.T) {
	got := luaQuoteList([]string{"cmd", "shift"})
	if got != `{"cmd","shift"}` {
		t.Errorf("got %s", got)
	}
}

func TestBuildListWindowsLua_Shape(t *testing.T) {
	src := buildListWindowsLua()
	if !strings.Contains(src, `hs.window.allWindows()`) {
		t.Error("list_windows: missing allWindows()")
	}
	if !strings.Contains(src, `return {windows = out}`) {
		t.Error("list_windows: must return a Lua table (bridge wraps + encodes — see lua.go header)")
	}
}

func TestBuildFocusAppLua_QuotesInput(t *testing.T) {
	src := buildFocusAppLua(`com.apple.Safari`)
	if !strings.Contains(src, `"com.apple.Safari"`) {
		t.Errorf("focus_app: bundle id not embedded as quoted literal: %s", src)
	}
	// A bundle id containing a backslash should be escape-quoted, not
	// raw-embedded — guards against Lua parse breaks from user-supplied
	// input.
	src2 := buildFocusAppLua(`weird"name\with`)
	if !strings.Contains(src2, `"weird\"name\\with"`) {
		t.Errorf("focus_app: did not escape weird input. got fragment containing: %q", src2)
	}
}

func TestBuildScreenshotLua_DefaultsScreen(t *testing.T) {
	src := buildScreenshotLua(ScreenshotParams{ReturnBase64: true})
	if !strings.Contains(src, `target="screen"`) {
		t.Errorf("default target not screen: %s", src)
	}
	if !strings.Contains(src, `hs.screen.mainScreen():snapshot()`) {
		t.Error("missing screen capture branch")
	}
}

func TestBuildScreenshotLua_ValidatesPNGPayload(t *testing.T) {
	// Regression net for the zero-buffer bug: a blank capture encodes to a raw
	// zeroed buffer, not a PNG. The snippet must reject anything whose base64
	// payload doesn't start with the PNG-signature marker rather than hand back
	// junk that the Go side would spill to disk.
	src := buildScreenshotLua(ScreenshotParams{ReturnBase64: true})
	if !strings.Contains(src, `"iVBORw0KGgo"`) {
		t.Errorf("screenshot: missing PNG-signature validation of the base64 payload:\n%s", src)
	}
}

func TestBuildScreenshotLua_WindowBranch(t *testing.T) {
	src := buildScreenshotLua(ScreenshotParams{Target: "window", WindowID: 12345})
	if !strings.Contains(src, `window_id=12345`) {
		t.Errorf("window id not embedded as numeric literal: %s", src)
	}
	if !strings.Contains(src, `target="window"`) {
		t.Errorf("target not window: %s", src)
	}
}

func TestBuildScreenshotLua_SavePathQuoted(t *testing.T) {
	src := buildScreenshotLua(ScreenshotParams{Target: "screen", SavePath: "/tmp/x.png"})
	if !strings.Contains(src, `save_path="/tmp/x.png"`) {
		t.Errorf("save_path not embedded: %s", src)
	}
}

func TestBuildSendKeysLua_TextBranch(t *testing.T) {
	src := buildSendKeysLua(SendKeysParams{Text: "hello"})
	if !strings.Contains(src, `text="hello"`) {
		t.Errorf("text not embedded: %s", src)
	}
	if !strings.Contains(src, `hs.eventtap.keyStrokes`) {
		t.Error("missing keyStrokes branch")
	}
}

func TestBuildSendKeysLua_ChordBranch(t *testing.T) {
	src := buildSendKeysLua(SendKeysParams{Keys: []string{"c"}, Modifiers: []string{"cmd"}})
	if !strings.Contains(src, `keys={"c"}`) {
		t.Errorf("keys not embedded as list: %s", src)
	}
	if !strings.Contains(src, `modifiers={"cmd"}`) {
		t.Errorf("modifiers not embedded: %s", src)
	}
	if !strings.Contains(src, `hs.eventtap.keyStroke(args.modifiers, k, 0)`) {
		t.Error("missing keyStroke branch")
	}
}

func TestBuildNotifyLua_QuotesAllFields(t *testing.T) {
	src := buildNotifyLua(NotifyParams{
		Title: "Hi", Subtitle: "From mcplexer", Body: "Body", Sound: "Glass",
	})
	for _, want := range []string{`title="Hi"`, `subTitle="From mcplexer"`, `informativeText="Body"`, `soundName="Glass"`} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in: %s", want, src)
		}
	}
}

func TestBuildExecLuaLua_Passthrough(t *testing.T) {
	user := `return hs.application.frontmostApplication():name()`
	got := buildExecLuaLua(user)
	if got != user {
		t.Errorf("exec_lua should be passthrough, got %q", got)
	}
}

// TestLuaTemplates_ReturnTablesNotStrings is the regression net for the
// historical double-encode bug. Every snippet must end with `return <table>`
// (or `return out`-style) — NEVER `return hs.json.encode(<table>)`. The
// bridge (~/.hammerspoon/hammerspoon-mcp.lua) already wraps the return value
// as `{ok=true, result=<value>}` and hs.json.encode's the wrapper. A snippet
// that pre-encodes produces `{"result":"<escaped-json>"}` instead of
// `{"result":{...}}`, which breaks any Go-side handler that re-parses
// env.Result (e.g. screenshot's inline-base64 cap) and silently
// double-escapes the MCP-text path for everything else. See the lua.go
// header comment for the full story.
func TestLuaTemplates_ReturnTablesNotStrings(t *testing.T) {
	cases := map[string]string{
		"list_windows": buildListWindowsLua(),
		"focus_app":    buildFocusAppLua("Safari"),
		"screenshot":   buildScreenshotLua(ScreenshotParams{}),
		"send_keys":    buildSendKeysLua(SendKeysParams{Text: "x"}),
		"notify":       buildNotifyLua(NotifyParams{Title: "x"}),
	}
	for name, src := range cases {
		if strings.Contains(src, "hs.json.encode") {
			t.Errorf("%s: must NOT pre-encode (the bridge wraps + encodes). Snippet:\n%s", name, src)
		}
	}
}
