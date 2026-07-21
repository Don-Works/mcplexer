package sqlite

import (
	"context"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// ListSkillRegistryScopeHeads returns the latest active row for each distinct
// (workspace, name) pair in scope. Limit is applied after head reduction.
func (d *DB) ListSkillRegistryScopeHeads(
	ctx context.Context, scope store.SkillScope, limit int,
) ([]store.SkillRegistryEntry, error) {
	clause, params := scopeWhereClause(scope)
	q := `
		WITH ranked AS (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY (workspace_id IS NULL), workspace_id, name
				ORDER BY version DESC
			) AS rn
			FROM skill_registry_entries
			WHERE deleted_at IS NULL ` + clause + `
		)
		SELECT ` + skillRegSelectCols + `
		FROM ranked
		WHERE rn = 1
		ORDER BY name ASC, (workspace_id IS NULL) DESC, workspace_id ASC`
	if limit > 0 {
		q += ` LIMIT ?`
		params = append(params, limit)
	}
	rows, err := d.q.QueryContext(ctx, q, params...)
	if err != nil {
		return nil, fmt.Errorf("list skill_registry_scope_heads: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.SkillRegistryEntry
	for rows.Next() {
		entry, scanErr := scanSkillRegistryEntry(rows.Scan)
		if scanErr != nil {
			return nil, fmt.Errorf("scan skill_registry_scope_head: %w", scanErr)
		}
		out = append(out, *entry)
	}
	return out, rows.Err()
}
