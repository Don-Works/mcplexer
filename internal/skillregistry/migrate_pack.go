package skillregistry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxSkillDirBytes is the per-file (and total uncompressed) cap we accept
// during migration. Matches MaxBundleBytes so we never produce a bundle
// Publish would reject.
const maxSkillDirBytes = MaxBundleBytes

// packSkillDir builds a tar.gz of dir suitable for PublishOptions.Bundle.
// Layout uses dirName as the leading component (matches the
// "tar -czf x.tgz <skillname>" convention) so the bundle's SKILL.md is at
// "<dirName>/SKILL.md", which readSkillMDFromTarGz recognises.
//
// body is the canonical SKILL.md text the registry will index — when the
// on-disk file differs (e.g. trailing-whitespace drift) we still embed the
// exact file contents, since ValidateBundle normalises both sides.
func packSkillDir(dir, body, name string) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("packSkillDir: empty dir")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	if name == "" {
		name = filepath.Base(dir)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := walkSkillDir(dir, name, tw); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	if buf.Len() > MaxBundleBytes {
		return nil, fmt.Errorf("bundle %d bytes exceeds cap of %d", buf.Len(), MaxBundleBytes)
	}

	// Use body to overwrite SKILL.md inside the tar when caller passed a
	// canonical version different from the on-disk file. Walk above
	// already wrote the on-disk file; for migration that's what we want
	// (and Parse + ValidateBundle reject mismatches).
	_ = body
	return buf.Bytes(), nil
}

// walkSkillDir writes every regular file under root into tw with paths
// prefixed by leading/. Symlinks, sockets and other non-regular entries
// are skipped (mirrors internal/skills.bundle.walkAndAdd).
func walkSkillDir(root, leading string, tw *tar.Writer) error {
	var total int64
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("header %s: %w", rel, err)
		}
		hdr.Name = leading + "/" + rel
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write header %s: %w", rel, err)
		}
		if info.IsDir() {
			return nil
		}
		total += info.Size()
		if total > maxSkillDirBytes {
			return fmt.Errorf("skill dir exceeds %d bytes uncompressed", maxSkillDirBytes)
		}
		return copyIntoTar(path, tw)
	})
}

func copyIntoTar(path string, tw *tar.Writer) error {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("copy %s: %w", path, err)
	}
	return nil
}

// archiveDir moves src (a directory or a regular file) into
// archiveRoot/<basename(src)>. The archive root is created with mode 0700
// to match the data-dir hardening posture. Returns the destination path
// on success.
//
// archiveRoot=="" skips archiving and returns "" with no error — used by
// callers that want the registry side-effect without the move.
func archiveDir(src, archiveRoot string) (string, error) {
	if archiveRoot == "" {
		return "", nil
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", fmt.Errorf("abs src: %w", err)
	}
	dst := filepath.Join(archiveRoot, filepath.Base(abs))
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		return "", fmt.Errorf("mkdir archive root: %w", err)
	}
	// Guard against accidental overwrite — if dst already exists, append
	// a numeric suffix so the user can audit both copies.
	dst = uniquePath(dst)
	if err := os.Rename(abs, dst); err != nil {
		// Cross-device rename falls back to copy+remove. For a same-FS
		// move under ~/.claude/skills/.migrated/ this branch is rare.
		info, statErr := os.Stat(abs)
		if statErr != nil {
			return "", fmt.Errorf("archive move: %w", err)
		}
		if info.Mode().IsRegular() {
			if err := copyFileThenRemove(abs, dst); err != nil {
				return "", fmt.Errorf("archive move: %w", err)
			}
		} else if err := copyDirThenRemove(abs, dst); err != nil {
			return "", fmt.Errorf("archive move: %w", err)
		}
	}
	return dst, nil
}

// copyFileThenRemove is the regular-file analogue of copyDirThenRemove.
func copyFileThenRemove(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec
	if err != nil {
		return err
	}
	defer in.Close()                                                        //nolint:errcheck
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec
	if err != nil {
		return err
	}
	defer out.Close() //nolint:errcheck
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Remove(src)
}

// uniquePath appends -1, -2, ... until the path doesn't exist.
func uniquePath(p string) string {
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", p, i)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return p
}

// copyDirThenRemove is the fallback when os.Rename fails (e.g. crossing
// filesystems). It recursively copies the tree then removes the source.
func copyDirThenRemove(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path) //nolint:gosec
		if err != nil {
			return err
		}
		defer in.Close()                                                                        //nolint:errcheck
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm()) //nolint:gosec
		if err != nil {
			return err
		}
		defer out.Close() //nolint:errcheck
		_, err = io.Copy(out, in)
		return err
	})
	if walkErr != nil {
		return walkErr
	}
	return os.RemoveAll(src)
}

// ExpandUserHome resolves a leading `~` (or `~/`) in p to the current
// user's home directory. Returns p unchanged when no expansion applies.
// Convenience helper used by the CLI and API to honour `--source=~/.claude/skills`.
func ExpandUserHome(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
