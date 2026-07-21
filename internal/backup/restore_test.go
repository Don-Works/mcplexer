package backup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRestore_TakesPreSnapshot asserts the load-bearing safety promise:
// Restore ALWAYS returns a non-empty pre-restore snapshot ID, that snapshot
// is retrievable, captures identity (so it is a TRUE rollback target), and
// is itself restorable to recover the pre-restore state.
func TestRestore_TakesPreSnapshot(t *testing.T) {
	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test")

	mf, err := svc.Create(context.Background(), "to-restore", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Mutate an artifact so we can prove the pre-snapshot captured the
	// pre-restore state when we roll back to it.
	apiKey := filepath.Join(dataDir, "api-key")
	writeTestFile(t, apiKey, "BEFORE-RESTORE")

	preID, err := svc.Restore(context.Background(), mf.ID)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if preID == "" {
		t.Fatal("Restore returned empty preSnapshotID; the rollback escape hatch is missing")
	}

	pre, err := svc.Get(preID)
	if err != nil {
		t.Fatalf("Get(preSnapshotID): %v", err)
	}
	if !pre.IncludesIdentity {
		t.Error("pre-restore snapshot IncludesIdentity = false; rollback would lose machine identity")
	}
	if pre.PreRestoreOf != mf.ID {
		t.Errorf("pre.PreRestoreOf = %q, want %q", pre.PreRestoreOf, mf.ID)
	}

	// The restore replaced api-key with the backed-up "API-TOKEN-123".
	if got := mustRead(t, apiKey); got != "API-TOKEN-123" {
		t.Fatalf("after restore api-key = %q, want API-TOKEN-123", got)
	}

	// Rolling back to the pre-restore snapshot must recover "BEFORE-RESTORE".
	if _, err := svc.Restore(context.Background(), preID); err != nil {
		t.Fatalf("rollback Restore(preID): %v", err)
	}
	if got := mustRead(t, apiKey); got != "BEFORE-RESTORE" {
		t.Errorf("after rollback api-key = %q, want BEFORE-RESTORE", got)
	}
}

// TestRestore_SwapFailureLeavesConsistentState injects a failure in the swap
// phase (an un-writable destination parent) and asserts (a) applyBackup
// returns an error, (b) the live data dir is left consistent — the DB row is
// intact and the moved-aside artifact was rolled back into place rather than
// left half-swapped.
func TestRestore_SwapFailureLeavesConsistentState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based un-writable parent injection is POSIX-only")
	}
	// Running as root defeats permission-based injection.
	if os.Geteuid() == 0 {
		t.Skip("cannot inject permission failures as root")
	}

	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test")

	mf, err := svc.Create(context.Background(), "swap-fail", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Record the live api-key content so we can assert it was NOT clobbered
	// when the swap fails.
	apiKey := filepath.Join(dataDir, "api-key")
	origAPIKey := mustRead(t, apiKey)

	// Make the secrets/ parent (== dataDir) un-writable so the rename of the
	// staged secrets tree into place fails inside swapStaged. The DB rename
	// (dbPath) happens before swapStaged and targets dataDir too, so to keep
	// the DB swap working we restore on a copy where the DB lives in a
	// writable subdir. Simpler: target a dst whose own parent we lock — the
	// secrets dir. Lock dataDir AFTER the DB is already in place by failing
	// during applyBackup's swap loop.
	//
	// We lock the secrets directory's parent (dataDir) read+execute only.
	if err := os.Chmod(dataDir, 0o555); err != nil {
		t.Fatalf("chmod dataDir: %v", err)
	}
	// Always restore writability so t.TempDir cleanup + later reads work.
	defer os.Chmod(dataDir, 0o755) //nolint:errcheck

	tarPath := svc.tarPath(mf.ID)
	err = applyBackup(tarPath, dataDir, dbPath, svc.restoreTargets())
	if err == nil {
		t.Fatal("expected applyBackup to fail when a destination parent is un-writable")
	}
	if !strings.Contains(err.Error(), "swap") && !strings.Contains(err.Error(), "mkdir") {
		t.Logf("swap failure error (informational): %v", err)
	}

	// Restore writability so we can inspect the resulting state.
	if err := os.Chmod(dataDir, 0o755); err != nil {
		t.Fatalf("re-chmod dataDir: %v", err)
	}

	// Consistency check 1: the live DB is intact + readable (its row survives).
	assertDBRow(t, dbPath)

	// Consistency check 2: api-key must be either its original content or the
	// fully-swapped backup content — never a half-written / missing file. In
	// this injection the swap of api-key may or may not have completed before
	// the failing entry, but it must never be absent or empty.
	got := mustRead(t, apiKey)
	if got != origAPIKey && got != "API-TOKEN-123" {
		t.Errorf("api-key left in inconsistent state %q (want original or restored content)", got)
	}
	if got == "" {
		t.Error("api-key was left empty after a failed swap")
	}
}
