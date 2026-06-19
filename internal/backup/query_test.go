package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "typical backup id", id: "20200101-000000-abc123", want: true},
		{name: "underscores and dashes", id: "a_b-C_9", want: true},
		{name: "single char", id: "x", want: true},
		{name: "max length 64", id: repeat("a", 64), want: true},
		{name: "empty", id: "", want: false},
		{name: "overlong 65", id: repeat("a", 65), want: false},
		{name: "parent traversal", id: "../x", want: false},
		{name: "slash", id: "a/b", want: false},
		{name: "dot segment", id: "..", want: false},
		{name: "backslash", id: `a\b`, want: false},
		{name: "space", id: "a b", want: false},
		{name: "null byte", id: "a\x00b", want: false},
		{name: "tilde", id: "~", want: false},
		{name: "dot extension", id: "abc.tar", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := validID(tc.id); got != tc.want {
				t.Errorf("validID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

func TestList(t *testing.T) {
	t.Run("missing backup dir returns empty", func(t *testing.T) {
		dataDir := t.TempDir()
		svc := New(dataDir, filepath.Join(dataDir, "mcplexer.db"), "test")
		got, err := svc.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("List on missing dir = %d entries, want 0", len(got))
		}
		if got == nil {
			t.Error("List should return a non-nil empty slice")
		}
	})

	t.Run("sorted newest-first and corrupt tarball skipped", func(t *testing.T) {
		dataDir, dbPath := fakeDataDir(t)
		svc := New(dataDir, dbPath, "test")

		// Two real backups. The id timestamp prefix orders them; create with
		// distinct ids by hand-stamping CreatedAt via the manifest the tarball
		// carries. Easiest is two Create calls — their ids differ by suffix and
		// CreatedAt by call time.
		first, err := svc.Create(context.Background(), "first", false)
		if err != nil {
			t.Fatalf("Create first: %v", err)
		}
		second, err := svc.Create(context.Background(), "second", false)
		if err != nil {
			t.Fatalf("Create second: %v", err)
		}

		// A corrupt tarball that List must skip (not fail the whole listing).
		corrupt := filepath.Join(svc.backupDir, "20990101-000000-corrupt.tar.gz")
		if err := os.WriteFile(corrupt, []byte("not a gzip"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A non-tarball file that must be ignored entirely.
		if err := os.WriteFile(filepath.Join(svc.backupDir, "README.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		got, err := svc.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("List = %d backups, want 2 (corrupt + non-tarball skipped)", len(got))
		}
		// Newest first: second was created after first.
		if got[0].CreatedAt.Before(got[1].CreatedAt) {
			t.Errorf("List not sorted newest-first: %v then %v", got[0].CreatedAt, got[1].CreatedAt)
		}
		ids := map[string]bool{got[0].ID: true, got[1].ID: true}
		if !ids[first.ID] || !ids[second.ID] {
			t.Errorf("List ids = %v, want both %s and %s", ids, first.ID, second.ID)
		}
	})
}

func TestGetPathDelete_NotFound(t *testing.T) {
	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test")

	tests := []struct {
		name string
		id   string
	}{
		{name: "well-formed but missing", id: "20200101-000000-missing"},
		{name: "invalid traversal", id: "../escape"},
		{name: "invalid slash", id: "a/b"},
		{name: "empty", id: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Get(tc.id); !errors.Is(err, ErrNotFound) {
				t.Errorf("Get(%q) err = %v, want ErrNotFound", tc.id, err)
			}
			if _, err := svc.Path(tc.id); !errors.Is(err, ErrNotFound) {
				t.Errorf("Path(%q) err = %v, want ErrNotFound", tc.id, err)
			}
			if err := svc.Delete(tc.id); !errors.Is(err, ErrNotFound) {
				t.Errorf("Delete(%q) err = %v, want ErrNotFound", tc.id, err)
			}
		})
	}
}

func TestGetPathDelete_HappyPath(t *testing.T) {
	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test")

	mf, err := svc.Create(context.Background(), "x", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got, err := svc.Get(mf.ID); err != nil || got.ID != mf.ID {
		t.Fatalf("Get(%s) = (%+v, %v)", mf.ID, got, err)
	}
	p, err := svc.Path(mf.ID)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("Path returned non-existent file: %v", err)
	}

	if err := svc.Delete(mf.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Deleting again is ErrNotFound.
	if err := svc.Delete(mf.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Get(mf.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete err = %v, want ErrNotFound", err)
	}
}
