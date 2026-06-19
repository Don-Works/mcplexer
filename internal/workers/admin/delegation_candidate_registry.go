package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

func (s *Service) appendWorkerDelegationModelCandidates(
	ctx context.Context,
	in *DelegationInput,
	out *[]DelegationModelCandidate,
	seen map[string]struct{},
) error {
	if strings.TrimSpace(in.WorkspaceID) == "" && s.workspaces == nil {
		return nil
	}
	workspaceIDs, err := s.collectWorkspaceIDs(ctx, strings.TrimSpace(in.WorkspaceID))
	if err != nil {
		return err
	}
	for _, workspaceID := range workspaceIDs {
		workers, err := s.store.ListWorkers(ctx, workspaceID, true)
		if err != nil {
			return fmt.Errorf("list workers in %s: %w", workspaceID, err)
		}
		for _, w := range workers {
			candidate := delegationWorkerModelCandidate(w)
			if candidate.ModelProvider == "" || candidate.ModelID == "" {
				continue
			}
			addDelegationModelCandidate(out, seen, candidate)
		}
	}
	return nil
}

func delegationWorkerModelCandidate(w *store.Worker) DelegationModelCandidate {
	if w == nil {
		return DelegationModelCandidate{}
	}
	tags := delegationWorkerCapabilityTags(w)
	return DelegationModelCandidate{
		Label:            w.Name,
		ModelProvider:    w.ModelProvider,
		ModelID:          w.ModelID,
		ModelEndpointURL: w.ModelEndpointURL,
		SecretScopeID:    w.SecretScopeID,
		CapabilityTags:   tags,
		InputModalities:  delegationWorkerInputModalities(w, tags),
		OutputModalities: []string{"text"},
	}
}

func addDelegationModelCandidate(
	out *[]DelegationModelCandidate,
	seen map[string]struct{},
	candidate DelegationModelCandidate,
) {
	key := delegationModelCandidateRegistryKey(candidate)
	if key == "/" || key == "" {
		return
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*out = append(*out, candidate)
}

func delegationModelCandidateRegistryKey(candidate DelegationModelCandidate) string {
	provider := strings.TrimSpace(candidate.ModelProvider)
	modelID := strings.TrimSpace(candidate.ModelID)
	if provider == "" && modelID == "" {
		return ""
	}
	if profileID := strings.TrimSpace(candidate.ModelProfileID); profileID != "" {
		return provider + "/" + modelID + "/profile:" + profileID
	}
	endpoint := strings.TrimSpace(candidate.ModelEndpointURL)
	scopeID := strings.TrimSpace(candidate.SecretScopeID)
	return provider + "/" + modelID + "/endpoint:" + endpoint + "/scope:" + scopeID
}

func delegationWorkerCapabilityTags(w *store.Worker) []string {
	text := strings.ToLower(strings.Join([]string{
		w.Name,
		w.Description,
		w.ModelID,
		w.PromptTemplate,
		w.ToolAllowlistJSON,
	}, " "))
	var tags []string
	add := func(tag string, needles ...string) {
		if containsAny(text, needles...) {
			tags = append(tags, tag)
		}
	}
	add("review", "review", "critique", "audit")
	add("coding", "code", "coding", "implement", "patch", "debug", "fix")
	add("architecture", "architect", "design", "plan")
	add("visual", "visual", "vision", "image", "screenshot", "multimodal")
	if allowlist := strings.TrimSpace(w.ToolAllowlistJSON); allowlist != "" && allowlist != "[]" {
		tags = append(tags, "tool_calling")
	}
	return normaliseStringList(tags)
}

func delegationWorkerInputModalities(w *store.Worker, tags []string) []string {
	modalities := []string{"text"}
	text := strings.ToLower(w.ModelID + " " + strings.Join(tags, " "))
	if containsAny(text, "visual", "vision", "image", "multimodal") {
		modalities = append(modalities, "image")
	}
	return normaliseStringList(modalities)
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
