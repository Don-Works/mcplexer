// Package workertemplates owns the publishable-Worker-template shape and
// its registry surface. Before migration 057 these lived in skillregistry
// under payload_type='worker'; splitting them into their own package +
// table keeps the agent-facing skill catalog markdown-only and gives the
// worker-template surface room to grow its own fields (parameter schema,
// secret slots, hints) without renegotiating the SKILL.md frontmatter.
//
// Storage is store.WorkerTemplateStore (worker_templates table). The
// JSON shape stored in Body is the WorkerTemplate type defined below.
package workertemplates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/toolgate"
)

// CapabilityProfile re-exports toolgate.CapabilityProfile so worker
// templates can carry a delegation capability scope without callers needing
// to import toolgate directly.
type CapabilityProfile = toolgate.CapabilityProfile

// WorkerTemplate is the JSON-marshalled shape of a publishable Worker.
// Stored verbatim in worker_templates.body.
//
// Every Hint field is a recommendation the installer can override:
// `model_*_hint` proposes a model but the install modal lets the user
// pick another, `schedule_spec_hint` pre-fills the cron field, etc.
// `tool_allowlist` is treated as a strong default (the install modal
// pre-checks those tools) but is not mandatory.
type WorkerTemplate struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	ModelProviderHint string `json:"model_provider_hint,omitempty"`
	ModelIDHint       string `json:"model_id_hint,omitempty"`

	SkillName    string `json:"skill_name,omitempty"`
	SkillVersion string `json:"skill_version,omitempty"`

	PromptTemplate   string   `json:"prompt_template"`
	ScheduleSpecHint string   `json:"schedule_spec_hint,omitempty"`
	ToolAllowlist    []string `json:"tool_allowlist,omitempty"`
	// CapabilityPreset / CapabilityProfile carry an optional delegation
	// capability scope for workers installed from this template. Empty /
	// nil means "no scoping" (today's behavior). Stored as marshalled JSON
	// on the worker's capability_profile_json column at install time.
	CapabilityPreset   string               `json:"capability_preset,omitempty"`
	CapabilityProfile  *CapabilityProfile   `json:"capability_profile,omitempty"`
	OutputChannelsHint []OutputChannelHint  `json:"output_channels_hint,omitempty"`
	ExecModeHint       string               `json:"exec_mode_hint,omitempty"`
	ParameterSchema    []TemplateParameter  `json:"parameter_schema,omitempty"`
	SecretSlots        []TemplateSecretSlot `json:"secret_slots,omitempty"`
}

// OutputChannelHint mirrors the shape the runner's output dispatcher
// expects in worker.output_channels_json. Carries `type` + `priority` +
// `priority_on_fail` plus the small union of channel-specific fields
// the runner consumes (webhook URL, slack prefix/channel, etc). The
// dashboard renders them verbatim — channels the user must customise
// (e.g. slack_webhook URL placeholders) install pre-populated, so the
// user only edits, never builds from scratch.
//
// PriorityOnFail (optional) overrides Priority when the run terminated
// in a non-success state (failure, cap_exceeded, awaiting_approval,
// rejected). Unset → the static Priority value applies to every status.
// Lets a template declare e.g. priority=low on a green night and
// priority_on_fail=high on a red one, without two channel entries.
type OutputChannelHint struct {
	Type           string `json:"type"`
	Priority       string `json:"priority,omitempty"`
	PriorityOnFail string `json:"priority_on_fail,omitempty"`
	Tags           string `json:"tags,omitempty"`
	NotifyUser     bool   `json:"notify_user,omitempty"`
	ReplyToTrigger bool   `json:"reply_to_trigger,omitempty"`
	ToPeer         string `json:"to_peer,omitempty"`
	BroadcastPeers bool   `json:"broadcast_peers,omitempty"`
	// Channel-specific fields. Each is consumed by a subset of types
	// and ignored by the rest — the runner's outputChannel struct
	// already takes the same wide-union shape, so the install path
	// is a straight passthrough.
	URL     string `json:"url,omitempty"`     // webhook / slack_webhook
	Channel string `json:"channel,omitempty"` // slack #channel hint
	Prefix  string `json:"prefix,omitempty"`  // slack / clickup / github prefix
	Path    string `json:"path,omitempty"`    // file output destination
	Mode    string `json:"mode,omitempty"`    // "append" | "overwrite"
}

// TemplateParameter is one user-fillable slot in the template's
// prompt_template. The installer presents an input per row; the result
// goes into the new Worker's parameters_json under Name.
type TemplateParameter struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Type        string `json:"type,omitempty"` // text|textarea|number — UI hint
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// TemplateSecretSlot names a credential the installed Worker needs.
// On install, the user picks an existing AuthScope OR creates a new
// one carrying the named secret key. The slot name is opaque to the
// runner — it's a UI label the install modal renders.
type TemplateSecretSlot struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	ProviderHint string `json:"provider_hint,omitempty"`
}

// Marshal canonicalises the template to a deterministic JSON byte
// slice. Used by Publish so content_hash dedup behaves like markdown-
// skill dedup — re-publishing identical content returns the existing
// version.
func Marshal(t *WorkerTemplate) ([]byte, error) {
	if t == nil {
		return nil, errors.New("workertemplates.Marshal: nil template")
	}
	if strings.TrimSpace(t.Name) == "" {
		return nil, errors.New("workertemplates.Marshal: name required")
	}
	if strings.TrimSpace(t.PromptTemplate) == "" {
		return nil, errors.New("workertemplates.Marshal: prompt_template required")
	}
	return json.Marshal(t)
}

// Unmarshal reads a body string (the registry stores Body as a string —
// caller passes that through verbatim).
func Unmarshal(body string) (*WorkerTemplate, error) {
	var t WorkerTemplate
	if err := json.Unmarshal([]byte(body), &t); err != nil {
		return nil, fmt.Errorf("unmarshal worker template: %w", err)
	}
	if t.Name == "" {
		return nil, errors.New("worker template missing name")
	}
	if t.PromptTemplate == "" {
		return nil, errors.New("worker template missing prompt_template")
	}
	return &t, nil
}

// ContentHash returns the sha256 hex of body. The store computes its
// own hash on Publish; this helper is exposed so callers (publisher,
// importer) can dedup-check before paying a DB round-trip.
func ContentHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
