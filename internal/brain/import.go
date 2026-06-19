package brain

import (
	"context"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// ImportReport summarises a one-way DB→Brain import (M5). It records how
// many rows of each Brain-canonical kind were serialized to files, the
// post-import verify drift (which MUST be empty for the import to be
// trusted), and whether the parity check passed.
type ImportReport struct {
	Workspaces   int          `json:"workspaces"`
	Tasks        int          `json:"tasks"`
	Memories     int          `json:"memories"`
	Skills       int          `json:"skills"`
	FilesChecked int          `json:"files_checked"`
	Drifts       []Drift      `json:"drifts"`
	ParityOK     bool         `json:"parity_ok"`
	Errors       []ImportItem `json:"errors,omitempty"`
}

// ImportItem records a single serialize failure during import (a row that
// could not be written to a file). A non-empty Errors slice fails parity.
type ImportItem struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Importer drives the parity-verified one-way import of every
// Brain-canonical DB row to its canonical .md/.yaml file, then reindexes
// and verifies the index re-derived from those files matches the live DB.
// It reuses the Serializer's outbound write path (hash-CAS + atomic +
// self-suppress) and the Indexer's ReindexAll + Verify backstop so the
// import shares one code path with the live dual-write engine.
type Importer struct {
	cfg   Config
	store store.Store
	ser   *Serializer
	ix    *Indexer
}

// NewImporter wires an importer from the already-constructed serializer +
// indexer so import-time writes are byte-identical to live dual-writes.
func NewImporter(cfg Config, s store.Store, ser *Serializer, ix *Indexer) *Importer {
	return &Importer{cfg: cfg, store: s, ser: ser, ix: ix}
}

// Run serializes every Brain-canonical row to a file, reindexes from the
// resulting tree, and verifies parity. It NEVER mutates the DB — the DB
// row remains authoritative and the file is the derived artifact during
// rollout (dual-read fallback, SPEC §10). The caller aborts the opt-in
// (does not flip brain_enabled) when ParityOK is false.
func (im *Importer) Run(ctx context.Context, skills SkillLister) (ImportReport, error) {
	var rep ImportReport

	wss, err := im.store.ListWorkspaces(ctx)
	if err != nil {
		return rep, fmt.Errorf("brain import: list workspaces: %w", err)
	}
	for i := range wss {
		w := wss[i]
		if err := im.ser.WriteWorkspace(ctx, &w); err != nil {
			rep.Errors = append(rep.Errors, ImportItem{Kind: EntityKindWorkspace, ID: w.ID, Reason: err.Error()})
			continue
		}
		rep.Workspaces++
	}

	tasks, err := im.store.ListTasks(ctx, store.TaskFilter{})
	if err != nil {
		return rep, fmt.Errorf("brain import: list tasks: %w", err)
	}
	for i := range tasks {
		t := tasks[i]
		if t.DeletedAt != nil {
			continue
		}
		if err := im.ser.WriteTask(ctx, &t); err != nil {
			rep.Errors = append(rep.Errors, ImportItem{Kind: EntityKindTask, ID: t.ID, Reason: err.Error()})
			continue
		}
		rep.Tasks++
	}

	mems, err := im.store.ListMemories(ctx, store.MemoryFilter{Scope: store.SkillScope{IncludeAll: true}})
	if err != nil {
		return rep, fmt.Errorf("brain import: list memories: %w", err)
	}
	for i := range mems {
		m := mems[i]
		if err := im.ser.WriteMemory(ctx, m.ID); err != nil {
			rep.Errors = append(rep.Errors, ImportItem{Kind: EntityKindMemory, ID: m.ID, Reason: err.Error()})
			continue
		}
		rep.Memories++
	}

	if skills != nil {
		if err := im.ser.ExportSkills(ctx, skills); err != nil {
			rep.Errors = append(rep.Errors, ImportItem{Kind: "skill", Reason: err.Error()})
		} else {
			heads, hErr := skills.ListSkillRegistryHeads(ctx, store.SkillScope{IncludeAll: true}, 0)
			if hErr == nil {
				rep.Skills = len(heads)
			}
		}
	}

	// Reindex from the freshly-written tree, then verify the re-derived
	// rows match the live DB. ReindexAll re-reads every file through the
	// same parse→validate→upsert path the live engine uses; Verify diffs
	// the result against the authoritative rows.
	if err := im.ix.ReindexAll(ctx); err != nil {
		return rep, fmt.Errorf("brain import: reindex: %w", err)
	}
	vr, err := Verify(ctx, im.cfg, im.store)
	if err != nil {
		return rep, fmt.Errorf("brain import: verify: %w", err)
	}
	rep.FilesChecked = vr.FilesChecked
	rep.Drifts = vr.Drifts
	rep.ParityOK = vr.OK() && len(rep.Errors) == 0
	return rep, nil
}
