package hammerspoon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Per-tool callX methods. Each one:
//   1. Unmarshals args into a typed struct.
//   2. Builds the Lua via a build* template.
//   3. Calls m.Bridge().Exec.
//   4. Maps the envelope into a CallToolResult (textResult / errorResult).
//
// Failures at any step surface as errorResult — the gateway gets a clean
// MCP-shaped error envelope rather than a Go error, so the client renders
// it inline.

// callTimeout is the per-call ceiling. Long enough for a Safari focus on a
// busy box, short enough that an agent stuck on a missing app gets feedback.
const callTimeout = 5 * time.Second

// screenshotInlineCap is the max PNG size we'll base64-inline into the MCP
// response. Larger captures spill to disk and return path only.
const screenshotInlineCap = 2 * 1024 * 1024 // 2 MB

func (s *MCPServer) callListWindows(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	env, err := s.m.Bridge().Exec(ctx, buildListWindowsLua(), callTimeout)
	return renderEnvelope(env, err, "list_windows")
}

func (s *MCPServer) callFocusApp(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		App string `json:"app"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResult("focus_app: " + err.Error()), nil
	}
	if req.App == "" {
		return errorResult("focus_app: app is required (bundle id or name)"), nil
	}
	env, err := s.m.Bridge().Exec(ctx, buildFocusAppLua(req.App), callTimeout)
	return renderEnvelope(env, err, "focus_app")
}

func (s *MCPServer) callScreenshot(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		Target       string `json:"target"`
		App          string `json:"app"`
		WindowID     int64  `json:"window_id"`
		SavePath     string `json:"save_path"`
		ReturnBase64 *bool  `json:"return_base64"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return errorResult("screenshot: " + err.Error()), nil
		}
	}
	wantBase64 := true
	if req.ReturnBase64 != nil {
		wantBase64 = *req.ReturnBase64
	}

	// Default spill path when no explicit save_path and the caller didn't
	// opt out of base64 — we don't know the size yet, so let Lua return
	// base64 unconditionally and decide here. The save_path field stays
	// empty so Lua only writes when the caller asked.
	p := ScreenshotParams{
		Target:       req.Target,
		App:          req.App,
		WindowID:     req.WindowID,
		SavePath:     req.SavePath,
		ReturnBase64: wantBase64,
	}
	env, err := s.m.Bridge().Exec(ctx, buildScreenshotLua(p), 15*time.Second)
	if err != nil {
		return errorResult("screenshot transport: " + err.Error()), nil
	}
	if !env.Ok {
		return errorResult("screenshot: " + envOrUnknown(env)), nil
	}

	// Enforce the inline-base64 cap on the Go side. If the PNG is larger
	// than the cap and the caller didn't supply a save_path, we spill to
	// the per-user screenshots dir and drop the base64 payload.
	var payload struct {
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Path      string `json:"path,omitempty"`
		Base64PNG string `json:"base64_png,omitempty"`
	}
	if err := json.Unmarshal(env.Result, &payload); err != nil {
		return errorResult("screenshot: bad result payload: " + err.Error()), nil
	}

	if payload.Base64PNG != "" {
		// Approx decoded size = base64-len * 3/4.
		decodedSize := len(payload.Base64PNG) * 3 / 4
		if decodedSize > screenshotInlineCap && payload.Path == "" {
			spillPath, spillErr := spillScreenshot(payload.Base64PNG)
			if spillErr != nil {
				return errorResult("screenshot spill failed: " + spillErr.Error()), nil
			}
			payload.Path = spillPath
			payload.Base64PNG = ""
		}
	}

	out, _ := json.Marshal(payload)
	return textResult(string(out)), nil
}

func (s *MCPServer) callSendKeys(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		Keys      []string `json:"keys"`
		Text      string   `json:"text"`
		Modifiers []string `json:"modifiers"`
		WindowID  int64    `json:"window_id"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResult("send_keys: " + err.Error()), nil
	}
	if req.Text == "" && len(req.Keys) == 0 {
		return errorResult("send_keys: either text or keys is required"), nil
	}
	if req.Text != "" && len(req.Keys) > 0 {
		return errorResult("send_keys: text and keys are mutually exclusive"), nil
	}
	env, err := s.m.Bridge().Exec(ctx, buildSendKeysLua(SendKeysParams{
		Keys: req.Keys, Text: req.Text, Modifiers: req.Modifiers, WindowID: req.WindowID,
	}), callTimeout)
	return renderEnvelope(env, err, "send_keys")
}

func (s *MCPServer) callNotify(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		Body     string `json:"body"`
		Sound    string `json:"sound"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResult("notify: " + err.Error()), nil
	}
	if req.Title == "" {
		return errorResult("notify: title is required"), nil
	}
	env, err := s.m.Bridge().Exec(ctx, buildNotifyLua(NotifyParams{
		Title: req.Title, Subtitle: req.Subtitle, Body: req.Body, Sound: req.Sound,
	}), callTimeout)
	return renderEnvelope(env, err, "notify")
}

func (s *MCPServer) callExecLua(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		Lua       string `json:"lua"`
		TimeoutMS int64  `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResult("exec_lua: " + err.Error()), nil
	}
	if req.Lua == "" {
		return errorResult("exec_lua: lua snippet is required"), nil
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = callTimeout
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	env, err := s.m.Bridge().Exec(ctx, buildExecLuaLua(req.Lua), timeout)
	return renderEnvelope(env, err, "exec_lua")
}

// renderEnvelope folds a bridge envelope + transport error into one of the
// two MCP CallToolResult shapes. Transport errors and envelope.Ok=false both
// become isError=true; ok=true with a JSON result becomes a textResult whose
// body is the result JSON pretty-printed.
func renderEnvelope(env Envelope, transportErr error, toolName string) (json.RawMessage, error) {
	if transportErr != nil {
		return errorResult(toolName + " transport: " + transportErr.Error()), nil
	}
	if !env.Ok {
		return errorResult(toolName + ": " + envOrUnknown(env)), nil
	}
	if len(env.Result) == 0 {
		return textResult("ok"), nil
	}
	// Pass JSON through verbatim — clients are MCP-aware and will parse it.
	return textResult(string(env.Result)), nil
}

// envOrUnknown extracts a human-readable error message from an envelope,
// falling back to a generic one when Err is empty.
func envOrUnknown(env Envelope) string {
	if env.Err != "" {
		return env.Err
	}
	return "unknown bridge error"
}

// pngMagic is the 8-byte PNG file signature. spillScreenshot validates against
// it before writing so a blank/garbage capture never lands on disk as a
// multi-MB junk .png — defence-in-depth behind the Lua-side PNG check in
// buildScreenshotLua. (Historically the launchd daemon, lacking Screen
// Recording permission, captured uninitialised buffers that encoded to raw
// zeroed bytes; ~87 such 2.36 MB all-zero files accumulated before this guard.)
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// decodeScreenshotPNG base64-decodes a screenshot payload and verifies it is a
// real PNG. Returns a descriptive error (not a path) when the payload is not a
// PNG, so callers surface a clean MCP error instead of persisting junk.
func decodeScreenshotPNG(b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if !bytes.HasPrefix(raw, pngMagic) {
		return nil, fmt.Errorf("payload is not a PNG (%d bytes) — capture was likely blank", len(raw))
	}
	return raw, nil
}

// spillScreenshot writes a base64-encoded PNG to the per-user spill dir and
// returns the absolute path. The dir is created on demand with 0700. The
// payload is validated as a genuine PNG before any file is created.
func spillScreenshot(b64 string) (string, error) {
	raw, err := decodeScreenshotPNG(b64)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".mcplexer", "hammerspoon-screenshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("20060102T150405.000")
	path := filepath.Join(dir, fmt.Sprintf("%s.png", ts))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
