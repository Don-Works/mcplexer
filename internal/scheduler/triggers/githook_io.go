package triggers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// hasPrefix is a separator-aware prefix check that tolerates a missing
// trailing separator on `prefix`.
func hasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// latestWriteFile picks the most-recent un-reversed write_file receipt
// targeting `target`. ListInstallReceipts already orders DESC by
// applied_at so the first match wins.
func latestWriteFile(receipts []store.InstallReceipt, target string) *store.InstallReceipt {
	for i := range receipts {
		r := receipts[i]
		if r.Action == "write_file" && r.TargetPath == target && r.ReversedAt == nil {
			return &r
		}
	}
	return nil
}

// reverseWriteFile is the pure filesystem half of uninstall: restore
// from backup or delete the wrapper if no prior file existed.
func reverseWriteFile(r store.InstallReceipt) error {
	if r.BackupPath == "" {
		if err := os.Remove(r.TargetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", r.TargetPath, err)
		}
		return nil
	}
	data, err := os.ReadFile(r.BackupPath)
	if err != nil {
		return fmt.Errorf("read backup %s: %w", r.BackupPath, err)
	}
	if err := writeExecAtomic(r.TargetPath, data, 0o755); err != nil {
		return fmt.Errorf("restore %s: %w", r.TargetPath, err)
	}
	// Backup removal is best-effort — the restore already succeeded.
	_ = os.Remove(r.BackupPath)
	return nil
}

// writeExecAtomic writes `data` to `target` via write-to-tmp + rename
// and chmods the final path to `mode`. The parent directory must exist
// — callers are expected to operate inside an already-initialised
// .git/hooks/ tree.
func writeExecAtomic(target string, data []byte, mode os.FileMode) error {
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// repoHash produces a short stable identifier for repoRoot so backup
// directories don't collide when one human has many repos with the
// same basename ("/Users/x/foo" vs "/Users/x/bar/foo").
func repoHash(repoRoot string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoRoot)))
	return hex.EncodeToString(sum[:8])
}

// newReceiptID returns a short opaque hex string for receipt primary
// keys. 16 bytes = 128 random bits.
func newReceiptID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("githook-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
