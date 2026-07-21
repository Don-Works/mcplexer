package brain

import (
	"context"
	"errors"
	"strings"
)

// ConflictDetail is the 409 payload the editor's ConflictReconciler consumes
// (DESIGN §3.6): the fresh on-disk record (so the GUI can diff YOURS vs ON
// DISK per field), the named writer that produced the on-disk change, and the
// raw .md for the prose-body escape hatch. It is returned wrapped in a
// ConflictDetailError so the handler can emit a structured 409 instead of the
// bare ErrConflict.
type ConflictDetail struct {
	// OnDiskTask / OnDiskMemory is the fresh parse of the canonical .md (only
	// the one matching the record's kind is populated).
	OnDiskTask   *TaskRecord   `json:"on_disk_task,omitempty"`
	OnDiskMemory *MemoryRecord `json:"on_disk_memory,omitempty"`
	// Writer is the best-effort attribution of the concurrent change, derived
	// from the on-disk record's source provenance ("agent sess_…", "VSCode",
	// "external edit"). Never names git to the non-technical user.
	Writer string `json:"writer"`
	// Path is the canonical .md path (the raw-file escape hatch target).
	Path string `json:"path"`
	// OnDiskHash is the current on-disk hash — the if_hash the GUI must submit
	// to win the next save after reconciling.
	OnDiskHash string `json:"on_disk_hash"`
}

// ConflictDetailError carries a ConflictDetail and unwraps to ErrConflict so
// existing errors.Is(err, ErrConflict) call sites keep working while the
// handler can pull the structured payload via errors.As.
type ConflictDetailError struct {
	Detail ConflictDetail
}

func (e *ConflictDetailError) Error() string {
	return "brain: record changed on disk since the editor loaded it (if_hash mismatch)"
}

// Unwrap lets errors.Is(err, ErrConflict) match a ConflictDetailError.
func (e *ConflictDetailError) Unwrap() error { return ErrConflict }

// checkTaskIfHash runs the if_hash CAS pre-check for a task PUT. When the
// submitted token no longer matches the current on-disk hash, it builds the
// fresh on-disk record + writer and returns a *ConflictDetailError. A blank
// token or a missing canonical file (a create) is never a conflict.
func (e *Editor) checkTaskIfHash(ctx context.Context, id, ifHash string) error {
	ifHash = strings.TrimSpace(ifHash)
	if ifHash == "" || e.ser == nil {
		return nil
	}
	path, _, _ := e.ser.IndexedSha(ctx, EntityKindTask, id)
	if path == "" {
		return nil // never indexed → no concurrent disk state to conflict with
	}
	cur := e.ser.CurrentDiskSha(path)
	if cur == "" || cur == ifHash {
		return nil
	}
	det := ConflictDetail{Path: path, OnDiskHash: cur, Writer: "external edit"}
	if raw, _ := e.ser.ReadRaw(path); raw != "" {
		if fm, body, err := ParseTask([]byte(raw)); err == nil {
			det.OnDiskTask = taskFrontmatterToRecord(fm, body)
			det.OnDiskTask.Raw = raw
			det.Writer = writerFrom(fm.Source)
		}
	}
	return &ConflictDetailError{Detail: det}
}

// checkMemoryIfHash is the memory counterpart of checkTaskIfHash.
func (e *Editor) checkMemoryIfHash(ctx context.Context, id, ifHash string) error {
	ifHash = strings.TrimSpace(ifHash)
	if ifHash == "" || e.ser == nil {
		return nil
	}
	path, _, _ := e.ser.IndexedSha(ctx, EntityKindMemory, id)
	if path == "" {
		return nil
	}
	cur := e.ser.CurrentDiskSha(path)
	if cur == "" || cur == ifHash {
		return nil
	}
	det := ConflictDetail{Path: path, OnDiskHash: cur, Writer: "external edit"}
	if raw, _ := e.ser.ReadRaw(path); raw != "" {
		if fm, body, err := ParseMemory([]byte(raw)); err == nil {
			det.OnDiskMemory = memoryFrontmatterToRecord(fm, body)
			det.OnDiskMemory.Raw = raw
			det.Writer = writerFrom(fm.Source)
		}
	}
	return &ConflictDetailError{Detail: det}
}

// checkPersonIfHash is the person counterpart of checkMemoryIfHash.
func (e *Editor) checkPersonIfHash(ctx context.Context, id, ifHash string) error {
	ifHash = strings.TrimSpace(ifHash)
	if ifHash == "" || e.ser == nil {
		return nil
	}
	path, _, _ := e.ser.IndexedSha(ctx, EntityKindPerson, id)
	if path == "" {
		return nil
	}
	cur := e.ser.CurrentDiskSha(path)
	if cur == "" || cur == ifHash {
		return nil
	}
	det := ConflictDetail{Path: path, OnDiskHash: cur, Writer: "external edit"}
	if raw, _ := e.ser.ReadRaw(path); raw != "" {
		if fm, _, err := ParsePerson([]byte(raw)); err == nil {
			det.Writer = writerFrom(fm.Source)
		}
	}
	return &ConflictDetailError{Detail: det}
}

// writerFrom names the concurrent writer from the on-disk record's source
// block. An agent/worker session reads as "agent sess_…"; a human/user edit
// (the common VSCode case, where the source is human or absent) reads as
// "external edit" — never names git to the non-technical user.
func writerFrom(src *SourceFM) string {
	if src == nil || src.Kind == "" {
		return "external edit"
	}
	switch src.Kind {
	case "agent", "worker", "system", "peer-import":
		if src.SessionID != "" {
			return src.Kind + " " + src.SessionID
		}
		return src.Kind
	default:
		return "external edit"
	}
}

// taskFrontmatterToRecord projects a parsed task frontmatter + body into the
// GUI record shape (used for the conflict reconciler's ON DISK column). It is
// a pure projection — no index lookups — so it reflects exactly the .md.
func taskFrontmatterToRecord(fm TaskFrontmatter, body string) *TaskRecord {
	tags := fm.Tags
	if tags == nil {
		tags = []string{}
	}
	rec := &TaskRecord{
		ID:          fm.ID,
		Workspace:   fm.Workspace,
		Title:       fm.Title,
		Status:      fm.Status,
		Priority:    fm.Priority,
		Tags:        tags,
		DueAt:       fm.DueAt,
		Pinned:      fm.Pinned,
		Description: body,
		Composes:    fm.Composes,
		CreatedAt:   fm.CreatedAt,
		UpdatedAt:   fm.UpdatedAt,
	}
	if fm.Source != nil {
		rec.Source = &SourceRecord{Kind: fm.Source.Kind, SessionID: fm.Source.SessionID}
	}
	if fm.Assignee != nil {
		rec.Assignee = &AssigneeRecord{OriginKind: fm.Assignee.OriginKind, SessionID: fm.Assignee.SessionID, PeerID: fm.Assignee.PeerID}
	}
	return rec
}

// memoryFrontmatterToRecord is the memory counterpart of
// taskFrontmatterToRecord.
func memoryFrontmatterToRecord(fm MemoryFrontmatter, body string) *MemoryRecord {
	tags := fm.Tags
	if tags == nil {
		tags = []string{}
	}
	rec := &MemoryRecord{
		ID:          fm.ID,
		Kind:        fm.Kind,
		Name:        fm.Name,
		Workspace:   fm.Workspace,
		Tags:        tags,
		Pinned:      fm.Pinned,
		Content:     body,
		Entities:    fm.Entities,
		TValidStart: fm.TValidStart,
		CreatedAt:   fm.CreatedAt,
		UpdatedAt:   fm.UpdatedAt,
	}
	if fm.Source != nil {
		rec.Source = &SourceRecord{Kind: fm.Source.Kind, SessionID: fm.Source.SessionID}
	}
	return rec
}

// AsConflictDetail extracts a *ConflictDetail from err, reporting whether one
// was present (the handler uses this to emit the structured 409 reconciler
// payload from an if_hash CAS mismatch).
func AsConflictDetail(err error) (*ConflictDetail, bool) {
	var cde *ConflictDetailError
	if errors.As(err, &cde) {
		return &cde.Detail, true
	}
	return nil, false
}
