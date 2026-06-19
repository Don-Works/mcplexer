package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestRecipeCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Insert a recipe.
	r := &store.Recipe{
		ToolName:       "github__list_issues",
		Namespace:      "github",
		Description:    "List repository issues",
		SuccessCount:   10,
		TotalCount:     12,
		ErrorRate:      0.1667,
		Score:          0.85,
		SessionCount:   4,
		ParamsPattern:  json.RawMessage(`{"keys":["owner","repo"],"optional":["state","labels"]}`),
		Tags:           json.RawMessage(`["ns:github","successful"]`),
		SourceAuditIDs: json.RawMessage(`["aud-1","aud-2"]`),
	}

	if err := db.UpsertRecipe(ctx, r); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if r.ID == "" {
		t.Fatal("expected ID to be set")
	}

	// Get by ID.
	got, err := db.GetRecipe(ctx, r.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ToolName != "github__list_issues" {
		t.Fatalf("tool_name = %q, want %q", got.ToolName, "github__list_issues")
	}
	if got.SuccessCount != 10 {
		t.Fatalf("success_count = %d, want %d", got.SuccessCount, 10)
	}

	// Get by tool name.
	got, err = db.GetRecipeByToolName(ctx, "github__list_issues")
	if err != nil {
		t.Fatalf("get by tool_name: %v", err)
	}
	if got.Namespace != "github" {
		t.Fatalf("namespace = %q, want %q", got.Namespace, "github")
	}

	// Upsert with same tool_name (update).
	r2 := &store.Recipe{
		ToolName:     "github__list_issues",
		Namespace:    "github",
		Description:  "List repository issues v2",
		SuccessCount: 25,
		TotalCount:   30,
		ErrorRate:    0.1667,
		Score:        0.92,
		SessionCount: 8,
	}
	if err := db.UpsertRecipe(ctx, r2); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if r2.ID == "" {
		t.Fatal("expected ID to be set on update")
	}

	// Verify update took effect.
	got, err = db.GetRecipeByToolName(ctx, "github__list_issues")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.SuccessCount != 25 {
		t.Fatalf("success_count after update = %d, want %d", got.SuccessCount, 25)
	}
	if got.TotalCount != 30 {
		t.Fatalf("total_count after update = %d, want %d", got.TotalCount, 30)
	}

	// List recipes.
	recipes, err := db.ListRecipes(ctx, store.RecipeFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recipes) != 1 {
		t.Fatalf("list count = %d, want %d", len(recipes), 1)
	}

	// List with namespace filter.
	ns := "github"
	recipes, err = db.ListRecipes(ctx, store.RecipeFilter{Namespace: &ns, Limit: 10})
	if err != nil {
		t.Fatalf("list by namespace: %v", err)
	}
	if len(recipes) != 1 {
		t.Fatalf("list by namespace count = %d, want %d", len(recipes), 1)
	}

	// List with non-matching namespace.
	ns2 := "postgres"
	recipes, err = db.ListRecipes(ctx, store.RecipeFilter{Namespace: &ns2, Limit: 10})
	if err != nil {
		t.Fatalf("list by non-matching namespace: %v", err)
	}
	if len(recipes) != 0 {
		t.Fatalf("list by non-matching namespace count = %d, want 0", len(recipes))
	}

	// Search via FTS.
	recipes, err = db.SearchRecipes(ctx, store.RecipeFilter{Query: "issues v2"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(recipes) == 0 {
		t.Fatal("expected search results for 'issues v2'")
	}
	if recipes[0].ToolName != "github__list_issues" {
		t.Fatalf("search result tool_name = %q, want %q", recipes[0].ToolName, "github__list_issues")
	}

	// Search non-matching.
	recipes, err = db.SearchRecipes(ctx, store.RecipeFilter{Query: "zzzzz"})
	if err != nil {
		t.Fatalf("search non-matching: %v", err)
	}
	if len(recipes) != 0 {
		t.Fatalf("expected no results for 'zzzzz', got %d", len(recipes))
	}

	// Model-generated search text often includes punctuation that is FTS5
	// syntax if passed through raw. It should be tokenised and quoted.
	recipes, err = db.SearchRecipes(ctx, store.RecipeFilter{Query: "github: issues?"})
	if err != nil {
		t.Fatalf("search punctuated query: %v", err)
	}
	if len(recipes) == 0 || recipes[0].ToolName != "github__list_issues" {
		t.Fatalf("punctuated search result = %+v, want github__list_issues", recipes)
	}

	recipes, err = db.SearchRecipes(ctx, store.RecipeFilter{Query: ":-)"})
	if err != nil {
		t.Fatalf("search punctuation-only query: %v", err)
	}
	if len(recipes) != 0 {
		t.Fatalf("punctuation-only query should return no recipes, got %d", len(recipes))
	}

	// Delete.
	if err := db.DeleteRecipe(ctx, r2.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Get after delete should fail.
	_, err = db.GetRecipe(ctx, r2.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestRecipeMultipleRecipes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	recipes := []store.Recipe{
		{
			ToolName: "github__create_issue", Namespace: "github",
			SuccessCount: 50, TotalCount: 55, Score: 0.90, SessionCount: 10,
			LastUsedAt: &now,
		},
		{
			ToolName: "postgres__query", Namespace: "postgres",
			SuccessCount: 100, TotalCount: 120, Score: 0.75, SessionCount: 20,
			LastUsedAt: &now,
		},
		{
			ToolName: "slack__send_message", Namespace: "slack",
			SuccessCount: 5, TotalCount: 10, Score: 0.50, SessionCount: 2,
			LastUsedAt: &now,
		},
	}

	for i := range recipes {
		if err := db.UpsertRecipe(ctx, &recipes[i]); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	// List should return all 3, ordered by score DESC.
	all, err := db.ListRecipes(ctx, store.RecipeFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list count = %d, want 3", len(all))
	}
	// Check ordering: highest score first.
	if all[0].Score < all[1].Score {
		t.Fatal("expected recipes ordered by score DESC")
	}

	// Search across multiple.
	results, err := db.SearchRecipes(ctx, store.RecipeFilter{Query: "github"})
	if err != nil {
		t.Fatalf("search 'github': %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("search 'github' count = %d, want 1", len(results))
	}
}

func TestRecipeEmptyStore(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// List on empty store.
	recipes, err := db.ListRecipes(ctx, store.RecipeFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(recipes) != 0 {
		t.Fatalf("expected empty list, got %d", len(recipes))
	}

	// Get non-existent.
	_, err = db.GetRecipe(ctx, "non-existent")
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRecipeMigrate102CreatesTables(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// The migration ran during New(). Verify recipes table exists.
	recipes, err := db.ListRecipes(ctx, store.RecipeFilter{Limit: 1})
	if err != nil {
		t.Fatalf("list after migration: %v", err)
	}
	_ = recipes // empty is fine, table exists

	// Insert to verify the table works end-to-end.
	r := &store.Recipe{
		ToolName: "test__tool", Namespace: "test",
		SuccessCount: 1, TotalCount: 1, Score: 0.5,
	}
	if err := db.UpsertRecipe(ctx, r); err != nil {
		t.Fatalf("upsert after migration: %v", err)
	}
}
