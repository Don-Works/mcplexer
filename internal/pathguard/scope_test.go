package pathguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScopeResolveCanonicalizesAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	working := filepath.Join(root, "pkg")
	if err := os.Mkdir(working, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(working, "escape")); err != nil {
		t.Fatal(err)
	}
	scope, err := New(root, working, nil)
	if err != nil {
		t.Fatal(err)
	}

	inside, err := scope.Resolve("new/file.go")
	if err != nil {
		t.Fatalf("resolve inside: %v", err)
	}
	canonicalWorking, err := filepath.EvalSymlinks(working)
	if err != nil {
		t.Fatal(err)
	}
	wantInside := filepath.Join(canonicalWorking, "new", "file.go")
	if inside != wantInside {
		t.Fatalf("inside = %q, want %q", inside, wantInside)
	}
	for name, candidate := range map[string]string{
		"absolute":  outside,
		"traversal": filepath.Join("..", "..", filepath.Base(outside)),
		"symlink":   filepath.Join("escape", "new.go"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scope.Resolve(candidate); err == nil {
				t.Fatalf("Resolve(%q) unexpectedly succeeded", candidate)
			}
		})
	}
}

func TestScopeClaimsNarrowWrites(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	if err := os.Mkdir(allowed, 0o700); err != nil {
		t.Fatal(err)
	}
	scope, err := New(root, root, []string{"allowed"})
	if err != nil {
		t.Fatal(err)
	}
	inside, err := scope.Resolve("allowed/new.go")
	if err != nil {
		t.Fatal(err)
	}
	outsideClaim, err := scope.Resolve("other/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if !scope.AllowsWrite(inside) {
		t.Fatalf("claimed path %q was not writable", inside)
	}
	if scope.AllowsWrite(outsideClaim) {
		t.Fatalf("unclaimed path %q was writable", outsideClaim)
	}
}

func TestScopeRejectsSymlinkClaimEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "claimed")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root, root, []string{"claimed/new.go"}); err == nil {
		t.Fatal("symlink claim outside root unexpectedly accepted")
	}
}
