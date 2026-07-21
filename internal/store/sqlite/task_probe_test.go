// task_probe_test.go — pins the degraded-mode boot probe: healthy on a
// fully-migrated schema (including an empty tasks table) and loudly
// failing when a column the tasks service depends on is missing.
package sqlite

import (
	"context"
	"strings"
	"testing"
)

func TestProbeTaskSchemaHealthy(t *testing.T) {
	d := newMemDB(t)
	if err := d.ProbeTaskSchema(context.Background()); err != nil {
		t.Fatalf("ProbeTaskSchema on fresh schema: %v", err)
	}
}

func TestProbeTaskSchemaMissingColumn(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)

	// Simulate the "schema_version bumped past migration 072 without
	// the ALTER applying" failure: rename the generated column the
	// probe (and meta_match queries) depend on. RENAME COLUMN updates
	// the partial index that references it, so the rest of the schema
	// stays intact — only lookups by the canonical name break.
	if _, err := d.q.ExecContext(ctx,
		`ALTER TABLE tasks RENAME COLUMN meta_composed_by TO meta_composed_by_broken`,
	); err != nil {
		t.Fatalf("break schema: %v", err)
	}

	err := d.ProbeTaskSchema(ctx)
	if err == nil {
		t.Fatal("expected ProbeTaskSchema to fail on broken schema")
	}
	if !strings.Contains(err.Error(), "tasks table schema check") {
		t.Fatalf("expected wrapped schema-check error, got: %v", err)
	}
}
