package store

import (
	"encoding/json"
	"time"
)

// Person source kinds — populate every row so a poisoned source can be
// forensically purged with one DELETE WHERE source_session_id = ?. Mirrors
// the memory source vocabulary.
const (
	PersonSourceAgent = "agent" // an MCP client wrote it
	PersonSourceUser  = "user"  // dashboard / human write
)

// PersonDefaultWorkspaceID is the restrictive default CRM workspace for
// person records. Legacy/global people are migrated here instead of remaining
// visible everywhere.
const PersonDefaultWorkspaceID = "crm"

// PersonEntry is one row in the crm_person table (migration 094).
//
// A Person is a workspace-scoped CRM contact record — the markdown-canonical
// brain stores it at <Dir>/workspaces/<workspace>/crm/people/<name>.md. Name
// is the unique human key only within a workspace. Unlike memory facts there
// is no bi-temporal chain: a Person is an atomic record whose human-editable
// fields are reconciled in place on every index pass. Provenance (Source*) is
// stamped once at create.
type PersonEntry struct {
	ID               string          `json:"id"`
	WorkspaceID      string          `json:"workspace_id"`
	Name             string          `json:"name"`
	Email            string          `json:"email,omitempty"`
	Phone            string          `json:"phone,omitempty"`
	Company          string          `json:"company,omitempty"`
	Role             string          `json:"role,omitempty"` // job title
	TagsJSON         json.RawMessage `json:"tags,omitempty"`
	Notes            string          `json:"notes,omitempty"` // markdown body
	SourceKind       string          `json:"source_kind"`
	SourceSessionID  string          `json:"source_session_id,omitempty"`
	SourceToolCallID string          `json:"source_tool_call_id,omitempty"`
	Pinned           bool            `json:"pinned,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
}

// PersonFilter narrows queries against the crm_person table. Tags require
// every listed tag to be present (AND). Entities narrow to people carrying
// every listed link.
type PersonFilter struct {
	WorkspaceID    string      // exact workspace; empty = all
	Name           string      // exact name match; empty = all
	Company        string      // exact company match; empty = all
	Tags           []string    // every tag must be present (AND)
	Entities       []EntityRef // AND: every link must exist (role optional)
	IncludeDeleted bool        // include soft-deleted rows
	Limit          int         // 0 = caller-implementation cap
	Offset         int
}

// PersonEntityRow is one link in the person_entities join table — the
// "what is this person linked to" axis (org/deal/task/peer/...).
type PersonEntityRow struct {
	ID         string    `json:"id"`
	PersonID   string    `json:"person_id"`
	EntityKind string    `json:"entity_kind"`
	EntityID   string    `json:"entity_id"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by,omitempty"`
}

// PersonHit is one FTS5 search result. Score is the BM25 rank (lower=better).
type PersonHit struct {
	Entry  PersonEntry `json:"entry"`
	Score  float64     `json:"score"`
	Source string      `json:"source"` // "fts" | "list"
}
