package codemode

import (
	"strings"
	"testing"
)

func TestLintNamespaceShadowing_WarnsOnMeshBinding(t *testing.T) {
	code := "const mesh = mesh.receive({ filter: \"new\" });\nprint(mesh);"
	tools := []string{"mesh__receive", "mesh__send", "memory__recall", "task__list"}
	result := LintWithTools(code, tools)
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "shadows tool namespace") &&
			strings.Contains(w.Message, "mesh") {
			found = true
			if w.Severity != "warning" {
				t.Errorf("severity = %q, want warning", w.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("expected namespace-shadow warning, got %+v", result.Warnings)
	}
}

func TestLintNamespaceShadowing_AllowsSafeAlias(t *testing.T) {
	code := "const inbox = mesh.receive({ filter: \"new\" });\nprint(inbox.stats);"
	tools := []string{"mesh__receive", "memory__recall"}
	result := LintWithTools(code, tools)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "shadows tool namespace") {
			t.Fatalf("unexpected shadow warning for safe alias: %+v", w)
		}
	}
}
