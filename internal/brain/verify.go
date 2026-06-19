package brain

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/don-works/mcplexer/internal/store"
)

// DriftKind classifies one verify finding.
type DriftKind string

const (
	// DriftMissingRow — a file exists (and is indexed) but the DB row is
	// gone (or soft-deleted).
	DriftMissingRow DriftKind = "missing_row"
	// DriftMissingFile — the index references an on-disk file that no
	// longer exists.
	DriftMissingFile DriftKind = "missing_file"
	// DriftContentMismatch — the row re-derived from the file differs from
	// the live DB row (frontmatter/body drift).
	DriftContentMismatch DriftKind = "content_mismatch"
	// DriftParseError — the file no longer parses/validates.
	DriftParseError DriftKind = "parse_error"
)

// Drift is one discrepancy between the on-disk files and the live DB rows.
type Drift struct {
	Kind     DriftKind `json:"kind"`
	Path     string    `json:"path"`
	EntityID string    `json:"entity_id,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// VerifyReport summarises a verify pass: the total files checked and any
// drift found. Empty Drifts == the index faithfully reflects the files.
type VerifyReport struct {
	FilesChecked int     `json:"files_checked"`
	Drifts       []Drift `json:"drifts"`
}

// OK reports whether the index is drift-free.
func (r VerifyReport) OK() bool { return len(r.Drifts) == 0 }

// Verify re-derives rows from every indexed file and diffs them against the
// live DB rows, reporting drift without mutating anything. It backs the
// mcplexer__brain_verify admin tool + the M5 no-data-loss parity gate
// (Importer.Run / ParityOK), so it MUST cover every materialised entity kind
// — task, memory (note + fact incl. bi-temporal fields + entity links), and
// workspace. A kind missing here makes ParityOK hollow for that kind, which
// is exactly the failure the parity finding flagged.
func Verify(ctx context.Context, cfg Config, s store.Store) (VerifyReport, error) {
	var report VerifyReport
	files, err := s.ListIndexFiles(ctx, "")
	if err != nil {
		return report, fmt.Errorf("brain: verify list index: %w", err)
	}
	for i := range files {
		f := files[i]
		switch f.EntityKind {
		case EntityKindTask:
			report.FilesChecked++
			report.Drifts = appendTaskDrift(ctx, s, cfg, f, report.Drifts)
		case EntityKindMemory:
			report.FilesChecked++
			report.Drifts = appendMemoryDrift(ctx, s, f, report.Drifts)
		case EntityKindWorkspace:
			report.FilesChecked++
			report.Drifts = appendWorkspaceDrift(ctx, s, f, report.Drifts)
		default:
			// Unknown kind — count it but record a parse error so an
			// unrecognised index row cannot silently pass the parity gate.
			report.FilesChecked++
			report.Drifts = append(report.Drifts, Drift{
				Kind: DriftParseError, Path: f.Path, EntityID: f.EntityID,
				Detail: "unknown entity kind " + f.EntityKind,
			})
		}
	}
	return report, nil
}

// appendTaskDrift checks one indexed task file against its DB row.
func appendTaskDrift(ctx context.Context, s store.Store, _ Config, f store.IndexFile, drifts []Drift) []Drift {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return append(drifts, Drift{Kind: DriftMissingFile, Path: f.Path, EntityID: f.EntityID})
	}
	fm, body, err := ParseTask(data)
	if err != nil {
		return append(drifts, Drift{Kind: DriftParseError, Path: f.Path, EntityID: f.EntityID, Detail: err.Error()})
	}
	derived, err := fm.ToTask(body)
	if err != nil {
		return append(drifts, Drift{Kind: DriftParseError, Path: f.Path, EntityID: f.EntityID, Detail: err.Error()})
	}

	row, err := s.GetTask(ctx, derived.ID)
	if errors.Is(err, store.ErrNotFound) {
		return append(drifts, Drift{Kind: DriftMissingRow, Path: f.Path, EntityID: derived.ID})
	}
	if err != nil {
		return append(drifts, Drift{Kind: DriftMissingRow, Path: f.Path, EntityID: derived.ID, Detail: err.Error()})
	}
	if row.DeletedAt != nil {
		return append(drifts, Drift{Kind: DriftMissingRow, Path: f.Path, EntityID: derived.ID, Detail: "row is soft-deleted"})
	}
	if d := taskContentDiff(derived, row); d != "" {
		return append(drifts, Drift{Kind: DriftContentMismatch, Path: f.Path, EntityID: derived.ID, Detail: d})
	}
	return drifts
}

// taskContentDiff compares the human-editable fields (those the file
// owns) between the re-derived task and the live row. Returns "" when
// they agree. Operational/derived fields (lease, closed_at) are not
// compared — the file does not own them.
func taskContentDiff(derived, row *store.Task) string {
	if derived.Title != row.Title {
		return fmt.Sprintf("title: file=%q db=%q", derived.Title, row.Title)
	}
	if derived.Status != row.Status {
		return fmt.Sprintf("status: file=%q db=%q", derived.Status, row.Status)
	}
	if normPriority(derived.Priority) != normPriority(row.Priority) {
		return fmt.Sprintf("priority: file=%q db=%q", derived.Priority, row.Priority)
	}
	if derived.Pinned != row.Pinned {
		return fmt.Sprintf("pinned: file=%v db=%v", derived.Pinned, row.Pinned)
	}
	dFile, _ := SplitBodyNotes(derived.Description)
	rDesc, _ := SplitBodyNotes(row.Description)
	if dFile != rDesc {
		return "description"
	}
	return ""
}

// normPriority maps an empty priority to the store's default ("normal")
// so a file with no explicit priority is not flagged as drift against a
// DB row that the store defaulted on insert.
func normPriority(p string) string {
	if p == "" {
		return "normal"
	}
	return p
}
