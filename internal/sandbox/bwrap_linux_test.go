//go:build linux

package sandbox

import "testing"

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
