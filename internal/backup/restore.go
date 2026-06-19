package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// applyBackup extracts a tarball produced by writeTarball over a live data
// directory. The DB and every artifact present in the tarball (master key,
// config, api-key, secrets/, addons/, and any identity files that were
// captured) are staged in a temp dir then atomically swapped into place per
// file/tree. Old files/trees are moved aside on the swap path so a failure
// leaves the prior state recoverable. The daemon must be restarted after.
//
// targets maps tarball entry names/prefixes to their on-disk destinations.
func applyBackup(tarPath, dataDir, dbPath string, targets map[string]artifact) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	// Stage extraction in a tmp dir so a failure mid-way never leaves a
	// half-restored tree on disk. Atomically swap at the very end.
	stageDir, err := os.MkdirTemp(filepath.Dir(dbPath), "restore-stage-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stageDir) }()

	stagedDB := filepath.Join(stageDir, "mcplexer.db")
	sawDB := false
	// staged tracks, per tarball-artifact name, the temp path we extracted
	// to, so we can swap each into place after a clean full extraction.
	staged := make(map[string]string)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name == "manifest.json" {
			continue
		}
		if hdr.Name == "mcplexer.db" {
			if err := writeFile(stagedDB, tr, 0o600); err != nil {
				return err
			}
			sawDB = true
			continue
		}
		if err := stageEntry(hdr.Name, tr, stageDir, targets, staged); err != nil {
			return err
		}
	}
	if !sawDB {
		return errors.New("backup contains no mcplexer.db")
	}

	// Stage every artifact into place FIRST, then swap the DB LAST. If the
	// artifact swap fails we must not leave a new DB next to old secrets/
	// api-key, so the DB rename is guarded by an explicit rollback.
	dbBackup := dbPath + ".pre-restore-" + filepath.Base(stageDir)
	dbMoved := false
	if _, err := os.Lstat(dbPath); err == nil {
		if err := os.Rename(dbPath, dbBackup); err != nil {
			return fmt.Errorf("move aside db: %w", err)
		}
		dbMoved = true
	}
	if err := os.Rename(stagedDB, dbPath); err != nil {
		if dbMoved {
			_ = os.Rename(dbBackup, dbPath)
		}
		return fmt.Errorf("swap db: %w", err)
	}
	if err := swapStaged(staged, targets, stageDir); err != nil {
		// Roll the DB back so we never leave new-DB + old-artifacts.
		if dbMoved {
			_ = os.Rename(dbBackup, dbPath)
		} else {
			_ = os.Remove(dbPath)
		}
		return err
	}
	if dbMoved {
		_ = os.RemoveAll(dbBackup)
	}
	return nil
}

// stageEntry extracts one tarball entry into stageDir if it matches a known
// restore target — either an exact-name file or a member of a tree prefix.
// Unknown entries are ignored. Path-traversal segments are rejected.
func stageEntry(name string, r io.Reader, stageDir string, targets map[string]artifact, staged map[string]string) error {
	// Exact-name (non-tree) artifact, e.g. db.age, api-key, mcplexer.yaml.
	if a, ok := targets[name]; ok && !a.isTree {
		if strings.Contains(name, "..") {
			return fmt.Errorf("restore: refusing path-traversal entry %q", name)
		}
		dst := filepath.Join(stageDir, filepath.FromSlash(name))
		if err := writeFile(dst, r, 0o600); err != nil {
			return err
		}
		staged[name] = dst
		return nil
	}
	// Tree member, e.g. secrets/foo, addons/bar.yaml.
	for treeName, a := range targets {
		if !a.isTree {
			continue
		}
		prefix := treeName + "/"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			return nil // tree-root entry, nothing to extract
		}
		if strings.Contains(rel, "..") {
			return fmt.Errorf("restore: refusing path-traversal entry %q", name)
		}
		root := filepath.Join(stageDir, treeName)
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if err := writeFile(dst, r, 0o600); err != nil {
			return err
		}
		staged[treeName] = root // record the tree root once; idempotent
		return nil
	}
	return nil
}

// swapStaged moves every staged artifact into its live destination,
// preserving the prior file/tree under a sibling .pre-restore- name so a
// failure rolls back to the old state.
func swapStaged(staged map[string]string, targets map[string]artifact, stageDir string) error {
	tag := filepath.Base(stageDir)
	for name, stagedPath := range staged {
		dst := targets[name].src
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("mkdir for %s: %w", name, err)
		}
		old := filepath.Join(filepath.Dir(dst), ".pre-restore-"+tag+"-"+sanitize(name))
		moved := false
		if _, err := os.Lstat(dst); err == nil {
			if err := os.Rename(dst, old); err != nil {
				return fmt.Errorf("move aside %s: %w", name, err)
			}
			moved = true
		}
		if err := os.Rename(stagedPath, dst); err != nil {
			if moved {
				_ = os.Rename(old, dst)
			}
			return fmt.Errorf("swap %s: %w", name, err)
		}
		if moved {
			_ = os.RemoveAll(old)
		}
	}
	return nil
}

// sanitize turns a tarball artifact name into a safe single path segment
// for the .pre-restore- sidecar (slashes would create nested dirs).
func sanitize(name string) string { return strings.ReplaceAll(name, "/", "_") }

func writeFile(path string, src io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, src)
	return err
}
