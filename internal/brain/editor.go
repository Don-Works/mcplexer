package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrConflict is returned by the Editor when an outbound write hit the
// hash-CAS gate: the on-disk .md file diverged from the last-indexed sha
// (a human or VSCode edited it since), so the GUI write was diverted to a
// .conflict sidecar rather than clobbering. The HTTP handler maps this to
// a 409 so the editor can surface the conflict inline.
var ErrConflict = errors.New("brain: record write conflicted with a concurrent edit")

// Editor is the canonical write path behind the dashboard's Notion-like
// record editor (M7, Appendix C.3). It is deliberately thin: every GUI
// save funnels through the SAME outbound Serializer the M1/M4 dual-write
// engine uses, so hash-CAS + atomic temp+rename + self-write suppression +
// autocommit all apply identically. The GUI is just another caller of the
// single outbound path — a record edited in the GUI is byte-identical to
// one edited by an agent tool or in VSCode.
//
// The flow for every mutation is: build the store row from submitted
// frontmatter fields + body -> persist via the Store (so FTS5 triggers and
// bi-temporal logic fire unchanged) -> Serializer.WriteTask/WriteMemory
// (writes the file, hash-CAS guarded, fires the autocommit notify). A CAS
// conflict surfaces as ErrConflict; a validation failure as *ValidationError.
type Editor struct {
	store store.Store
	ser   *Serializer
	// reEmbed is an optional hook fired after a memory row's content is
	// rewritten in place (store.UpdateMemory), so the memory Service can
	// rebuild the vector that UpdateMemory drops on a content change. Wired
	// by the daemon from memory.Service.ReEmbedAfterUpdate; nil = no-op
	// (FTS-only after edit, the pre-wave-B2 behaviour).
	reEmbed ReEmbedHook
}

// ReEmbedHook is the optional sink the brain calls after it rewrites a
// memory row's content via store.UpdateMemory, so the memory Service can
// schedule an async re-embed of the new content. Best-effort + nil-safe:
// the brain never fails an edit on a re-embed scheduling problem.
type ReEmbedHook func(ctx context.Context, e *store.MemoryEntry)

// fireReEmbedIfContentChanged fires hook iff the in-place update actually
// changed the body. store.UpdateMemory drops the stale memories_vec row only
// on a content change, so a metadata-only edit leaves the vector valid and the
// hook must NOT fire (re-embedding would be wasted work). Shared by the Editor
// (GUI/agent edits) and Indexer (file-watch edits) so both paths apply one
// content-change rule. nil hook = no-op.
func fireReEmbedIfContentChanged(
	ctx context.Context, hook ReEmbedHook, prior, updated *store.MemoryEntry,
) {
	if hook == nil || prior == nil || updated == nil {
		return
	}
	if prior.Content == updated.Content {
		return
	}
	hook(ctx, updated)
}

// NewEditor wires an Editor from the live store + the already-constructed
// outbound Serializer (the same instance set as the tasks/memory BrainHook
// in serve.go). nil ser disables writes (reads still work) — the handler
// degrades to read-only when the brain serializer is unavailable.
func NewEditor(s store.Store, ser *Serializer) *Editor {
	return &Editor{store: s, ser: ser}
}

// SetReEmbedHook installs the re-embed-on-edit hook post-construction. Nil is
// safe (the default) — a content edit then stays FTS-only until something
// else re-embeds. Wire it from the memory Service in the daemon.
func (e *Editor) SetReEmbedHook(h ReEmbedHook) {
	if e == nil {
		return
	}
	e.reEmbed = h
}

// AssigneeRecord is the GUI-facing projection of a task assignee. All fields
// are read-only in the editor (the server owns lease + assignment); they are
// shown as mono provenance, not edited.
type AssigneeRecord struct {
	OriginKind string `json:"origin_kind,omitempty"` // local|peer
	SessionID  string `json:"session_id,omitempty"`
	PeerID     string `json:"peer_id,omitempty"`
}

// SourceRecord is the GUI-facing projection of a record's provenance block.
type SourceRecord struct {
	Kind      string `json:"kind,omitempty"` // agent|worker|user|peer-import|system|human
	SessionID string `json:"session_id,omitempty"`
}

// TaskRecord is the GUI-facing shape of an editable task: the structured
// frontmatter fields the form binds to plus the prose description body.
// It is a flattened, JSON-friendly projection — the handler marshals it
// straight to the React editor and back.
//
// The read-side flags (LiveLease, ValidationError) and provenance fields
// (Source, Composes, OnDiskHash, Raw) are populated by the browse/detail
// reads; a write submits only the human-editable fields and the server-owned
// ones are preserved from the existing row.
type TaskRecord struct {
	ID          string          `json:"id"`
	Workspace   string          `json:"workspace"`
	Title       string          `json:"title"`
	Status      string          `json:"status"`
	Priority    string          `json:"priority,omitempty"`
	Tags        []string        `json:"tags"`
	DueAt       *time.Time      `json:"due_at,omitempty"`
	Pinned      bool            `json:"pinned"`
	Description string          `json:"description"`
	Assignee    *AssigneeRecord `json:"assignee,omitempty"`
	Composes    []string        `json:"composes,omitempty"`
	Source      *SourceRecord   `json:"source,omitempty"`
	Path        string          `json:"path,omitempty"`
	IndexSource string          `json:"index_source,omitempty"` // central|repo
	CreatedAt   time.Time       `json:"created_at,omitempty"`
	UpdatedAt   time.Time       `json:"updated_at,omitempty"`
	// LiveLease is true when the task currently holds an unexpired lease (an
	// agent is touching it right now) — drives the browser's shimmer row.
	LiveLease bool `json:"live_lease"`
	// ValidationError, when non-empty, is the human-readable reason this
	// record failed to index (the agent cannot see it) — drives the pulse-slow
	// marker + inline banner.
	ValidationError string `json:"validation_error,omitempty"`
	// ValidationField names the offending frontmatter field for an inline fix.
	ValidationField string `json:"validation_field,omitempty"`
	// OnDiskHash is the last-indexed content sha of the canonical .md, supplied
	// to the editor as the if_hash CAS token for the next save.
	OnDiskHash string `json:"on_disk_hash,omitempty"`
	// Raw is the verbatim serialized .md (the FileTruthDisclosure "this is
	// exactly what your agent reads"). Populated only by the detail read.
	Raw string `json:"raw,omitempty"`
	// IfHash is the CAS token the editor submits on a PUT: the on-disk hash
	// the user's edit was based on. When non-empty and it no longer matches
	// the current on-disk hash, the save aborts with a 409 carrying the
	// fresh on-disk record + writer for the reconciler. Empty skips the
	// pre-check (a create, or a client that opted out of CAS).
	IfHash string `json:"if_hash,omitempty"`
}

// MemoryRecord is the GUI-facing shape of an editable memory.
type MemoryRecord struct {
	ID              string         `json:"id"`
	Kind            string         `json:"kind"` // note|fact
	Name            string         `json:"name"`
	Workspace       string         `json:"workspace,omitempty"`
	Tags            []string       `json:"tags"`
	Pinned          bool           `json:"pinned"`
	Content         string         `json:"content"`
	Entities        []EntityLinkFM `json:"entities,omitempty"`
	TValidStart     *time.Time     `json:"t_valid_start,omitempty"`
	Source          *SourceRecord  `json:"source,omitempty"`
	Path            string         `json:"path,omitempty"`
	IndexSource     string         `json:"index_source,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
	UpdatedAt       time.Time      `json:"updated_at,omitempty"`
	ValidationError string         `json:"validation_error,omitempty"`
	ValidationField string         `json:"validation_field,omitempty"`
	OnDiskHash      string         `json:"on_disk_hash,omitempty"`
	Raw             string         `json:"raw,omitempty"`
	IfHash          string         `json:"if_hash,omitempty"`
}

// PersonRecord is the GUI/agent-facing shape of an editable CRM person: the
// structured frontmatter fields plus the markdown notes body. A flattened,
// JSON-friendly projection. Server-owned provenance (Source) is read-only.
type PersonRecord struct {
	ID              string         `json:"id"`
	Workspace       string         `json:"workspace"`
	Name            string         `json:"name"`
	Email           string         `json:"email,omitempty"`
	Phone           string         `json:"phone,omitempty"`
	Company         string         `json:"company,omitempty"`
	Role            string         `json:"role,omitempty"`
	Tags            []string       `json:"tags"`
	Pinned          bool           `json:"pinned"`
	Notes           string         `json:"notes"`
	Entities        []EntityLinkFM `json:"entities,omitempty"`
	Source          *SourceRecord  `json:"source,omitempty"`
	Path            string         `json:"path,omitempty"`
	IndexSource     string         `json:"index_source,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
	UpdatedAt       time.Time      `json:"updated_at,omitempty"`
	ValidationError string         `json:"validation_error,omitempty"`
	ValidationField string         `json:"validation_field,omitempty"`
	OnDiskHash      string         `json:"on_disk_hash,omitempty"`
	Raw             string         `json:"raw,omitempty"`
	IfHash          string         `json:"if_hash,omitempty"`
}

// newID mints a fresh lowercase ULID for a created record. Matches the
// store's own default-when-empty behaviour but lets the Editor learn the
// id before the write so the file path and the row agree.
func newID() string {
	return strings.ToLower(ulid.Make().String())
}

// SaveTask persists a task record (create when ID is empty, update
// otherwise) then serializes it to its .md file via the shared outbound
// path. It returns the saved record (with the assigned id + canonical
// path). A CAS conflict is detected by a preflight check that runs BEFORE
// the DB row is written, so a stale edit aborts without diverging the index
// from the canonical .md; the GUI's intended content is diverted to a
// .conflict sidecar for the user to reconcile.
func (e *Editor) SaveTask(ctx context.Context, rec TaskRecord, vocab []string) (*TaskRecord, error) {
	if e.ser == nil {
		return nil, errors.New("brain: editor has no serializer (brain disabled)")
	}
	t, err := e.taskRecordToRow(ctx, rec)
	if err != nil {
		return nil, err
	}
	if err := validateTaskRow(t, vocab); err != nil {
		return nil, err
	}

	// if_hash CAS pre-check: when the editor submitted the on-disk hash its
	// edit was based on and the canonical .md has since diverged, abort with a
	// structured 409 carrying the fresh on-disk record + writer for the
	// field-level reconciler (DESIGN §3.6). This fires BEFORE any DB/file
	// mutation so a stale edit never lands.
	if cErr := e.checkTaskIfHash(ctx, t.ID, rec.IfHash); cErr != nil {
		return nil, cErr
	}

	// CAS pre-check BEFORE persisting the DB row: if the canonical .md
	// diverged from the last-indexed sha (a human/VSCode edited it since),
	// abort the save, divert the GUI's intended content to a .conflict
	// sidecar, and return ErrConflict WITHOUT writing the DB row. This keeps
	// the index from reflecting an edit the canonical file never received —
	// the next reindex would otherwise silently revert the row to disk and
	// the user's save would be lost (the files-are-canonical invariant).
	path, conflict, err := e.ser.PreflightTaskConflict(ctx, t)
	if err != nil {
		return nil, err
	}
	if conflict {
		data, rErr := e.ser.RenderTask(ctx, t)
		if rErr != nil {
			return nil, rErr
		}
		if cErr := e.ser.RecordConflict(ctx, path, data, EntityKindTask); cErr != nil {
			return nil, cErr
		}
		return nil, ErrConflict
	}

	if err := e.persistTask(ctx, t); err != nil {
		return nil, err
	}

	writeErr := e.ser.WriteTask(ctx, t)
	saved := e.taskRowToRecordValue(ctx, t)
	if writeErr != nil {
		return saved, writeErr
	}
	if e.conflictRecorded(ctx, EntityKindTask, t.ID) {
		return saved, ErrConflict
	}
	return saved, nil
}

// taskRecordToRow builds the store.Task from the submitted record. On
// update it loads the existing row first so server-owned fields
// (status_history, source, assignee, meta) are preserved — the GUI only
// owns the human-editable frontmatter + body.
func (e *Editor) taskRecordToRow(ctx context.Context, rec TaskRecord) (*store.Task, error) {
	var base *store.Task
	if rec.ID != "" {
		existing, err := e.store.GetTask(ctx, rec.ID)
		if err != nil {
			return nil, fmt.Errorf("brain editor: load task %s: %w", rec.ID, err)
		}
		base = existing
	} else {
		now := time.Now().UTC()
		base = &store.Task{ID: newID(), CreatedAt: now, SourceKind: "user"}
	}

	base.WorkspaceID = strings.TrimSpace(rec.Workspace)
	base.Title = strings.TrimSpace(rec.Title)
	base.Status = strings.TrimSpace(rec.Status)
	base.Priority = strings.TrimSpace(rec.Priority)
	base.DueAt = rec.DueAt
	base.Pinned = rec.Pinned
	base.Description = rec.Description
	base.UpdatedAt = time.Now().UTC()

	tags, err := encodeStringSlice(normalizeTags(rec.Tags))
	if err != nil {
		return nil, fmt.Errorf("brain editor: encode tags: %w", err)
	}
	base.TagsJSON = tags
	return base, nil
}

// persistTask routes to CreateTask vs UpdateTask depending on whether the
// row already exists in the index. Both fire the FTS5 triggers.
func (e *Editor) persistTask(ctx context.Context, t *store.Task) error {
	_, err := e.store.GetTask(ctx, t.ID)
	switch {
	case err == nil:
		if uerr := e.store.UpdateTask(ctx, t); uerr != nil {
			return fmt.Errorf("brain editor: update task: %w", uerr)
		}
	case errors.Is(err, store.ErrNotFound):
		if cerr := e.store.CreateTask(ctx, t); cerr != nil {
			return fmt.Errorf("brain editor: create task: %w", cerr)
		}
	default:
		return fmt.Errorf("brain editor: probe task: %w", err)
	}
	return nil
}

// conflictRecorded reports whether the just-attempted outbound write was
// diverted to a .conflict sidecar (the serializer records a brain_errors
// row keyed to the entity's path with field "_file" on CAS divergence).
func (e *Editor) conflictRecorded(ctx context.Context, kind, id string) bool {
	f, err := e.ser.findIndexByEntity(ctx, kind, id)
	if err != nil || f == nil {
		return false
	}
	errs, err := e.store.ListBrainErrors(ctx)
	if err != nil {
		return false
	}
	for _, be := range errs {
		if be.Path == f.Path && be.Field == "_file" {
			return true
		}
	}
	return false
}

// normalizeTags trims, drops empties, and de-dupes a tag slice so the
// stored tags_json is clean regardless of GUI input.
func normalizeTags(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
