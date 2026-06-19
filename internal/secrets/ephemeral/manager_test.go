package ephemeral_test

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newTestManager(t *testing.T) (*ephemeral.Manager, *ephemeral.Bus, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.New(context.Background(), dir+"/test.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bus := ephemeral.NewBus()
	mgr, err := ephemeral.New(context.Background(), db, dir, nil, bus, nil)
	if err != nil {
		t.Fatalf("ephemeral.New: %v", err)
	}
	t.Cleanup(mgr.Stop)
	return mgr, bus, dir
}

// TestPromptSubmitFlow exercises the happy path: request -> submit -> the
// agent receives a path to a 0600 file that contains the secret bytes.
func TestPromptSubmitFlow(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	ctx := context.Background()

	created, err := mgr.RequestPrompt(ctx, ephemeral.PromptRequest{
		Reason:       "test reason",
		Label:        "TEST_KEY",
		Timeout:      2 * time.Second,
		DeleteOnRead: false,
	})
	if err != nil {
		t.Fatalf("RequestPrompt: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created.ID should be set")
	}

	resCh := make(chan ephemeral.PromptResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := mgr.Wait(ctx, created.ID)
		if err != nil {
			errCh <- err
			return
		}
		resCh <- r
	}()

	// Give the goroutine time to register on the result channel.
	time.Sleep(50 * time.Millisecond)
	if err := mgr.Submit(ctx, created.ID, []byte("super-secret-value")); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case res := <-resCh:
		if res.Path == "" {
			t.Fatal("expected non-empty path")
		}
		info, err := os.Stat(res.Path)
		if err != nil {
			t.Fatalf("stat secret file: %v", err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
			t.Errorf("perms = %v, want %v", got, want)
		}
		data, err := os.ReadFile(res.Path)
		if err != nil {
			t.Fatalf("read secret file: %v", err)
		}
		if string(data) != "super-secret-value" {
			t.Errorf("file body = %q, want %q", data, "super-secret-value")
		}
		// 256-bit (= 32-byte = 64 hex char) random filename.
		if len(filepathBase(res.Path)) != 64 {
			t.Errorf("filename length = %d, want 64", len(filepathBase(res.Path)))
		}
	case err := <-errCh:
		t.Fatalf("Wait error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return in time")
	}
}

// TestCancelReturnsSentinel verifies Cancel surfaces ErrUserCancelled to the
// blocked Wait caller and writes no file.
func TestCancelReturnsSentinel(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	ctx := context.Background()

	created, err := mgr.RequestPrompt(ctx, ephemeral.PromptRequest{
		Reason: "cancel-me", Label: "X", Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("RequestPrompt: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := mgr.Wait(ctx, created.ID)
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := mgr.Cancel(ctx, created.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, ephemeral.ErrUserCancelled) {
			t.Fatalf("Wait err = %v, want ErrUserCancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after Cancel")
	}
}

// TestTimeoutFires verifies a Wait that runs past expires_at returns
// ErrPromptTimeout.
func TestTimeoutFires(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	ctx := context.Background()

	created, err := mgr.RequestPrompt(ctx, ephemeral.PromptRequest{
		Reason: "t/o", Label: "X", Timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RequestPrompt: %v", err)
	}
	_, err = mgr.Wait(ctx, created.ID)
	if !errors.Is(err, ephemeral.ErrPromptTimeout) {
		t.Fatalf("Wait err = %v, want ErrPromptTimeout", err)
	}
	_ = created
}

// TestDeleteOnReadDarwin asserts that on macOS the kqueue watcher hard-
// deletes the file the first time something opens it. On other platforms
// we skip — Linux inotify is exercised on CI; the polling fallback is best-
// effort and racy in CI sandboxes.
func TestDeleteOnReadDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("kqueue is darwin-only; runtime=%s", runtime.GOOS)
	}
	mgr, _, _ := newTestManager(t)
	ctx := context.Background()

	created, err := mgr.RequestPrompt(ctx, ephemeral.PromptRequest{
		Reason: "d/r", Label: "X", Timeout: 5 * time.Second,
		DeleteOnRead: true,
	})
	if err != nil {
		t.Fatalf("RequestPrompt: %v", err)
	}
	resCh := make(chan ephemeral.PromptResult, 1)
	go func() {
		r, _ := mgr.Wait(ctx, created.ID)
		resCh <- r
	}()
	time.Sleep(50 * time.Millisecond)
	if err := mgr.Submit(ctx, created.ID, []byte("delete-me")); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	res := <-resCh
	// Trigger a "first read" — open the file. kqueue should detect and
	// remove. Sleep tolerantly, kqueue notification is async.
	if _, err := os.ReadFile(res.Path); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(res.Path); err != nil && os.IsNotExist(err) {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("file %s was not deleted after first read", res.Path)
}

// TestSubmitWritesFile0600 directly checks the 0600 owner-only perm bits.
func TestSubmitWritesFile0600(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	ctx := context.Background()

	created, err := mgr.RequestPrompt(ctx, ephemeral.PromptRequest{
		Reason: "perm", Label: "X", Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("RequestPrompt: %v", err)
	}
	resCh := make(chan ephemeral.PromptResult, 1)
	go func() {
		r, _ := mgr.Wait(ctx, created.ID)
		resCh <- r
	}()
	time.Sleep(50 * time.Millisecond)
	if err := mgr.Submit(ctx, created.ID, []byte("hello")); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	res := <-resCh
	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("perm = %v, want %v", got, want)
	}
}

// TestBusFanout verifies pending and resolved Events are published.
func TestBusFanout(t *testing.T) {
	mgr, bus, _ := newTestManager(t)
	ctx := context.Background()

	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	created, err := mgr.RequestPrompt(ctx, ephemeral.PromptRequest{
		Reason: "fanout", Label: "X", Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("RequestPrompt: %v", err)
	}
	select {
	case e := <-sub:
		if e.Type != "pending" || e.ID != created.ID {
			t.Errorf("first event = %+v, want pending/%s", e, created.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("no pending event published")
	}
	go func() { _, _ = mgr.Wait(ctx, created.ID) }()
	time.Sleep(50 * time.Millisecond)
	if err := mgr.Cancel(ctx, created.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case e := <-sub:
		if e.Type != "resolved" || e.Status != "cancelled" {
			t.Errorf("second event = %+v, want resolved/cancelled", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no resolved event published")
	}
}

// filepathBase mirrors filepath.Base without dragging path/filepath into
// every assertion line; declared inline for the test.
func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
