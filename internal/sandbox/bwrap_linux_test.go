//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBwrapDriver_Available is a smoke test that the driver compiles +
// is instantiable. We deliberately do NOT assert the boolean — bwrap may
// or may not be installed on the build host. Live verification of
// sandbox isolation lives in the Pi appliance integration suite.
func TestBwrapDriver_Available(t *testing.T) {
	d := &bwrapDriver{}
	if d.Name() != "bwrap" {
		t.Fatalf("Name(): got %q want %q", d.Name(), "bwrap")
	}
	_ = d.Available()
}

// TestBwrapArgv_Shape verifies the argv assembly logic without invoking
// bwrap. This is the one Linux test we can meaningfully run from any
// host: it's pure string-mungling.
func TestBwrapArgv_Shape(t *testing.T) {
	cfg := Config{
		ReadOnlyPaths:  []string{"/repo"},
		ReadWritePaths: []string{"/scratch"},
		WorkingDir:     "/workspace",
		Network:        NetworkHost,
	}
	argv := bwrapArgv(cfg, "/home/test", "/bin/sh", []string{"-c", "true"})
	wantSubstrings := [][]string{
		{"--unshare-all"},
		{"--share-net"},
		{"--ro-bind", "/repo", "/repo"},
		{"--bind", "/scratch", "/scratch"},
		{"--chdir", "/workspace"},
		{"--", "/bin/sh", "-c", "true"},
	}
	for _, want := range wantSubstrings {
		if !sliceContainsRun(argv, want) {
			t.Errorf("argv missing %v\nfull argv: %v", want, argv)
		}
	}
}

// TestBwrapArgv_FiltersDenyPaths ensures a caller-provided
// ReadOnlyPath/ReadWritePath that collides with the default deny list
// gets dropped, NOT mounted with a "but I asked for it" override.
func TestBwrapArgv_FiltersDenyPaths(t *testing.T) {
	cfg := Config{
		ReadOnlyPaths: []string{"/home/test/.ssh"},
	}
	argv := bwrapArgv(cfg, "/home/test", "/bin/true", nil)
	for i, a := range argv {
		if a == "/home/test/.ssh" {
			t.Fatalf("deny path mounted anyway at argv[%d]: %v", i, argv)
		}
	}
}

func TestBwrapArgv_MasksDeniedChildrenOfBroaderBind(t *testing.T) {
	cfg := Config{
		ReadWritePaths: []string{"/home/test"},
		DenyPaths:      []string{"/home/test/private"},
	}
	argv := bwrapArgv(cfg, "/home/test", "/bin/true", nil)

	parentBind := indexOfRun(argv, []string{"--bind", "/home/test", "/home/test"})
	childMask := indexOfRun(argv, []string{"--tmpfs", "/home/test/private"})
	if parentBind < 0 {
		t.Fatalf("parent bind missing: %v", argv)
	}
	if childMask < 0 {
		t.Fatalf("denied child mask missing: %v", argv)
	}
	if childMask < parentBind {
		t.Fatalf("denied child mask must follow parent bind: %v", argv)
	}
}

func TestBwrapArgv_MasksHardDenyBelowDenyWriteBind(t *testing.T) {
	cfg := Config{
		DenyWritePaths: []string{"/home/test/.claude"},
		DenyPaths:      []string{"/home/test/.claude/secrets"},
	}
	argv := bwrapArgv(cfg, "/home/test", "/bin/true", nil)

	readOnlyBind := indexOfRun(argv, []string{
		"--ro-bind", "/home/test/.claude", "/home/test/.claude",
	})
	hardDenyMask := indexOfRun(argv, []string{"--tmpfs", "/home/test/.claude/secrets"})
	if readOnlyBind < 0 || hardDenyMask < 0 {
		t.Fatalf("expected read-only parent and hard-deny child mask: %v", argv)
	}
	if hardDenyMask < readOnlyBind {
		t.Fatalf("hard-deny child mask must follow read-only parent bind: %v", argv)
	}
}

func TestBwrapArgv_SkipsPathInsideDeniedSubtree(t *testing.T) {
	cfg := Config{
		ReadWritePaths: []string{"/home/test/private/nested"},
		DenyPaths:      []string{"/home/test/private"},
	}
	argv := bwrapArgv(cfg, "/home/test", "/bin/true", nil)
	if sliceContainsRun(argv, []string{"--bind", "/home/test/private/nested", "/home/test/private/nested"}) {
		t.Fatalf("path inside denied subtree was mounted: %v", argv)
	}
}

func indexOfRun(s, run []string) int {
	for i := 0; i+len(run) <= len(s); i++ {
		if sliceContainsRun(s[i:i+len(run)], run) {
			return i
		}
	}
	return -1
}

// sliceContainsRun checks whether `run` appears as a contiguous
// subsequence of `s`. Lets the argv tests assert that a flag and its
// argument arrive together rather than in opposite ends of the slice.
func sliceContainsRun(s, run []string) bool {
	if len(run) == 0 {
		return true
	}
	for i := 0; i+len(run) <= len(s); i++ {
		match := true
		for j, r := range run {
			if s[i+j] != r {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestBwrapArgv_BindsSystemRuntime — without the OS runtime binds the
// namespace has no /usr or /lib, so exec of ANY program fails. Every
// linux host running this test has /usr and /etc.
func TestBwrapArgv_BindsSystemRuntime(t *testing.T) {
	argv := bwrapArgv(Config{}, "/home/test", "/bin/true", nil)
	for _, p := range []string{"/usr", "/etc"} {
		if !sliceContainsRun(argv, []string{"--ro-bind", p, p}) {
			t.Errorf("argv missing runtime bind for %s: %v", p, argv)
		}
	}
}

// TestBwrapArgv_BindsExistingPathOutsideHome is the regression test for
// the over-broad symlink guard that skipped EVERY bind resolving
// outside $HOME — which made non-home grants (/opt binaries, a repo
// under /srv, a scratch dir under /tmp) silently unmountable.
func TestBwrapArgv_BindsExistingPathOutsideHome(t *testing.T) {
	outside := t.TempDir()
	resolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		resolved = outside
	}
	home := t.TempDir()
	argv := bwrapArgv(Config{ReadWritePaths: []string{outside}}, home, "/bin/true", nil)
	if !sliceContainsRun(argv, []string{"--bind", resolved, resolved}) {
		t.Fatalf("existing path outside home must be bindable, argv: %v", argv)
	}
}

// TestBwrapArgv_SkipsHomeSymlinkEscape — a grant UNDER home that
// resolves outside home is a symlink escape and must be dropped.
func TestBwrapArgv_SkipsHomeSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(home, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		resolvedTarget = target
	}
	argv := bwrapArgv(Config{ReadOnlyPaths: []string{link}}, home, "/bin/true", nil)
	if sliceContainsRun(argv, []string{"--ro-bind", link, link}) ||
		sliceContainsRun(argv, []string{"--ro-bind", resolvedTarget, resolvedTarget}) {
		t.Fatalf("home symlink escape must not be bound, argv: %v", argv)
	}
}

// TestWrapForPlatform_FailsClosedWithoutBwrap — if bwrap disappears
// between the availability probe and the wrap, the spawn must run a
// guaranteed non-success program, never the unsandboxed original.
func TestWrapForPlatform_FailsClosedWithoutBwrap(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	prog, args, cleanup := wrapForPlatform(Config{}, "/home/test", "/bin/true", nil, func() {})
	cleanup()
	if prog != linuxWrapFailureProgram || args != nil {
		t.Fatalf("got %q %v, want fail-closed %q", prog, args, linuxWrapFailureProgram)
	}
}

func TestDescribeForPlatform_Linux(t *testing.T) {
	if got := describeForPlatform(Config{}); got != "bwrap(deny-creds,deny-net)" {
		t.Errorf("deny-net config: got %q", got)
	}
	if got := describeForPlatform(Config{Network: NetworkHost}); got != "bwrap(deny-creds)" {
		t.Errorf("host-net config: got %q", got)
	}
}
