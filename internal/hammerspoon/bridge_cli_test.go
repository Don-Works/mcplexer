package hammerspoon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeStubHS drops an executable shell script at tmpDir/hs that emits the
// supplied stdout, exits with the given code, and (optionally) sleeps before
// returning. Returns the absolute path passed to NewCLIDriver.
func writeStubHS(t *testing.T, stdout string, exitCode int, sleepSec int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("CLI driver tests require a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hs")
	script := "#!/bin/sh\n"
	if sleepSec > 0 {
		script += "sleep " + itoa(sleepSec) + "\n"
	}
	script += "printf '%s' " + shellQuote(stdout) + "\n"
	script += "exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

func shellQuote(s string) string {
	// Single-quote the string for /bin/sh; escape any embedded quote.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestCLIDriver_Happy verifies a successful envelope round-trip.
func TestCLIDriver_Happy(t *testing.T) {
	bin := writeStubHS(t, `{"ok":true,"result":42}`, 0, 0)
	d := NewCLIDriver(bin)
	env, err := d.Exec(context.Background(), "return 42", 2*5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !env.Ok {
		t.Fatalf("envelope ok: got false err=%q", env.Err)
	}
	if string(env.Result) != "42" {
		t.Errorf("result: got %s", env.Result)
	}
}

// TestCLIDriver_ErrEnvelope verifies a Lua-side error envelope passes through.
func TestCLIDriver_ErrEnvelope(t *testing.T) {
	bin := writeStubHS(t, `{"ok":false,"err":"boom"}`, 0, 0)
	d := NewCLIDriver(bin)
	env, err := d.Exec(context.Background(), "return 1", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false")
	}
	if env.Err != "boom" {
		t.Errorf("err: want 'boom' got %q", env.Err)
	}
}

// TestCLIDriver_NonZeroExit verifies a non-zero exit folds stderr into err.
func TestCLIDriver_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hs")
	script := "#!/bin/sh\necho 'bad config' >&2\nexit 3\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	d := NewCLIDriver(path)
	env, err := d.Exec(context.Background(), "return 1", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on non-zero exit")
	}
	if !strings.Contains(env.Err, "bad config") {
		t.Errorf("err: want stderr in env, got %q", env.Err)
	}
}

// TestCLIDriver_EmptyStdout verifies a 0-exit + no stdout surfaces a helpful err.
func TestCLIDriver_EmptyStdout(t *testing.T) {
	bin := writeStubHS(t, "", 0, 0)
	d := NewCLIDriver(bin)
	env, err := d.Exec(context.Background(), "return 1", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on empty stdout")
	}
	if !strings.Contains(env.Err, "empty output") {
		t.Errorf("err: want 'empty output' got %q", env.Err)
	}
}

// TestCLIDriver_MalformedJSON verifies non-JSON stdout becomes a clean err.
func TestCLIDriver_MalformedJSON(t *testing.T) {
	bin := writeStubHS(t, "not json", 0, 0)
	d := NewCLIDriver(bin)
	env, err := d.Exec(context.Background(), "return 1", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on malformed stdout")
	}
	if !strings.Contains(env.Err, "malformed JSON") {
		t.Errorf("err: want 'malformed JSON' got %q", env.Err)
	}
}

// TestCLIDriver_Timeout verifies a slow `hs` is cancelled.
func TestCLIDriver_Timeout(t *testing.T) {
	bin := writeStubHS(t, `{"ok":true}`, 0, 2)
	d := NewCLIDriver(bin)
	env, err := d.Exec(context.Background(), "return 1", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on timeout")
	}
	if !strings.Contains(env.Err, "timed out") && !strings.Contains(env.Err, "failed") {
		t.Errorf("err: want timeout-ish, got %q", env.Err)
	}
}

// TestCLIDriver_BinaryMissing verifies a missing bin surfaces an install hint.
func TestCLIDriver_BinaryMissing(t *testing.T) {
	d := NewCLIDriver(filepath.Join(t.TempDir(), "no-such-hs"))
	env, err := d.Exec(context.Background(), "return 1", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false")
	}
	if !strings.Contains(env.Err, "not found on PATH") {
		t.Errorf("err: want install hint, got %q", env.Err)
	}
}

// TestWrapLuaForCLI_ContainsUserCode is a string-shape assertion that the
// wrapper embeds the user snippet inside a Lua long-bracket literal and emits
// the envelope-encoding call.
func TestWrapLuaForCLI_ContainsUserCode(t *testing.T) {
	wrapped := wrapLuaForCLI(`return hs.application.frontmostApplication():name()`)
	if !strings.Contains(wrapped, `[==[return hs.application.frontmostApplication():name()]==]`) {
		t.Errorf("user code not embedded as long bracket: %s", wrapped)
	}
	if !strings.Contains(wrapped, `hs.json.encode({ok=true, result=_res})`) {
		t.Errorf("envelope ok-path missing: %s", wrapped)
	}
	if !strings.Contains(wrapped, `pcall(_fn)`) {
		t.Errorf("pcall guard missing: %s", wrapped)
	}
}
