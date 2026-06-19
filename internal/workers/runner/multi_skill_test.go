// multi_skill_test.go (M0.7) — verify the runner loads the ordered
// SkillRefs list and joins their bodies with the markdown separator.
// The legacy single-skill path is covered by TestRun_SkillBodyLoaded in
// runner_test.go and remains the regression check for pre-M0.7 workers.
package runner_test

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

func TestRun_MultiSkillBodiesJoined(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.SkillRefs = []store.SkillRef{
		{Name: "first", Version: ""},
		{Name: "second", Version: ""},
	}
	createWorker(t, db, w)

	adapter := &captureAdapter{response: models.SendResponse{Text: "ok", StopReason: models.StopEndTurn}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Skills: &fakeSkills{bodies: map[string]string{
			"first":  "## Body A",
			"second": "## Body B",
		}},
		Adapter: func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(adapter.lastSystem, "## Body A") {
		t.Fatalf("missing first body in system prompt: %q", adapter.lastSystem)
	}
	if !strings.Contains(adapter.lastSystem, "## Body B") {
		t.Fatalf("missing second body in system prompt: %q", adapter.lastSystem)
	}
	// Order matters — first body comes before separator and second body.
	idxA := strings.Index(adapter.lastSystem, "## Body A")
	idxB := strings.Index(adapter.lastSystem, "## Body B")
	if idxA == -1 || idxB == -1 || idxA >= idxB {
		t.Fatalf("body order wrong: A@%d B@%d (full %q)", idxA, idxB, adapter.lastSystem)
	}
	if !strings.Contains(adapter.lastSystem, "\n\n---\n\n") {
		t.Fatalf("missing markdown separator: %q", adapter.lastSystem)
	}
}

func TestRun_MultiSkillBodiesAggregateCap(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.SkillRefs = []store.SkillRef{
		{Name: "first", Version: ""},
		{Name: "second", Version: ""},
	}
	createWorker(t, db, w)

	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Skills: &fakeSkills{bodies: map[string]string{
			"first":  strings.Repeat("a", 60*1024),
			"second": strings.Repeat("b", 60*1024),
		}},
		Adapter: func(_ models.Config) (models.ModelAdapter, error) {
			return &captureAdapter{response: models.SendResponse{Text: "never", StopReason: models.StopEndTurn}}, nil
		},
	})
	if _, err := r.Run(context.Background(), w.ID); err == nil || !strings.Contains(err.Error(), "skill bodies exceed") {
		t.Fatalf("run err = %v, want skill bodies exceed", err)
	}
}

func TestRun_RenderedUserPromptCap(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.PromptTemplate = "{big}{big}{big}"
	w.ParametersJSON = `{"big":"` + strings.Repeat("x", 50*1024) + `"}`
	createWorker(t, db, w)

	adapter := &captureAdapter{response: models.SendResponse{Text: "never", StopReason: models.StopEndTurn}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	if _, err := r.Run(context.Background(), w.ID); err == nil || !strings.Contains(err.Error(), "user prompt exceeds") {
		t.Fatalf("run err = %v, want user prompt exceeds", err)
	}
	if adapter.lastSystem != "" || len(adapter.lastMsgs) != 0 {
		t.Fatalf("adapter should not be called when prompt exceeds cap")
	}
}

func TestRun_PreamblePrependedBeforeSkills(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.SkillRefs = []store.SkillRef{{Name: "first", Version: ""}}
	createWorker(t, db, w)

	adapter := &captureAdapter{response: models.SendResponse{Text: "ok", StopReason: models.StopEndTurn}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Skills:     &fakeSkills{bodies: map[string]string{"first": "## Body A"}},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
		Preamble:   "## PREAMBLE: you are inside mcplexer",
	})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(adapter.lastSystem, "## PREAMBLE: you are inside mcplexer") {
		t.Fatalf("preamble missing or not at top of system prompt: %q", adapter.lastSystem)
	}
	if !strings.Contains(adapter.lastSystem, "## Body A") {
		t.Fatalf("skill body missing after preamble: %q", adapter.lastSystem)
	}
	// The separator MUST sit between the preamble and the skill body.
	preambleEnd := len("## PREAMBLE: you are inside mcplexer")
	bodyIdx := strings.Index(adapter.lastSystem, "## Body A")
	if !strings.Contains(adapter.lastSystem[preambleEnd:bodyIdx], "\n\n---\n\n") {
		t.Fatalf("missing markdown separator between preamble and body: %q", adapter.lastSystem)
	}
}

func TestRun_PreambleOnlyWhenNoSkills(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &captureAdapter{response: models.SendResponse{Text: "ok", StopReason: models.StopEndTurn}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
		Preamble:   "ONLY-PREAMBLE",
	})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if adapter.lastSystem != "ONLY-PREAMBLE" {
		t.Fatalf("expected system prompt to equal preamble exactly when no skills, got %q", adapter.lastSystem)
	}
}

func TestRun_EmptySkillRefsEmptySystem(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	// No skills configured at all.
	createWorker(t, db, w)

	adapter := &captureAdapter{response: models.SendResponse{Text: "ok", StopReason: models.StopEndTurn}}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if adapter.lastSystem != "" {
		t.Fatalf("expected empty system prompt, got %q", adapter.lastSystem)
	}
}
