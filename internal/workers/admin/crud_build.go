// crud_build.go — helpers for assembling a store.Worker from a
// CreateInput. Pulled out of crud.go to honour the 300-line-per-file
// budget. No exported surface; these are tightly coupled to Create.
package admin

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/store"
)

// buildWorkerFromCreate runs validation + defaults and returns a fresh
// Worker ready for insertion. Pulled out so Create can stay small.
func (s *Service) buildWorkerFromCreate(in CreateInput) (*store.Worker, error) {
	if err := validateCreate(in); err != nil {
		return nil, err
	}
	if err := s.validateScheduleSpec(in.ScheduleSpec); err != nil {
		return nil, fmt.Errorf("invalid schedule_spec: %w", err)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	now := s.clock.Now()
	skillName, skillVer, skillRefs := resolveSkillRefs(in)
	parametersJSON := defaultStr(strings.TrimSpace(in.ParametersJSON), "{}")
	return &store.Worker{
		ID:                     "wkr-" + uuid.NewString(),
		Name:                   in.Name,
		Description:            in.Description,
		ModelProvider:          in.ModelProvider,
		ModelID:                in.ModelID,
		ModelEndpointURL:       in.ModelEndpointURL,
		SecretScopeID:          in.SecretScopeID,
		SkillName:              skillName,
		SkillVersion:           skillVer,
		SkillRefs:              skillRefs,
		PromptTemplate:         in.PromptTemplate,
		ParametersJSON:         parametersJSON,
		ScheduleSpec:           in.ScheduleSpec,
		ToolAllowlistJSON:      defaultStr(in.ToolAllowlistJSON, "[]"),
		CapabilityProfileJSON:  strings.TrimSpace(in.CapabilityProfileJSON),
		PreExecuteScript:       in.PreExecuteScript,
		PostExecuteScript:      in.PostExecuteScript,
		OutputChannelsJSON:     defaultStr(in.OutputChannelsJSON, `[{"type":"mesh","priority":"normal"}]`),
		ExecMode:               defaultStr(in.ExecMode, "propose"),
		ConcurrencyPolicy:      defaultStr(in.ConcurrencyPolicy, "skip"),
		MemoryScopeID:          in.MemoryScopeID,
		MaxInputTokens:         in.MaxInputTokens,
		MaxOutputTokens:        in.MaxOutputTokens,
		MaxToolCalls:           in.MaxToolCalls,
		MaxWallClockSeconds:    in.MaxWallClockSeconds,
		MaxMonthlyCostUSD:      in.MaxMonthlyCostUSD,
		MaxConsecutiveFailures: in.MaxConsecutiveFailures,
		Enabled:                enabled,
		WorkspaceID:            in.WorkspaceID,
		WorkspaceAccess:        workerWorkspaceAccessOrDefault(in.WorkspaceID, in.WorkspaceAccess),
		SourceTemplateName:     in.sourceTemplateName,
		SourceTemplateVersion:  in.sourceTemplateVersion,
		CreatedAt:              now,
		UpdatedAt:              now,
	}, nil
}

func workerWorkspaceAccessOrDefault(
	preferredWorkspaceID string,
	grants []store.WorkerWorkspaceAccess,
) []store.WorkerWorkspaceAccess {
	if len(grants) == 0 {
		return []store.WorkerWorkspaceAccess{{
			WorkspaceID: preferredWorkspaceID,
			Access:      store.WorkerWorkspaceAccessWrite,
		}}
	}
	out := make([]store.WorkerWorkspaceAccess, 0, len(grants)+1)
	seen := map[string]int{}
	for _, g := range grants {
		wsID := strings.TrimSpace(g.WorkspaceID)
		if wsID == "" {
			continue
		}
		g.WorkspaceID = wsID
		out = append(out, g)
		seen[wsID] = len(out) - 1
	}
	if preferredWorkspaceID != "" {
		if idx, ok := seen[preferredWorkspaceID]; ok {
			out[idx].Access = store.WorkerWorkspaceAccessWrite
		} else {
			out = append(out, store.WorkerWorkspaceAccess{
				WorkspaceID: preferredWorkspaceID,
				Access:      store.WorkerWorkspaceAccessWrite,
			})
		}
	}
	return out
}

// resolveSkillRefs picks the authoritative skill refs slice for an
// incoming CreateInput and mirrors the first entry into the legacy
// (SkillName, SkillVersion) pair so consumers still on the old shape
// see the same skill. Priority: explicit SkillRefs > legacy single.
func resolveSkillRefs(in CreateInput) (string, string, []store.SkillRef) {
	if len(in.SkillRefs) > 0 {
		first := in.SkillRefs[0]
		return first.Name, first.Version, in.SkillRefs
	}
	if strings.TrimSpace(in.SkillName) == "" {
		return "", "", nil
	}
	refs := []store.SkillRef{{Name: in.SkillName, Version: in.SkillVersion}}
	return in.SkillName, in.SkillVersion, refs
}
