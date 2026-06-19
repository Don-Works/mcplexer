package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// errAfterWriter passes the first failAt writes through to the wrapped
// buffer, then fails every subsequent write. Used to inject an I/O error
// during the gzip/tar Close flush (the footer + final compressed bytes),
// proving writeTarballStream surfaces that error instead of swallowing it.
type errAfterWriter struct {
	buf    *bytes.Buffer
	n      int
	failAt int
	err    error
}

func (w *errAfterWriter) Write(p []byte) (int, error) {
	w.n++
	if w.n > w.failAt {
		return 0, w.err
	}
	return w.buf.Write(p)
}

// TestWriteTarballStream_SurfacesCloseError is the regression test for the
// bug where gzip/tar Close() errors were discarded via bare defers, so a
// failed final flush produced a nil error for a truncated, unrestorable
// tarball. Each case fails writes at a different point; in every case the
// error MUST propagate.
func TestWriteTarballStream_SurfacesCloseError(t *testing.T) {
	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test")
	arts := existingArtifacts(svc.dataArtifacts())
	mf := Manifest{ID: "x", DBSHA256: "y"}

	injected := errors.New("injected write failure")

	// Trial run with no injected failure to learn how many writes the gzip
	// stream emits to the underlying writer for this payload. The last of
	// those writes is the one flushed by gz.Close() — failing exactly there
	// is the precise regression: an error on the closing flush that the old
	// bare-defer code swallowed.
	count := &errAfterWriter{buf: &bytes.Buffer{}, failAt: 1 << 30}
	if err := writeTarballStream(count, mf, dbPath, arts); err != nil {
		t.Fatalf("trial run: %v", err)
	}
	totalWrites := count.n
	if totalWrites < 1 {
		t.Fatalf("trial run produced %d writes, expected >=1", totalWrites)
	}

	tests := []struct {
		name   string
		failAt int // pass this many writes, then fail every subsequent write
	}{
		{name: "fail on first write", failAt: 0},
		{name: "fail mid-stream", failAt: 1},
		// Let every write but the last succeed; the last write is the
		// gz.Close() flush of the gzip footer/trailer.
		{name: "fail only on final close flush", failAt: totalWrites - 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &errAfterWriter{buf: &bytes.Buffer{}, failAt: tc.failAt, err: injected}
			err := writeTarballStream(w, mf, dbPath, arts)
			if err == nil {
				t.Fatal("expected writeTarballStream to surface the write/close error, got nil")
			}
			if !errors.Is(err, injected) {
				t.Fatalf("expected injected error, got %v", err)
			}
		})
	}
}

// TestWriteTarball_HappyPathFullyFlushed asserts the success path produces a
// complete, re-readable gzip+tar archive (footer present, every expected
// entry decodable) — i.e. the Sync/Close changes did not break the normal
// write path.
func TestWriteTarball_HappyPathFullyFlushed(t *testing.T) {
	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test")
	arts := existingArtifacts(svc.dataArtifacts())
	mf := Manifest{ID: "happy", DBSHA256: "z"}

	tarPath := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := writeTarball(tarPath, mf, dbPath, arts); err != nil {
		t.Fatalf("writeTarball: %v", err)
	}

	// Decode end-to-end: a truncated/un-footered archive would error here.
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	got := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next (archive truncated/corrupt?): %v", err)
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			t.Fatalf("read entry %s: %v", hdr.Name, err)
		}
		got[hdr.Name] = true
	}
	for _, want := range []string{"manifest.json", "mcplexer.db", "db.age"} {
		if !got[want] {
			t.Errorf("expected %q in fully-flushed tarball, got %v", want, keys(got))
		}
	}
}
