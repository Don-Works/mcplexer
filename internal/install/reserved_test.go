package install

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestReservedToolNames_AllPrefixedUniformly(t *testing.T) {
	const prefix = "mcplexer__"
	for _, name := range ReservedToolNames() {
		t.Run(name, func(t *testing.T) {
			if !strings.HasPrefix(name, prefix) {
				t.Errorf("reserved tool %q must start with %q", name, prefix)
			}
		})
	}
}

func TestReservedToolNames_NoDuplicates(t *testing.T) {
	seen := make(map[string]int)
	for _, name := range ReservedToolNames() {
		seen[name]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("reserved tool %q appears %d times (expected 1)", name, count)
		}
	}
}

func TestReservedToolNames_NoCollisionWithBuiltins(t *testing.T) {
	sourceFiles := []string{
		"../gateway/admin_gate.go",
		"../gateway/builtin_tools.go",
		"../gateway/handler_codemode.go",
	}

	for _, path := range sourceFiles {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("source file %s does not exist; skipping", path)
				return
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, name := range ReservedToolNames() {
				if bytes.Contains(data, []byte(strconv.Quote(name))) {
					t.Errorf("reserved tool %q already appears in %s", name, path)
				}
			}
		})
	}
}
