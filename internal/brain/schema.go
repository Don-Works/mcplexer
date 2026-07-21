package brain

import "time"

// Schema version identifiers stamped into every entity's frontmatter.
// They let the indexer migrate frontmatter on read as the format evolves
// (docs/brain.md §11 "schema drift").
const (
	SchemaTaskV1      = "task/v1"
	SchemaMemoryV1    = "memory/v1"
	SchemaWorkspaceV1 = "workspace/v1"
	SchemaPersonV1    = "person/v1"
)

// Memory kinds.
const (
	MemoryKindNote = "note"
	MemoryKindFact = "fact"
)

// IMPORTANT: struct field declaration order IS the on-disk YAML key
// order. yaml.v3 encodes struct fields in declaration order, which gives
// us deterministic, diff-friendly frontmatter (docs/brain.md §5). Do NOT
// reorder fields without intending to reformat every existing file.

// AssigneeFM is the nested `assignee:` block on a task. All fields
// omitempty so an unassigned task emits an empty/absent block.
type AssigneeFM struct {
	OriginKind string `yaml:"origin_kind,omitempty"` // local|peer
	SessionID  string `yaml:"session_id,omitempty"`
	PeerID     string `yaml:"peer_id,omitempty"`
}

// IsZero reports whether the assignee block carries no information.
func (a AssigneeFM) IsZero() bool {
	return a.OriginKind == "" && a.SessionID == "" && a.PeerID == ""
}

// SourceFM is the nested `source:` block recording provenance.
type SourceFM struct {
	Kind       string `yaml:"kind,omitempty"` // agent|worker|user|peer-import|system|human
	SessionID  string `yaml:"session_id,omitempty"`
	ToolCallID string `yaml:"tool_call_id,omitempty"`
}

// IsZero reports whether the source block carries no information.
func (s SourceFM) IsZero() bool {
	return s.Kind == "" && s.SessionID == "" && s.ToolCallID == ""
}

// StatusEventFM is one append-only entry in a task's `status_history:`.
type StatusEventFM struct {
	At        time.Time `yaml:"at"`
	Evt       string    `yaml:"evt"`
	From      string    `yaml:"from,omitempty"`
	To        string    `yaml:"to,omitempty"`
	BySession string    `yaml:"by_session,omitempty"`
	ByPeer    string    `yaml:"by_peer,omitempty"`
	Note      string    `yaml:"note,omitempty"`
}

// TaskFrontmatter is the YAML frontmatter for a task `.md` file. Field
// order matches docs/brain.md §5.
type TaskFrontmatter struct {
	ID        string      `yaml:"id"`
	Schema    string      `yaml:"schema"`
	Workspace string      `yaml:"workspace"`
	Title     string      `yaml:"title"`
	Status    string      `yaml:"status"`
	Priority  string      `yaml:"priority,omitempty"`
	Tags      []string    `yaml:"tags,omitempty"`
	DueAt     *time.Time  `yaml:"due_at,omitempty"`
	Pinned    bool        `yaml:"pinned"`
	Assignee  *AssigneeFM `yaml:"assignee,omitempty"`
	Composes  []string    `yaml:"composes,omitempty"`
	// Meta carries every other task-meta key (composed_by, rollup_to,
	// work_context, worktree, and arbitrary user-supplied keys). composes
	// is promoted to its own field above; everything else round-trips
	// through here so the brain serialize<->parse path is lossless. Keys
	// are emitted in sorted order by yaml.v3 map encoding, matching the
	// DB's canonical sorted-key meta JSON (tasks.encodeMetaJSON).
	Meta          map[string]any  `yaml:"meta,omitempty"`
	Source        *SourceFM       `yaml:"source,omitempty"`
	StatusHistory []StatusEventFM `yaml:"status_history,omitempty"`
	CreatedAt     time.Time       `yaml:"created_at"`
	UpdatedAt     time.Time       `yaml:"updated_at"`
}

// EntityLinkFM is one `entities:` link on a memory record.
type EntityLinkFM struct {
	Kind string `yaml:"kind"`
	ID   string `yaml:"id"`
	Role string `yaml:"role,omitempty"`
}

// MemoryFrontmatter is the YAML frontmatter for a memory `.md` file. The
// bi-temporal fields (TValidStart/TValidEnd/InvalidatedBy) are only
// emitted for kind=fact; the serializer omits them for notes.
type MemoryFrontmatter struct {
	ID            string         `yaml:"id"`
	Schema        string         `yaml:"schema"`
	Kind          string         `yaml:"kind"` // note|fact
	Name          string         `yaml:"name"`
	Workspace     string         `yaml:"workspace,omitempty"` // omitted/"global" for global scope
	Tags          []string       `yaml:"tags,omitempty"`
	Pinned        bool           `yaml:"pinned"`
	Source        *SourceFM      `yaml:"source,omitempty"`
	Entities      []EntityLinkFM `yaml:"entities,omitempty"`
	TValidStart   *time.Time     `yaml:"t_valid_start,omitempty"`
	TValidEnd     *time.Time     `yaml:"t_valid_end,omitempty"`
	InvalidatedBy string         `yaml:"invalidated_by,omitempty"`
	CreatedAt     time.Time      `yaml:"created_at"`
	UpdatedAt     time.Time      `yaml:"updated_at"`
}

// PersonFrontmatter is the YAML frontmatter for a CRM person `.md` file.
// A Person is scoped to one workspace and the canonical file lives at
// <Dir>/workspaces/<workspace>/crm/people/<name>.md. Field declaration order
// IS the on-disk YAML key order (see the note above).
type PersonFrontmatter struct {
	ID        string         `yaml:"id"`
	Schema    string         `yaml:"schema"`
	Workspace string         `yaml:"workspace"`
	Name      string         `yaml:"name"`
	Email     string         `yaml:"email,omitempty"`
	Phone     string         `yaml:"phone,omitempty"`
	Company   string         `yaml:"company,omitempty"`
	Role      string         `yaml:"role,omitempty"`
	Tags      []string       `yaml:"tags,omitempty"`
	Pinned    bool           `yaml:"pinned"`
	Source    *SourceFM      `yaml:"source,omitempty"`
	Entities  []EntityLinkFM `yaml:"entities,omitempty"`
	CreatedAt time.Time      `yaml:"created_at"`
	UpdatedAt time.Time      `yaml:"updated_at"`
}

// WorkspaceFrontmatter is the YAML frontmatter for a `workspace.md` file.
// Parent is unused until M6 (hierarchy) but declared now to avoid a
// reformat-churn commit later.
type WorkspaceFrontmatter struct {
	ID            string    `yaml:"id"`
	Schema        string    `yaml:"schema"`
	Name          string    `yaml:"name"`
	RootPath      string    `yaml:"root_path,omitempty"`
	Parent        string    `yaml:"parent,omitempty"`
	Tags          []string  `yaml:"tags,omitempty"`
	DefaultPolicy string    `yaml:"default_policy,omitempty"`
	Source        string    `yaml:"source,omitempty"`
	CreatedAt     time.Time `yaml:"created_at"`
	UpdatedAt     time.Time `yaml:"updated_at"`
}
