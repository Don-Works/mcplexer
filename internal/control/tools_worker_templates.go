package control

import (
	"github.com/don-works/mcplexer/internal/gateway"
)

// workerPublishAsTemplateToolDef declares
// mcplexer__publish_worker_as_template (M3). The handler reads the
// Worker by id, snapshots its config into the skill registry as a
// payload_type=worker entry, and returns the new
// SkillRegistryEntry (with version + content_hash).
func workerPublishAsTemplateToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "publish_worker_as_template",
		Description: "Publish a configured Worker as a re-installable template in the skill registry. The template captures the Worker's prompt template (with {placeholder} tokens auto-extracted into a parameter schema), model + skill hints, schedule, tool allowlist, output channels, exec mode, and a model_api_key secret slot. Other agents — including on paired peers — can install it via mcplexer__install_worker_template, supplying their own parameter values + secret bindings. The template is versioned linearly by the registry; re-publishing identical content dedups to the existing version.",
		InputSchema: schema(props{
			"worker_id":   propStr("Worker ID (wkr-...) to snapshot."),
			"name":        propStr("Optional template name (lower-case, hyphenated). Defaults to a normalised version of the Worker name."),
			"description": propStr("Optional description shown on the template card. Defaults to the Worker's description."),
		}, []string{"worker_id"}),
	}
}

// workerInstallTemplateToolDef declares mcplexer__install_worker_template
// (M3). The handler resolves the template, validates required parameters,
// merges defaults, and creates a new Worker with source_template_*
// columns pointing back at the registry row.
func workerInstallTemplateToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "install_worker_template",
		Description: "Install a Worker from a registry template (payload_type=worker entry). Required: template_name, worker_name, workspace_id, secret_scope_id (existing AuthScope carrying the model API key). Optional: template_version (0=latest), parameters (per-parameter value map), schedule_spec / exec_mode (overrides the template hint), enabled. Required parameters that aren't supplied produce a validation error. Returns the newly-created Worker.",
		InputSchema: schema(props{
			"template_name":    propStr("Registry template name."),
			"template_version": propInt("Specific version; 0 = latest."),
			"worker_name":      propStr("Name for the newly-created Worker — must be unique within the workspace."),
			"workspace_id":     propStr("Workspace the Worker belongs to."),
			"secret_scope_id":  propStr("AuthScope id supplying the model API key."),
			"parameters": map[string]any{
				"type":                 "object",
				"description":          "Per-parameter values keyed by parameter_schema.name. Defaults from the template fill missing optional params.",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"schedule_spec": propStr("Optional schedule override; falls back to schedule_spec_hint."),
			"exec_mode":     propStr("Optional exec mode override (propose|autonomous); falls back to exec_mode_hint."),
			"enabled":       map[string]any{"type": "boolean", "description": "Initial enabled state. Default true."},
		}, []string{"template_name", "worker_name", "workspace_id", "secret_scope_id"}),
	}
}

// workerListTemplatesToolDef declares mcplexer__list_worker_templates
// (M3). Returns one head row per template name visible in the admin
// scope. Optional search filters on name + description.
func workerListTemplatesToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "list_worker_templates",
		Description: "List Worker templates from the skill registry (payload_type=worker rows). Returns slim cards — name, version, description, model hint, parameter / secret-slot counts. Use mcpx__skill_get with payload_type-aware callers, or mcplexer__install_worker_template, to fetch the full body.",
		InputSchema: schema(props{
			"search": propStr("Optional case-insensitive substring search over name + description."),
			"limit":  propInt("Max rows (default unbounded)."),
		}, nil),
	}
}
