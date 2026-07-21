//go:build darwin

package sandbox

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildSandboxExecProfile_DenyByDefaultWithExplicitConfigGrants(t *testing.T) {
	cfg := Config{
		ReadOnlyPaths:  []string{"/Users/example/src/read-only"},
		ReadWritePaths: []string{"/Users/example/src/repo"},
		DenyWritePaths: []string{"/Users/example/.client"},
		DenyPaths:      []string{"/Users/example/src/repo/private"},
		WorkingDir:     "/Users/example/src/repo",
		Network:        NetworkHost,
	}
	profile := buildSandboxExecProfile(cfg, "/Users/example")

	want := []string{
		"(deny default)",
		`(allow file-read* file-test-existence (subpath "/Users/example/src/read-only"))`,
		`(allow file-read* file-test-existence file-write* (subpath "/Users/example/src/repo"))`,
		`(allow file-read* file-test-existence (subpath "/Users/example/.client"))`,
		`(deny file-write-data file-write-create file-write-unlink file-write-mode file-write* (regex #"^/Users/example/\.client(/|$)"))`,
		`(path-ancestors "/Users/example/src/repo")`,
		`(deny file-read* file-write* file-read-data file-write-data file-write-create file-write-unlink file-write-mode (subpath "/Users/example/src/repo/private"))`,
		`(literal "/private/etc/hosts")`,
		`(literal "/private/etc/resolv.conf")`,
		`(global-name "com.apple.SystemConfiguration.SCNetworkReachability")`,
		`(global-name "com.apple.dnssd.service")`,
		"(allow network*)",
	}
	for _, fragment := range want {
		if !strings.Contains(profile, fragment) {
			t.Errorf("profile missing %q\n---\n%s", fragment, profile)
		}
	}
	if strings.Contains(profile, "(allow default)") {
		t.Fatalf("deny-by-default profile contains allow-default:\n%s", profile)
	}
	if strings.Contains(profile, "(allow file-read*)") {
		t.Fatalf("profile contains an unfiltered file-read allow:\n%s", profile)
	}
}

func TestBuildSandboxExecProfile_ZeroValueFailsClosed(t *testing.T) {
	profile := buildSandboxExecProfile(Config{}, "/Users/example")
	if !strings.Contains(profile, "(deny default)") {
		t.Fatalf("zero config does not deny default:\n%s", profile)
	}
	if !strings.Contains(profile, "(deny network*)") {
		t.Fatalf("zero config does not deny network:\n%s", profile)
	}
	if strings.Contains(profile, "(allow network*)") {
		t.Fatalf("zero config unexpectedly allows host network:\n%s", profile)
	}
	if strings.Contains(profile, "com.apple.SystemConfiguration.SCNetworkReachability") ||
		strings.Contains(profile, "com.apple.dnssd.service") {
		t.Fatalf("zero config unexpectedly grants resolver services:\n%s", profile)
	}
	if strings.Contains(profile, `(allow file-read* file-test-existence (subpath "/Users/example"))`) {
		t.Fatalf("zero config unexpectedly exposes home:\n%s", profile)
	}
	if got := describeForPlatform(Config{}); !strings.Contains(got, "deny-net") {
		t.Fatalf("zero config description = %q, want deny-net", got)
	}
	if got := describeForPlatform(Config{Network: NetworkHost}); strings.Contains(got, "deny-net") {
		t.Fatalf("NetworkHost description = %q, unexpectedly says deny-net", got)
	}
}

func TestSandboxExec_ConfiguredRepoReadWriteAndWorkingDir(t *testing.T) {
	repo := canonicalTestDir(t)
	if err := os.WriteFile(filepath.Join(repo, "input.txt"), []byte("ready\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{ReadWritePaths: []string{repo}, WorkingDir: repo}
	code := runSandboxTest(t, cfg, "/bin/sh", "-c",
		`test "$(cat input.txt)" = ready && printf written > output.txt`)
	if code != 0 {
		t.Fatalf("configured repo command exit = %d, want 0", code)
	}
	got, err := os.ReadFile(filepath.Join(repo, "output.txt"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "written" {
		t.Fatalf("output = %q, want written", got)
	}
}

func TestSandboxExec_ReadOnlyPathAllowsReadButBlocksWrite(t *testing.T) {
	dir := canonicalTestDir(t)
	marker := filepath.Join(dir, "marker")
	if err := os.WriteFile(marker, []byte("readable"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ReadOnlyPaths: []string{dir}, WorkingDir: dir}

	if code := runSandboxTest(t, cfg, "/bin/cat", marker); code != 0 {
		t.Fatalf("read-only configured file was unreadable, exit = %d", code)
	}
	if code := runSandboxTest(t, cfg, "/usr/bin/touch", marker); code == 0 {
		t.Fatal("read-only configured file was writable")
	}
}

func TestSandboxExec_DenyWritePathAllowsReadButBlocksWrite(t *testing.T) {
	dir := canonicalTestDir(t)
	marker := filepath.Join(dir, "credential")
	if err := os.WriteFile(marker, []byte("readable"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DenyWritePaths: []string{dir}}

	if code := runSandboxTest(t, cfg, "/bin/cat", marker); code != 0 {
		t.Fatalf("write-denied configured file was unreadable, exit = %d", code)
	}
	if code := runSandboxTest(t, cfg, "/usr/bin/touch", marker); code == 0 {
		t.Fatal("write-denied configured file was writable")
	}
}

func TestSandboxExec_DenyPathOverridesAllowedParent(t *testing.T) {
	parent := canonicalTestDir(t)
	denied := filepath.Join(parent, "credentials")
	if err := os.Mkdir(denied, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(denied, "token")
	if err := os.WriteFile(marker, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ReadWritePaths: []string{parent},
		DenyPaths:      []string{denied},
		WorkingDir:     parent,
	}

	if code := runSandboxTest(t, cfg, "/bin/cat", marker); code == 0 {
		t.Fatal("denied credential remained readable through allowed parent")
	}
	if code := runSandboxTest(t, cfg, "/usr/bin/touch", filepath.Join(denied, "new")); code == 0 {
		t.Fatal("denied credential directory remained writable through allowed parent")
	}
}

func TestSandboxExec_UnlistedHomePathIsInaccessible(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(home, ".mcplexer-sandbox-unlisted-*")
	if err != nil {
		t.Fatal(err)
	}
	marker := f.Name()
	if _, err := f.WriteString("private"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(marker) })

	if code := runSandboxTest(t, Config{}, "/bin/cat", marker); code == 0 {
		t.Fatal("unlisted home file was readable")
	}
	if code := runSandboxTest(t, Config{}, "/usr/bin/touch", marker); code == 0 {
		t.Fatal("unlisted home file was writable")
	}
}

func TestSandboxExec_NetworkPolicyIsExplicit(t *testing.T) {
	if _, err := os.Stat("/usr/bin/nc"); err != nil {
		t.Skip("nc not present")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	hostArgs := []string{"-z", "-w", "2", "localhost", port}
	closedArgs := []string{"-z", "-w", "2", "127.0.0.1", port}

	if code := runSandboxTest(t, Config{Network: NetworkHost}, "/usr/bin/nc", hostArgs...); code != 0 {
		t.Fatalf("NetworkHost hostname-resolved loopback exit = %d, want 0", code)
	}
	if code := runSandboxTest(t, Config{Network: NetworkDeny}, "/usr/bin/nc", closedArgs...); code == 0 {
		t.Fatal("NetworkDeny allowed a loopback connection")
	}
	if code := runSandboxTest(t, Config{Network: NetworkProxy, ProxySocket: "/tmp/not-used.sock"}, "/usr/bin/nc", closedArgs...); code == 0 {
		t.Fatal("NetworkProxy allowed a connection before proxy support exists")
	}
}

func canonicalTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func runSandboxTest(t *testing.T, cfg Config, program string, args ...string) ExitCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := (&sandboxExecDriver{}).Run(ctx, cfg, program, args)
	if err != nil {
		t.Fatalf("Run(%s): %v", program, err)
	}
	return code
}

// TestBuildSandboxExecProfile_GrantsSymlinkAndTarget is the regression
// test for the live "execvp ... Operation not permitted" failure when a
// CLI worker's binary is reached through a symlink (mimo ->
// node_modules script, grok -> downloads/ binary). The profile must
// allow reading BOTH the symlink location and its resolved target, or
// sandbox-exec cannot exec through the symlink.
func TestBuildSandboxExecProfile_GrantsSymlinkAndTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-binary")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked-binary")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		resolvedTarget = target
	}

	profile := buildSandboxExecProfile(Config{ReadOnlyPaths: []string{link}}, "/Users/example")

	for _, want := range []string{
		`(subpath "` + link + `")`,           // the symlink location itself
		`(subpath "` + resolvedTarget + `")`, // its resolved target
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing %q\n---\n%s", want, profile)
		}
	}
}

func TestSandboxPathVariants(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(target)

	if got := sandboxPathVariants(""); got != nil {
		t.Errorf("empty = %v, want nil", got)
	}
	// A symlink yields both its literal location and its resolved target,
	// literal first so exec-through-symlink is granted.
	got := sandboxPathVariants(link)
	if len(got) != 2 || got[0] != filepath.Clean(link) || got[1] != resolved {
		t.Errorf("symlink variants = %v, want [%s %s]", got, link, resolved)
	}
	// A path with no symlink component collapses to a single grant.
	plain := "/usr/bin"
	if got := sandboxPathVariants(plain); len(got) != 1 || got[0] != plain {
		t.Errorf("plain path = %v, want [%s]", got, plain)
	}
}

// TestBuildSandboxExecProfile_GrantsAncestorMetadata is the regression
// test for the live "EPERM lstat '/opt'" abort: node's realpathSync
// lstats every ancestor of a granted path. The profile must allow
// metadata on the ancestors of read/read-write grants.
func TestBuildSandboxExecProfile_GrantsAncestorMetadata(t *testing.T) {
	profile := buildSandboxExecProfile(Config{
		ReadOnlyPaths:  []string{"/opt/homebrew/lib"},
		ReadWritePaths: []string{"/Users/example/scratch"},
	}, "/Users/example")

	for _, want := range []string{
		`(allow file-read-metadata file-test-existence (path-ancestors "/opt/homebrew/lib"))`,
		`(allow file-read-metadata file-test-existence (path-ancestors "/Users/example/scratch"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing ancestor grant %q\n---\n%s", want, profile)
		}
	}
}
