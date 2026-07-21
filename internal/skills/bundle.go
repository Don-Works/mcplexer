// Package skills — gzip+tar packing and extraction for .mcskill bundles.
//
// Wire format: gzip-compressed POSIX tar of a skill directory. Files are
// stored relative to the bundle root, with manifest.toml mandatory at the
// root. Symlinks, hardlinks, devices, and parent-traversal entries
// ("../foo") are rejected at extract time.
package skills

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

// manifestFilename is the canonical path of the manifest inside a bundle.
const manifestFilename = "manifest.toml"

// PackDir reads a skill directory rooted at srcDir and writes a gzipped tar
// to dst. The on-disk layout:
//
//	srcDir/
//	  manifest.toml      (required)
//	  skill.md           (entry_point — required for usable bundles)
//	  README.md          (optional)
//	  scripts/...        (optional)
//	  assets/...         (optional)
//
// Returns the raw bundle bytes too so callers (e.g. the CLI) can sign them
// without re-reading the file.
func PackDir(srcDir string) ([]byte, error) {
	info, err := os.Stat(srcDir)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrBundleMalformed, srcDir)
	}
	if _, err := os.Stat(filepath.Join(srcDir, manifestFilename)); err != nil {
		return nil, fmt.Errorf("%w: %s missing", ErrBundleMalformed, manifestFilename)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := walkAndAdd(srcDir, tw); err != nil {
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
	return buf.Bytes(), nil
}

// walkAndAdd writes every regular file under root into tw with paths
// relative to root. Symlinks and special files are skipped to keep bundles
// reproducible and safe to extract.
func walkAndAdd(root string, tw *tar.Writer) error {
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
			return nil // skip symlinks, devices, sockets, etc.
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("header %s: %w", rel, err)
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write header %s: %w", rel, err)
		}
		if info.IsDir() {
			return nil
		}
		return copyFileIntoTar(path, tw)
	})
}

func copyFileIntoTar(path string, tw *tar.Writer) error {
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

// manifestFromBundle reads only the manifest.toml entry from bundleBytes.
// It does not extract any other file — used during the signature check
// and capability review before we touch disk.
func manifestFromBundle(bundleBytes []byte) (*Manifest, error) {
	tr, closer, err := openBundle(bundleBytes)
	if err != nil {
		return nil, err
	}
	defer closer()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBundleMalformed, err)
		}
		if hdr.Name != manifestFilename {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, MaxBundleSize))
		if err != nil {
			return nil, fmt.Errorf("read manifest: %w", err)
		}
		return Parse(data)
	}
	return nil, fmt.Errorf("%w: %s missing", ErrBundleMalformed, manifestFilename)
}

// stageBundle extracts bundleBytes into <skillsDir>/.staging-<name>/. The
// caller renames this dir into place after the DB row is committed.
func stageBundle(skillsDir, name string, bundleBytes []byte) (string, error) {
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", skillsDir, err)
	}
	stagedDir, err := os.MkdirTemp(skillsDir, ".staging-"+name+"-*")
	if err != nil {
		return "", fmt.Errorf("mkdir staging: %w", err)
	}
	if err := extractBundle(bundleBytes, stagedDir); err != nil {
		_ = os.RemoveAll(stagedDir)
		return "", err
	}
	return stagedDir, nil
}

// extractBundle un-tars bundleBytes into destDir. Refuses entries whose
// resolved path escapes destDir (zip-slip), and skips non-regular non-dir
// entries (symlinks, devices, etc.).
func extractBundle(bundleBytes []byte, destDir string) error {
	tr, closer, err := openBundle(bundleBytes)
	if err != nil {
		return err
	}
	defer closer()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrBundleMalformed, err)
		}
		if err := extractEntry(tr, hdr, destDir); err != nil {
			return err
		}
	}
}

// extractEntry writes one tar entry into destDir, enforcing the safety
// invariants documented on extractBundle.
func extractEntry(tr *tar.Reader, hdr *tar.Header, destDir string) error {
	cleaned := filepath.Clean(hdr.Name)
	if cleaned == "." || cleaned == "/" {
		return nil
	}
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") ||
		filepath.IsAbs(cleaned) {
		return fmt.Errorf("%w: unsafe path %q", ErrBundleMalformed, hdr.Name)
	}
	target := filepath.Join(destDir, cleaned)
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		return writeRegular(tr, target, hdr)
	default:
		return nil // skip symlinks, devices, fifos, etc.
	}
}

func writeRegular(tr *tar.Reader, target string, hdr *tar.Header) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	mode := os.FileMode(hdr.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode) //nolint:gosec
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	defer f.Close() //nolint:errcheck
	if _, err := io.Copy(f, io.LimitReader(tr, MaxBundleSize)); err != nil {
		return fmt.Errorf("copy %s: %w", target, err)
	}
	return nil
}

// openBundle wraps gzip + tar readers around bundleBytes and returns a
// closer that releases the gzip reader.
func openBundle(bundleBytes []byte) (*tar.Reader, func(), error) {
	if len(bundleBytes) == 0 {
		return nil, func() {}, fmt.Errorf("%w: empty bundle", ErrBundleMalformed)
	}
	gz, err := gzip.NewReader(bytes.NewReader(bundleBytes))
	if err != nil {
		return nil, func() {}, fmt.Errorf("%w: %w", ErrBundleMalformed, err)
	}
	closer := func() { _ = gz.Close() }
	return tar.NewReader(gz), closer, nil
}
