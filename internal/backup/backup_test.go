package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// fakeDataDir builds a self-contained data dir in t.TempDir() populated
// with every artifact a real install would carry, and returns the data dir
// and db path. The db is a real (tiny) SQLite file so VACUUM INTO works.
func fakeDataDir(t *testing.T) (dataDir, dbPath string) {
	t.Helper()
	dataDir = t.TempDir()
	dbPath = filepath.Join(dataDir, "mcplexer.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t (v) VALUES ('hello')"); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	writeTestFile(t, dbPath+".age", "MASTER-AGE-KEY")
	writeTestFile(t, filepath.Join(dataDir, "mcplexer.yaml"), "servers: []\n")
	writeTestFile(t, filepath.Join(dataDir, "api-key"), "API-TOKEN-123")
	writeTestFile(t, filepath.Join(dataDir, "addons", "foo.yaml"), "name: foo\n")
	writeTestFile(t, filepath.Join(dataDir, "secrets", "vault.age"), "ENCRYPTED-SECRET")
	writeTestFile(t, filepath.Join(dataDir, "skills", "demo", "SKILL.md"), "---\nname: demo\n---\n")
	writeTestFile(t, filepath.Join(dataDir, "p2p", "identity.key.age"), "P2P-IDENTITY")
	writeTestFile(t, filepath.Join(dataDir, "secret-transfer.age.key"), "XFER-KEY")
	return dataDir, dbPath
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// tarNames returns the set of entry names inside a tarball.
func tarNames(t *testing.T, tarPath string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open tar: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names[hdr.Name] = true
	}
	return names
}

func TestCreate_ArtifactInclusion(t *testing.T) {
	tests := []struct {
		name            string
		includeIdentity bool
		wantPresent     []string
		wantAbsent      []string
		wantIdentity    bool
	}{
		{
			name:            "without identity",
			includeIdentity: false,
			wantPresent:     []string{"manifest.json", "mcplexer.db", "db.age", "mcplexer.yaml", "api-key", "addons/foo.yaml", "secrets/vault.age", "skills/demo/SKILL.md"},
			wantAbsent:      []string{"p2p/identity.key.age", "secret-transfer.age.key"},
			wantIdentity:    false,
		},
		{
			name:            "with identity",
			includeIdentity: true,
			wantPresent:     []string{"manifest.json", "mcplexer.db", "db.age", "mcplexer.yaml", "api-key", "addons/foo.yaml", "secrets/vault.age", "skills/demo/SKILL.md", "p2p/identity.key.age", "secret-transfer.age.key"},
			wantAbsent:      nil,
			wantIdentity:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dataDir, dbPath := fakeDataDir(t)
			svc := New(dataDir, dbPath, "test-1.0")

			mf, err := svc.Create(context.Background(), "note", tc.includeIdentity)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			tarPath, err := svc.Path(mf.ID)
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			names := tarNames(t, tarPath)
			for _, n := range tc.wantPresent {
				if !names[n] {
					t.Errorf("expected %q present in tarball, got %v", n, keys(names))
				}
			}
			for _, n := range tc.wantAbsent {
				if names[n] {
					t.Errorf("expected %q ABSENT from tarball", n)
				}
			}

			if mf.SchemaVersion != currentSchemaVersion {
				t.Errorf("SchemaVersion = %d, want %d", mf.SchemaVersion, currentSchemaVersion)
			}
			if !mf.IncludesMasterKey {
				t.Error("IncludesMasterKey = false, want true")
			}
			if !mf.IncludesConfig {
				t.Error("IncludesConfig = false, want true")
			}
			if !mf.IncludesSecrets {
				t.Error("IncludesSecrets = false, want true")
			}
			if !mf.IncludesSkills {
				t.Error("IncludesSkills = false, want true")
			}
			if mf.IncludesIdentity != tc.wantIdentity {
				t.Errorf("IncludesIdentity = %v, want %v", mf.IncludesIdentity, tc.wantIdentity)
			}

			// Manifest read back from disk must agree with the returned one.
			got, err := svc.Get(mf.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.IncludesIdentity != tc.wantIdentity || !got.IncludesSkills ||
				got.SchemaVersion != currentSchemaVersion {
				t.Errorf("on-disk manifest drift: %+v", got)
			}
		})
	}
}

func TestRestore_RoundTrip(t *testing.T) {
	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test-1.0")

	// Capture the original bytes of every artifact we expect to be restored.
	captured := map[string]string{
		dbPath + ".age":                                      "",
		filepath.Join(dataDir, "mcplexer.yaml"):              "",
		filepath.Join(dataDir, "api-key"):                    "",
		filepath.Join(dataDir, "addons", "foo.yaml"):         "",
		filepath.Join(dataDir, "secrets", "vault.age"):       "",
		filepath.Join(dataDir, "skills", "demo", "SKILL.md"): "",
		filepath.Join(dataDir, "p2p", "identity.key.age"):    "",
		filepath.Join(dataDir, "secret-transfer.age.key"):    "",
	}
	for p := range captured {
		captured[p] = sha256Of(t, p)
	}
	dbSum := sha256Of(t, dbPath)

	// Backup WITH identity so every artifact is in the tarball.
	mf, err := svc.Create(context.Background(), "pre-mutate", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Mutate/delete the originals to prove restore actually replaces them.
	if err := os.Remove(dbPath + ".age"); err != nil {
		t.Fatalf("rm master key: %v", err)
	}
	writeTestFile(t, filepath.Join(dataDir, "api-key"), "TAMPERED")
	if err := os.RemoveAll(filepath.Join(dataDir, "secrets")); err != nil {
		t.Fatalf("rm secrets: %v", err)
	}
	writeTestFile(t, filepath.Join(dataDir, "addons", "foo.yaml"), "name: tampered\n")
	writeTestFile(t, filepath.Join(dataDir, "skills", "demo", "SKILL.md"), "TAMPERED")
	writeTestFile(t, filepath.Join(dataDir, "p2p", "identity.key.age"), "TAMPERED")

	if _, err := svc.Restore(context.Background(), mf.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Every captured artifact must be byte-identical to the backed-up copy.
	for p, want := range captured {
		if got := sha256Of(t, p); got != want {
			t.Errorf("artifact %s sha256 = %s, want %s", p, got, want)
		}
	}
	// The DB restored via VACUUM INTO will not be byte-identical to the
	// original on-disk file (VACUUM rewrites pages), so assert content
	// equivalence by reading the row back instead.
	_ = dbSum
	assertDBRow(t, dbPath)
}

func TestRestore_MissingDB(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "mcplexer.db")
	svc := New(dataDir, dbPath, "test")

	// Hand-craft a tarball with manifest + secrets but no mcplexer.db.
	tarPath := filepath.Join(dataDir, "backups", "bad.tar.gz")
	if err := os.MkdirAll(filepath.Dir(tarPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeRawTarball(t, tarPath, map[string]string{
		"manifest.json": `{"id":"bad"}`,
		"secrets/x.age": "data",
	})

	targets := svc.restoreTargets()
	err := applyBackup(tarPath, dataDir, dbPath, targets)
	if err == nil {
		t.Fatal("expected error when tarball has no mcplexer.db")
	}
}

func TestRestore_BackwardCompatV1(t *testing.T) {
	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test")

	// A v1 tarball: manifest (no schema_version) + db + secrets only.
	tarPath := filepath.Join(dataDir, "backups", "20200101-000000-v1abc.tar.gz")
	if err := os.MkdirAll(filepath.Dir(tarPath), 0o700); err != nil {
		t.Fatal(err)
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	writeRawTarball(t, tarPath, map[string]string{
		"manifest.json":  `{"id":"20200101-000000-v1abc","db_sha256":"x","includes_secrets":true}`,
		"mcplexer.db":    string(dbBytes),
		"secrets/v1.age": "V1-SECRET",
	})

	targets := svc.restoreTargets()
	if err := applyBackup(tarPath, dataDir, dbPath, targets); err != nil {
		t.Fatalf("v1 restore failed: %v", err)
	}
	// The v1 secret must have landed.
	if got := mustRead(t, filepath.Join(dataDir, "secrets", "v1.age")); got != "V1-SECRET" {
		t.Errorf("v1 secret = %q, want V1-SECRET", got)
	}
}

func TestRestore_PathTraversalRejected(t *testing.T) {
	dataDir, dbPath := fakeDataDir(t)
	svc := New(dataDir, dbPath, "test")

	tarPath := filepath.Join(dataDir, "backups", "evil.tar.gz")
	if err := os.MkdirAll(filepath.Dir(tarPath), 0o700); err != nil {
		t.Fatal(err)
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	writeRawTarball(t, tarPath, map[string]string{
		"manifest.json":    `{"id":"evil"}`,
		"mcplexer.db":      string(dbBytes),
		"addons/../evil":   "PWNED",
		"secrets/../evil2": "PWNED2",
	})

	targets := svc.restoreTargets()
	// A tarball with a path-traversal entry must be rejected outright, not
	// silently skipped — silent-skip lets a hostile archive hide data.
	err = applyBackup(tarPath, dataDir, dbPath, targets)
	if err == nil {
		t.Fatal("restore accepted a tarball with path-traversal entries; want error")
	}
	if !strings.Contains(err.Error(), "path-traversal") {
		t.Fatalf("want path-traversal error, got: %v", err)
	}
	// The traversal targets must NOT have been written anywhere outside.
	if _, err := os.Stat(filepath.Join(dataDir, "evil")); err == nil {
		t.Error("path traversal addons/../evil escaped into dataDir/evil")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "evil2")); err == nil {
		t.Error("path traversal secrets/../evil2 escaped into dataDir/evil2")
	}
}

// --- helpers ---

func sha256Of(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	sum := sha256.Sum256(data)
	return string(sum[:])
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(data)
}

func assertDBRow(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var v string
	if err := db.QueryRow("SELECT v FROM t WHERE id = 1").Scan(&v); err != nil {
		t.Fatalf("query restored db: %v", err)
	}
	if v != "hello" {
		t.Errorf("restored db row = %q, want hello", v)
	}
}

func writeRawTarball(t *testing.T, tarPath string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tarPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
