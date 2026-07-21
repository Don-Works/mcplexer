package workertemplates_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// helloTemplateBody is a minimal valid template body — the smallest
// JSON shape that survives Unmarshal.
const helloTemplateBody = `{"name":"hello","prompt_template":"Hi {who}."}`

func newTestRegistry(t *testing.T) (*workertemplates.Registry, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return workertemplates.New(db), db
}

func TestPublishCreatesThenDedups(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	first, err := reg.Publish(ctx, workertemplates.PublishOptions{
		Body:   helloTemplateBody,
		Author: "test",
	})
	if err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if first.Version != 1 || first.Action != "created" {
		t.Fatalf("first publish: %+v", first)
	}

	second, err := reg.Publish(ctx, workertemplates.PublishOptions{
		Body:   helloTemplateBody,
		Author: "test",
	})
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	if second.Version != 1 || second.Action != "deduped" {
		t.Fatalf("dedup expected, got %+v", second)
	}
}

func TestPublishNewVersionOnContentChange(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.Publish(ctx, workertemplates.PublishOptions{Body: helloTemplateBody}); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	v2body := `{"name":"hello","prompt_template":"Hi {who}!"}`
	res, err := reg.Publish(ctx, workertemplates.PublishOptions{Body: v2body})
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	if res.Version != 2 || res.Action != "created" {
		t.Fatalf("expected v2 created, got %+v", res)
	}
}

func TestGetAndListHeads(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.Publish(ctx, workertemplates.PublishOptions{Body: helloTemplateBody}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, err := reg.Get(ctx, workertemplates.AdminScope(), "hello", workertemplates.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "hello" || got.Version != 1 {
		t.Fatalf("get returned %+v", got)
	}
	heads, err := reg.ListHeads(ctx, workertemplates.AdminScope(), 0)
	if err != nil {
		t.Fatalf("list heads: %v", err)
	}
	// At least one head from seeds or the test publish; keep the floor
	// low so the test is robust to seed curation.
	if len(heads) < 1 {
		t.Fatalf("expected at least 1 head, got %d", len(heads))
	}
	var found bool
	for _, h := range heads {
		if h.Name == "hello" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hello not in list heads: %+v", heads)
	}
}

func TestPublishRejectsMalformedBody(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, err := reg.Publish(context.Background(), workertemplates.PublishOptions{
		Body: `{"prompt_template":"missing name"}`,
	})
	if err == nil {
		t.Fatal("expected error for body without name")
	}
}

func TestBundledTemplatesPresent(t *testing.T) {
	reg, _ := newTestRegistry(t)
	heads, err := reg.ListHeads(context.Background(), workertemplates.AdminScope(), 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := map[string]bool{
		"daily-status-digest": false,
		"audit-summary":       false,
		"cost-watcher":        false,
		"hello-world":         false,
		// Added in migration 084 — placeholder out-of-box automations.
		"telegram-responder":  false,
		"slack-status-notify": false,
		// Added in migration 109 — create-only Telegram task intake.
		"telegram-task-intake": false,
	}
	for _, h := range heads {
		if _, ok := want[h.Name]; ok {
			want[h.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("bundled template %q not present after migrations", name)
		}
	}
}
