// task_probe.go — cheap schema-health probe for the tasks subsystem.
// Selects the late-migration columns the tasks service depends on
// (hlc_at from 072, meta_composed_by from 072, lease_expires_at from
// 071) so a database whose schema_version was bumped past those
// migrations without the ALTERs applying fails fast at boot instead of
// erroring opaquely on every task__* call.
package sqlite

import (
	"context"
	"fmt"
)

// ProbeTaskSchema runs one bounded SELECT touching the columns the
// tasks service needs. Returns nil on a healthy schema (including an
// empty tasks table); wraps the driver error otherwise.
func (d *DB) ProbeTaskSchema(ctx context.Context) error {
	var n int
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT hlc_at, meta_composed_by, lease_expires_at
			FROM tasks LIMIT 1
		)`).Scan(&n)
	if err != nil {
		return fmt.Errorf("tasks table schema check: %w", err)
	}
	return nil
}
