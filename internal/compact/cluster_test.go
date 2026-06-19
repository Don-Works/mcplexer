package compact

import "testing"

func TestClusterLexicalTaskEventsIgnoreVariableIDsAndTimes(t *testing.T) {
	items := []LexicalItem{
		{
			ID:   "evt-1",
			Text: "Task 01KTBPH535224Q8NZCFJ0AAAAA updated status to doing at 2026-06-05T10:00:00Z",
		},
		{
			ID:   "evt-2",
			Text: "Task 01KTBPH535224Q8NZCFJ0BBBBB updated status to doing at 2026-06-05T10:01:00Z",
		},
		{
			ID:   "evt-3",
			Text: "Task 01KTBPH535224Q8NZCFJ0CCCCC updated status to blocked at 2026-06-05T10:02:00Z",
		},
	}

	got := ClusterLexical(items, LexicalClusterOptions{})

	if got.Total != 3 {
		t.Fatalf("Total = %d, want 3", got.Total)
	}
	if got.Clustered != 2 {
		t.Fatalf("Clustered = %d, want 2; groups=%+v", got.Clustered, got.Groups)
	}
	if len(got.Groups) != 2 {
		t.Fatalf("len(Groups) = %d, want 2: %+v", len(got.Groups), got.Groups)
	}
	if got.Groups[0].Count != 2 {
		t.Fatalf("first group Count = %d, want 2: %+v", got.Groups[0].Count, got.Groups[0])
	}
	if got.Groups[1].Count != 1 {
		t.Fatalf("second group Count = %d, want 1: %+v", got.Groups[1].Count, got.Groups[1])
	}
}

func TestClusterLexicalBoundsMeshChatterExamples(t *testing.T) {
	items := []LexicalItem{
		{ID: "m1", Label: "agent-a", Text: "Build finished: no source changes detected; waiting for next task."},
		{ID: "m2", Label: "agent-b", Text: "Build finished: no source changes detected; waiting for next task."},
		{ID: "m3", Label: "agent-c", Text: "Build finished: no source changes detected; waiting for next task."},
		{ID: "m4", Label: "agent-d", Text: "Build finished: no source changes detected; waiting for next task."},
	}
	opts := DefaultLexicalClusterOptions()
	opts.MaxExamples = 2

	got := ClusterLexical(items, opts)

	if len(got.Groups) != 1 {
		t.Fatalf("len(Groups) = %d, want 1: %+v", len(got.Groups), got.Groups)
	}
	g := got.Groups[0]
	if g.Count != 4 {
		t.Fatalf("Count = %d, want 4", g.Count)
	}
	if len(g.Examples) != 2 {
		t.Fatalf("len(Examples) = %d, want 2", len(g.Examples))
	}
	if g.Omitted != 2 {
		t.Fatalf("Omitted = %d, want 2", g.Omitted)
	}
}

func TestClusterLexicalDoesNotMergeDistinctMemoryFacts(t *testing.T) {
	items := []LexicalItem{
		{ID: "mem-1", Label: "database", Text: "Database URL rotated and stored in auth scope production."},
		{ID: "mem-2", Label: "payments", Text: "Payment gateway API key rotated for checkout service."},
	}

	got := ClusterLexical(items, LexicalClusterOptions{})

	if got.Clustered != 0 {
		t.Fatalf("Clustered = %d, want 0: %+v", got.Clustered, got.Groups)
	}
	if len(got.Groups) != 2 {
		t.Fatalf("len(Groups) = %d, want 2: %+v", len(got.Groups), got.Groups)
	}
	for _, g := range got.Groups {
		if g.Count != 1 {
			t.Fatalf("distinct fact grouped unexpectedly: %+v", g)
		}
	}
}

func TestClusterLexicalClustersRepetitiveSearchHits(t *testing.T) {
	items := []LexicalItem{
		{ID: "github__list_issues", Label: "github__list_issues", Text: "List issues in a repository."},
		{ID: "linear__list_issues", Label: "linear__list_issues", Text: "List issues for a repository."},
		{ID: "gmail__send", Label: "gmail__send", Text: "Send a draft email message."},
	}

	got := ClusterLexical(items, LexicalClusterOptions{})

	if got.Clustered != 2 {
		t.Fatalf("Clustered = %d, want 2: %+v", got.Clustered, got.Groups)
	}
	if len(got.Groups) != 2 {
		t.Fatalf("len(Groups) = %d, want 2: %+v", len(got.Groups), got.Groups)
	}
	if got.Groups[0].Count != 2 {
		t.Fatalf("first group Count = %d, want 2: %+v", got.Groups[0].Count, got.Groups[0])
	}
	if got.Groups[0].Examples[0].ID != "github__list_issues" {
		t.Fatalf("order changed: first example ID = %q", got.Groups[0].Examples[0].ID)
	}
}

func TestClusterLexicalBoundsItemsAndGroups(t *testing.T) {
	items := []LexicalItem{
		{ID: "1", Text: "alpha one unique"},
		{ID: "2", Text: "bravo two unique"},
		{ID: "3", Text: "charlie three unique"},
		{ID: "4", Text: "delta four unique"},
		{ID: "5", Text: "echo five unique"},
	}
	opts := DefaultLexicalClusterOptions()
	opts.MaxItems = 4
	opts.MaxGroups = 2

	got := ClusterLexical(items, opts)

	if got.Total != 5 {
		t.Fatalf("Total = %d, want 5", got.Total)
	}
	if got.Truncated != 3 {
		t.Fatalf("Truncated = %d, want 3: %+v", got.Truncated, got.Groups)
	}
	if len(got.Groups) != 2 {
		t.Fatalf("len(Groups) = %d, want 2: %+v", len(got.Groups), got.Groups)
	}
}

func TestClusterLexicalTruncatesExampleText(t *testing.T) {
	opts := DefaultLexicalClusterOptions()
	opts.MaxExampleText = 12
	got := ClusterLexical([]LexicalItem{{
		ID:   "x",
		Text: "this is a long repeated message body",
	}}, opts)

	if len(got.Groups) != 1 || len(got.Groups[0].Examples) != 1 {
		t.Fatalf("unexpected groups: %+v", got.Groups)
	}
	if got.Groups[0].Examples[0].Text != "this is a..." {
		t.Fatalf("Text = %q, want truncated preview", got.Groups[0].Examples[0].Text)
	}
}
