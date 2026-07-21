package hammerspoon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBridge records the last Lua snippet + timeout it received and returns a
// configurable envelope (or transport error) on Exec. Tests use it to assert
// dispatch produces the expected Lua and folds the bridge response into the
// right MCP shape without spinning up a real Hammerspoon.
type fakeBridge struct {
	mu       sync.Mutex
	lastLua  string
	lastTO   time.Duration
	callN    int
	envelope Envelope
	err      error
}

func (f *fakeBridge) Exec(_ context.Context, lua string, timeout time.Duration) (Envelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastLua = lua
	f.lastTO = timeout
	f.callN++
	return f.envelope, f.err
}

// newServerWithFake constructs an MCPServer wired to a recording fake.
func newServerWithFake(env Envelope, transportErr error, allowExec bool) (*MCPServer, *fakeBridge) {
	fb := &fakeBridge{envelope: env, err: transportErr}
	return NewMCPServer(NewManager(fb, allowExec)), fb
}

// parseResult unmarshals an MCP CallToolResult-shaped JSON blob.
func parseResult(t *testing.T, raw json.RawMessage) (text string, isError bool) {
	t.Helper()
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("parse result: %v: %s", err, raw)
	}
	if len(r.Content) == 0 {
		return "", r.IsError
	}
	return r.Content[0].Text, r.IsError
}

func TestListTools_AlwaysOn(t *testing.T) {
	s := NewMCPServer(NewManager(nullBridge{}, false))
	raw, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var got struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]bool{
		"list_windows": false, "focus_app": false, "screenshot": false,
		"send_keys": false, "notify": false,
	}
	for _, tt := range got.Tools {
		if _, ok := want[tt.Name]; ok {
			want[tt.Name] = true
		}
		if tt.Name == "exec_lua" {
			t.Errorf("exec_lua leaked into ListTools with gate off")
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestCall_NilManager_CleanError(t *testing.T) {
	s := NewMCPServer(nil)
	out, err := s.Call(context.Background(), "list_windows", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	txt, isErr := parseResult(t, out)
	if !isErr {
		t.Fatalf("want isError=true, got false (text=%q)", txt)
	}
	if !strings.Contains(txt, "downstream not enabled") {
		t.Errorf("want 'downstream not enabled' message, got %q", txt)
	}
}

func TestCall_NullBridge_PassesThroughNotEnabled(t *testing.T) {
	s := NewMCPServer(NewManager(nullBridge{}, false))
	out, _ := s.Call(context.Background(), "list_windows", nil)
	txt, isErr := parseResult(t, out)
	if !isErr {
		t.Fatalf("want isError=true, got false (text=%q)", txt)
	}
	if !strings.Contains(txt, "downstream not enabled") {
		t.Errorf("expected nullBridge passthrough, got %q", txt)
	}
}

func TestCall_UnknownTool(t *testing.T) {
	s, _ := newServerWithFake(Envelope{Ok: true}, nil, false)
	out, _ := s.Call(context.Background(), "bogus", nil)
	txt, isErr := parseResult(t, out)
	if !isErr {
		t.Fatalf("want isError on unknown tool, got text=%q", txt)
	}
	if !strings.Contains(txt, "unknown hammerspoon tool") {
		t.Errorf("unexpected err message: %q", txt)
	}
}

func TestCall_ListWindows_PassesThroughResult(t *testing.T) {
	s, fb := newServerWithFake(Envelope{
		Ok:     true,
		Result: json.RawMessage(`{"windows":[{"app":"Safari"}]}`),
	}, nil, false)

	out, err := s.Call(context.Background(), "list_windows", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	txt, isErr := parseResult(t, out)
	if isErr {
		t.Fatalf("unexpected isError=true: %s", txt)
	}
	if txt != `{"windows":[{"app":"Safari"}]}` {
		t.Errorf("result body: got %s", txt)
	}
	if !strings.Contains(fb.lastLua, "hs.window.allWindows()") {
		t.Errorf("dispatch did not call list_windows Lua: %s", fb.lastLua)
	}
}

func TestCall_FocusApp_Validates(t *testing.T) {
	s, _ := newServerWithFake(Envelope{Ok: true}, nil, false)
	out, _ := s.Call(context.Background(), "focus_app", json.RawMessage(`{}`))
	txt, isErr := parseResult(t, out)
	if !isErr || !strings.Contains(txt, "app is required") {
		t.Errorf("want validation error, got isErr=%v text=%q", isErr, txt)
	}
}

func TestCall_FocusApp_BuildsLua(t *testing.T) {
	s, fb := newServerWithFake(Envelope{
		Ok:     true,
		Result: json.RawMessage(`{"ok":true,"focused_app":"Safari"}`),
	}, nil, false)
	_, err := s.Call(context.Background(), "focus_app", json.RawMessage(`{"app":"com.apple.Safari"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(fb.lastLua, `"com.apple.Safari"`) {
		t.Errorf("bundle id missing from lua: %s", fb.lastLua)
	}
}

func TestCall_TransportErr_RendersAsIsError(t *testing.T) {
	s, _ := newServerWithFake(Envelope{}, errors.New("network down"), false)
	out, _ := s.Call(context.Background(), "list_windows", nil)
	txt, isErr := parseResult(t, out)
	if !isErr || !strings.Contains(txt, "network down") {
		t.Errorf("transport err not surfaced: isErr=%v text=%q", isErr, txt)
	}
}

func TestCall_EnvelopeNotOk_RendersAsIsError(t *testing.T) {
	s, _ := newServerWithFake(Envelope{Ok: false, Err: "app not found"}, nil, false)
	out, _ := s.Call(context.Background(), "focus_app", json.RawMessage(`{"app":"Nope"}`))
	txt, isErr := parseResult(t, out)
	if !isErr || !strings.Contains(txt, "app not found") {
		t.Errorf("envelope err not surfaced: isErr=%v text=%q", isErr, txt)
	}
}

func TestCall_SendKeys_RequiresKeysOrText(t *testing.T) {
	s, _ := newServerWithFake(Envelope{Ok: true}, nil, false)
	out, _ := s.Call(context.Background(), "send_keys", json.RawMessage(`{}`))
	txt, isErr := parseResult(t, out)
	if !isErr || !strings.Contains(txt, "either text or keys") {
		t.Errorf("want either-or validation, got %q", txt)
	}
}

func TestCall_SendKeys_MutuallyExclusive(t *testing.T) {
	s, _ := newServerWithFake(Envelope{Ok: true}, nil, false)
	out, _ := s.Call(context.Background(), "send_keys",
		json.RawMessage(`{"text":"x","keys":["c"]}`))
	txt, isErr := parseResult(t, out)
	if !isErr || !strings.Contains(txt, "mutually exclusive") {
		t.Errorf("want mutually-exclusive validation, got %q", txt)
	}
}

func TestCall_SendKeys_BuildsLua(t *testing.T) {
	s, fb := newServerWithFake(Envelope{Ok: true}, nil, false)
	_, _ = s.Call(context.Background(), "send_keys",
		json.RawMessage(`{"keys":["c"],"modifiers":["cmd","shift"]}`))
	if !strings.Contains(fb.lastLua, `keys={"c"}`) {
		t.Errorf("keys missing in lua: %s", fb.lastLua)
	}
	if !strings.Contains(fb.lastLua, `modifiers={"cmd","shift"}`) {
		t.Errorf("modifiers missing in lua: %s", fb.lastLua)
	}
}

func TestCall_Notify_RequiresTitle(t *testing.T) {
	s, _ := newServerWithFake(Envelope{Ok: true}, nil, false)
	out, _ := s.Call(context.Background(), "notify", json.RawMessage(`{"body":"hi"}`))
	txt, isErr := parseResult(t, out)
	if !isErr || !strings.Contains(txt, "title is required") {
		t.Errorf("want title-required, got %q", txt)
	}
}

func TestCall_Notify_BuildsLua(t *testing.T) {
	s, fb := newServerWithFake(Envelope{Ok: true}, nil, false)
	_, _ = s.Call(context.Background(), "notify", json.RawMessage(`{"title":"Hi"}`))
	if !strings.Contains(fb.lastLua, `title="Hi"`) {
		t.Errorf("title missing in lua: %s", fb.lastLua)
	}
}

func TestCall_Screenshot_SmallBase64InlinePassthrough(t *testing.T) {
	// Small base64 (<2MB) — dispatch should pass it through unchanged.
	small := strings.Repeat("A", 100)
	s, _ := newServerWithFake(Envelope{
		Ok:     true,
		Result: json.RawMessage(`{"width":100,"height":100,"base64_png":"` + small + `"}`),
	}, nil, false)
	out, _ := s.Call(context.Background(), "screenshot", json.RawMessage(`{}`))
	txt, isErr := parseResult(t, out)
	if isErr {
		t.Fatalf("unexpected isError: %s", txt)
	}
	if !strings.Contains(txt, `"base64_png":"`+small+`"`) {
		t.Errorf("small base64 not passed through: %s", txt)
	}
}

func TestCall_Screenshot_LargeSpillsToDisk(t *testing.T) {
	// Base64-len * 3/4 > 2 MB → spill. ~3 MB of valid PNG bytes: the magic
	// signature followed by filler. Must be a genuine PNG so the decode-time
	// validation in spillScreenshot accepts it (a non-PNG blob is rejected by
	// design — see TestDecodeScreenshotPNG_RejectsNonPNG).
	raw := append(append([]byte{}, pngMagic...), make([]byte, 3*1024*1024)...)
	big := base64.StdEncoding.EncodeToString(raw)
	s, _ := newServerWithFake(Envelope{
		Ok:     true,
		Result: json.RawMessage(`{"width":3840,"height":2160,"base64_png":"` + big + `"}`),
	}, nil, false)
	out, _ := s.Call(context.Background(), "screenshot", json.RawMessage(`{}`))
	txt, isErr := parseResult(t, out)
	if isErr {
		t.Fatalf("unexpected isError: %s", txt)
	}
	// The result should now carry a path and NOT carry the inline base64.
	if strings.Contains(txt, "base64_png") {
		t.Errorf("expected base64 dropped after spill: %s", txt)
	}
	if !strings.Contains(txt, "hammerspoon-screenshots") {
		t.Errorf("expected spill path in result: %s", txt)
	}
}

func TestCall_ExecLua_BlockedWhenGateOff(t *testing.T) {
	s, _ := newServerWithFake(Envelope{Ok: true}, nil, false)
	out, _ := s.Call(context.Background(), "exec_lua", json.RawMessage(`{"lua":"return 1"}`))
	txt, isErr := parseResult(t, out)
	if !isErr || !strings.Contains(txt, "HAMMERSPOON_ALLOW_EXEC_LUA") {
		t.Errorf("expected gate-off message, got isErr=%v text=%q", isErr, txt)
	}
}

func TestCall_ExecLua_PassesThroughWhenGateOn(t *testing.T) {
	s, fb := newServerWithFake(Envelope{
		Ok:     true,
		Result: json.RawMessage(`"hello"`),
	}, nil, true)
	out, _ := s.Call(context.Background(), "exec_lua",
		json.RawMessage(`{"lua":"return 'hello'"}`))
	txt, isErr := parseResult(t, out)
	if isErr {
		t.Fatalf("unexpected isError: %s", txt)
	}
	if fb.lastLua != `return 'hello'` {
		t.Errorf("exec_lua should be verbatim, got %q", fb.lastLua)
	}
}
