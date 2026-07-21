package sshx

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRun_StderrCaptured proves a container's stderr-origin line is
// no longer silently dropped — the bug this change fixes. Docker
// preserves stream separation, so app-stderr lines never reach
// sess.StdoutPipe(); they must come back on Result.Stderr.
func TestRun_StderrCaptured(t *testing.T) {
	srv := startFakeServer(t, func(_ io.Writer, stderr io.Writer) uint32 {
		_, _ = stderr.Write([]byte("2026-07-08T14:00:00.000000000Z panic: boom\n"))
		return 0
	})
	c := srv.dial(t)
	res, err := c.Run(context.Background(), "docker logs --timestamps app", 1<<20)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(string(res.Stderr), "panic: boom") {
		t.Fatalf("stderr not captured: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if len(res.Stdout) != 0 {
		t.Fatalf("unexpected stdout: %q", res.Stdout)
	}
	if res.Truncated {
		t.Fatal("unexpected truncation")
	}
}

// TestRun_CombinedCapEnforced proves stdout+stderr share ONE budget —
// neither stream alone can smuggle output past maxBytes — and that
// the two streams are drained CONCURRENTLY, not sequentially: each
// side writes several MiB, well past any single SSH channel's default
// flow-control window, so a sequential stdout-then-stderr reader
// would stall forever waiting for window credit the untouched stream
// never releases. A bounded wait proves that doesn't happen.
func TestRun_CombinedCapEnforced(t *testing.T) {
	const maxBytes = 100
	big := bytes.Repeat([]byte("a"), 4<<20) // 4 MiB, well past any default channel window
	srv := startFakeServer(t, func(stdout, stderr io.Writer) uint32 {
		// Write both streams concurrently: server-side, so a client that
		// only drains one of them (the old bug's shape) leaves the other
		// blocked on write forever instead of erroring out at close.
		done := make(chan struct{}, 2)
		go func() { _, _ = stdout.Write(big); done <- struct{}{} }()
		go func() { _, _ = stderr.Write(big); done <- struct{}{} }()
		<-done
		<-done
		return 0
	})
	c := srv.dial(t)

	type outcome struct {
		res Result
		err error
	}
	resCh := make(chan outcome, 1)
	go func() {
		res, err := c.Run(context.Background(), "docker logs --timestamps app", maxBytes)
		resCh <- outcome{res, err}
	}()

	select {
	case o := <-resCh:
		if o.err != nil {
			t.Fatalf("run: %v", o.err)
		}
		if !o.res.Truncated {
			t.Fatal("expected Truncated=true")
		}
		if total := len(o.res.Stdout) + len(o.res.Stderr); total > maxBytes {
			t.Fatalf("combined output %d exceeds shared cap %d", total, maxBytes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return — stdout/stderr drain likely serialized/deadlocked")
	}
}

// TestRun_ContextCancellation proves a hung remote command is
// unblocked promptly by context cancellation, and (as feasible for a
// goroutine-count check) the drain/watchdog goroutines don't leak.
func TestRun_ContextCancellation(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := startFakeServer(t, func(_, _ io.Writer) uint32 {
		<-release // simulate a hung remote command
		return 0
	})
	c := srv.dial(t)

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = c.Run(ctx, "docker logs --timestamps app", 1<<20)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	deadline := time.Now().Add(1 * time.Second)
	for {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count did not settle: before=%d after=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRun_NonZeroExitDiagnosticIsRedacted(t *testing.T) {
	const secret = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_part_long_enough"
	srv := startFakeServer(t, func(_ io.Writer, stderr io.Writer) uint32 {
		_, _ = stderr.Write([]byte("Authorization: Bearer " + secret + "\n"))
		return 1
	})
	client := srv.dial(t)
	_, err := client.Run(context.Background(), "docker logs --timestamps app", 1<<20)
	if err == nil {
		t.Fatal("non-zero remote exit returned nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("credential leaked in diagnostic: %v", err)
	}
}
