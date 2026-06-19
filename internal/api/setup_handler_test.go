package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"path/filepath"

	"github.com/don-works/mcplexer/internal/harnesssync"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestSetupHandler_Install_UnknownHarness(t *testing.T) {
	h := &harnessSetupHandler{store: nil, installMgr: nil}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/setup/install", strings.NewReader(`{"harness":"unknown"}`))
	h.install(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp setupError
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != "unknown_harness" {
		t.Errorf("error code = %q, want unknown_harness", resp.Error.Code)
	}
}

func TestSetupHandler_Install_ValidHarness(t *testing.T) {
	dir := t.TempDir()
	h := &harnessSetupHandler{store: nil, installMgr: nil}

	origHomeDir := HomeDirForTest
	HomeDirForTest = func() (string, error) { return dir, nil }
	defer func() { HomeDirForTest = origHomeDir }()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/setup/install", strings.NewReader(`{"harness":"codex"}`))
	h.install(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp harnessRow
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Key != "codex" {
		t.Errorf("key = %q, want codex", resp.Key)
	}
	if !resp.BootstrapInstalled {
		t.Error("bootstrap_installed should be true")
	}
}

func TestSetupHandler_Recheck_UnknownHarness(t *testing.T) {
	h := &harnessSetupHandler{store: nil, installMgr: nil}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/setup/recheck", strings.NewReader(`{"harness":"invalid"}`))
	h.recheck(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp setupError
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != "unknown_harness" {
		t.Errorf("error code = %q, want unknown_harness", resp.Error.Code)
	}
}

func TestSetupHandler_Status(t *testing.T) {
	dir := t.TempDir()
	h := &harnessSetupHandler{store: nil, installMgr: nil}

	origHomeDir := HomeDirForTest
	HomeDirForTest = func() (string, error) { return dir, nil }
	defer func() { HomeDirForTest = origHomeDir }()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	h.status(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp setupStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Harnesses) != len(harnesssync.AllKeys()) {
		t.Errorf("harness count = %d, want %d", len(resp.Harnesses), len(harnesssync.AllKeys()))
	}
}

func TestSetupHandler_Install_MissingHarness(t *testing.T) {
	h := &harnessSetupHandler{store: nil, installMgr: nil}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/setup/install", strings.NewReader(`{}`))
	h.install(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp setupError
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != "bad_request" {
		t.Errorf("error code = %q, want bad_request", resp.Error.Code)
	}
}

func TestSetupHandler_Status_UsesRegistryHeadVersion(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	reg := skillregistry.New(db)
	seed, err := skillregistry.SeedBody("using-mcplexer")
	if err != nil {
		t.Fatalf("SeedBody: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "using-mcplexer", Body: seed, Author: "system",
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "using-mcplexer", Body: seed + "\n\n<!-- v2 bump -->", Author: "system",
	}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	dir := t.TempDir()
	h := &harnessSetupHandler{store: db, skillRegistry: reg}
	origHomeDir := HomeDirForTest
	HomeDirForTest = func() (string, error) { return dir, nil }
	defer func() { HomeDirForTest = origHomeDir }()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	h.status(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp setupStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	for _, row := range resp.Harnesses {
		if row.RegistryVersion != 2 {
			t.Errorf("harness %s registry_version = %d, want 2", row.Key, row.RegistryVersion)
		}
	}
}
