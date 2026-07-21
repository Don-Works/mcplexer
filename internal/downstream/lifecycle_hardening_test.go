package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

const wedgedMCPSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	r := bufio.NewScanner(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	for r.Scan() {
		var req struct {
			ID json.RawMessage ` + "`json:\"id\"`" + `
			Method string ` + "`json:\"method\"`" + `
		}
		if json.Unmarshal(r.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		if req.Method == "initialize" {
			fmt.Fprintf(w, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{}}\n", req.ID)
			w.Flush()
			continue
		}
		// Stay alive while never producing the requested response.
		for {
			time.Sleep(time.Hour)
		}
	}
}
`

const crashOnceMCPSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	marker := os.Getenv("CRASH_MARKER")
	r := bufio.NewScanner(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	for r.Scan() {
		var req struct {
			ID json.RawMessage ` + "`json:\"id\"`" + `
			Method string ` + "`json:\"method\"`" + `
		}
		if json.Unmarshal(r.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		fmt.Fprintf(w, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"ok\":true}}\n", req.ID)
		w.Flush()
		if req.Method == "tools/call" {
			if _, err := os.Stat(marker); os.IsNotExist(err) {
				_ = os.WriteFile(marker, []byte("crashed"), 0600)
				os.Exit(17)
			}
		}
	}
}
`

func buildLifecycleStub(t *testing.T, source, name string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binPath := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", binPath, sourcePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, stderr.String())
	}
	return binPath
}

func TestWedgedResponseDeadlineTearsDownReaderAndProcess(t *testing.T) {
	bin := buildLifecycleStub(t, wedgedMCPSource, "wedged-mcp")
	inst := newInstance(
		InstanceKey{ServerID: "wedged"}, bin, nil,
		append(os.Environ(), "PATH="+os.Getenv("PATH")), 0, nil, "never",
	)
	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(inst.stop)

	inst.mu.Lock()
	loopDone := inst.done
	readerDone := inst.readerDone
	processDone := inst.processDone
	cmd := inst.cmd
	inst.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	started := time.Now()
	_, err := inst.Call(ctx, "tools/call", json.RawMessage(`{"name":"wedge"}`))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("deadline teardown took %s, want under 2s", elapsed)
	}

	waitForState(t, inst, StateStopped, 3*time.Second)
	assertClosed(t, loopDone, "process loop")
	assertClosed(t, readerDone, "stdout reader")
	assertClosed(t, processDone, "process monitor")
	if cmd.ProcessState == nil {
		t.Fatal("wedged child was not reaped")
	}

	nextCtx, nextCancel := context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	if _, err := inst.Call(nextCtx, "tools/call", json.RawMessage(`{}`)); err == nil {
		t.Fatal("stopped, poisoned stream was reused")
	}
}

func TestCrashRestartCleansPriorWrapperProfile(t *testing.T) {
	bin := buildLifecycleStub(t, crashOnceMCPSource, "crash-once-mcp")
	dir := t.TempDir()
	marker := filepath.Join(dir, "crash-marker")
	profile := filepath.Join(dir, "sandbox-profile")
	if err := os.WriteFile(profile, []byte("profile"), 0o600); err != nil {
		t.Fatalf("write fake profile: %v", err)
	}
	env := append(os.Environ(), "PATH="+os.Getenv("PATH"), "CRASH_MARKER="+marker)
	inst := newInstance(
		InstanceKey{ServerID: "crash-cleanup"}, bin, nil, env, 0, nil, "on-failure",
	)
	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(inst.stop)

	inst.mu.Lock()
	firstCmd := inst.cmd
	var cleanups atomic.Int32
	inst.wrapperCleanup = onceCleanup(func() {
		cleanups.Add(1)
		_ = os.Remove(profile)
	})
	inst.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, err := inst.Call(ctx, "tools/call", json.RawMessage(`{"name":"crash"}`))
	cancel()
	if err != nil {
		t.Fatalf("crashing call response: %v", err)
	}

	waitFor(t, func() bool { return cleanups.Load() == 1 }, 3*time.Second)
	waitFor(t, func() bool {
		inst.mu.Lock()
		defer inst.mu.Unlock()
		return inst.state == StateReady && inst.cmd != firstCmd
	}, 3*time.Second)
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prior sandbox profile still exists after restart: %v", err)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("prior generation cleanup count = %d, want 1", got)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	_, err = inst.Call(ctx, "tools/call", json.RawMessage(`{"name":"stable"}`))
	cancel()
	if err != nil {
		t.Fatalf("call after restart: %v", err)
	}
}

func TestTerminalCrashCleansWrapperExactlyOnce(t *testing.T) {
	bin := buildLifecycleStub(t, crashOnceMCPSource, "terminal-crash-mcp")
	marker := filepath.Join(t.TempDir(), "crash-marker")
	env := append(os.Environ(), "PATH="+os.Getenv("PATH"), "CRASH_MARKER="+marker)
	inst := newInstance(
		InstanceKey{ServerID: "terminal-cleanup"}, bin, nil, env, 0, nil, "never",
	)
	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(inst.stop)

	var cleanups atomic.Int32
	inst.mu.Lock()
	inst.wrapperCleanup = onceCleanup(func() { cleanups.Add(1) })
	inst.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, err := inst.Call(ctx, "tools/call", json.RawMessage(`{"name":"crash"}`))
	cancel()
	if err != nil {
		t.Fatalf("crashing call response: %v", err)
	}
	waitForState(t, inst, StateStopped, 3*time.Second)
	waitFor(t, func() bool { return cleanups.Load() == 1 }, time.Second)
	inst.stop()
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("terminal cleanup count after redundant stop = %d, want 1", got)
	}
}

func assertClosed(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	if ch == nil {
		t.Fatalf("%s channel is nil", name)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not exit", name)
	}
}
