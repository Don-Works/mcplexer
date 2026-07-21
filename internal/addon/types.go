package addon

// AddonFile represents a YAML addon definition file.
type AddonFile struct {
	ParentServer string    `yaml:"parent_server"` // ID of the downstream server to inherit auth from
	AuthScope    string    `yaml:"auth_scope"`    // optional: auth scope name for addon HTTP calls (overrides route's scope)
	Tools        []ToolDef `yaml:"tools"`
}

// ToolDef defines a single addon tool that maps to a REST API endpoint.
type ToolDef struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Method      string            `yaml:"method"` // GET, POST, PUT, PATCH, DELETE
	URL         string            `yaml:"url"`    // with {{param}} placeholders
	QueryParams map[string]string `yaml:"query_params,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	BodyMapping string            `yaml:"body_mapping,omitempty"` // "all_remaining" (default) or "none"
	InputSchema map[string]any    `yaml:"input_schema"`
	Annotations *Annotations      `yaml:"annotations,omitempty"`
}

// Annotations holds MCP tool annotation hints.
type Annotations struct {
	ReadOnlyHint    *bool `yaml:"readOnlyHint,omitempty"`
	DestructiveHint *bool `yaml:"destructiveHint,omitempty"`
	IdempotentHint  *bool `yaml:"idempotentHint,omitempty"`
}

// ResolvedTool is an addon tool resolved against its parent server.
type ResolvedTool struct {
	ToolDef
	ParentServerID string
	Namespace      string // e.g., "clickup"
	FullName       string // e.g., "clickup__get_chat_messages"
	AuthScopeID    string // resolved from auth_scope name; empty = use route's scope
}
