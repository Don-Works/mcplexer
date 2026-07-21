package admin

import (
	"strings"
	"testing"
)

func TestClassifyTaskShape(t *testing.T) {
	long := strings.Repeat("context detail ", 30) // pushes past the 300-char fallback
	cases := []struct {
		name      string
		objective string
		handoff   string
		want      string
	}{
		{"pure review", "Audit the auth package for race conditions", "", taskShapeReview},
		{"pure research", "Investigate how the routing engine resolves overlaps", "", taskShapeResearch},
		{"repo-wide scan", "Sweep the whole repo and inventory every TODO", "", taskShapeScan},
		{"short multi-file beats small-edit fallback", "Refactor across files", "", taskShapeMulti},
		{"cross-cutting migrate is multi", "Migrate the cross-cutting logging calls", "", taskShapeMulti},
		{"small fix", "Fix the typo in README", "", taskShapeSmall},
		{"short unmatched falls back small", "Do the thing", "", taskShapeSmall},
		{"long code work is multi", "Implement the new capability", long, taskShapeMulti},
		{"review keyword with code verbs is not review", "Fix the issues the review found", "", taskShapeSmall},
		{"no signal long text", strings.Repeat("lorem ipsum dolor sit amet ", 20), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyTaskShape(tc.objective, tc.handoff); got != tc.want {
				t.Fatalf("classifyTaskShape(%q) = %q, want %q", tc.objective, got, tc.want)
			}
		})
	}
}

func TestNormalizeTaskShape(t *testing.T) {
	cases := map[string]string{
		"multi-file":    taskShapeMulti,
		" Small Edit ":  taskShapeSmall,
		"codebase-scan": taskShapeScan,
		"review":        taskShapeReview,
		"nonsense":      "",
		"":              "",
	}
	for in, want := range cases {
		if got := normalizeTaskShape(in); got != want {
			t.Errorf("normalizeTaskShape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInferredTaskKindForShape(t *testing.T) {
	cases := map[string]string{
		taskShapeReview:   "review",
		taskShapeResearch: "research",
		taskShapeScan:     "research",
		taskShapeSmall:    "coding",
		taskShapeMulti:    "coding",
		"":                "",
	}
	for shape, want := range cases {
		if got := inferredTaskKindForShape(shape); got != want {
			t.Errorf("inferredTaskKindForShape(%q) = %q, want %q", shape, got, want)
		}
	}
}

func TestNormalizeDelegationInputShapeAndKindInference(t *testing.T) {
	svc := &Service{clock: realClock{}}

	// Omitted kind is inferred from the classified shape, with provenance.
	in := &DelegationInput{
		WorkspaceID: "ws", Objective: "Audit the auth package for races",
		ModelProvider: "grok_cli", ModelID: "grok-build", SecretScopeID: "scope-test", WorkerIsolation: "none",
	}
	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if in.TaskShape != taskShapeReview || in.TaskKind != "review" || !in.taskKindInferred {
		t.Fatalf("shape=%q kind=%q inferred=%v", in.TaskShape, in.TaskKind, in.taskKindInferred)
	}

	// Caller-supplied kind wins; no provenance flag.
	in = &DelegationInput{
		WorkspaceID: "ws", Objective: "Audit the auth package for races", TaskKind: "architecture",
		ModelProvider: "grok_cli", ModelID: "grok-build", SecretScopeID: "scope-test", WorkerIsolation: "none",
	}
	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if in.TaskKind != "architecture" || in.taskKindInferred {
		t.Fatalf("kind=%q inferred=%v, want caller value uninferred", in.TaskKind, in.taskKindInferred)
	}

	// Caller-supplied shape is respected (canonicalized).
	in = &DelegationInput{
		WorkspaceID: "ws", Objective: "Do the thing", TaskShape: "multi-file",
		ModelProvider: "grok_cli", ModelID: "grok-build", SecretScopeID: "scope-test", WorkerIsolation: "none",
	}
	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if in.TaskShape != taskShapeMulti {
		t.Fatalf("caller shape = %q, want %q", in.TaskShape, taskShapeMulti)
	}

	// Review mode forces the review shape regardless of text.
	in = &DelegationInput{
		WorkspaceID: "ws", Objective: "Implement the new module", WorkerMode: "review",
		ModelProvider: "grok_cli", ModelID: "grok-build", SecretScopeID: "scope-test", WorkerIsolation: "none",
	}
	if err := svc.normalizeDelegationInput(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if in.TaskShape != taskShapeReview {
		t.Fatalf("review mode shape = %q, want review", in.TaskShape)
	}
}
