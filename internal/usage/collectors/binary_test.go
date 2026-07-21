package collectors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBinaryUsesExecutableFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if got := resolveBinary("provider-cli", []string{path}); got != path {
		t.Fatalf("resolved = %q, want %q", got, path)
	}
}

func TestGrokCandidatesPreferCanonicalInstall(t *testing.T) {
	candidates := binaryCandidates("grok")
	if len(candidates) == 0 || !strings.HasSuffix(candidates[0], ".grok/bin/grok") {
		t.Fatalf("grok candidates = %v", candidates)
	}
}
