package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newMigrationTestEnv builds an in-process registry + handler against a
// throwaway SQLite DB, plus a tempdir scratch source.
func newMigrationTestEnv(t *testing.T) (*skillMigrationHandler, *skillregistry.Registry, string) {
	t.Helper()
	dbDir := t.TempDir()
	db, err := sqlite.New(context.Background(), filepath.Join(dbDir, "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := skillregistry.New(db)
	return &skillMigrationHandler{registry: reg}, reg, t.TempDir()
}

func writeSKILL(t *testing.T, parent, name, desc, body string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	md := body
	if md == "" {
		md = "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n\nbody.\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func TestListLocalUnpublished_MixedStatuses(t *testing.T) {
	h, reg, src := newMigrationTestEnv(t)

	writeSKILL(t, src, "fresh", "Use when fresh needed", "")
	dupBody := "---\nname: dup\ndescription: matching\n---\n# dup\n\nbody.\n"
	writeSKILL(t, src, "dup", "matching", dupBody)
	writeSKILL(t, src, "conflict", "Use when conflict needed", "")

	// Seed: dup matches; conflict has different content.
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "dup", Body: dupBody}); err != nil {
		t.Fatalf("seed dup: %v", err)
	}
	conflictBody := "---\nname: conflict\ndescription: OLDER conflict description\n---\nold body.\n"
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "conflict", Body: conflictBody}); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/local-unpublished?source="+src, nil)
	rr := httptest.NewRecorder()
	h.listLocalUnpublished(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp localUnpublishedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Path != src {
		t.Errorf("path=%s; want %s", resp.Path, src)
	}
	got := map[string]skillregistry.MigrationStatus{}
	for _, s := range resp.Skills {
		got[s.Name] = s.Status
	}
	if got["fresh"] != skillregistry.StatusNew {
		t.Errorf("fresh=%s; want new", got["fresh"])
	}
	if got["dup"] != skillregistry.StatusDuplicate {
		t.Errorf("dup=%s; want duplicate", got["dup"])
	}
	if got["conflict"] != skillregistry.StatusVersionConflict {
		t.Errorf("conflict=%s; want version-conflict", got["conflict"])
	}
}

func TestListLocalUnpublished_BadSource(t *testing.T) {
	h, _, _ := newMigrationTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/local-unpublished?source=/does/not/exist/anywhere", nil)
	rr := httptest.NewRecorder()
	h.listLocalUnpublished(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestListLocalUnpublished_DefaultMissingSourceIsEmpty(t *testing.T) {
	h, _, _ := newMigrationTestEnv(t)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/local-unpublished", nil)
	rr := httptest.NewRecorder()
	h.listLocalUnpublished(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp localUnpublishedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Skills) != 0 {
		t.Fatalf("skills len=%d, want 0", len(resp.Skills))
	}
	if !strings.HasSuffix(resp.Path, filepath.Join(".claude", "skills")) {
		t.Fatalf("path=%q, want default skills path", resp.Path)
	}
}

func TestImportLocalSkill_PublishesNewVersionAndArchives(t *testing.T) {
	h, _, src := newMigrationTestEnv(t)
	dir := writeSKILL(t, src, "newone", "Use when newone needed", "")

	body, _ := json.Marshal(importLocalSkillRequest{Name: "newone", SourceDir: dir})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.importLocalSkill(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var res skillregistry.MigrationResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Action != skillregistry.ActionImported || res.Version != 1 {
		t.Errorf("action=%s version=%d; want imported/v1", res.Action, res.Version)
	}
	if res.ArchivedTo == "" {
		t.Errorf("ArchivedTo empty")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("source still present: %v", err)
	}
}

func TestImportLocalSkill_DuplicateReturnsSkipped(t *testing.T) {
	h, reg, src := newMigrationTestEnv(t)
	body := "---\nname: dup\ndescription: matching\n---\n# dup\n\nbody.\n"
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "dup", Body: body}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dir := writeSKILL(t, src, "dup", "matching", body)

	reqBody, _ := json.Marshal(importLocalSkillRequest{Name: "dup", SourceDir: dir})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", bytes.NewReader(reqBody))
	rr := httptest.NewRecorder()
	h.importLocalSkill(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var res skillregistry.MigrationResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Action != skillregistry.ActionSkipped {
		t.Errorf("action=%s; want skipped", res.Action)
	}
}

func TestImportLocalSkill_VersionConflictNoOverwriteReturns422(t *testing.T) {
	h, reg, src := newMigrationTestEnv(t)
	old := "---\nname: vc\ndescription: original\n---\n# vc\n\noriginal body.\n"
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "vc", Body: old}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dir := writeSKILL(t, src, "vc", "rewritten", "")

	reqBody, _ := json.Marshal(importLocalSkillRequest{Name: "vc", SourceDir: dir})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", bytes.NewReader(reqBody))
	rr := httptest.NewRecorder()
	h.importLocalSkill(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s; want 422", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "version-conflict") {
		t.Errorf("missing version-conflict error: %s", rr.Body.String())
	}
}

func TestImportLocalSkill_OverwritePublishesV2(t *testing.T) {
	h, reg, src := newMigrationTestEnv(t)
	old := "---\nname: vc\ndescription: original\n---\n# vc\n\nold body.\n"
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Name: "vc", Body: old}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dir := writeSKILL(t, src, "vc", "rewritten", "")

	body, _ := json.Marshal(importLocalSkillRequest{Name: "vc", SourceDir: dir, Overwrite: true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.importLocalSkill(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var res skillregistry.MigrationResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Action != skillregistry.ActionUpdated || res.Version != 2 {
		t.Errorf("action=%s version=%d; want updated/v2", res.Action, res.Version)
	}
}

func TestImportLocalSkill_RejectsRelativeSourceDir(t *testing.T) {
	h, _, _ := newMigrationTestEnv(t)
	body, _ := json.Marshal(importLocalSkillRequest{Name: "x", SourceDir: "relative/path"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.importLocalSkill(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d; want 400", rr.Code)
	}
}

func TestImportLocalSkill_RequiresNameAndPath(t *testing.T) {
	h, _, _ := newMigrationTestEnv(t)
	body, _ := json.Marshal(importLocalSkillRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/import", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.importLocalSkill(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d; want 400", rr.Code)
	}
}

// readBody is a tiny helper to keep the assertions terse when comparing
// response bytes against expected substrings.
func readBody(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// keep readBody referenced so future tests have a ready helper.
var _ = readBody
