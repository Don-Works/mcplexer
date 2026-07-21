package sqlite

import (
	"context"
	"encoding/json"
)

func (d *DB) GetSettings(ctx context.Context) (json.RawMessage, error) {
	var data string
	err := d.q.QueryRowContext(ctx,
		`SELECT data FROM settings WHERE id = 1`,
	).Scan(&data)
	if err != nil {
		return json.RawMessage("{}"), err
	}
	return json.RawMessage(data), nil
}

func (d *DB) UpdateSettings(ctx context.Context, data json.RawMessage) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE settings SET data = ?, updated_at = datetime('now') WHERE id = 1`,
		string(data),
	)
	return err
}
