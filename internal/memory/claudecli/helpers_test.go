// helpers_test.go — fake store, fixture builders, and frontmatter
// constants shared by claudecli_test.go. Split to honor the 300-line
// file cap; nothing here is interesting on its own.
package claudecli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeStore is the in-memory MemoryWriter used by the unit table tests.
// We hold rows in a map keyed by ID so GetMemory can serve the
// idempotency-skip path.
type fakeStore struct {
	mu     sync.Mutex
	rows   map[string]*store.MemoryEntry
	writes []*store.MemoryEntry
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]*store.MemoryEntry{}}
}

func (f *fakeStore) WriteMemory(_ context.Context, e *store.MemoryEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[e.ID]; ok {
		return errors.New("duplicate id")
	}
	clone := *e
	f.rows[e.ID] = &clone
	f.writes = append(f.writes, &clone)
	return nil
}

func (f *fakeStore) GetMemory(_ context.Context, id string) (*store.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.rows[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return e, nil
}

// writeFile writes p with permissions 0644 and creates parent dirs.
func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// fixtureProject builds one `<base>/<flattened-repo>/memory/*.md` set.
// Returns the flattened repo dir name.
func fixtureProject(t *testing.T, base, repo string, files map[string]string) string {
	t.Helper()
	flat := strings.ReplaceAll(repo, "/", "-")
	memDir := filepath.Join(base, flat, "memory")
	for name, body := range files {
		writeFile(t, filepath.Join(memDir, name), body)
	}
	return flat
}

const projectFrontmatter = `---
name: workers-overnight-complete
description: "Workers M0-M3 overnight build complete"
metadata:
  node_type: memory
  type: project
  originSessionId: sess-abc-123
---

# Workers overnight complete

Workers M0-M3 all shipped + reviewed + P0 fixed.
`

const referenceFrontmatter = `---
name: reference-hosts
description: "peer.example + ai-example host hints"
metadata:
  node_type: memory
  type: reference
  originSessionId: sess-def-456
---

Linux peer boxes, SSH as user@.
`

const noFrontmatter = `Just a plain markdown blob with no YAML at the top.
`

const indexFile = `# MEMORY index
- [Workers overnight complete](project_workers_overnight_complete.md)
`
