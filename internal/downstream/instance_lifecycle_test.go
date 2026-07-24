package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubMCPSource is a tiny, self-contained stdio MCP server compiled into a
// throwaway binary by buildStubMCP. It implements just enough of the
// protocol for the lifecycle tests: respond to `initialize`, ignore the
// `notifications/initialized` notification, and echo back any other
// request's id with a trivial result. It loops reading newline-delimited
// JSON-RPC on stdin until EOF (which happens when the Instance closes
// stdin / kills the process on stop()).
//
// It is a real binary (not a shell -c invocation) so it passes
// ValidateCommand without needing MCPLEXER_UNSAFE_DOWNSTREAM_COMMANDS.
const stubMCPSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
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
		// Notifications (no id) get no reply.
		if len(req.ID) == 0 {
			continue
		}
		if req.Method == "initialize" {
			fmt.Fprintf(w, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{}}}\n", string(req.ID))
			w.Flush()
			continue
		}
		// Echo the request id with a trivial result. The exact result
		// shape doesn't matter for the lifecycle test — only that an
		// id-matched response comes back.
		fmt.Fprintf(w, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"ok\":true,\"method\":%q}}\n", string(req.ID), req.Method)
		w.Flush()
	}
}
`

// buildStubMCP compiles stubMCPSource into a binary in a temp dir and
// returns its absolute path. Skips the test if the go toolchain is not
// available on PATH (CI sandboxes without a compiler).
func buildStubMCP(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping real-process lifecycle test")
	}
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, []byte(stubMCPSource), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	binName := "stubmcp"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(dir, binName)
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build stub mcp: %v\n%s", err, stderr.String())
	}
	return binPath
}

// TestInstanceLifecycle drives a real stdio Instance through its full
// lifecycle and asserts the load-bearing invariants that previously had
// zero coverage: start handshake -> StateReady, a Call round-trips, the
// idle timer stops the instance, stop() is idempotent, and the per-spawn
// wrapperCleanup fires exactly once across the stop()/monitorProcess race.
func TestInstanceLifecycle(t *testing.T) {
	tests := []struct {
		name        string
		idleTimeout time.Duration
		// driver runs after the instance has started and made one
		// successful Call. It returns once the instance should be in (or
		// heading to) StateStopped.
		driver func(t *testing.T, inst *Instance)
	}{
		{
			name:        "explicit stop is idempotent and cleanup fires once",
			idleTimeout: 0, // no idle timer; we drive stop() directly
			driver: func(t *testing.T, inst *Instance) {
				// Call stop() concurrently from several goroutines plus
				// let monitorProcess observe the exit — wrapperCleanup
				// must still fire exactly once.
				var wg sync.WaitGroup
				for i := 0; i < 5; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						inst.stop()
					}()
				}
				wg.Wait()
				// Redundant final stop — still a no-op.
				inst.stop()
			},
		},
		{
			name:        "idle timeout stops the instance",
			idleTimeout: 150 * time.Millisecond,
			driver: func(t *testing.T, inst *Instance) {
				// Wait for the idle timer (armed after the Call's
				// response) to fire and stop the instance.
				waitForState(t, inst, StateStopped, 3*time.Second)
				// stop() after an idle-driven stop is still a no-op.
				inst.stop()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := buildStubMCP(t)
			inst := newInstance(
				InstanceKey{ServerID: "stub"},
				bin, nil,
				append(os.Environ(), "PATH="+os.Getenv("PATH")),
				tt.idleTimeout, nil, "never",
			)

			if err := inst.start(context.Background()); err != nil {
				t.Fatalf("start: %v", err)
			}

			// After start() the instance must be Ready.
			if got := inst.getState(); got != StateReady {
				t.Fatalf("post-start state = %s, want ready", got)
			}

			// Replace the (no-op) wrapperCleanup with a counting one so
			// we can assert it fires exactly once. start() set it to the
			// identity no-op (wrapper is nil); overwrite under the lock.
			var cleanups atomic.Int32
			inst.mu.Lock()
			inst.wrapperCleanup = func() { cleanups.Add(1) }
			inst.mu.Unlock()

			// A Call must round-trip against the real stub.
			callCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			res, err := inst.Call(callCtx, "tools/call", json.RawMessage(`{"name":"noop","arguments":{}}`))
			cancel()
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(res, &parsed); err != nil {
				t.Fatalf("unmarshal call result: %v (raw=%s)", err, string(res))
			}
			if parsed["ok"] != true {
				t.Errorf("call result = %v, want ok:true", parsed)
			}

			tt.driver(t, inst)

			// Terminal invariants: stopped + cleanup fired exactly once.
			if got := inst.getState(); got != StateStopped {
				t.Errorf("final state = %s, want stopped", got)
			}
			// Give monitorProcess a beat to observe the exit if it hasn't.
			waitFor(t, func() bool { return cleanups.Load() >= 1 }, 2*time.Second)
			if n := cleanups.Load(); n != 1 {
				t.Errorf("wrapperCleanup fired %d times, want exactly 1", n)
			}
		})
	}
}

// waitForState polls inst.getState() until it equals want or the deadline
// elapses.
func waitForState(t *testing.T, inst *Instance, want InstanceState, timeout time.Duration) {
	t.Helper()
	waitFor(t, func() bool { return inst.getState() == want }, timeout)
}

// waitFor polls cond until it returns true or timeout elapses, failing the
// test on timeout.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}
