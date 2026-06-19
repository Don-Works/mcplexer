package brain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldRepo_Idempotent(t *testing.T) {
	dir := t.TempDir()

	if err := ScaffoldRepo(dir); err != nil {
		t.Fatalf("ScaffoldRepo(first): %v", err)
	}

	// Assert the scaffold files + their contents.
	assertFileContains(t, filepath.Join(dir, ".gitignore"), "brain-index.db")
	assertFileContains(t, filepath.Join(dir, ".gitignore"), ".attachments/")
	assertFileContains(t, filepath.Join(dir, ".gitattributes"), "*.jsonl text merge=union")
	assertFileContains(t, filepath.Join(dir, ".gitattributes"), "eol=lf")
	assertFileContains(t, filepath.Join(dir, "README.md"), "MCPlexer Brain")

	// brain.json parses with the expected schema version.
	manifest := readFile(t, filepath.Join(dir, "brain.json"))
	var m brainManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("brain.json invalid: %v", err)
	}
	if m.SchemaVersion != BrainSchemaVersion {
		t.Fatalf("brain.json schema_version = %d, want %d", m.SchemaVersion, BrainSchemaVersion)
	}

	// Directory structure exists.
	for _, sub := range []string{"global/config", "global/secrets", "workspaces", "clients"} {
		if fi, err := os.Stat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			t.Fatalf("expected dir %s: err=%v", sub, err)
		}
	}

	// Mutate a file, then re-run: idempotent run must NOT clobber it.
	custom := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(custom, []byte("# hand-edited\n"), 0o644); err != nil {
		t.Fatalf("write custom: %v", err)
	}
	if err := ScaffoldRepo(dir); err != nil {
		t.Fatalf("ScaffoldRepo(second): %v", err)
	}
	if got := string(readFile(t, custom)); got != "# hand-edited\n" {
		t.Fatalf("second ScaffoldRepo clobbered hand-edited file: %q", got)
	}
}

func TestScaffoldRepo_EmptyDir(t *testing.T) {
	if err := ScaffoldRepo(""); err == nil {
		t.Fatal("ScaffoldRepo(\"\"): expected error")
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	got := string(readFile(t, path))
	if !strings.Contains(got, want) {
		t.Fatalf("file %s does not contain %q\n--- contents ---\n%s", path, want, got)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
