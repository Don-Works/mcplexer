package recipes

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeAuditQuerier implements AuditQuerier for testing.
type fakeAuditQuerier struct {
	mu      sync.Mutex
	records []AuditCall
}

func (f *fakeAuditQuerier) QueryRecentToolCalls(_ context.Context, since time.Time, _ int) ([]AuditCall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AuditCall
	for _, r := range f.records {
		if r.Timestamp.After(since) || r.Timestamp.Equal(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeAuditQuerier) QueryToolCallsByName(_ context.Context, toolName string, since time.Time, _ int) ([]AuditCall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AuditCall
	for _, r := range f.records {
		if r.ToolName == toolName && (r.Timestamp.After(since) || r.Timestamp.Equal(since)) {
			out = append(out, r)
		}
	}
	return out, nil
}

// fakeRecipeWriter implements RecipeWriter for testing.
type fakeRecipeWriter struct {
	mu      sync.Mutex
	recipes map[string]*store.Recipe // tool_name -> recipe
}

func (f *fakeRecipeWriter) UpsertRecipe(_ context.Context, r *store.Recipe) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recipes == nil {
		f.recipes = make(map[string]*store.Recipe)
	}
	existing, ok := f.recipes[r.ToolName]
	if ok {
		existing.SuccessCount = r.SuccessCount
		existing.TotalCount = r.TotalCount
		existing.ErrorRate = r.ErrorRate
		existing.Score = r.Score
		existing.SessionCount = r.SessionCount
		existing.Description = r.Description
	} else {
		cp := *r
		cp.ID = "test-" + r.ToolName
		f.recipes[r.ToolName] = &cp
	}
	return nil
}

func (f *fakeRecipeWriter) ListRecipes(_ context.Context, filter store.RecipeFilter) ([]store.Recipe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Recipe, 0, len(f.recipes))
	for _, r := range f.recipes {
		if filter.ToolName != nil && r.ToolName != *filter.ToolName {
			continue
		}
		if filter.Namespace != nil && r.Namespace != *filter.Namespace {
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

func (f *fakeRecipeWriter) DeleteRecipe(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, r := range f.recipes {
		if r.ID == id {
			delete(f.recipes, name)
			return nil
		}
	}
	return store.ErrNotFound
}

func TestHarvesterEmptyAudit(t *testing.T) {
	audit := &fakeAuditQuerier{}
	store := &fakeRecipeWriter{}
	h := NewHarvester(audit, store, HarvesterConfig{Since: 7 * 24 * time.Hour, MinOccurrences: 3})

	n, err := h.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run on empty audit: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 recipes from empty audit, got %d", n)
	}
}

func TestHarvesterBelowThreshold(t *testing.T) {
	now := time.Now()
	audit := &fakeAuditQuerier{
		records: []AuditCall{
			{ToolName: "tool__a", Status: "success", Timestamp: now, SessionID: "s1"},
			{ToolName: "tool__a", Status: "success", Timestamp: now, SessionID: "s2"},
			// Only 2 calls for tool__a, below min 3
		},
	}
	store := &fakeRecipeWriter{}
	h := NewHarvester(audit, store, HarvesterConfig{Since: 7 * 24 * time.Hour, MinOccurrences: 3})

	n, err := h.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 recipes below threshold, got %d", n)
	}
}

func TestHarvesterBasicHarvest(t *testing.T) {
	now := time.Now()
	audit := &fakeAuditQuerier{
		records: []AuditCall{
			{ToolName: "github__list_issues", Status: "success", LatencyMs: 100, Timestamp: now, SessionID: "s1", Params: json.RawMessage(`{"owner":"test","repo":"test"}`)},
			{ToolName: "github__list_issues", Status: "success", LatencyMs: 150, Timestamp: now, SessionID: "s1", Params: json.RawMessage(`{"owner":"test","repo":"other"}`)},
			{ToolName: "github__list_issues", Status: "error", LatencyMs: 2000, Timestamp: now, SessionID: "s2", Params: json.RawMessage(`{"owner":"test","repo":"broken"}`)},
			{ToolName: "github__list_issues", Status: "success", LatencyMs: 80, Timestamp: now, SessionID: "s2", Params: json.RawMessage(`{"owner":"test","repo":"test","state":"open"}`)},
			{ToolName: "postgres__query", Status: "success", LatencyMs: 5, Timestamp: now, SessionID: "s3", Params: json.RawMessage(`{"query":"SELECT 1"}`)},
			{ToolName: "postgres__query", Status: "success", LatencyMs: 3, Timestamp: now, SessionID: "s3", Params: json.RawMessage(`{"query":"SELECT 2"}`)},
			{ToolName: "postgres__query", Status: "success", LatencyMs: 7, Timestamp: now, SessionID: "s4", Params: json.RawMessage(`{"query":"SELECT 3"}`)},
		},
	}
	recipeStore := &fakeRecipeWriter{}
	h := NewHarvester(audit, recipeStore, HarvesterConfig{Since: 7 * 24 * time.Hour, MinOccurrences: 3})

	n, err := h.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 recipes (github__list_issues + postgres__query), got %d", n)
	}

	// Verify github__list_issues recipe.
	githubRecipe, err := recipeStore.ListRecipes(context.Background(), store.RecipeFilter{ToolName: strPtr("github__list_issues")})
	if err != nil {
		t.Fatalf("list github recipe: %v", err)
	}
	if len(githubRecipe) == 0 {
		t.Fatal("expected github recipe")
	}
	var r *store.Recipe
	for i := range githubRecipe {
		if githubRecipe[i].ToolName == "github__list_issues" {
			r = &githubRecipe[i]
			break
		}
	}
	if r == nil {
		t.Fatal("expected github recipe")
	}
	if r.Namespace != "github" {
		t.Fatalf("namespace = %q, want 'github'", r.Namespace)
	}
	if r.SuccessCount != 3 {
		t.Fatalf("success count = %d, want 3", r.SuccessCount)
	}
	if r.TotalCount != 4 {
		t.Fatalf("total count = %d, want 4", r.TotalCount)
	}
	if r.SessionCount != 2 {
		t.Fatalf("session count = %d, want 2", r.SessionCount)
	}
	if r.Score <= 0 {
		t.Fatal("expected positive score")
	}

	// Verify params pattern captured.
	if len(r.ParamsPattern) == 0 {
		t.Fatal("expected params pattern")
	}
	var pp ParamKeys
	if err := json.Unmarshal(r.ParamsPattern, &pp); err != nil {
		t.Fatalf("unmarshal params pattern: %v", err)
	}
	// owner and repo appear in all 4 calls, state in 1
	foundOwner := false
	foundRepo := false
	foundState := false
	for _, k := range pp.Keys {
		switch k {
		case "owner":
			foundOwner = true
		case "repo":
			foundRepo = true
		case "state":
			foundState = true
		}
	}
	if !foundOwner || !foundRepo {
		t.Fatal("expected owner and repo in common keys")
	}
	if foundState {
		t.Fatal("state should be optional, not common")
	}
}

func TestHarvesterLastUsedAndTimestamp(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)
	audit := &fakeAuditQuerier{
		records: []AuditCall{
			{ToolName: "tool__x", Status: "success", Timestamp: old, SessionID: "s1"},
			{ToolName: "tool__x", Status: "success", Timestamp: old, SessionID: "s2"},
			{ToolName: "tool__x", Status: "success", Timestamp: recent, SessionID: "s1"},
		},
	}
	recipeStore := &fakeRecipeWriter{}
	h := NewHarvester(audit, recipeStore, HarvesterConfig{Since: 7 * 24 * time.Hour, MinOccurrences: 3})

	n, err := h.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 recipe, got %d", n)
	}
}

func strPtr(s string) *string { return &s }
