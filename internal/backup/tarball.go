package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// writeTarball produces tarPath containing manifest.json, the snapshot
// DB (renamed to mcplexer.db), and every resolved artifact (master key,
// config, api-key, secrets/, addons/, and — when opted in — identity
// files). arts is the pre-filtered set of artifacts known to exist.
func writeTarball(tarPath string, mf Manifest, snapshotDB string, arts []artifact) (err error) {
	out, err := os.OpenFile(tarPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	// out.Sync() forces dirty pages to the device before close so a
	// disk-full / quota / I/O error surfaces here rather than being
	// reported as a successful — but truncated, unrestorable — backup
	// (the worst failure mode for a backup tool). Capture into the named
	// return only when no earlier error already failed the call.
	defer func() {
		if err == nil {
			if cerr := out.Sync(); cerr != nil {
				err = cerr
			}
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()

	return writeTarballStream(out, mf, snapshotDB, arts)
}

// writeTarballStream writes the gzipped tar to w. It is split out from
// writeTarball so the streaming-write + Close-flush error path is testable
// against an arbitrary io.Writer (the file open + fsync stay in the caller).
//
// The gzip + tar writers MUST be Closed before w is considered complete:
// gzip.Writer.Close() flushes the final compressed bytes and
// tar.Writer.Close() flushes padding + the footer. Earlier code swallowed
// those Close errors via bare defers, so a failed final write produced a
// nil error for a corrupt archive. We now close tw then gz in order and
// surface the FIRST non-nil error via the named return.
func writeTarballStream(w io.Writer, mf Manifest, snapshotDB string, arts []artifact) (err error) {
	// Encode manifest first so its size is part of the on-disk tarball
	// — we don't yet know the final size, so SizeBytes is set to 0 and
	// patched in patchManifestSize after the file lands. Keeps writing
	// streaming + cheap.
	mfBytes, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	defer func() {
		if cerr := tw.Close(); err == nil {
			err = cerr
		}
		if cerr := gz.Close(); err == nil {
			err = cerr
		}
	}()

	if err := writeTarFile(tw, "manifest.json", mfBytes); err != nil {
		return err
	}

	if err := writeTarFromPath(tw, "mcplexer.db", snapshotDB); err != nil {
		return err
	}

	for _, a := range arts {
		if a.isTree {
			if err := writeTarTree(tw, a.name, a.src); err != nil {
				return err
			}
			continue
		}
		if err := writeTarFromPath(tw, a.name, a.src); err != nil {
			return err
		}
	}
	return nil
}

func writeTarFile(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o600,
		Size: int64(len(data)),
	}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func writeTarFromPath(tw *tar.Writer, nameInTar, srcPath string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: nameInTar,
		Mode: 0o600,
		Size: info.Size(),
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// writeTarTree walks srcDir and writes every regular file beneath it,
// rooted at prefixInTar/. Skips symlinks/devices to keep the archive
// portable and to avoid loops.
func writeTarTree(tw *tar.Writer, prefixInTar, srcDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: filepath.ToSlash(filepath.Join(prefixInTar, rel)),
			Mode: 0o600,
			Size: info.Size(),
		}); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	})
}

// patchManifestSize updates the SizeBytes field in the on-disk
// manifest.json after we know the tarball's final size. Re-streams the
// tarball through a buffer with the manifest replaced.
func patchManifestSize(tarPath string, mf Manifest) error {
	in, err := os.ReadFile(tarPath)
	if err != nil {
		return err
	}

	out := &bytes.Buffer{}
	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)

	gz, err := gzip.NewReader(bytes.NewReader(in))
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	mfBytes, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name == "manifest.json" {
			hdr.Size = int64(len(mfBytes))
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := tw.Write(mfBytes); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	return os.WriteFile(tarPath, out.Bytes(), 0o600)
}
