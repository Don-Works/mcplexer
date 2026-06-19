package hammerspoon

import (
	"encoding/json"
	"strings"
)

// Lua snippet builders, one per v0.1 tool. Each function returns a Lua chunk
// whose final statement is `return <table>` — a Lua TABLE, NOT a string. The
// bridge wraps every successful return as `{ok=true, result=<value>}` and
// then `hs.json.encode`s the wrapper. Returning a TABLE makes the bridge
// serialize the result as a proper JSON object (`{"result":{...}}`).
//
// Returning a STRING (e.g. `hs.json.encode(<table>)`) instead — the historical
// pattern — was a double-encode bug: the bridge sees a string and emits
// `{"result":"<escaped-json>"}`, the Go envelope's `Result json.RawMessage`
// then carries a quoted JSON-encoded string rather than the inner object, and
// every Go-side handler that wants to inspect the payload (screenshot, which
// must enforce its own inline-base64 cap) blows up with
// `cannot unmarshal string into Go value of type struct {...}`. The MCP-text
// path (list_windows, notify, focus_app, send_keys) silently passed the
// double-escaped JSON through as the tool's text result — wrong shape but no
// visible failure. Returning tables fixes both surfaces in one move.
//
// User-supplied strings are embedded as Go-marshalled JSON literals — JSON
// string syntax is a strict subset of Lua string syntax for printable ASCII
// + standard escapes, so a `json.Marshal(s)` of any Go string yields a valid
// Lua string literal. This avoids the manual escaping hazards of
// fmt.Sprintf("%q", …) (Go %q ≠ Lua %q for some edge cases).

// luaQuote returns a Lua string literal containing the supplied Go string.
// Implemented by marshalling to JSON — Lua accepts JSON's escape set.
func luaQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// luaQuoteList returns a Lua array literal of quoted strings: {"a","b",...}.
// Empty input yields "{}".
func luaQuoteList(xs []string) string {
	if len(xs) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, luaQuote(x))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// luaBool turns a Go bool into a Lua bool literal.
func luaBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// buildListWindowsLua iterates hs.window.allWindows() and emits one record per
// visible window. The frontmost flag is derived from
// hs.window.focusedWindow() so callers can prioritise an app's active window.
func buildListWindowsLua() string {
	return `
local fw = hs.window.focusedWindow()
local fwid = fw and fw:id() or nil
local out = {}
for _, w in ipairs(hs.window.allWindows()) do
  local app = w:application()
  local f = w:frame()
  table.insert(out, {
    app = app and app:name() or "",
    pid = app and app:pid() or 0,
    title = w:title() or "",
    window_id = w:id(),
    frontmost = (w:id() == fwid),
    frame = {x = f.x, y = f.y, w = f.w, h = f.h},
  })
end
return {windows = out}
`
}

// buildFocusAppLua activates an app by bundleID first, falling back to name.
// Returns {ok, focused_app, focused_window_title}.
func buildFocusAppLua(app string) string {
	q := luaQuote(app)
	return `
local target = ` + q + `
local appObj = hs.application.get(target)
if not appObj then
  appObj = hs.application.find(target)
end
if not appObj then
  return {ok = false, err = "app not found: " .. target}
end
appObj:activate(true)
local w = appObj:focusedWindow()
return {
  ok = true,
  focused_app = appObj:name() or "",
  focused_window_title = w and w:title() or "",
}
`
}

// buildScreenshotLua captures the screen, a single window, or all windows of
// an app, returns base64-encoded PNG + metadata. Spill-to-disk for large PNGs
// is enforced on the Go side after this returns, because Hammerspoon doesn't
// expose a streaming hook — but the Lua does honour an explicit save_path.
type ScreenshotParams struct {
	Target       string // "screen" | "window" | "app", default "screen"
	App          string
	WindowID     int64
	SavePath     string
	ReturnBase64 bool // default true
}

func buildScreenshotLua(p ScreenshotParams) string {
	target := p.Target
	if target == "" {
		target = "screen"
	}
	// Drive the capture decision from a small Lua table so the rest of the
	// snippet is target-agnostic.
	args := "{target=" + luaQuote(target) +
		", app=" + luaQuote(p.App) +
		", window_id=" + int64Lua(p.WindowID) +
		", save_path=" + luaQuote(p.SavePath) +
		", return_base64=" + luaBool(p.ReturnBase64) + "}"

	return `
local args = ` + args + `
local img
if args.target == "window" then
  if args.window_id == 0 then
    return {ok=false, err="window_id required for target=window"}
  end
  local w = hs.window.get(args.window_id)
  if not w then
    return {ok=false, err="window not found: " .. tostring(args.window_id)}
  end
  img = w:snapshot()
elseif args.target == "app" then
  if args.app == "" then
    return {ok=false, err="app required for target=app"}
  end
  local appObj = hs.application.get(args.app) or hs.application.find(args.app)
  if not appObj then
    return {ok=false, err="app not found: " .. args.app}
  end
  local w = appObj:focusedWindow() or (appObj:allWindows()[1])
  if not w then
    return {ok=false, err="no windows for app: " .. args.app}
  end
  img = w:snapshot()
else
  img = hs.screen.mainScreen():snapshot()
end
if not img then
  return {ok=false, err="capture returned nil"}
end
local size = img:size()
local out = {width = size.w, height = size.h}
if args.save_path ~= "" then
  img:saveToFile(args.save_path)
  out.path = args.save_path
end
if args.return_base64 then
  -- encodeAsURLString returns 'data:image/png;base64,<payload>'.
  -- Strip the prefix and return the base64 PAYLOAD directly — re-encoding
  -- it would double-encode (the Go side base64-decodes once for spill).
  local b64 = img:encodeAsURLString("png"):match("base64,(.+)") or ""
  -- Validate the payload is a real PNG before handing it back. A blank /
  -- uninitialised capture (e.g. the launchd daemon lacking Screen Recording
  -- permission) returns a non-nil image whose encode is a raw zeroed backing
  -- buffer, NOT a compressed PNG — and the Go side would otherwise spill that
  -- multi-MB junk straight to ~/.mcplexer/hammerspoon-screenshots. Every PNG
  -- data URL's base64 payload begins with "iVBORw0KGgo" (= base64 of the
  -- 8-byte PNG signature 89 50 4E 47 0D 0A 1A 0A); anything else is not a PNG.
  if b64:sub(1, 11) ~= "iVBORw0KGgo" then
    return {ok=false, err="screen capture produced no PNG data (grant Screen Recording permission to the Hammerspoon host running the mcplexer daemon)"}
  end
  out.base64_png = b64
end
return out
`
}

// SendKeysParams models the send_keys tool input on the Go side. Either Text
// OR (Keys+Modifiers) should be set; both is a caller error and surfaced
// inside the Lua.
type SendKeysParams struct {
	Keys      []string
	Text      string
	Modifiers []string
	WindowID  int64
}

func buildSendKeysLua(p SendKeysParams) string {
	args := "{" +
		"keys=" + luaQuoteList(p.Keys) +
		", text=" + luaQuote(p.Text) +
		", modifiers=" + luaQuoteList(p.Modifiers) +
		", window_id=" + int64Lua(p.WindowID) +
		"}"
	return `
local args = ` + args + `
if args.window_id ~= 0 then
  local w = hs.window.get(args.window_id)
  if w then w:focus() end
end
if args.text ~= "" then
  hs.eventtap.keyStrokes(args.text)
elseif #args.keys > 0 then
  for _, k in ipairs(args.keys) do
    hs.eventtap.keyStroke(args.modifiers, k, 0)
  end
else
  return {ok=false, err="either text or keys is required"}
end
return {ok=true}
`
}

// NotifyParams models the notify tool input on the Go side. Title is
// required; the rest are optional Lua nil → no-op.
type NotifyParams struct {
	Title    string
	Subtitle string
	Body     string
	Sound    string
}

func buildNotifyLua(p NotifyParams) string {
	spec := "{title=" + luaQuote(p.Title) +
		", subTitle=" + luaQuote(p.Subtitle) +
		", informativeText=" + luaQuote(p.Body) +
		", soundName=" + luaQuote(p.Sound) + "}"
	return `
local n = hs.notify.new(` + spec + `)
n:send()
return {ok=true}
`
}

// buildExecLuaLua simply forwards the user's snippet verbatim. The bridge
// (HTTP handler or CLI wrapper) wraps it in pcall + envelope encoding, so the
// builder is a passthrough.
func buildExecLuaLua(userLua string) string {
	return userLua
}

// int64Lua renders a Go int64 as a Lua numeric literal. Zero stays "0".
func int64Lua(v int64) string {
	if v == 0 {
		return "0"
	}
	// strconv would be cleaner; staying stdlib-light via fmt would still
	// pull in fmt for one call. json.Marshal of an int gives bare digits.
	b, _ := json.Marshal(v)
	return string(b)
}
