package clistats

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeCommandRunner struct {
	path      string
	lookupErr error
	output    []byte
	runErr    error
	wait      bool
	name      string
	args      []string
	maxBytes  int
}

func (f *fakeCommandRunner) LookPath(string) (string, error) {
	return f.path, f.lookupErr
}

func (f *fakeCommandRunner) CombinedOutput(
	ctx context.Context, name string, args []string, maxBytes int,
) ([]byte, error) {
	f.name, f.args, f.maxBytes = name, append([]string(nil), args...), maxBytes
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.output, f.runErr
}

func TestRunnerBuildsSafeStatsCommandAndParsesOutput(t *testing.T) {
	fake := &fakeCommandRunner{
		path:   "/usr/local/bin/mimo",
		output: []byte("│ MODEL USAGE │\n├────┤\n│ xiaomi/mimo │\n│  Messages 7 │\n│  Input Tokens 1.2K │\n└────┘\n"),
	}
	runner := NewRunner(fake)
	result := runner.Run(context.Background(), "mimo", 14)
	if result.Status != RunStatusOK || result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	wantArgs := []string{"stats", "--days", "14", "--models"}
	if fake.name != fake.path || !reflect.DeepEqual(fake.args, wantArgs) {
		t.Fatalf("command = %q %v, want %q %v", fake.name, fake.args, fake.path, wantArgs)
	}
	if fake.maxBytes != defaultMaxOutput {
		t.Errorf("max output = %d, want %d", fake.maxBytes, defaultMaxOutput)
	}
	if len(result.Models) != 1 || result.Models[0].InputTokens != 1200 {
		t.Errorf("models = %+v", result.Models)
	}
}

func TestRunnerUnavailableAndCommandError(t *testing.T) {
	missing := NewRunner(&fakeCommandRunner{lookupErr: errors.New("not found")})
	result := missing.Run(context.Background(), "mimo", 30)
	if result.Status != RunStatusUnavailable || !errors.Is(result.Err, ErrUnavailable) {
		t.Fatalf("missing result = %+v", result)
	}

	failed := NewRunner(&fakeCommandRunner{path: "/bin/opencode", runErr: errors.New("exit 1")})
	result = failed.Run(context.Background(), "opencode", 30)
	if result.Status != RunStatusError || !errors.Is(result.Err, ErrCommandFailed) {
		t.Fatalf("failed result = %+v", result)
	}
}

func TestRunnerBoundsContext(t *testing.T) {
	fake := &fakeCommandRunner{path: "/bin/mimo", wait: true}
	runner := NewRunner(fake)
	runner.Timeout = 10 * time.Millisecond
	started := time.Now()
	result := runner.Run(context.Background(), "mimo", 30)
	if result.Status != RunStatusError || !errors.Is(result.Err, ErrCommandFailed) {
		t.Fatalf("result = %+v", result)
	}
	if time.Since(started) > time.Second {
		t.Fatal("runner did not enforce its timeout")
	}
}

func TestBoundedBufferReportsOverflow(t *testing.T) {
	var buffer boundedBuffer
	buffer.limit = 4
	n, err := buffer.Write([]byte("123456"))
	if err != nil || n != 6 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if !buffer.overflow || string(buffer.Bytes()) != "1234" {
		t.Fatalf("buffer = %q overflow=%v", buffer.Bytes(), buffer.overflow)
	}
}
