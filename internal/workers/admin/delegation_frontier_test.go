package admin

import (
	"strings"
	"testing"
)

func resolvedCandidate(provider, modelID string) delegationResolvedModelCandidate {
	c := delegationResolvedModelCandidate{}
	c.ModelProvider = provider
	c.ModelID = modelID
	return c
}

func TestFrontierWorkerWarnings(t *testing.T) {
	t.Run("flags frontier execute worker", func(t *testing.T) {
		plan := []delegationResolvedModelCandidate{
			resolvedCandidate("claude_cli", "claude-opus-4-8"),
		}
		got := frontierWorkerWarnings("execute", plan)
		if len(got) != 1 {
			t.Fatalf("want 1 warning, got %d (%v)", len(got), got)
		}
		if !strings.Contains(got[0], "claude_cli/claude-opus-4-8") {
			t.Errorf("warning should name the model: %q", got[0])
		}
	})

	t.Run("silent for workhorse worker", func(t *testing.T) {
		plan := []delegationResolvedModelCandidate{
			resolvedCandidate("opencode_cli", "zai-coding-plan/glm-5.1"),
			resolvedCandidate("opencode_cli", "minimax/MiniMax-M3"),
		}
		if got := frontierWorkerWarnings("execute", plan); len(got) != 0 {
			t.Errorf("want no warnings for workhorse models, got %v", got)
		}
	})

	t.Run("review mode exempts frontier", func(t *testing.T) {
		plan := []delegationResolvedModelCandidate{
			resolvedCandidate("claude_cli", "claude-opus-4-8"),
		}
		if got := frontierWorkerWarnings("review", plan); len(got) != 0 {
			t.Errorf("review mode must not warn (frontier judge is sanctioned), got %v", got)
		}
	})

	t.Run("dedups repeated frontier model", func(t *testing.T) {
		plan := []delegationResolvedModelCandidate{
			resolvedCandidate("openai", "gpt-5.5"),
			resolvedCandidate("openai", "gpt-5.5"),
			resolvedCandidate("opencode_cli", "zai-coding-plan/glm-5.1"),
		}
		if got := frontierWorkerWarnings("execute", plan); len(got) != 1 {
			t.Errorf("want 1 deduped warning, got %d (%v)", len(got), got)
		}
	})
}
