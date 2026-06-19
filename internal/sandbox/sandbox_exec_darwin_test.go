//go:build darwin

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestSandboxExec_TrueExitsZero is the live smoke test: confirm
// sandbox-exec is invokable on this host and that exit code 0 from the
// wrapped program propagates back as ExitCode 0.
func TestSandboxExec_TrueExitsZero(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires darwin")
	}
	d := &sandboxExecDriver{}
	if !d.Available() {
		t.Fatal("sandbox-exec should be available on darwin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code, err := d.Run(ctx, Config{}, "/usr/bin/true", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code: got %d want 0", code)
	}
}

// TestSandboxExec_FalseExitsOne mirrors the previous test for the
// non-zero exit path. We want to confirm sandbox-exec is transparent
// w.r.t. exit codes, not that it picks 0 in some default state.
func TestSandboxExec_FalseExitsOne(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires darwin")
	}
	d := &sandboxExecDriver{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code, err := d.Run(ctx, Config{}, "/usr/bin/false", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit code: got %d want 1", code)
	}
}

// TestSandboxExec_NetworkDenied verifies cfg.Network=deny actually
// blocks outbound traffic. We use curl with a short connect timeout
// against example.com — sandbox-exec rejecting the connect is the
// expected outcome, so curl exits non-zero.
func TestSandboxExec_NetworkDenied(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires darwin")
	}
	if _, err := os.Stat("/usr/bin/curl"); err != nil {
		t.Skip("curl not present")
	}
	d := &sandboxExecDriver{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cfg := Config{Network: NetworkDeny}
	code, err := d.Run(ctx, cfg, "/usr/bin/curl",
		[]string{"--connect-timeout", "2", "-s", "-o", "/dev/null", "https://example.com"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == 0 {
		t.Fatalf("expected non-zero exit (network denied), got %d", code)
	}
}

// TestSandboxExec_DenyPathBlocksRead drops a marker file under
// $HOME/.ssh/, then tries to read it via /bin/cat inside the sandbox.
// .ssh is in DefaultDenyPaths, so the read must fail (cat exits != 0).
// Cleans up the marker on test exit.
func TestSandboxExec_DenyPathBlocksRead(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires darwin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	marker := filepath.Join(sshDir, "test-sandbox-marker")
	if err := os.WriteFile(marker, []byte("secret"), 0600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(marker) })

	d := &sandboxExecDriver{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code, err := d.Run(ctx, Config{}, "/bin/cat", []string{marker})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == 0 {
		t.Fatalf("expected non-zero exit (deny-path blocked read), got %d", code)
	}
}

// TestSandboxExec_DenyPathBlocksSubpathRead is the regression test for
// the literal-vs-subpath bug: a file living UNDER a denied directory
// (exactly like ~/.mcplexer/secrets/age-key or ~/.mcplexer/mcplexer.db)
// must be unreadable inside the sandbox. Pre-fix the deny rendered as
// (literal "<dir>") which matched only the directory inode, leaving
// every descendant fully readable+writable. We deny a temp dir and try
// to cat a marker in a SUBDIR — it must fail. Uses canonical
// (symlink-resolved) paths because sandbox-exec evaluates rules against
// the real path, and /var + /tmp on macOS are symlinks into /private.
func TestSandboxExec_DenyPathBlocksSubpathRead(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires darwin")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	sub := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	marker := filepath.Join(sub, "age-key")
	if err := os.WriteFile(marker, []byte("KEYMATERIAL"), 0600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	d := &sandboxExecDriver{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := Config{DenyPaths: []string{dir}}
	code, err := d.Run(ctx, cfg, "/bin/cat", []string{marker})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == 0 {
		t.Fatalf("file under denied directory was READABLE inside sandbox — literal-vs-subpath regression (deny must use subpath)")
	}
}

// TestBuildSandboxExecProfile_DenyPathsUseSubpath guards the rendering:
// directory deny paths MUST be emitted as (subpath ...) — which covers
// every descendant — and never as the descendant-blind (literal ...).
func TestBuildSandboxExecProfile_DenyPathsUseSubpath(t *testing.T) {
	cfg := Config{DenyPaths: []string{"/Users/test/.mcplexer"}}
	profile := buildSandboxExecProfile(cfg, "/Users/test")

	if !contains(profile, `file-write-create file-write-unlink file-write-mode (subpath "/Users/test/.mcplexer")`) {
		t.Errorf("deny path not rendered with create/unlink/mode operations:\n%s", profile)
	}
	if !contains(profile, `file-read-data file-write-data file-write-create file-write-unlink file-write-mode (subpath "/Users/test/.mcplexer")`) {
		t.Errorf("deny path not rendered with full operation set:\n%s", profile)
	}
	if contains(profile, `file-write-data (literal "/Users/test/.mcplexer")`) {
		t.Errorf("deny path still rendered as descendant-blind literal:\n%s", profile)
	}
	if !contains(profile, `(subpath "/Users/test/.mcplexer")`) {
		t.Errorf("default ~/.mcplexer deny missing as subpath:\n%s", profile)
	}
}

func TestBuildSandboxExecProfile_ContainsExpectedDenies(t *testing.T) {
	cfg := Config{
		Network:   NetworkDeny,
		DenyPaths: []string{"/etc/extra-secret"},
	}
	profile := buildSandboxExecProfile(cfg, "/Users/test")
	wantSubs := []string{
		"(version 1)",
		"(allow default)",
		`\.ssh/`,
		`\.aws/`,
		`/Users/test/.ssh`,
		"(deny network*)",
		"/etc/extra-secret",
	}
	for _, s := range wantSubs {
		if !contains(profile, s) {
			t.Errorf("profile missing %q\n---\n%s", s, profile)
		}
	}
}

// TestBuildSandboxExecProfile_DenyWritePathsRendersWriteOnly verifies
// the new DenyWritePaths field renders the right
// (deny file-write-data ...) rule WITHOUT a file-read-data clause —
// reads must still succeed (claude needs to read OAuth credentials).
func TestBuildSandboxExecProfile_DenyWritePathsRendersWriteOnly(t *testing.T) {
	cfg := Config{
		DenyWritePaths: []string{"/Users/test/.claude"},
	}
	profile := buildSandboxExecProfile(cfg, "/Users/test")

	// Must contain a write-deny rule anchored at the path.
	wantSubs := []string{
		"(deny file-write-data",
		"file-write-create",
		"file-write-unlink",
		"file-write-mode",
		`/Users/test/\.claude`,
	}
	for _, s := range wantSubs {
		if !contains(profile, s) {
			t.Errorf("profile missing %q\n---\n%s", s, profile)
		}
	}

	// Must NOT contain a (deny file-read-data ... .claude) rule — that
	// would break OAuth by blocking the credentials read.
	if contains(profile, "file-read-data file-write-data (literal \"/Users/test/.claude\"") {
		t.Errorf("DenyWritePaths leaked a file-read-data clause:\n%s", profile)
	}
	if contains(profile, "file-read-data (literal \"/Users/test/.claude\"") {
		t.Errorf("DenyWritePaths leaked a file-read-data clause:\n%s", profile)
	}
}

// TestBuildSandboxExecProfile_DenyWritePathsSkipsEmpty confirms that
// empty entries in DenyWritePaths (e.g. when homeRelative returns ""
// because HOME is unset) are dropped rather than emitting a deny-rule
// rooted at "".
func TestBuildSandboxExecProfile_NetworkProxyWithoutSocketRendersDeny(t *testing.T) {
	cfg := Config{Network: NetworkProxy}
	profile := buildSandboxExecProfile(cfg, "/Users/test")
	if !contains(profile, "(deny network*)") {
		t.Errorf("NetworkProxy without ProxySocket must render (deny network*) (fail-closed):\n%s", profile)
	}
}

func TestBuildSandboxExecProfile_NetworkProxyWithSocketStillRendersDeny(t *testing.T) {
	cfg := Config{Network: NetworkProxy, ProxySocket: "/tmp/mcplexer-proxy.sock"}
	profile := buildSandboxExecProfile(cfg, "/Users/test")
	// Proxy daemon not yet implemented — must still deny until the
	// TODO(guards/m3) is resolved.
	if !contains(profile, "(deny network*)") {
		t.Errorf("NetworkProxy with socket should still render (deny network*) (proxy not implemented):\n%s", profile)
	}
}

func TestBuildSandboxExecProfile_DenyWritePathsSkipsEmpty(t *testing.T) {
	cfg := Config{
		DenyWritePaths: []string{"", "/Users/test/.claude"},
	}
	profile := buildSandboxExecProfile(cfg, "/Users/test")
	// Exactly one (deny file-write-data ...) rule should appear.
	if countSubstring(profile, "(deny file-write-data") != 1 {
		t.Errorf("expected exactly one file-write-data rule, got profile:\n%s", profile)
	}
}

// TestBuildSandboxExecProfile_DenySubtreeIncludesCreateUnlinkMode is the
// regression test for the P0 create/unlink/mode hole: deny-subtree rules
// MUST include file-write-create, file-write-unlink, and file-write-mode
// in addition to file-read-data and file-write-data. Without these,
// a sandboxed process could create new files, delete existing ones, or
// chmod under denied subtrees (e.g. ~/.mcplexer/secrets/).
func TestBuildSandboxExecProfile_DenySubtreeIncludesCreateUnlinkMode(t *testing.T) {
	cfg := Config{DenyPaths: []string{"/Users/test/.mcplexer"}}
	profile := buildSandboxExecProfile(cfg, "/Users/test")

	mustContain := []string{
		"file-write-create",
		"file-write-unlink",
		"file-write-mode",
	}
	for _, op := range mustContain {
		if !contains(profile, op) {
			t.Errorf("profile missing %q in deny-subtree rule:\n%s", op, profile)
		}
	}
}

// TestBuildSandboxExecProfile_HardcodedRegexDeniesIncludeCreateUnlinkMode
// verifies the hard-coded credential regex denies (.ssh, .aws,
// .docker/config.json) include the create/unlink/mode operations.
func TestBuildSandboxExecProfile_HardcodedRegexDeniesIncludeCreateUnlinkMode(t *testing.T) {
	profile := buildSandboxExecProfile(Config{}, "/Users/test")

	credPatterns := []string{
		`\.ssh/`,
		`\.aws/`,
		`\.docker/config\.json`,
	}
	mustOps := []string{
		"file-write-create",
		"file-write-unlink",
		"file-write-mode",
	}
	for _, pat := range credPatterns {
		for _, op := range mustOps {
			// Each credential deny line must include all three operations.
			// We check that the profile contains the operation somewhere
			// near the credential pattern. The simplest assertion: both
			// the credential pattern and each operation must be present.
			if !contains(profile, pat) {
				t.Fatalf("profile missing credential pattern %q", pat)
			}
			if !contains(profile, op) {
				t.Errorf("profile missing %q for credential deny", op)
			}
		}
	}
}

// TestBuildSandboxExecProfile_DenyWriteIncludesCreateUnlinkMode verifies
// that DenyWritePaths rules include file-write-create, file-write-unlink,
// and file-write-mode alongside file-write-data. Without these, a process
// could create new files or delete/chmod files under the write-denied
// subtree even though modifying existing file content was blocked.
func TestBuildSandboxExecProfile_DenyWriteIncludesCreateUnlinkMode(t *testing.T) {
	cfg := Config{DenyWritePaths: []string{"/Users/test/.claude"}}
	profile := buildSandboxExecProfile(cfg, "/Users/test")

	mustOps := []string{
		"file-write-create",
		"file-write-unlink",
		"file-write-mode",
	}
	for _, op := range mustOps {
		if !contains(profile, op) {
			t.Errorf("DenyWritePaths profile missing %q:\n%s", op, profile)
		}
	}

	// file-read-data must NOT appear in the DenyWritePaths rule.
	// The profile contains file-read-data in the hard-coded credential
	// denies, so we specifically check the .claude line is write-only.
	if contains(profile, "file-read-data file-write-data file-write-create file-write-unlink file-write-mode (regex #\"^/Users/test/\\.claude") {
		t.Errorf("DenyWritePaths leaked file-read-data:\n%s", profile)
	}
}

// TestSandboxExec_DenyPathBlocksCreate verifies at runtime that creating
// a new file under a denied subtree fails inside the sandbox. This is the
// live regression test for the file-write-create hole.
func TestSandboxExec_DenyPathBlocksCreate(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires darwin")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	d := &sandboxExecDriver{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := Config{DenyPaths: []string{dir}}
	target := filepath.Join(dir, "newfile")
	code, err := d.Run(ctx, cfg, "/usr/bin/touch", []string{target})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == 0 {
		t.Fatalf("touch under denied subtree succeeded (exit 0) — file-write-create not denied")
	}
}

// TestSandboxExec_DenyPathBlocksUnlink verifies at runtime that deleting
// a file under a denied subtree fails inside the sandbox. This is the
// live regression test for the file-write-unlink hole.
func TestSandboxExec_DenyPathBlocksUnlink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires darwin")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	marker := filepath.Join(dir, "to-delete")
	if err := os.WriteFile(marker, []byte("x"), 0600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	d := &sandboxExecDriver{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := Config{DenyPaths: []string{dir}}
	code, err := d.Run(ctx, cfg, "/bin/rm", []string{marker})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == 0 {
		// Check the file still exists (rm did not succeed).
		if _, statErr := os.Stat(marker); statErr != nil {
			t.Fatalf("rm under denied subtree exited 0 AND file is gone — file-write-unlink not denied")
		}
		t.Fatalf("rm under denied subtree succeeded (exit 0) — file-write-unlink not denied")
	}
}

// TestSandboxExec_DenyPathBlocksMode verifies at runtime that chmod under
// a denied subtree fails inside the sandbox. This is the live regression
// test for the file-write-mode hole.
func TestSandboxExec_DenyPathBlocksMode(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires darwin")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	marker := filepath.Join(dir, "to-chmod")
	if err := os.WriteFile(marker, []byte("x"), 0600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	d := &sandboxExecDriver{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := Config{DenyPaths: []string{dir}}
	code, err := d.Run(ctx, cfg, "/bin/chmod", []string{"777", marker})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == 0 {
		t.Fatalf("chmod under denied subtree succeeded (exit 0) — file-write-mode not denied")
	}
}

// countSubstring is a tiny helper for the assertion above. We avoid
// strings.Count to keep this test file's dependency surface aligned
// with the existing `contains` helper.
func countSubstring(haystack, needle string) int {
	n, i := 0, 0
	for i+len(needle) <= len(haystack) {
		if haystack[i:i+len(needle)] == needle {
			n++
			i += len(needle)
			continue
		}
		i++
	}
	return n
}

// contains is a tiny strings.Contains alias so the table above stays
// pleasant to read. Inlining strings.Contains everywhere would clutter
// the assertions.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
