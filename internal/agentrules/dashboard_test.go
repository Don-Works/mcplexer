package agentrules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderWithDashboardInjectsURL(t *testing.T) {
	out := RenderWithDashboard(CurrentVersion, "http://localhost:13333")
	if !strings.Contains(out, "**Dashboard:** http://localhost:13333") {
		t.Fatalf("injected URL missing:\n%s", out)
	}
	if strings.Contains(out, "**Dashboard:** "+DashboardURL) {
		t.Fatal("default URL should have been replaced")
	}
}

func TestRenderWithDashboardEmptyKeepsDefault(t *testing.T) {
	if RenderWithDashboard(CurrentVersion, "") != Render(CurrentVersion) {
		t.Fatal("empty dashboard URL must be byte-identical to Render")
	}
}

// TestSyncWithDashboardIsIdempotentAcrossURLs is the load-bearing guard: a file
// synced with a runtime URL must NOT be seen as drifted by a default-URL
// Status/Sync, and re-syncing with the same URL must be a no-op — otherwise
// every session would rewrite the block and churn the port.
func TestSyncWithDashboardIsIdempotentAcrossURLs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")

	changed, err := SyncWithDashboard(path, CurrentVersion, "http://localhost:13333")
	if err != nil || !changed {
		t.Fatalf("first sync: changed=%v err=%v", changed, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "http://localhost:13333") {
		t.Fatalf("written block missing runtime URL:\n%s", data)
	}

	// Same URL again → no-op.
	changed, err = SyncWithDashboard(path, CurrentVersion, "http://localhost:13333")
	if err != nil || changed {
		t.Fatalf("re-sync same URL should be no-op: changed=%v err=%v", changed, err)
	}

	// A default-URL Status must consider the runtime-URL file up to date.
	present, _, upToDate, err := Status(path, CurrentVersion)
	if err != nil || !present || !upToDate {
		t.Fatalf("default Status on runtime-URL file: present=%v upToDate=%v err=%v", present, upToDate, err)
	}

	// A plain default Sync ("" URL = preserve) must be a no-op and keep :13333.
	changed, err = Sync(path, CurrentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("plain Sync rewrote a runtime-URL block — port would churn every session")
	}
	data2, _ := os.ReadFile(path)
	if !strings.Contains(string(data2), "http://localhost:13333") {
		t.Fatalf("plain Sync clobbered the runtime URL:\n%s", data2)
	}
}

// TestSyncWithDashboardCorrectsStaleURL is the other half: when a block carries
// the wrong port, a sync with the right one must rewrite it (not no-op).
func TestSyncWithDashboardCorrectsStaleURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")

	// Seed with the default URL.
	if _, err := Sync(path, CurrentVersion); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "**Dashboard:** "+DashboardURL) {
		t.Fatalf("seed missing default URL:\n%s", data)
	}

	// Now the daemon binds a different port → sync must correct it.
	changed, err := SyncWithDashboard(path, CurrentVersion, "http://localhost:13333")
	if err != nil || !changed {
		t.Fatalf("stale-URL sync should rewrite: changed=%v err=%v", changed, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "**Dashboard:** http://localhost:13333") {
		t.Fatalf("URL not corrected:\n%s", data)
	}
	if strings.Contains(string(data), "**Dashboard:** "+DashboardURL) {
		t.Fatal("stale default URL still present after correction")
	}
}
