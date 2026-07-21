package embedding

import (
	"testing"
)

func TestNewIndex_SearchFindsRelevant(t *testing.T) {
	docs := []Document{
		{ID: "github__list_issues", Text: "github list issues search bugs pull requests"},
		{ID: "github__create_issue", Text: "github create issue bug report"},
		{ID: "slack__send_message", Text: "slack send message channel chat"},
		{ID: "linear__list_tasks", Text: "linear list tasks project management"},
		{ID: "calendar__schedule_event", Text: "calendar schedule event meeting appointment"},
	}

	idx := NewIndex(docs)

	tests := []struct {
		query    string
		wantTop  string
		wantSize int
	}{
		{"bugs", "github__list_issues", 1},
		{"send chat", "slack__send_message", 1},
		{"meeting", "calendar__schedule_event", 1},
		{"project tasks", "linear__list_tasks", 1},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			results := idx.Search(tt.query, 5)
			if len(results) < tt.wantSize {
				t.Fatalf("expected at least %d results, got %d", tt.wantSize, len(results))
			}
			if results[0].ID != tt.wantTop {
				t.Errorf("top result = %q, want %q", results[0].ID, tt.wantTop)
			}
			if results[0].Score <= 0 {
				t.Errorf("score should be positive, got %f", results[0].Score)
			}
		})
	}
}

func TestNewIndex_EmptyQuery(t *testing.T) {
	docs := []Document{
		{ID: "a", Text: "hello world"},
	}
	idx := NewIndex(docs)
	results := idx.Search("", 5)
	if len(results) != 0 {
		t.Errorf("empty query should return no results, got %d", len(results))
	}
}

func TestNewIndex_MaxResults(t *testing.T) {
	docs := make([]Document, 20)
	for i := range docs {
		docs[i] = Document{ID: string(rune('a' + i)), Text: "common word test"}
	}
	idx := NewIndex(docs)
	results := idx.Search("common", 3)
	if len(results) > 3 {
		t.Errorf("expected max 3 results, got %d", len(results))
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"hello world", 2},
		{"github__list_issues", 3},
		{"CamelCase", 1},
		{"", 0},
		{"a-b-c", 3},
	}
	for _, tt := range tests {
		tokens := tokenize(tt.input)
		if len(tokens) != tt.want {
			t.Errorf("tokenize(%q) = %d tokens, want %d", tt.input, len(tokens), tt.want)
		}
	}
}

func TestCosineSimilarity_Identical(t *testing.T) {
	a := map[string]float64{"x": 1, "y": 2}
	sim := cosineSimilarity(a, a)
	if sim < 0.999 {
		t.Errorf("identical vectors should have similarity ~1.0, got %f", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := map[string]float64{"x": 1}
	b := map[string]float64{"y": 1}
	sim := cosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("orthogonal vectors should have similarity 0, got %f", sim)
	}
}
