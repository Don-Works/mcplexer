package models

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMiMoCLIBinaryEndpointRunsNativeCLI(t *testing.T) {
	t.Parallel()
	good := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"native mimo ok"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":7,"output":3}}}`,
	}, "\n")

	var gotBinary string
	var gotArgs []string
	var gotStdin string
	var gotWorkspace string
	a := newMiMoCLIAdapter("/custom/bin/mimo", "xiaomi/mimo-v2.5")
	a.runner = func(_ context.Context, binary string, args []string, stdin string, workspacePath string) ([]byte, []byte, error) {
		gotBinary = binary
		gotArgs = append([]string(nil), args...)
		gotStdin = stdin
		gotWorkspace = workspacePath
		return []byte(good), nil, nil
	}

	resp, err := a.Send(context.Background(), SendRequest{
		System:        "system text",
		Messages:      []Message{{Role: RoleUser, Content: "ping"}},
		WorkspacePath: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "native mimo ok" {
		t.Fatalf("Text=%q, want native mimo ok", resp.Text)
	}
	if gotBinary != "/custom/bin/mimo" {
		t.Fatalf("binary = %q, want /custom/bin/mimo", gotBinary)
	}
	wantArgs := []string{
		"run", "--pure", "--format", "json", "--dangerously-skip-permissions",
		"--dir", "/tmp/project",
		"--model", "xiaomi/mimo-v2.5",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if gotWorkspace != "/tmp/project" {
		t.Fatalf("workspace = %q, want /tmp/project", gotWorkspace)
	}
	if gotStdin == "" || !strings.Contains(gotStdin, "ping") || !strings.Contains(gotStdin, "system text") {
		t.Fatalf("stdin did not include rendered prompt: %q", gotStdin)
	}
}

func TestMiMoCLIHTTPSEndpointAttachesToServer(t *testing.T) {
	t.Parallel()
	good := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"text","part":{"type":"text","text":"server mimo ok"}}`,
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":5,"output":3}}}`,
	}, "\n")

	var gotArgs []string
	a := newMiMoCLIAdapter("http://127.0.0.1:4096", "xiaomi/mimo-v2.5-pro")
	a.runner = func(_ context.Context, _ string, args []string, _ string, _ string) ([]byte, []byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(good), nil, nil
	}
	resp, err := a.Send(context.Background(), SendRequest{WorkspacePath: "/tmp/project"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "server mimo ok" {
		t.Fatalf("Text=%q, want server mimo ok", resp.Text)
	}
	wantArgs := []string{
		"run", "--pure", "--format", "json", "--dangerously-skip-permissions",
		"--attach", "http://127.0.0.1:4096",
		"--dir", "/tmp/project",
		"--model", "xiaomi/mimo-v2.5-pro",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

// TestMiMoCLIArgsAlwaysSkipPermissions is a regression guard: every mimo
// run argv MUST carry --dangerously-skip-permissions. Without it, headless
// --pure mode auto-rejects out-of-scope (external_directory) access and the
// run wedges until the wall-clock cap kills it — a 32-min, zero-output
// failure (see buildMimoCLIArgs). Asserted across the flag-shape variants
// (with/without attach + dir + model) so no config path can silently drop it.
func TestMiMoCLIArgsAlwaysSkipPermissions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                              string
		modelID, attachURL, workspacePath string
	}{
		{"full", "xiaomi/mimo-v2.5", "", "/tmp/project"},
		{"attach", "xiaomi/mimo-v2.5-pro", "http://127.0.0.1:4096", "/tmp/project"},
		{"bare", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := buildMimoCLIArgs(tc.modelID, tc.attachURL, tc.workspacePath)
			if !slices.Contains(args, "--dangerously-skip-permissions") {
				t.Fatalf("buildMimoCLIArgs(%q,%q,%q) = %#v, missing --dangerously-skip-permissions",
					tc.modelID, tc.attachURL, tc.workspacePath, args)
			}
		})
	}
}

func TestMiMoCLIErrorEventFailsRun(t *testing.T) {
	t.Parallel()
	raw := `{"type":"error","timestamp":1781297011113,"sessionID":"ses_x","error":{"name":"APIError","data":{"message":"Invalid API Key: Please provide valid API Key","statusCode":401,"metadata":{"url":"https://api.xiaomimimo.com/v1/chat/completions"}}}}`
	a := newMiMoCLIAdapter("/custom/bin/mimo", "xiaomi/mimo-v2.5")
	a.runner = func(_ context.Context, _ string, _ []string, _ string, _ string) ([]byte, []byte, error) {
		return []byte(raw), nil, nil
	}
	_, err := a.Send(context.Background(), SendRequest{})
	if err == nil {
		t.Fatal("expected error event to fail the run")
	}
	msg := err.Error()
	for _, want := range []string{"mimo_cli", "Invalid API Key", "401"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("err = %q, want substring %q", msg, want)
		}
	}
}
