package downstream

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// stubExitMCP is a stdio MCP server that responds to requests and exits
// with the code specified in the STUB_EXIT_CODE env var (default 0) on the
// first tools/call it receives. Initialize and notifications pass through
// normally so the instance startup handshake succeeds.
const stubExitMCP = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	code := 0
	if s := os.Getenv("STUB_EXIT_CODE"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			code = v
		}
	}
	r := bufio.NewScanner(os.Stdin)
	r.Buffer(make([]byte, 1024*1024), 1024*1024)
	w := bufio.NewWriter(os.Stdout)
	for r.Scan() {
		var req struct {
			ID     json.RawMessage ` + "`json:\"id\"`" + `
			Method string          ` + "`json:\"method\"`" + `
		}
		if err := json.Unmarshal(r.Bytes(), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		if req.Method == "initialize" {
			fmt.Fprintf(w, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{}}}\n",
				string(req.ID))
			w.Flush()
			continue
		}
		fmt.Fprintf(w, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"ok\":true,\"method\":%q}}\n",
			string(req.ID), req.Method)
		w.Flush()
		// Exit only on tools/call so initialize succeeds.
		if req.Method == "tools/call" {
			time.Sleep(50 * time.Millisecond)
			os.Exit(code)
		}
	}
}
`

func buildExitStubMCP(t *testing.T, exitCode int) (binPath string, binDir string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping restart-policy test")
	}
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, []byte(stubExitMCP), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	binName := "stubexit"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath = filepath.Join(dir, binName)
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stub exit mcp: %v\n%s", err, string(out))
	}
	return binPath, dir
}

// TestInstance_RestartPolicy exercises all three restart_policy values
// with both zero and non-zero exit codes.
func TestInstance_RestartPolicy(t *testing.T) {
	tests := []struct {
		name        string
		policy      string
		exitCode    int
		wantRestart bool
	}{
		{name: "never/exit0", policy: "never", exitCode: 0, wantRestart: false},
		{name: "never/exit1", policy: "never", exitCode: 1, wantRestart: false},
		{name: "on-failure/exit0", policy: "on-failure", exitCode: 0, wantRestart: false},
		{name: "on-failure/exit1", policy: "on-failure", exitCode: 1, wantRestart: true},
		{name: "always/exit0", policy: "always", exitCode: 0, wantRestart: true},
		{name: "always/exit1", policy: "always", exitCode: 1, wantRestart: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, tmpDir := buildExitStubMCP(t, tt.exitCode)
			env := append(os.Environ(),
				"PATH="+os.Getenv("PATH"),
				"STUB_EXIT_CODE="+strconv.Itoa(tt.exitCode),
			)

			inst := newInstance(
				InstanceKey{ServerID: "restart-test-" + tt.name},
				bin, nil, env, 0, nil, tt.policy,
			)

			if err := inst.start(context.Background()); err != nil {
				t.Fatalf("start: %v", err)
			}
			if got := inst.getState(); got != StateReady {
				t.Fatalf("post-start state = %s, want ready", got)
			}

			// Make one call. The stub responds then exits with
			// tt.exitCode after a short sleep.
			callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := inst.Call(callCtx, "tools/call", json.RawMessage(`{"name":"noop","arguments":{}}`))
			cancel()
			if err != nil {
				t.Fatalf("Call: %v", err)
			}

			if tt.wantRestart {
				waitForState(t, inst, StateReady, 5*time.Second)
				// Suppress further restarts so the inevitable second
				// exit doesn't race with temp-dir cleanup.
				inst.mu.Lock()
				inst.restartPolicy = "never"
				inst.mu.Unlock()

				// The restarted instance must also accept a call.
				callCtx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
				_, err2 := inst.Call(callCtx2, "tools/call", json.RawMessage(`{"name":"noop2","arguments":{}}`))
				cancel2()
				if err2 != nil {
					t.Fatalf("Call after restart: %v", err2)
				}
			} else {
				waitForState(t, inst, StateStopped, 5*time.Second)
			}

			_ = tmpDir
		})
	}
}

// TestInstance_RestartPolicy_Default verifies that an empty restart_policy
// (zero-value) treats the exit as a no-restart, matching the "never"
// default for test-constructed instances.
func TestInstance_RestartPolicy_Default(t *testing.T) {
	bin, tmpDir := buildExitStubMCP(t, 0)
	env := append(os.Environ(),
		"PATH="+os.Getenv("PATH"),
		"STUB_EXIT_CODE=0",
	)

	// Empty policy string — should default to "never"
	inst := newInstance(
		InstanceKey{ServerID: "restart-default"},
		bin, nil, env, 0, nil, "",
	)

	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := inst.getState(); got != StateReady {
		t.Fatalf("post-start state = %s, want ready", got)
	}

	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := inst.Call(callCtx, "tools/call", json.RawMessage(`{"name":"noop","arguments":{}}`))
	cancel()
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	// Empty policy → no restart
	waitForState(t, inst, StateStopped, 5*time.Second)
	_ = tmpDir
}

// TestExitCodeFrom exercises the exitCodeFrom helper.
func TestExitCodeFrom(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T) *exec.Cmd
		want    int
	}{
		{
			name: "clean exit 0",
			prepare: func(t *testing.T) *exec.Cmd {
				return exec.Command("/bin/sh", "-c", "exit 0")
			},
			want: 0,
		},
		{
			name: "exit 42",
			prepare: func(t *testing.T) *exec.Cmd {
				return exec.Command("/bin/sh", "-c", "exit 42")
			},
			want: 42,
		},
		{
			name: "exit 1",
			prepare: func(t *testing.T) *exec.Cmd {
				return exec.Command("/bin/sh", "-c", "exit 1")
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.prepare(t)
			err := cmd.Run()
			got := exitCodeFrom(err)
			if got != tt.want {
				t.Errorf("exitCodeFrom(%v) = %d, want %d", err, got, tt.want)
			}
		})
	}
}

// TestShouldRestart exercises the shouldRestart helper directly.
func TestShouldRestart(t *testing.T) {
	tests := []struct {
		policy   string
		exitCode int
		want     bool
	}{
		{policy: "never", exitCode: 0, want: false},
		{policy: "never", exitCode: 1, want: false},
		{policy: "never", exitCode: -1, want: false},
		{policy: "on-failure", exitCode: 0, want: false},
		{policy: "on-failure", exitCode: 1, want: true},
		{policy: "on-failure", exitCode: 2, want: true},
		{policy: "on-failure", exitCode: -1, want: true},
		{policy: "always", exitCode: 0, want: true},
		{policy: "always", exitCode: 1, want: true},
		{policy: "always", exitCode: -1, want: true},
		{policy: "", exitCode: 0, want: false},
		{policy: "", exitCode: 1, want: false},
		{policy: "unknown", exitCode: 0, want: false},
		{policy: "unknown", exitCode: 1, want: false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/exit%d", tt.policy, tt.exitCode), func(t *testing.T) {
			inst := &Instance{restartPolicy: tt.policy}
			got := inst.shouldRestart(tt.exitCode)
			if got != tt.want {
				t.Errorf("shouldRestart(%d) with policy %q = %v, want %v",
					tt.exitCode, tt.policy, got, tt.want)
			}
		})
	}
}
