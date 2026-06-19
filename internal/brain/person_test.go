package brain_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// TestSerializePerson_RoundTripByteStable serializes a person, parses it back,
// converts to a row, re-serializes, and asserts the bytes are identical — the
// markdown is the canonical form, so the round-trip must be lossless.
func TestSerializePerson_RoundTripByteStable(t *testing.T) {
	ts := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	p := &store.PersonEntry{
		ID:              "01PERSON01",
		WorkspaceID:     store.PersonDefaultWorkspaceID,
		Name:            "Ada Lovelace",
		Email:           "ada@analytical.engine",
		Phone:           "+44 1234",
		Company:         "Analytical Engine Co",
		Role:            "Chief Mathematician",
		Notes:           "First programmer.\nLikes punch cards.",
		Pinned:          true,
		SourceKind:      store.PersonSourceUser,
		SourceSessionID: "sess_abc",
		CreatedAt:       ts,
		UpdatedAt:       ts,
	}
	tags, _ := json.Marshal([]string{"vip", "math"})
	p.TagsJSON = tags

	links := []store.PersonEntityRow{
		{PersonID: p.ID, EntityKind: "org", EntityID: "analytical-engine", Role: "subject"},
		{PersonID: p.ID, EntityKind: "deal", EntityID: "01DEAL01", Role: "mentioned"},
	}

	data, err := brain.SerializePerson(p, links)
	if err != nil {
		t.Fatalf("SerializePerson: %v", err)
	}
	if !strings.Contains(string(data), "schema: person/v1") {
		t.Errorf("missing schema marker:\n%s", data)
	}
	if !strings.Contains(string(data), "workspace: crm") {
		t.Errorf("missing workspace marker:\n%s", data)
	}
	if !strings.Contains(string(data), "First programmer.") {
		t.Errorf("missing notes body:\n%s", data)
	}

	// Parse → convert → re-serialize and compare bytes.
	fm, body, err := brain.ParsePerson(data)
	if err != nil {
		t.Fatalf("ParsePerson: %v", err)
	}
	row, refs, err := fm.ToPerson(body)
	if err != nil {
		t.Fatalf("ToPerson: %v", err)
	}
	if row.Name != "Ada Lovelace" || row.Company != "Analytical Engine Co" {
		t.Errorf("field mismatch after parse: %+v", row)
	}
	if row.WorkspaceID != store.PersonDefaultWorkspaceID {
		t.Errorf("workspace mismatch after parse: %q", row.WorkspaceID)
	}
	if len(refs) != 2 {
		t.Fatalf("want 2 entity refs, got %d: %+v", len(refs), refs)
	}

	// Rebuild the link rows from the parsed refs to re-serialize identically.
	reLinks := make([]store.PersonEntityRow, 0, len(refs))
	for _, r := range refs {
		reLinks = append(reLinks, store.PersonEntityRow{
			PersonID: row.ID, EntityKind: r.Kind, EntityID: r.ID, Role: r.Role,
		})
	}
	again, err := brain.SerializePerson(row, reLinks)
	if err != nil {
		t.Fatalf("re-SerializePerson: %v", err)
	}
	if string(again) != string(data) {
		t.Errorf("round-trip not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", data, again)
	}
}

// TestValidatePerson_RejectsMissingName guards the required-name invariant
// plus the schema + filename-stem checks.
func TestValidatePerson_RejectsMissingName(t *testing.T) {
	tc := []struct {
		name     string
		fm       brain.PersonFrontmatter
		filename string
		wantErr  bool
	}{
		{
			name:    "missing name",
			fm:      brain.PersonFrontmatter{ID: "01P", Workspace: "crm", Schema: brain.SchemaPersonV1},
			wantErr: true,
		},
		{
			name:    "missing id",
			fm:      brain.PersonFrontmatter{Workspace: "crm", Name: "Bob"},
			wantErr: true,
		},
		{
			name:    "missing workspace",
			fm:      brain.PersonFrontmatter{ID: "01P", Name: "Bob", Schema: brain.SchemaPersonV1},
			wantErr: true,
		},
		{
			name:     "name mismatches filename stem",
			fm:       brain.PersonFrontmatter{ID: "01P", Workspace: "crm", Name: "Bob"},
			filename: "Alice.md",
			wantErr:  true,
		},
		{
			name:    "unknown schema",
			fm:      brain.PersonFrontmatter{ID: "01P", Workspace: "crm", Name: "Bob", Schema: "person/v9"},
			wantErr: true,
		},
		{
			name:     "valid",
			fm:       brain.PersonFrontmatter{ID: "01P", Workspace: "crm", Name: "Bob", Schema: brain.SchemaPersonV1},
			filename: "Bob.md",
			wantErr:  false,
		},
	}
	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			err := brain.ValidatePerson(tt.fm, tt.filename)
			if tt.wantErr && err == nil {
				t.Errorf("expected validation error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected validation error: %v", err)
			}
		})
	}
}

// TestSavePerson_PersistsAndFile drives the full Editor write path: the row is
// persisted, the canonical .md lands on disk under workspaces/crm/crm/people/,
// and the record reads back with its entity links.
func TestSavePerson_PersistsAndFile(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ser.ShareSelfWrites(ix)
	ed := brain.NewEditor(st, ser)
	ctx := context.Background()

	saved, err := ed.SavePerson(ctx, brain.PersonRecord{
		Name:    "Grace Hopper",
		Email:   "grace@navy.mil",
		Company: "US Navy",
		Role:    "Rear Admiral",
		Tags:    []string{"compiler"},
		Notes:   "Coined 'debugging'.",
		Entities: []brain.EntityLinkFM{
			{Kind: "org", ID: "us-navy"},
		},
	})
	if err != nil {
		t.Fatalf("SavePerson: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("expected a minted person id")
	}

	// Filenames are slugified (recordStem) — never the raw free-form name.
	wantPath := filepath.Join(dir, "workspaces", "crm", "crm", "people", "grace-hopper.md")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected person file at %s: %v", wantPath, err)
	}

	got, err := ed.GetPerson(ctx, saved.ID)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	if got.Workspace != store.PersonDefaultWorkspaceID || got.Name != "Grace Hopper" || got.Role != "Rear Admiral" {
		t.Fatalf("record mismatch: %+v", got)
	}
	if len(got.Entities) != 1 || got.Entities[0].Kind != "org" {
		t.Fatalf("entity link missing: %+v", got.Entities)
	}
}

// TestIndexPersonFile_RoundTrips writes a person .md by hand, indexes it, and
// asserts the DB row materialises with the right fields + entity link.
func TestIndexPersonFile_RoundTrips(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	peopleDir := filepath.Join(dir, "crm", "people")
	if err := os.MkdirAll(peopleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: 01IDX01\nschema: person/v1\nname: Linus Torvalds\ncompany: Linux Foundation\npinned: false\nentities:\n  - kind: org\n    id: linux-foundation\ncreated_at: 2026-06-04T10:00:00Z\nupdated_at: 2026-06-04T10:00:00Z\n---\n\nMaintains the kernel.\n"
	path := filepath.Join(peopleDir, "Linus Torvalds.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	got, err := st.GetPerson(ctx, "01IDX01")
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	if got.Company != "Linux Foundation" || got.Notes != "Maintains the kernel." {
		t.Fatalf("indexed row mismatch: %+v", got)
	}
	if got.WorkspaceID != store.PersonDefaultWorkspaceID {
		t.Fatalf("legacy central person should index into crm workspace, got %q", got.WorkspaceID)
	}
	links, err := st.ListPersonEntities(ctx, "01IDX01")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].EntityKind != "org" || links[0].EntityID != "linux-foundation" {
		t.Fatalf("entity link not indexed: %+v", links)
	}
}
