package downstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubCrashLoopMCP is a stdio MCP server that crashes N times before
// becoming stable. It reads STUB_CRASH_COUNT (default 1) for how many
// tools/call requests to crash on before serving normally.
const stubCrashLoopMCP = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

func main() {
	crashCount := 1
	if s := os.Getenv("STUB_CRASH_COUNT"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			crashCount = v
		}
	}
	calls := 0
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
		if req.Method == "tools/call" {
			calls++
			if calls <= crashCount {
				os.Exit(1)
			}
		}
	}
}
`

func buildCrashLoopStub(t *testing.T, crashCount int) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(stubCrashLoopMCP), 0o600); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	bin := filepath.Join(dir, "crashloop")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build crash loop stub: %v\n%s", err, string(out))
	}
	return bin
}

func TestInstance_RestartBackoff_FirstRestartImmediate(t *testing.T) {
	prevMin := MinRestartBackoff
	prevMax := MaxRestartBackoff
	MinRestartBackoff = 500 * time.Millisecond
	MaxRestartBackoff = 2 * time.Second
	t.Cleanup(func() {
		MinRestartBackoff = prevMin
		MaxRestartBackoff = prevMax
	})

	bin := buildCrashLoopStub(t, 1)
	env := append(os.Environ(),
		"PATH="+os.Getenv("PATH"),
		"STUB_CRASH_COUNT=1",
	)

	inst := newInstance(
		InstanceKey{ServerID: "backoff-first"},
		bin, nil, env, 0, nil, "on-failure",
	)

	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	start := time.Now()
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := inst.Call(callCtx, "tools/call", json.RawMessage(`{"name":"t","arguments":{}}`))
	cancel()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Wait for restart (first crash restart should be immediate, < 200ms)
	waitForState(t, inst, StateReady, 3*time.Second)
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("first restart took %s, expected immediate (<200ms)", elapsed)
	}

	// Verify the restarted instance works
	callCtx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	res, err := inst.Call(callCtx2, "tools/call", json.RawMessage(`{"name":"t2","arguments":{}}`))
	cancel2()
	if err != nil {
		t.Fatalf("call after first restart: %v", err)
	}
	if res == nil {
		t.Fatal("nil result after restart")
	}

	inst.stop()
}

func TestInstance_RestartBackoff_SecondRestartBacksOff(t *testing.T) {
	prevMin := MinRestartBackoff
	prevMax := MaxRestartBackoff
	MinRestartBackoff = 100 * time.Millisecond
	MaxRestartBackoff = 5 * time.Second
	t.Cleanup(func() {
		MinRestartBackoff = prevMin
		MaxRestartBackoff = prevMax
	})

	// Crash twice, then become stable
	bin := buildCrashLoopStub(t, 2)
	env := append(os.Environ(),
		"PATH="+os.Getenv("PATH"),
		"STUB_CRASH_COUNT=2",
	)

	inst := newInstance(
		InstanceKey{ServerID: "backoff-second"},
		bin, nil, env, 0, nil, "always",
	)

	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// First call: process crashes (crash #1)
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := inst.Call(callCtx, "tools/call", json.RawMessage(`{"name":"t","arguments":{}}`))
	cancel()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Wait for first restart (immediate)
	waitForState(t, inst, StateReady, 3*time.Second)

	// Second call on restarted process: crashes again (crash #2)
	secondStart := time.Now()
	callCtx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = inst.Call(callCtx2, "tools/call", json.RawMessage(`{"name":"t2","arguments":{}}`))
	cancel2()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Second restart should back off (MinRestartBackoff = 100ms)
	waitForState(t, inst, StateReady, 5*time.Second)
	elapsed := time.Since(secondStart)
	if elapsed < MinRestartBackoff-20*time.Millisecond {
		t.Fatalf("second restart took %s, expected backoff >= %s", elapsed, MinRestartBackoff)
	}

	// Third call should succeed (crash count exhausted)
	callCtx3, cancel3 := context.WithTimeout(context.Background(), 3*time.Second)
	res, err := inst.Call(callCtx3, "tools/call", json.RawMessage(`{"name":"t3","arguments":{}}`))
	cancel3()
	if err != nil {
		t.Fatalf("third call after backoff restart: %v", err)
	}
	if res == nil {
		t.Fatal("nil result on third call")
	}

	inst.stop()
}

func TestInstance_RestartBackoff_ResetsOnStop(t *testing.T) {
	prevMin := MinRestartBackoff
	prevMax := MaxRestartBackoff
	MinRestartBackoff = 200 * time.Millisecond
	MaxRestartBackoff = 2 * time.Second
	t.Cleanup(func() {
		MinRestartBackoff = prevMin
		MaxRestartBackoff = prevMax
	})

	bin := buildCrashLoopStub(t, 1)
	env := append(os.Environ(),
		"PATH="+os.Getenv("PATH"),
		"STUB_CRASH_COUNT=1",
	)

	inst := newInstance(
		InstanceKey{ServerID: "backoff-reset"},
		bin, nil, env, 0, nil, "on-failure",
	)

	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = inst.Call(callCtx, "tools/call", json.RawMessage(`{"name":"t","arguments":{}}`))
	cancel()
	waitForState(t, inst, StateReady, 3*time.Second)

	callCtx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	_, err := inst.Call(callCtx2, "tools/call", json.RawMessage(`{"name":"t2","arguments":{}}`))
	cancel2()
	if err != nil {
		t.Fatalf("call after restart: %v", err)
	}

	inst.mu.Lock()
	attempt := inst.restartAttempt
	inst.mu.Unlock()
	if attempt == 0 {
		t.Fatalf("restartAttempt = 0 after crash loop, want > 0 (should not reset on successful call)")
	}

	inst.stop()

	inst.mu.Lock()
	attempt = inst.restartAttempt
	inst.mu.Unlock()
	if attempt != 0 {
		t.Fatalf("restartAttempt = %d after stop(), want 0", attempt)
	}
}

func TestInstance_CallFailsDuringRestart(t *testing.T) {
	prevMin := MinRestartBackoff
	MinRestartBackoff = 2 * time.Second
	t.Cleanup(func() { MinRestartBackoff = prevMin })

	bin := buildCrashLoopStub(t, 1)
	env := append(os.Environ(),
		"PATH="+os.Getenv("PATH"),
		"STUB_CRASH_COUNT=1",
	)

	inst := newInstance(
		InstanceKey{ServerID: "call-during-restart"},
		bin, nil, env, 0, nil, "always",
	)

	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// First crash → restart (attempt 1, backoff=0)
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = inst.Call(callCtx, "tools/call", json.RawMessage(`{"name":"t","arguments":{}}`))
	cancel()
	waitForState(t, inst, StateReady, 3*time.Second)

	// Second crash → restart (attempt 2, backoff=MinRestartBackoff=2s)
	callCtx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = inst.Call(callCtx2, "tools/call", json.RawMessage(`{"name":"t2","arguments":{}}`))
	cancel2()

	// Now the instance should be in StateRestarting with a 2s backoff
	waitForState(t, inst, StateRestarting, 3*time.Second)

	// Call during restart should fail fast
	callCtx3, cancel3 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, err := inst.Call(callCtx3, "tools/call", json.RawMessage(`{"name":"t3","arguments":{}}`))
	cancel3()
	if err == nil {
		t.Fatal("expected error during restart, got nil")
	}

	inst.stop()
}

func TestComputeRestartBackoff(t *testing.T) {
	prevMin := MinRestartBackoff
	prevMax := MaxRestartBackoff
	MinRestartBackoff = 100 * time.Millisecond
	MaxRestartBackoff = 10 * time.Second
	t.Cleanup(func() {
		MinRestartBackoff = prevMin
		MaxRestartBackoff = prevMax
	})

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 0},
		{attempt: 1, want: 0},
		{attempt: 2, want: 100 * time.Millisecond},
		{attempt: 3, want: 200 * time.Millisecond},
		{attempt: 4, want: 400 * time.Millisecond},
		{attempt: 5, want: 800 * time.Millisecond},
		{attempt: 6, want: 1600 * time.Millisecond},
		{attempt: 7, want: 3200 * time.Millisecond},
		{attempt: 8, want: 6400 * time.Millisecond},
		{attempt: 9, want: 10 * time.Second},  // capped
		{attempt: 20, want: 10 * time.Second}, // still capped
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt%d", tt.attempt), func(t *testing.T) {
			inst := &Instance{restartAttempt: tt.attempt}
			got := inst.computeRestartBackoff()
			if got != tt.want {
				t.Errorf("computeRestartBackoff(attempt=%d) = %s, want %s",
					tt.attempt, got, tt.want)
			}
		})
	}
}

func TestInstance_RestartWait_SignalsCompletion(t *testing.T) {
	bin := buildCrashLoopStub(t, 1)
	env := append(os.Environ(),
		"PATH="+os.Getenv("PATH"),
		"STUB_CRASH_COUNT=1",
	)

	prevMin := MinRestartBackoff
	MinRestartBackoff = 50 * time.Millisecond
	t.Cleanup(func() { MinRestartBackoff = prevMin })

	inst := newInstance(
		InstanceKey{ServerID: "restart-wait"},
		bin, nil, env, 0, nil, "on-failure",
	)

	if err := inst.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Crash it
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = inst.Call(callCtx, "tools/call", json.RawMessage(`{"name":"t","arguments":{}}`))
	cancel()

	// Wait for the restart to signal completion
	waitCh := inst.waitRestartDone()
	select {
	case <-waitCh:
	case <-time.After(5 * time.Second):
		t.Fatal("restartWait never closed")
	}

	// After restartWait fires, instance should be running (Ready or Idle)
	if s := inst.getState(); s == StateStopped {
		t.Fatalf("state after restartWait = stopped, want running (ready/idle)")
	}

	inst.stop()
}

func TestManagerGetOrStart_EvictDuringRestart_NoDuplicate(t *testing.T) {
	m := newHTTPManager(t)

	var startCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			startCount.Add(1)
			time.Sleep(50 * time.Millisecond)
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			writeRPCResult(t, w, req.ID, `{"tools":[]}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer ts.Close()
	registerHTTPServer(t, m, "evict-test", ts.URL)

	key := InstanceKey{ServerID: "evict-test"}

	// First getOrStart to populate the instance
	_, err := m.getOrStart(context.Background(), key)
	if err != nil {
		t.Fatalf("first getOrStart: %v", err)
	}
	initialStarts := startCount.Load()

	// Concurrently evict and getOrStart for the same key
	var wg sync.WaitGroup
	var getErr error
	var getErrMu sync.Mutex

	wg.Add(3)

	go func() {
		defer wg.Done()
		m.evict(key)
	}()

	go func() {
		defer wg.Done()
		inst, err := m.getOrStart(context.Background(), key)
		getErrMu.Lock()
		getErr = err
		getErrMu.Unlock()
		if err == nil && inst == nil {
			t.Error("getOrStart returned nil instance with nil error")
		}
	}()

	go func() {
		defer wg.Done()
		m.evict(key)
	}()

	wg.Wait()

	if getErr != nil {
		t.Fatalf("getOrStart after concurrent evict: %v", getErr)
	}

	// Verify exactly one new start happened (the initial + one more from getOrStart)
	finalStarts := startCount.Load()
	if finalStarts != initialStarts+1 {
		t.Fatalf("initialize calls = %d, want %d (no duplicate starts)", finalStarts, initialStarts+1)
	}
}

func TestManagerGetOrStart_ConcurrentEvictsNoOrphans(t *testing.T) {
	m := newHTTPManager(t)

	var startCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			startCount.Add(1)
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			writeRPCResult(t, w, req.ID, `{"tools":[]}`)
		default:
			writeRPCResult(t, w, req.ID, `{"ok":true}`)
		}
	}))
	defer ts.Close()
	registerHTTPServer(t, m, "concurrent-test", ts.URL)

	key := InstanceKey{ServerID: "concurrent-test"}

	const rounds = 10
	for i := 0; i < rounds; i++ {
		var wg sync.WaitGroup
		wg.Add(4)
		go func() { defer wg.Done(); m.evict(key) }()
		go func() { defer wg.Done(); m.evict(key) }()
		go func() {
			defer wg.Done()
			_, _ = m.getOrStart(context.Background(), key)
		}()
		go func() {
			defer wg.Done()
			_, _ = m.getOrStart(context.Background(), key)
		}()
		wg.Wait()
	}

	m.mu.Lock()
	instanceCount := len(m.instances)
	m.mu.Unlock()
	if instanceCount > 1 {
		t.Fatalf("instances for key = %d, want <=1", instanceCount)
	}
}

func TestManagerGetOrStart_DifferentKeysNotBlocked(t *testing.T) {
	// Verify per-key locks don't deadlock different keys
	m := newHTTPManager(t)

	slowEntered := make(chan struct{})
	releaseSlow := make(chan struct{})

	tsSlow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			close(slowEntered)
			<-releaseSlow
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			writeRPCResult(t, w, req.ID, `{"tools":[]}`)
		default:
			writeRPCResult(t, w, req.ID, `{"ok":true}`)
		}
	}))
	defer tsSlow.Close()
	registerHTTPServer(t, m, "slow-key", tsSlow.URL)

	tsFast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		switch req.Method {
		case "initialize":
			writeRPCResult(t, w, req.ID, `{"protocolVersion":"2025-03-26","capabilities":{}}`)
		case "notifications/initialized":
			writeRPCResult(t, w, req.ID, `{}`)
		case "tools/list":
			writeRPCResult(t, w, req.ID, `{"tools":[]}`)
		default:
			writeRPCResult(t, w, req.ID, `{"ok":true}`)
		}
	}))
	defer tsFast.Close()
	registerHTTPServer(t, m, "fast-key", tsFast.URL)

	// Start slow key (holds its per-key lock during slow initialize)
	slowDone := make(chan error, 1)
	go func() {
		_, err := m.getOrStart(context.Background(), InstanceKey{ServerID: "slow-key"})
		slowDone <- err
	}()
	<-slowEntered

	// Fast key should not be blocked by slow key
	fastDone := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := m.getOrStart(context.Background(), InstanceKey{ServerID: "fast-key"})
		fastDone <- err
	}()

	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("fast key getOrStart: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("fast key blocked behind slow key: %s", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fast key did not complete while slow key was starting")
	}

	close(releaseSlow)
	if err := <-slowDone; err != nil {
		t.Fatalf("slow key getOrStart: %v", err)
	}
}
