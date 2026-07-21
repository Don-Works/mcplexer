package skills_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"aead.dev/minisign"

	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// installFixture wires up a fresh sqlite store, a temp skills dir, and a
// signing keypair the tests share.
type installFixture struct {
	ctx       context.Context
	db        *sqlite.DB
	skillsDir string
	pub       *minisign.PublicKey
	priv      *minisign.PrivateKey
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	db, err := sqlite.New(ctx, filepath.Join(tmp, "mcplexer.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pub, priv, err := minisign.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return &installFixture{
		ctx: ctx, db: db,
		skillsDir: filepath.Join(tmp, "skills"),
		pub:       &pub, priv: &priv,
	}
}

// addTrust inserts the fixture's pubkey into trusted_signers.
func (f *installFixture) addTrust(t *testing.T, name string) {
	t.Helper()
	row := &store.TrustedSigner{
		PubkeyID:     skills.PublicKeyID(f.pub),
		PubkeyString: skills.FormatPublicKey(f.pub),
		Name:         name,
	}
	if err := f.db.AddTrustedSigner(f.ctx, row); err != nil {
		t.Fatalf("AddTrustedSigner: %v", err)
	}
}

// writeSkillSource writes a minimal valid skill source tree at <root>/<name>
// and returns the path. The skill declares zero MCP servers by default,
// network disabled, filesystem none.
func writeSkillSource(t *testing.T, root, name, version string, mcpServers []string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	mustWrite(t, filepath.Join(dir, "manifest.toml"),
		buildManifestTOML(name, version, mcpServers))
	mustWrite(t, filepath.Join(dir, "skill.md"), "# "+name+"\n\nA test skill.\n")
	return dir
}

// buildManifestTOML builds a minimal manifest.toml with optional MCP server
// dependencies. Kept separate so writeSkillSource stays compact.
func buildManifestTOML(name, version string, mcpServers []string) string {
	servers := ""
	for _, s := range mcpServers {
		servers += "  { name = \"" + s + "\" },\n"
	}
	manifest := "manifest_version = 1\n" +
		"name = \"" + name + "\"\n" +
		"version = \"" + version + "\"\n" +
		"description = \"test fixture skill\"\n" +
		"author = \"tester\"\n\n" +
		"[capabilities]\n"
	if servers != "" {
		manifest += "mcp_servers = [\n" + servers + "]\n"
	}
	manifest += "\n[capabilities.network]\nenabled = false\n\n" +
		"[capabilities.filesystem]\nmode = \"none\"\n"
	return manifest
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write %s: %v", path, err)
	}
}

// packAndSign packs srcDir into <dst>.mcskill and writes <dst>.mcskill.minisig
// using the fixture's keypair. Returns the bundle path.
func (f *installFixture) packAndSign(t *testing.T, srcDir, outPath string) string {
	t.Helper()
	bundle, err := skills.PackDir(srcDir)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}
	if err := os.WriteFile(outPath, bundle, 0o644); err != nil { //nolint:gosec
		t.Fatalf("write bundle: %v", err)
	}
	sig, err := skills.Sign(bundle, f.priv, "skill="+filepath.Base(srcDir))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := os.WriteFile(outPath+".minisig", sig, 0o644); err != nil { //nolint:gosec
		t.Fatalf("write sig: %v", err)
	}
	return outPath
}
