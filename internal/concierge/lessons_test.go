package concierge_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newLessonsMemoryService(t *testing.T) *memory.Service {
	t.Helper()
	d, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "lessons.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return memory.NewService(d, memory.NoopEmbedder{}, nil)
}

func TestPinLessonGlobal(t *testing.T) {
	ctx := context.Background()
	svc := newLessonsMemoryService(t)
	id, err := concierge.PinLesson(ctx, svc, concierge.PinLessonOptions{
		LessonText:         "Default to terse responses; expand only when asked.",
		SourceRefinementID: "refine-abc",
	})
	if err != nil {
		t.Fatalf("PinLesson: %v", err)
	}
	if id == "" {
		t.Fatal("expected memory id")
	}
	body, err := concierge.RecentLessonsFor(ctx, svc, "", "", 10)
	if err != nil {
		t.Fatalf("RecentLessonsFor: %v", err)
	}
	if !strings.Contains(body, "Default to terse") {
		t.Errorf("RecentLessonsFor body missing global lesson: %q", body)
	}
}

func TestPinLessonPerUserAndGlobalMerge(t *testing.T) {
	ctx := context.Background()
	svc := newLessonsMemoryService(t)
	if _, err := concierge.PinLesson(ctx, svc, concierge.PinLessonOptions{
		LessonText: "Global rule: never apologize twice in one turn.",
	}); err != nil {
		t.Fatalf("PinLesson global: %v", err)
	}
	if _, err := concierge.PinLesson(ctx, svc, concierge.PinLessonOptions{
		LessonText:     "For this user specifically: prefer Linear-inbox terseness.",
		Channel:        "telegram",
		UserIDExternal: "telegram:42",
	}); err != nil {
		t.Fatalf("PinLesson per-user: %v", err)
	}
	body, err := concierge.RecentLessonsFor(ctx, svc, "telegram", "telegram:42", 10)
	if err != nil {
		t.Fatalf("RecentLessonsFor: %v", err)
	}
	if !strings.Contains(body, "Global rule") {
		t.Errorf("body missing global lesson: %q", body)
	}
	if !strings.Contains(body, "For this user specifically") {
		t.Errorf("body missing per-user lesson: %q", body)
	}
	// Per-user lesson should appear FIRST (most-specific wins).
	if strings.Index(body, "For this user specifically") > strings.Index(body, "Global rule") {
		t.Error("per-user lesson should appear before global in merged body")
	}
}

func TestPinLessonRejectsEmpty(t *testing.T) {
	ctx := context.Background()
	svc := newLessonsMemoryService(t)
	if _, err := concierge.PinLesson(ctx, svc, concierge.PinLessonOptions{}); err == nil {
		t.Error("PinLesson should reject empty lesson_text")
	}
}
