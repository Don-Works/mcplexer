package skills_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
)

// TestInstallRoundTrip is the headline integration check: pack a real skill
// directory, install it on the same daemon, verify list, then remove.
func TestInstallRoundTrip(t *testing.T) {
	f := newInstallFixture(t)
	f.addTrust(t, "test-key")

	srcDir := writeSkillSource(t, t.TempDir(), "round-trip", "0.1.0", nil)
	bundlePath := f.packAndSign(t, srcDir, filepath.Join(t.TempDir(), "rt.mcskill"))

	row, review, err := skills.Install(f.ctx, f.db, bundlePath, skills.InstallOptions{
		SkillsDir: f.skillsDir,
		Source:    "file:" + bundlePath,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if row.Name != "round-trip" || row.Version != "0.1.0" {
		t.Fatalf("row mismatch: %+v", row)
	}
	if review == nil || review.Manifest == nil || review.Manifest.Name != "round-trip" {
		t.Fatalf("review missing: %+v", review)
	}
	if review.SignerPubkey == "" {
		t.Fatalf("expected signer pubkey to be set after verify")
	}

	// list
	got, err := skills.List(f.ctx, f.db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "round-trip" {
		t.Fatalf("List: %+v", got)
	}

	// on-disk skill dir exists and has the expected files
	mfPath := filepath.Join(f.skillsDir, "round-trip", "manifest.toml")
	if _, err := os.Stat(mfPath); err != nil {
		t.Fatalf("manifest.toml missing on disk: %v", err)
	}

	// remove
	if err := skills.Remove(f.ctx, f.db, f.skillsDir, "round-trip"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, err = skills.List(f.ctx, f.db)
	if err != nil {
		t.Fatalf("List after remove: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(f.skillsDir, "round-trip")); !os.IsNotExist(err) {
		t.Fatalf("expected skill dir gone, stat err = %v", err)
	}
}

// TestInstall_RejectsBadSignature tampers with the bundle bytes after the
// signature is created and confirms install fails without leaving state.
func TestInstall_RejectsBadSignature(t *testing.T) {
	f := newInstallFixture(t)
	f.addTrust(t, "test-key")

	srcDir := writeSkillSource(t, t.TempDir(), "tamper", "0.1.0", nil)
	bundlePath := f.packAndSign(t, srcDir, filepath.Join(t.TempDir(), "tamper.mcskill"))

	// Corrupt the bundle bytes after signing.
	b, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(bundlePath, b, 0o644); err != nil { //nolint:gosec
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, err = skills.Install(f.ctx, f.db, bundlePath, skills.InstallOptions{
		SkillsDir: f.skillsDir, Source: "file:" + bundlePath,
	})
	if err == nil {
		t.Fatal("Install accepted tampered bundle")
	}
	if !errors.Is(err, skills.ErrInvalidSignature) && !errors.Is(err, skills.ErrBundleMalformed) {
		t.Fatalf("err = %v, want ErrInvalidSignature or ErrBundleMalformed", err)
	}
	// State must be untouched.
	if rows, _ := skills.List(f.ctx, f.db); len(rows) != 0 {
		t.Fatalf("expected empty list after failure, got %+v", rows)
	}
	if _, err := os.Stat(filepath.Join(f.skillsDir, "tamper")); !os.IsNotExist(err) {
		t.Fatalf("expected no on-disk skill dir, stat err = %v", err)
	}
}

// TestInstall_RejectsMissingCapability ensures bundles requesting MCP
// namespaces not in the local config are refused with a clear sentinel
// error and no partial state.
func TestInstall_RejectsMissingCapability(t *testing.T) {
	f := newInstallFixture(t)
	f.addTrust(t, "test-key")

	srcDir := writeSkillSource(t, t.TempDir(), "needs-gh", "0.1.0", []string{"github"})
	bundlePath := f.packAndSign(t, srcDir, filepath.Join(t.TempDir(), "needs-gh.mcskill"))

	_, review, err := skills.Install(f.ctx, f.db, bundlePath, skills.InstallOptions{
		SkillsDir: f.skillsDir, Source: "file:" + bundlePath,
	})
	if err == nil {
		t.Fatal("Install accepted bundle with missing capability")
	}
	if !errors.Is(err, skills.ErrCapabilityNotConfigured) {
		t.Fatalf("err = %v, want ErrCapabilityNotConfigured", err)
	}
	if review == nil || len(review.MissingMCP) == 0 || review.MissingMCP[0] != "github" {
		t.Fatalf("review.MissingMCP = %v, want [github]", review)
	}
	if rows, _ := skills.List(f.ctx, f.db); len(rows) != 0 {
		t.Fatalf("partial state after capability error: %+v", rows)
	}
}

// TestInstall_AcceptsConfiguredCapability creates a downstream_servers row
// and confirms the same bundle then installs cleanly.
func TestInstall_AcceptsConfiguredCapability(t *testing.T) {
	f := newInstallFixture(t)
	f.addTrust(t, "test-key")

	if err := f.db.CreateDownstreamServer(f.ctx, &store.DownstreamServer{
		Name: "github", Transport: "stdio",
		ToolNamespace: "github", RestartPolicy: "on-failure",
	}); err != nil {
		t.Fatalf("CreateDownstreamServer: %v", err)
	}

	srcDir := writeSkillSource(t, t.TempDir(), "uses-gh", "0.1.0", []string{"github"})
	bundlePath := f.packAndSign(t, srcDir, filepath.Join(t.TempDir(), "uses-gh.mcskill"))

	row, _, err := skills.Install(f.ctx, f.db, bundlePath, skills.InstallOptions{
		SkillsDir: f.skillsDir, Source: "file:" + bundlePath,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if row.Name != "uses-gh" {
		t.Fatalf("row name = %q", row.Name)
	}
}

// TestInstall_AlreadyInstalled checks that re-installing without --force is
// rejected with ErrSkillAlreadyInstalled.
func TestInstall_AlreadyInstalled(t *testing.T) {
	f := newInstallFixture(t)
	f.addTrust(t, "test-key")

	srcDir := writeSkillSource(t, t.TempDir(), "twice", "0.1.0", nil)
	bundlePath := f.packAndSign(t, srcDir, filepath.Join(t.TempDir(), "twice.mcskill"))

	if _, _, err := skills.Install(f.ctx, f.db, bundlePath, skills.InstallOptions{
		SkillsDir: f.skillsDir,
	}); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	_, _, err := skills.Install(f.ctx, f.db, bundlePath, skills.InstallOptions{
		SkillsDir: f.skillsDir,
	})
	if !errors.Is(err, skills.ErrSkillAlreadyInstalled) {
		t.Fatalf("err = %v, want ErrSkillAlreadyInstalled", err)
	}
}

// TestPackInstallRoundTrip confirms PackDir output round-trips through
// Install + on-disk extraction such that the unpacked manifest matches
// the source manifest.
func TestPackInstallRoundTrip(t *testing.T) {
	f := newInstallFixture(t)
	f.addTrust(t, "test-key")

	srcDir := writeSkillSource(t, t.TempDir(), "rt2", "1.2.3", nil)
	bundlePath := f.packAndSign(t, srcDir, filepath.Join(t.TempDir(), "rt2.mcskill"))

	if _, _, err := skills.Install(f.ctx, f.db, bundlePath, skills.InstallOptions{
		SkillsDir: f.skillsDir,
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	srcMf, err := os.ReadFile(filepath.Join(srcDir, "manifest.toml"))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	dstMf, err := os.ReadFile(filepath.Join(f.skillsDir, "rt2", "manifest.toml"))
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(srcMf) != string(dstMf) {
		t.Fatalf("manifest mismatch:\nsrc=%s\ndst=%s", srcMf, dstMf)
	}

	row, err := f.db.GetInstalledSkill(f.ctx, "rt2")
	if err != nil {
		t.Fatalf("GetInstalledSkill: %v", err)
	}
	var m skills.Manifest
	if err := json.Unmarshal(row.ManifestJSON, &m); err != nil {
		t.Fatalf("unmarshal stored manifest: %v", err)
	}
	if m.Name != "rt2" || m.Version != "1.2.3" {
		t.Fatalf("manifest fields mismatch: %+v", m)
	}
}

// TestRemove_NotInstalled exercises the sentinel error path.
func TestRemove_NotInstalled(t *testing.T) {
	f := newInstallFixture(t)
	err := skills.Remove(f.ctx, f.db, f.skillsDir, "missing")
	if !errors.Is(err, skills.ErrSkillNotInstalled) {
		t.Fatalf("err = %v, want ErrSkillNotInstalled", err)
	}
}

// TestInstall_UnsignedRejectedByDefault confirms a bundle without a sibling
// .minisig is refused unless AllowUnsigned=true.
func TestInstall_UnsignedRejectedByDefault(t *testing.T) {
	f := newInstallFixture(t)

	srcDir := writeSkillSource(t, t.TempDir(), "no-sig", "0.1.0", nil)
	bundle, err := skills.PackDir(srcDir)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "no-sig.mcskill")
	if err := os.WriteFile(bundlePath, bundle, 0o644); err != nil { //nolint:gosec
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, err = skills.Install(f.ctx, f.db, bundlePath, skills.InstallOptions{
		SkillsDir: f.skillsDir,
	})
	if err == nil {
		t.Fatal("Install accepted unsigned bundle without AllowUnsigned")
	}
}
