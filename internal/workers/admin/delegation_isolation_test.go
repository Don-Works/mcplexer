package admin

import (
	"reflect"
	"testing"
)

func TestNormalizeDelegationIsolationDefaultsAndDeduplicates(t *testing.T) {
	in := &DelegationInput{TouchesFiles: []string{" internal/a.go ", "internal\\a.go", "internal/b.go"}}
	if err := normalizeDelegationIsolationInput(in); err != nil {
		t.Fatal(err)
	}
	if in.WorkerIsolation != "worktree" {
		t.Fatalf("isolation = %q, want worktree", in.WorkerIsolation)
	}
	want := []string{"internal/a.go", "internal/b.go"}
	if !reflect.DeepEqual(in.TouchesFiles, want) {
		t.Fatalf("touches_files = %v, want %v", in.TouchesFiles, want)
	}
}

func TestNormalizeDelegationIsolationRejectsEscapes(t *testing.T) {
	for _, path := range []string{"../outside", "/etc/passwd", "internal/*", "internal/[ab].go"} {
		t.Run(path, func(t *testing.T) {
			in := &DelegationInput{TouchesFiles: []string{path}}
			if err := normalizeDelegationIsolationInput(in); err == nil {
				t.Fatalf("path %q unexpectedly accepted", path)
			}
		})
	}
}

func TestNormalizeDelegationIsolationNoneCannotClaimFiles(t *testing.T) {
	in := &DelegationInput{WorkerIsolation: "none", TouchesFiles: []string{"internal/a.go"}}
	if err := normalizeDelegationIsolationInput(in); err == nil {
		t.Fatal("trusted non-isolated delegation unexpectedly accepted touches_files")
	}
}
