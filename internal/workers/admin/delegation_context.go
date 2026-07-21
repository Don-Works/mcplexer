package admin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// MemoryRecaller is the subset of the memory service the delegation
// service needs to auto-inject prior knowledge into worker prompts.
// *memory.Service satisfies it structurally.
type MemoryRecaller interface {
	Recall(ctx context.Context, f store.MemoryFilter, query string, k int) ([]store.MemoryHit, error)
}

// SetMemoryRecaller injects the memory recall dependency (optional; the
// stdio construction path never wires it, so injection nil-degrades).
func (s *Service) SetMemoryRecaller(m MemoryRecaller) { s.memory = m }

const (
	// autoContextRecallK bounds the memory recall fan-out.
	autoContextRecallK = 6
	// autoContextMeshLimit bounds the recent-mesh window.
	autoContextMeshLimit = 8
	// autoContextMemoryCharCap truncates each rendered memory body.
	autoContextMemoryCharCap = 500
	// autoContextMeshCharCap truncates each rendered mesh preview.
	autoContextMeshCharCap = 280
	// autoContextBudgetChars caps the whole injected packet so context
	// enrichment cannot dwarf the objective/handoff.
	autoContextBudgetChars = 6000
	// autoContextTimeout bounds the synchronous packet build so a wedged
	// embedder endpoint cannot stall Delegate.
	autoContextTimeout = 2 * time.Second
	// autoContextMeshWindow is how far back the recent-mesh read looks.
	autoContextMeshWindow = 24 * time.Hour
)

// buildAutoContext assembles a bounded context packet — recalled memory
// plus recent mesh activity for the workspace — and returns the rendered
// prompt section and its estimated token count. Entirely best-effort:
// any error yields an empty packet, never a delegation failure. Returns
// ("", 0) when both sources are unavailable or the caller opted out.
func (s *Service) buildAutoContext(ctx context.Context, in *DelegationInput) (string, int) {
	if in.NoAutoContext {
		return "", 0
	}
	if s.memory == nil && s.meshStore == nil {
		return "", 0
	}
	cctx, cancel := context.WithTimeout(ctx, autoContextTimeout)
	defer cancel()

	var b strings.Builder
	budget := autoContextBudgetChars

	if mem := s.recallMemorySection(cctx, in, &budget); mem != "" {
		b.WriteString(mem)
	}
	if msh := s.recentMeshSection(cctx, in, &budget); msh != "" {
		b.WriteString(msh)
	}
	packet := b.String()
	if strings.TrimSpace(packet) == "" {
		return "", 0
	}
	return packet, estimateBriefTokens(packet)
}

func (s *Service) recallMemorySection(ctx context.Context, in *DelegationInput, budget *int) string {
	if s.memory == nil || *budget <= 0 {
		return ""
	}
	query := strings.TrimSpace(in.Objective + " " + in.Handoff)
	filter := store.MemoryFilter{Scope: store.SkillScope{WorkspaceIDs: []string{in.WorkspaceID}}}
	if in.TaskID != "" {
		filter.EntitiesAny = []store.EntityRef{{Kind: "task", ID: in.TaskID}}
	}
	hits, err := s.memory.Recall(ctx, filter, query, autoContextRecallK)
	if err != nil || len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Prior knowledge (auto-recalled memory)\n")
	wrote := false
	for _, h := range hits {
		line := "- " + h.Entry.Name + ": " + truncateForContext(h.Entry.Content, autoContextMemoryCharCap) + "\n"
		if len(line) > *budget {
			break
		}
		b.WriteString(line)
		*budget -= len(line)
		wrote = true
	}
	if !wrote {
		return ""
	}
	b.WriteString("\n")
	return b.String()
}

func (s *Service) recentMeshSection(ctx context.Context, in *DelegationInput, budget *int) string {
	if s.meshStore == nil || *budget <= 0 {
		return ""
	}
	// Scope by workspace only. The mesh Repo field is a git-remote
	// identifier, a different identifier space from the file-claim scope
	// key, so filtering mesh by the claim scope would wrongly exclude
	// everything.
	since := s.clock.Now().Add(-autoContextMeshWindow)
	filter := store.MeshMessageFilter{
		WorkspaceIDs: []string{in.WorkspaceID, ""},
		SinceTime:    &since,
		StatusLive:   true,
		OrderRecent:  true,
		Limit:        autoContextMeshLimit,
		ExcludeKinds: []string{"task_event"},
	}
	msgs, err := s.meshStore.QueryMeshMessages(ctx, filter)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Recent mesh activity (auto-injected)\n")
	wrote := false
	for _, m := range msgs {
		// Skip periodic progress telemetry — it is noise for a fresh worker.
		if strings.Contains(m.Tags, "delegation_progress") {
			continue
		}
		who := m.SenderDisplayName
		if who == "" {
			who = m.AgentName
		}
		line := fmt.Sprintf("- [%s] %s: %s\n", m.Kind, who,
			truncateForContext(m.Content, autoContextMeshCharCap))
		if len(line) > *budget {
			break
		}
		b.WriteString(line)
		*budget -= len(line)
		wrote = true
	}
	if !wrote {
		return ""
	}
	b.WriteString("\n")
	return b.String()
}

func truncateForContext(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
