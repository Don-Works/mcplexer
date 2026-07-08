package collect

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestPull_Integration exercises the REAL sshx runner against a live
// box. Skipped unless explicitly armed:
//
//	MCPLEXER_SSH_ITEST=1 \
//	MCPLEXER_SSH_ITEST_HOST=<host> MCPLEXER_SSH_ITEST_USER=<user> \
//	MCPLEXER_SSH_ITEST_KEY_FILE=~/.ssh/id_ed25519 \
//	MCPLEXER_SSH_ITEST_CONTAINER=<name> go test ./internal/logwatch/collect/ -run Integration -v
//
// First run TOFU-pins the host key (asserted); the pull must return
// at least one redacted line from the container.
func TestPull_Integration(t *testing.T) {
	if os.Getenv("MCPLEXER_SSH_ITEST") != "1" {
		t.Skip("set MCPLEXER_SSH_ITEST=1 (+_HOST/_USER/_KEY_FILE/_CONTAINER) to run")
	}
	host := os.Getenv("MCPLEXER_SSH_ITEST_HOST")
	user := os.Getenv("MCPLEXER_SSH_ITEST_USER")
	keyFile := os.Getenv("MCPLEXER_SSH_ITEST_KEY_FILE")
	container := os.Getenv("MCPLEXER_SSH_ITEST_CONTAINER")
	if host == "" || user == "" || keyFile == "" || container == "" {
		t.Fatal("MCPLEXER_SSH_ITEST_{HOST,USER,KEY_FILE,CONTAINER} all required")
	}
	port := 22
	if p := os.Getenv("MCPLEXER_SSH_ITEST_PORT"); p != "" {
		var err error
		if port, err = strconv.Atoi(p); err != nil {
			t.Fatalf("bad port: %v", err)
		}
	}
	pem, err := os.ReadFile(keyFile) // #nosec G304 — operator-supplied test path
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	fs := &fakeStore{
		host: &store.RemoteHost{
			ID: "it-host", Name: "itest", SSHUser: user, SSHHost: host,
			SSHPort: port, AuthScopeID: "it-scope", Enabled: true,
		},
		scope: &store.AuthScope{ID: "it-scope", Type: "ssh_key"},
	}
	sink := &captureSink{}
	m := NewManager(fs, staticSecrets{pem: pem}, sink, nil) // nil = real sshx runner

	src := srcDocker()
	src.Selector = container
	src.MaxPullBytes = 1 << 20

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := m.pullSource(ctx, src); err != nil {
		t.Fatalf("live pull: %v", err)
	}
	if fs.pin == "" {
		t.Fatal("expected TOFU pin to be recorded on first dial")
	}
	t.Logf("pinned %s; pulled %d lines; cursor=%v", fs.pin, len(sink.lines), fs.cursorTS)
	if len(sink.lines) == 0 {
		t.Fatal("expected at least one line from the container")
	}
}

type staticSecrets struct{ pem []byte }

func (s staticSecrets) Get(context.Context, string, string) ([]byte, error) {
	return s.pem, nil
}
