package collect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/sshx"
	"github.com/don-works/mcplexer/internal/store"
)

// fakeStore implements the narrow collect.Store seam in memory.
type fakeStore struct {
	sources  []*store.LogSource
	host     *store.RemoteHost
	scope    *store.AuthScope
	cursorTS time.Time
	cursorH  string
	failures int
	pin      string
}

func (f *fakeStore) ListEnabledLogSources(context.Context) ([]*store.LogSource, error) {
	return f.sources, nil
}
func (f *fakeStore) GetRemoteHost(context.Context, string) (*store.RemoteHost, error) {
	return f.host, nil
}
func (f *fakeStore) GetAuthScope(context.Context, string) (*store.AuthScope, error) {
	return f.scope, nil
}
func (f *fakeStore) UpdateLogSourceCursor(_ context.Context, _ string, ts time.Time, h string) error {
	f.cursorTS, f.cursorH, f.failures = ts, h, 0
	return nil
}
func (f *fakeStore) SetLogSourceFailures(_ context.Context, _ string, n int) error {
	f.failures = n
	return nil
}
func (f *fakeStore) SetRemoteHostPin(_ context.Context, _ string, pin string) error {
	f.pin = pin
	return nil
}

type fakeSecrets struct{}

func (fakeSecrets) Get(context.Context, string, string) ([]byte, error) {
	return []byte("irrelevant-for-fake-runner"), nil
}

type fakeRunner struct {
	out       string
	truncated bool
	newPin    string
	err       error
	gotSince  time.Time
	gotKind   string
}

func (r *fakeRunner) Pull(_ context.Context, _ *store.RemoteHost, _ sshx.Credential, src *store.LogSource, since time.Time) (sshx.Result, error) {
	r.gotSince = since
	r.gotKind = src.Kind
	return sshx.Result{Output: []byte(r.out), Truncated: r.truncated, NewPin: r.newPin}, r.err
}

type captureSink struct{ lines []Line }

func (s *captureSink) Ingest(_ context.Context, _ *store.LogSource, _ *store.RemoteHost, lines []Line) error {
	s.lines = append(s.lines, lines...)
	return nil
}

func newFixture(runner *fakeRunner) (*Manager, *fakeStore, *captureSink) {
	fs := &fakeStore{
		host:  &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHUser: "logwatch", SSHHost: "10.0.0.1", SSHPort: 22, AuthScopeID: "sc1", Enabled: true},
		scope: &store.AuthScope{ID: "sc1", Type: sshx.AuthScopeTypeSSHKey},
	}
	sink := &captureSink{}
	m := NewManager(fs, fakeSecrets{}, sink, runner)
	return m, fs, sink
}

func srcDocker() *store.LogSource {
	return &store.LogSource{
		ID: "s1", WorkspaceID: "ws", RemoteHostID: "h1", Name: "api",
		Kind: store.LogSourceKindDocker, Selector: "api",
		ScheduleSpec: "2m", MaxPullBytes: 1 << 20, Enabled: true,
	}
}

// TestPull_FirstRun ingests everything, advances the cursor to the
// last line, and TOFU-persists the pin.
func TestPull_FirstRun(t *testing.T) {
	runner := &fakeRunner{
		out: "2026-07-08T14:00:00.000000001Z hello\n" +
			"2026-07-08T14:00:01.000000001Z ERROR pgx: connection refused host=db-3\n",
		newPin: "SHA256:firstseen",
	}
	m, fs, sink := newFixture(runner)

	if err := m.pullSource(context.Background(), srcDocker()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(sink.lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(sink.lines))
	}
	if !runner.gotSince.IsZero() {
		t.Fatalf("first pull must not pass --since, got %v", runner.gotSince)
	}
	want := time.Date(2026, 7, 8, 14, 0, 1, 1, time.UTC)
	if !fs.cursorTS.Equal(want) || fs.cursorH == "" {
		t.Fatalf("cursor not advanced: %v %q", fs.cursorTS, fs.cursorH)
	}
	if fs.pin != "SHA256:firstseen" {
		t.Fatalf("TOFU pin not persisted: %q", fs.pin)
	}
}

// TestPull_ContinuousCursor drops the duplicated tail line and does
// NOT emit a discontinuity event.
func TestPull_ContinuousCursor(t *testing.T) {
	tail := "2026-07-08T14:00:01.000000001Z ERROR pgx: connection refused host=db-3"
	runner := &fakeRunner{out: tail + "\n2026-07-08T14:00:02.000000001Z next line\n"}
	m, fs, sink := newFixture(runner)

	src := srcDocker()
	ts := time.Date(2026, 7, 8, 14, 0, 1, 1, time.UTC)
	src.CursorTS = &ts
	src.CursorHash = lineHash(tail)

	if err := m.pullSource(context.Background(), src); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(sink.lines) != 1 || sink.lines[0].Text != "next line" {
		t.Fatalf("expected exactly the new line, got %+v", sink.lines)
	}
	if !runner.gotSince.Equal(ts) {
		t.Fatalf("pull must pass cursor as --since, got %v", runner.gotSince)
	}
	if !fs.cursorTS.Equal(ts.Add(time.Second)) {
		t.Fatalf("cursor: %v", fs.cursorTS)
	}
}

// TestPull_Discontinuity injects the synthetic event when the tail
// hash no longer matches (restart / recreation / rotation).
func TestPull_Discontinuity(t *testing.T) {
	runner := &fakeRunner{out: "2026-07-08T15:00:00Z fresh container banner\n"}
	m, _, sink := newFixture(runner)

	src := srcDocker()
	ts := time.Date(2026, 7, 8, 14, 0, 1, 1, time.UTC)
	src.CursorTS = &ts
	src.CursorHash = "deadbeefdeadbeef"

	if err := m.pullSource(context.Background(), src); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(sink.lines) != 2 || !strings.HasPrefix(sink.lines[0].Text, "logwatch: source discontinuity") {
		t.Fatalf("expected discontinuity event first, got %+v", sink.lines)
	}
}

// TestPull_Truncation appends the synthetic truncation event so a
// capped window can't masquerade as quiet logs.
func TestPull_Truncation(t *testing.T) {
	runner := &fakeRunner{out: "2026-07-08T14:00:00Z spam\n", truncated: true}
	m, _, sink := newFixture(runner)
	if err := m.pullSource(context.Background(), srcDocker()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	lastLine := sink.lines[len(sink.lines)-1].Text
	if !strings.HasPrefix(lastLine, "logwatch: pull truncated") {
		t.Fatalf("expected truncation event, got %+v", sink.lines)
	}
}

// TestPull_RedactsSecrets proves redaction happens BEFORE the sink —
// the distiller and everything downstream only ever see scrubbed text.
func TestPull_RedactsSecrets(t *testing.T) {
	runner := &fakeRunner{
		out: "2026-07-08T14:00:00Z auth header Bearer sk-ant-api03-abcdefghijklmnopqrstuvwx-1234567890abcdefghijklmn\n",
	}
	m, _, sink := newFixture(runner)
	if err := m.pullSource(context.Background(), srcDocker()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(sink.lines) != 1 {
		t.Fatalf("lines: %+v", sink.lines)
	}
	if strings.Contains(sink.lines[0].Text, "sk-ant-") {
		t.Fatalf("secret survived redaction: %q", sink.lines[0].Text)
	}
}

// TestPull_ErrorLeavesCursor keeps the cursor untouched on failure so
// the next pull re-covers the window, and records the failure count.
func TestPull_ErrorLeavesCursor(t *testing.T) {
	runner := &fakeRunner{err: errors.New("connection refused")}
	m, fs, _ := newFixture(runner)

	src := srcDocker()
	src.ConsecutiveFailures = 2
	if err := m.pullSource(context.Background(), src); err == nil {
		t.Fatal("expected error")
	}
	if !fs.cursorTS.IsZero() {
		t.Fatalf("cursor must not advance on failure: %v", fs.cursorTS)
	}
	// tick() owns failure accounting; verify it via the loop path.
	fs.sources = []*store.LogSource{src}
	m.tick(context.Background())
	if fs.failures != 3 {
		t.Fatalf("expected failures=3 via tick, got %d", fs.failures)
	}
}

// TestPull_JournaldAndCompose: M6 kinds flow through the same pull
// pipeline, and the runner sees the right kind.
func TestPull_JournaldAndCompose(t *testing.T) {
	for _, kind := range []string{store.LogSourceKindJournald, store.LogSourceKindCompose} {
		runner := &fakeRunner{out: "2026-07-08T14:00:00.000000Z hello from " + kind + "\n"}
		m, _, sink := newFixture(runner)
		src := srcDocker()
		src.Kind = kind
		src.Selector = "myunit"
		if err := m.pullSource(context.Background(), src); err != nil {
			t.Fatalf("%s pull: %v", kind, err)
		}
		if runner.gotKind != kind {
			t.Fatalf("runner saw kind %q, want %q", runner.gotKind, kind)
		}
		if len(sink.lines) != 1 {
			t.Fatalf("%s: expected 1 line, got %d", kind, len(sink.lines))
		}
	}
}

// TestPull_FileRefused: plain-file kind still needs byte-offset
// cursoring (tracked in M6), so it is not collected yet.
func TestPull_FileRefused(t *testing.T) {
	m, _, _ := newFixture(&fakeRunner{})
	src := srcDocker()
	src.Kind = store.LogSourceKindFile
	if err := m.pullSource(context.Background(), src); err == nil {
		t.Fatal("file kind must be refused until byte-offset cursoring lands")
	}
}
