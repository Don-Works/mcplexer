//go:build darwin || linux || freebsd || openbsd || netbsd

package models

import (
	"context"
	"testing"
	"time"
)

// TestNewSandboxedCLICmd_Configured asserts the hard-stop knobs are set:
// process-group isolation (Setpgid), a group-kill Cancel hook, and a
// bounded WaitDelay. These are what let operator hard-stop reach the real
// model process under the sandbox-exec wrapper instead of orphaning it.
func TestNewSandboxedCLICmd_Configured(t *testing.T) {
	cmd := newSandboxedCLICmd(context.Background(), "/bin/sh", []string{"-c", "true"})
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("expected SysProcAttr.Setpgid=true for process-group isolation")
	}
	if cmd.WaitDelay != cliHardStopWaitDelay {
		t.Fatalf("WaitDelay = %v, want %v", cmd.WaitDelay, cliHardStopWaitDelay)
	}
	if cmd.Cancel == nil {
		t.Fatal("expected a Cancel hook that kills the process group")
	}
}

// TestNewSandboxedCLICmd_CancelKillsSubprocess proves the real kill path:
// a started subprocess is terminated promptly when its context is
// cancelled (within the WaitDelay budget), exercising the group-kill
// Cancel hook end-to-end rather than just its configuration.
func TestNewSandboxedCLICmd_CancelKillsSubprocess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := newSandboxedCLICmd(ctx, "/bin/sh", []string{"-c", "sleep 30"})
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	// Let the child establish its own process group before we cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Wait returned → the process was killed via the Cancel hook.
	case <-time.After(cliHardStopWaitDelay + 3*time.Second):
		t.Fatal("subprocess not terminated within WaitDelay after cancel")
	}
}
