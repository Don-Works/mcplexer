// templates.go (M3) — publish + install for the publishable Worker
// template surface. The admin Service grows two new capabilities:
// PublishAsTemplate (snapshot a Worker into the worker_templates table)
// and InstallFromTemplate (create a new Worker from a registry row +
// user-supplied parameters + secret bindings).
//
// Both helpers live behind a TemplatePublisher interface so the rest of
// the Service can stay decoupled from the concrete *workertemplates.Registry
// type — the daemon wires the real registry in via SetTemplatePublisher,
// and tests pass a fake.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// TemplatePublisher is the slim view of *workertemplates.Registry the
// admin Service needs for template publish + install. Defined here so
// admin doesn't depend on the concrete registry type at construction
// time — tests pass a fake.
type TemplatePublisher interface {
	Publish(ctx context.Context, opts workertemplates.PublishOptions) (*workertemplates.PublishResult, error)
	Get(ctx context.Context, scope store.SkillScope, name string, ref workertemplates.VersionRef) (*store.WorkerTemplateEntry, error)
}

// SetTemplatePublisher wires the registry into the Service. Optional —
// when unset, PublishAsTemplate + InstallFromTemplate return a
// descriptive error rather than panicking.
func (s *Service) SetTemplatePublisher(p TemplatePublisher) {
	s.templates = p
}

// PublishAsTemplateInput is the MCP / HTTP arg payload.
type PublishAsTemplateInput struct {
	WorkerID    string `json:"worker_id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// PublishAsTemplate snapshots a Worker into the worker_templates table.
// Returns the published WorkerTemplateEntry (caller wants the version +
// content_hash for UI confirmation).
func (s *Service) PublishAsTemplate(
	ctx context.Context, in PublishAsTemplateInput,
) (*store.WorkerTemplateEntry, error) {
	if s.templates == nil {
		return nil, errors.New("template publisher not wired")
	}
	if strings.TrimSpace(in.WorkerID) == "" {
		return nil, errors.New("worker_id required")
	}
	w, err := s.store.GetWorker(ctx, in.WorkerID)
	if err != nil {
		return nil, err
	}
	name := defaultStr(strings.TrimSpace(in.Name), templateNameFromWorker(w.Name))
	desc := defaultStr(in.Description, w.Description)
	tmpl := buildTemplateFromWorker(w, name, desc)
	body, err := workertemplates.Marshal(tmpl)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}
	res, err := s.templates.Publish(ctx, workertemplates.PublishOptions{
		Body:        string(body),
		Author:      "worker_publish",
		Description: desc,
	})
	if err != nil {
		return nil, err
	}
	return s.templates.Get(ctx,
		workertemplates.AdminScope(), res.Name,
		workertemplates.VersionRef{Version: res.Version})
}

// buildTemplateFromWorker copies the worker's runtime config into the
// template shape, extracting {placeholder} tokens from the prompt
// template + seeding a model_api_key secret slot.
func buildTemplateFromWorker(
	w *store.Worker, name, desc string,
) *workertemplates.WorkerTemplate {
	return &workertemplates.WorkerTemplate{
		Name:               name,
		Description:        desc,
		ModelProviderHint:  w.ModelProvider,
		ModelIDHint:        w.ModelID,
		SkillName:          w.SkillName,
		SkillVersion:       w.SkillVersion,
		PromptTemplate:     w.PromptTemplate,
		ScheduleSpecHint:   w.ScheduleSpec,
		ToolAllowlist:      decodeStringArray(w.ToolAllowlistJSON),
		CapabilityProfile:  decodeCapabilityProfile(w.CapabilityProfileJSON),
		OutputChannelsHint: decodeOutputChannels(w.OutputChannelsJSON),
		ExecModeHint:       w.ExecMode,
		ParameterSchema:    extractParameterSchema(w.PromptTemplate),
		SecretSlots:        []workertemplates.TemplateSecretSlot{modelAPIKeySlot(w.ModelProvider)},
	}
}

// placeholderRE matches {single_token} style template placeholders.
// Token is lower-case ASCII + underscore + digit, no nested braces.
var placeholderRE = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)

// extractParameterSchema scans the prompt template for {placeholder}
// tokens and emits one TemplateParameter per unique token in first-seen
// order. The label defaults to a human-cased version of the name.
func extractParameterSchema(prompt string) []workertemplates.TemplateParameter {
	seen := map[string]bool{}
	out := []workertemplates.TemplateParameter{}
	for _, m := range placeholderRE.FindAllStringSubmatch(prompt, -1) {
		token := m[1]
		if seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, workertemplates.TemplateParameter{
			Name:     token,
			Label:    labelize(token),
			Type:     "text",
			Required: true,
		})
	}
	return out
}

// labelize turns "subreddit_name" into "Subreddit Name".
func labelize(token string) string {
	parts := strings.Split(token, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// modelAPIKeySlot returns the always-on secret slot for the model
// provider API key. providerHint surfaces in the install modal so the
// user knows what kind of key to paste.
func modelAPIKeySlot(provider string) workertemplates.TemplateSecretSlot {
	return workertemplates.TemplateSecretSlot{
		Name:         "model_api_key",
		Description:  "API key for the LLM provider this Worker dispatches to.",
		ProviderHint: provider,
	}
}

// templateNameFromWorker normalises a Worker name into a skill-registry
// name (lowercase, hyphenated, alnum-only). Falls back to "worker-template"
// when the input is empty or normalises away.
func templateNameFromWorker(workerName string) string {
	s := strings.ToLower(strings.TrimSpace(workerName))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == ' ' || r == '_' || r == '-':
			if !prevHyphen && b.Len() > 0 {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "worker-template"
	}
	return out
}

// decodeStringArray unwraps a "[]" JSON array of strings to a []string,
// returning nil on any decode failure. Used to coerce
// worker.tool_allowlist_json into the template's typed field.
func decodeStringArray(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil
	}
	return arr
}

// decodeCapabilityProfile unwraps worker.capability_profile_json into the
// template's CapabilityProfile so a published worker carries its delegation
// scope forward. Empty / decode-failure returns nil (no scoping).
func decodeCapabilityProfile(raw string) *workertemplates.CapabilityProfile {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil
	}
	var p workertemplates.CapabilityProfile
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil
	}
	return &p
}

// decodeOutputChannels unwraps worker.output_channels_json into the
// template's typed hint slice. Empty / decode-failure returns nil.
func decodeOutputChannels(raw string) []workertemplates.OutputChannelHint {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var hints []workertemplates.OutputChannelHint
	if err := json.Unmarshal([]byte(raw), &hints); err != nil {
		return nil
	}
	return hints
}
