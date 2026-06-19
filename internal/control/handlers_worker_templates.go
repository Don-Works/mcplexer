// handlers_worker_templates.go — backend-side dispatch for the
// list_worker_templates MCP tool. Reads off workertemplates.Registry
// (the worker_templates table since migration 057). Publish + install
// paths route through the admin Service.
package control

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// templateSummary mirrors api.TemplateSummary verbatim — we keep it
// duplicated rather than importing the api package (which would create
// an import cycle: api → control → api).
type templateSummary struct {
	Name              string `json:"name"`
	Version           int    `json:"version"`
	Description       string `json:"description"`
	ModelProviderHint string `json:"model_provider_hint,omitempty"`
	ModelIDHint       string `json:"model_id_hint,omitempty"`
	ParameterCount    int    `json:"parameter_count"`
	SecretSlotCount   int    `json:"secret_slot_count"`
	PublishedAt       string `json:"published_at"`
	Author            string `json:"author,omitempty"`
}

// handleListWorkerTemplates serves mcplexer__list_worker_templates.
func (b *InternalBackend) handleListWorkerTemplates(
	ctx context.Context, args json.RawMessage,
) json.RawMessage {
	if b.workerTplReg == nil {
		return errorResult("worker template registry not wired")
	}
	var in struct {
		Search string `json:"search,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return errorResult("invalid params: " + err.Error())
		}
	}
	heads, err := b.workerTplReg.ListHeads(
		ctx, workertemplates.AdminScope(), in.Limit,
	)
	if err != nil {
		return errorResult(err.Error())
	}
	search := strings.ToLower(strings.TrimSpace(in.Search))
	out := make([]templateSummary, 0, len(heads))
	for _, e := range heads {
		if search != "" && !templateNameOrDescMatches(e, search) {
			continue
		}
		out = append(out, summariseTemplateRow(e))
	}
	return mustJSONResult(out)
}

// templateNameOrDescMatches is the substring filter for the search arg.
func templateNameOrDescMatches(e store.WorkerTemplateEntry, query string) bool {
	if strings.Contains(strings.ToLower(e.Name), query) {
		return true
	}
	return strings.Contains(strings.ToLower(e.Description), query)
}

// summariseTemplateRow decodes counts off the body. Failure degrades
// silently so a malformed row still appears in the list (operator
// can soft-delete via the registry tools).
func summariseTemplateRow(e store.WorkerTemplateEntry) templateSummary {
	row := templateSummary{
		Name:        e.Name,
		Version:     e.Version,
		Description: e.Description,
		PublishedAt: e.PublishedAt.Format("2006-01-02T15:04:05Z"),
		Author:      e.Author,
	}
	if tmpl, err := workertemplates.Unmarshal(e.Body); err == nil {
		row.ModelProviderHint = tmpl.ModelProviderHint
		row.ModelIDHint = tmpl.ModelIDHint
		row.ParameterCount = len(tmpl.ParameterSchema)
		row.SecretSlotCount = len(tmpl.SecretSlots)
	}
	return row
}
