package brain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// appendMemoryDrift checks one indexed memory file (note or fact) against
// its DB row. It diffs the human-editable fields the file owns — name, kind,
// content, pinned, workspace, the fact's bi-temporal window
// (t_valid_start/t_valid_end/invalidated_by), and the entity-link set — so
// the parity gate is genuine for memories, not just tasks.
func appendMemoryDrift(ctx context.Context, s store.Store, f store.IndexFile, drifts []Drift) []Drift {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return append(drifts, Drift{Kind: DriftMissingFile, Path: f.Path, EntityID: f.EntityID})
	}
	fm, body, err := ParseMemory(data)
	if err != nil {
		return append(drifts, Drift{Kind: DriftParseError, Path: f.Path, EntityID: f.EntityID, Detail: err.Error()})
	}
	derived, refs, err := fm.ToMemory(body)
	if err != nil {
		return append(drifts, Drift{Kind: DriftParseError, Path: f.Path, EntityID: f.EntityID, Detail: err.Error()})
	}

	row, err := s.GetMemory(ctx, derived.ID)
	if errors.Is(err, store.ErrNotFound) {
		return append(drifts, Drift{Kind: DriftMissingRow, Path: f.Path, EntityID: derived.ID})
	}
	if err != nil {
		return append(drifts, Drift{Kind: DriftMissingRow, Path: f.Path, EntityID: derived.ID, Detail: err.Error()})
	}
	if d := memoryContentDiff(derived, row); d != "" {
		return append(drifts, Drift{Kind: DriftContentMismatch, Path: f.Path, EntityID: derived.ID, Detail: d})
	}
	if d := memoryEntityDiff(ctx, s, derived.ID, refs); d != "" {
		return append(drifts, Drift{Kind: DriftContentMismatch, Path: f.Path, EntityID: derived.ID, Detail: d})
	}
	return drifts
}

// memoryContentDiff compares the file-owned scalar fields between the
// re-derived memory and the live row. Returns "" when they agree.
func memoryContentDiff(derived, row *store.MemoryEntry) string {
	if derived.Name != row.Name {
		return fmt.Sprintf("name: file=%q db=%q", derived.Name, row.Name)
	}
	if normMemKind(derived.Kind) != normMemKind(row.Kind) {
		return fmt.Sprintf("kind: file=%q db=%q", derived.Kind, row.Kind)
	}
	if strings.TrimRight(derived.Content, "\n") != strings.TrimRight(row.Content, "\n") {
		return "content"
	}
	if derived.Pinned != row.Pinned {
		return fmt.Sprintf("pinned: file=%v db=%v", derived.Pinned, row.Pinned)
	}
	if ptrOrEmpty(derived.WorkspaceID) != ptrOrEmpty(row.WorkspaceID) {
		return fmt.Sprintf("workspace: file=%q db=%q", ptrOrEmpty(derived.WorkspaceID), ptrOrEmpty(row.WorkspaceID))
	}
	if d := factTemporalDiff(derived, row); d != "" {
		return d
	}
	return ""
}

// factTemporalDiff compares the bi-temporal window for facts. Notes carry no
// temporal window, so this is a no-op for them.
func factTemporalDiff(derived, row *store.MemoryEntry) string {
	if normMemKind(derived.Kind) != MemoryKindFact {
		return ""
	}
	if !derived.TValidStart.IsZero() && !derived.TValidStart.Equal(row.TValidStart) {
		return fmt.Sprintf("t_valid_start: file=%s db=%s",
			derived.TValidStart.UTC(), row.TValidStart.UTC())
	}
	if validEndUnix(derived) != validEndUnix(row) {
		return fmt.Sprintf("t_valid_end: file=%d db=%d", validEndUnix(derived), validEndUnix(row))
	}
	if derived.InvalidatedBy != row.InvalidatedBy {
		return fmt.Sprintf("invalidated_by: file=%q db=%q", derived.InvalidatedBy, row.InvalidatedBy)
	}
	return ""
}

// validEndUnix returns the t_valid_end as a unix second, or 0 when nil (the
// "currently valid" sentinel), so two nil ends compare equal.
func validEndUnix(m *store.MemoryEntry) int64 {
	if m.TValidEnd == nil {
		return 0
	}
	return m.TValidEnd.Unix()
}

// normMemKind defaults an empty kind to note (the store's default), matching
// how the row is persisted, so a kind-omitted file does not flag as drift.
func normMemKind(k string) string {
	if k == "" {
		return MemoryKindNote
	}
	return k
}

// memoryEntityDiff diffs the file's entity-link set against the DB join rows.
// The file is canonical for `entities:`, so any mismatch (extra/missing link)
// is drift. Comparison is canonicalised (kind+id lower-cased, role defaulted)
// to match how the store persists links.
func memoryEntityDiff(ctx context.Context, s store.Store, memoryID string, refs []store.EntityRef) string {
	want := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		if r.Kind == "" || r.ID == "" {
			continue
		}
		want[entityKey(normalizeEntityRef(r))] = struct{}{}
	}
	rows, err := s.ListMemoryEntities(ctx, memoryID)
	if err != nil {
		return "entities: " + err.Error()
	}
	have := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		ref := store.EntityRef{Kind: row.EntityKind, ID: row.EntityID, Role: row.Role}
		have[entityKey(normalizeEntityRef(ref))] = struct{}{}
	}
	var missing, extra []string
	for k := range want {
		if _, ok := have[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range have {
		if _, ok := want[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Sprintf("entities: file_only=%v db_only=%v", missing, extra)
}

// appendWorkspaceDrift checks one indexed workspace.md file against its DB
// row, diffing the file-owned fields (name, root_path, parent, default
// policy). A missing row is drift; operational fields are not compared.
func appendWorkspaceDrift(ctx context.Context, s store.Store, f store.IndexFile, drifts []Drift) []Drift {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return append(drifts, Drift{Kind: DriftMissingFile, Path: f.Path, EntityID: f.EntityID})
	}
	fm, _, err := ParseWorkspace(data)
	if err != nil {
		return append(drifts, Drift{Kind: DriftParseError, Path: f.Path, EntityID: f.EntityID, Detail: err.Error()})
	}
	derived, err := fm.ToWorkspace()
	if err != nil {
		return append(drifts, Drift{Kind: DriftParseError, Path: f.Path, EntityID: f.EntityID, Detail: err.Error()})
	}

	row, err := s.GetWorkspace(ctx, derived.ID)
	if errors.Is(err, store.ErrNotFound) {
		return append(drifts, Drift{Kind: DriftMissingRow, Path: f.Path, EntityID: derived.ID})
	}
	if err != nil {
		return append(drifts, Drift{Kind: DriftMissingRow, Path: f.Path, EntityID: derived.ID, Detail: err.Error()})
	}
	if d := workspaceContentDiff(derived, row); d != "" {
		return append(drifts, Drift{Kind: DriftContentMismatch, Path: f.Path, EntityID: derived.ID, Detail: d})
	}
	return drifts
}

// workspaceContentDiff compares the file-owned workspace fields. Returns ""
// when they agree.
func workspaceContentDiff(derived, row *store.Workspace) string {
	if derived.Name != row.Name {
		return fmt.Sprintf("name: file=%q db=%q", derived.Name, row.Name)
	}
	if derived.RootPath != row.RootPath {
		return fmt.Sprintf("root_path: file=%q db=%q", derived.RootPath, row.RootPath)
	}
	if derived.ParentID != row.ParentID {
		return fmt.Sprintf("parent: file=%q db=%q", derived.ParentID, row.ParentID)
	}
	if derived.DefaultPolicy != row.DefaultPolicy {
		return fmt.Sprintf("default_policy: file=%q db=%q", derived.DefaultPolicy, row.DefaultPolicy)
	}
	return ""
}
