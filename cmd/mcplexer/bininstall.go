package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// stableBinPath returns the stable binary path: ~/.mcplexer/bin/mcplexer
func stableBinPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".mcplexer", "bin", "mcplexer"), nil
}

// installBinary copies srcPath to the stable binary location, creating
// directories as needed. It is idempotent — safe to call repeatedly.
//
// Uses write-then-rename so replacing a currently-executing binary is safe:
// running processes keep their inode, new invocations see the new one. A
// plain O_TRUNC write on macOS corrupts the in-memory image of an executing
// binary, which made freshly-installed CLIs silently produce no output.
func installBinary(srcPath string) error {
	dst, err := stableBinPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read binary: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".mcplexer-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after successful rename
	if _, err := tmp.Write(src); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
