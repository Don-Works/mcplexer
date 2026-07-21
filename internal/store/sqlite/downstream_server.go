package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

func (d *DB) CreateDownstreamServer(ctx context.Context, ds *store.DownstreamServer) error {
	if ds.ID == "" {
		ds.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	ds.CreatedAt = now
	ds.UpdatedAt = now

	args := normalizeJSON(ds.Args, "[]")
	caps := normalizeJSON(ds.CapabilitiesCache, "{}")
	cacheCfg := normalizeJSON(ds.CacheConfig, "{}")

	if ds.Discovery == "" {
		ds.Discovery = "dynamic"
	}
	if ds.Source == "" {
		ds.Source = "api"
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO downstream_servers
			(id, name, transport, command, args, url, tool_namespace, discovery,
			 capabilities_cache, cache_config, idle_timeout_sec, max_instances,
			 restart_policy, disabled, source, created_at, updated_at,
			 call_timeout_sec)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ds.ID, ds.Name, ds.Transport, ds.Command, args, ds.URL,
		ds.ToolNamespace, ds.Discovery, caps, cacheCfg, ds.IdleTimeoutSec,
		ds.MaxInstances, ds.RestartPolicy, ds.Disabled, ds.Source,
		formatTime(ds.CreatedAt), formatTime(ds.UpdatedAt),
		ds.CallTimeoutSec,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return nil
}

func (d *DB) GetDownstreamServer(ctx context.Context, id string) (*store.DownstreamServer, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, name, transport, command, args, url, tool_namespace, discovery,
		       capabilities_cache, cache_config, idle_timeout_sec, max_instances,
		       restart_policy, disabled, source, created_at, updated_at,
		       call_timeout_sec
		FROM downstream_servers WHERE id = ?`, id)
	return scanDownstreamServer(row)
}

func (d *DB) GetDownstreamServersByIDs(ctx context.Context, ids []string) ([]store.DownstreamServer, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	query := `SELECT id, name, transport, command, args, url, tool_namespace, discovery,
		capabilities_cache, cache_config, idle_timeout_sec, max_instances,
		restart_policy, disabled, source, created_at, updated_at,
		call_timeout_sec
	FROM downstream_servers WHERE id IN (` + strings.Join(ph, ",") + `)`
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.DownstreamServer
	for rows.Next() {
		ds, err := scanDownstreamServerRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}

func (d *DB) GetDownstreamServerByName(ctx context.Context, name string) (*store.DownstreamServer, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, name, transport, command, args, url, tool_namespace, discovery,
		       capabilities_cache, cache_config, idle_timeout_sec, max_instances,
		       restart_policy, disabled, source, created_at, updated_at,
		       call_timeout_sec
		FROM downstream_servers WHERE name = ?`, name)
	return scanDownstreamServer(row)
}

func (d *DB) ListDownstreamServers(ctx context.Context) ([]store.DownstreamServer, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, name, transport, command, args, url, tool_namespace, discovery,
		       capabilities_cache, cache_config, idle_timeout_sec, max_instances,
		       restart_policy, disabled, source, created_at, updated_at,
		       call_timeout_sec
		FROM downstream_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.DownstreamServer
	for rows.Next() {
		ds, err := scanDownstreamServerRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}

func (d *DB) UpdateDownstreamServer(ctx context.Context, ds *store.DownstreamServer) error {
	ds.UpdatedAt = time.Now().UTC()
	args := normalizeJSON(ds.Args, "[]")
	caps := normalizeJSON(ds.CapabilitiesCache, "{}")
	cacheCfg := normalizeJSON(ds.CacheConfig, "{}")
	if ds.Source == "" {
		ds.Source = "api"
	}

	res, err := d.q.ExecContext(ctx, `
		UPDATE downstream_servers
		SET name = ?, transport = ?, command = ?, args = ?, url = ?,
		    tool_namespace = ?, discovery = ?, capabilities_cache = ?,
		    cache_config = ?, idle_timeout_sec = ?, max_instances = ?,
		    restart_policy = ?, disabled = ?, source = ?, updated_at = ?,
		    call_timeout_sec = ?
		WHERE id = ?`,
		ds.Name, ds.Transport, ds.Command, args, ds.URL,
		ds.ToolNamespace, ds.Discovery, caps, cacheCfg,
		ds.IdleTimeoutSec, ds.MaxInstances, ds.RestartPolicy,
		ds.Disabled, ds.Source, formatTime(ds.UpdatedAt),
		ds.CallTimeoutSec, ds.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return checkRowsAffected(res)
}

func (d *DB) DeleteDownstreamServer(ctx context.Context, id string) error {
	return d.withTx(ctx, func(q queryable) error {
		if _, err := q.ExecContext(ctx,
			`DELETE FROM route_rules WHERE downstream_server_id = ?`, id); err != nil {
			return fmt.Errorf("cascade delete route_rules: %w", err)
		}
		if _, err := q.ExecContext(ctx,
			`UPDATE tool_approvals SET status = 'cancelled', resolved_at = ?
			 WHERE downstream_server_id = ? AND status = 'pending'`,
			formatTime(time.Now().UTC()), id); err != nil {
			return fmt.Errorf("cascade cancel tool_approvals: %w", err)
		}
		res, err := q.ExecContext(ctx, `DELETE FROM downstream_servers WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}

func (d *DB) UpdateCapabilitiesCache(
	ctx context.Context, id string, cache json.RawMessage,
) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE downstream_servers
		SET capabilities_cache = ?, updated_at = ?
		WHERE id = ?`,
		string(cache), formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func scanDownstreamServer(row *sql.Row) (*store.DownstreamServer, error) {
	var ds store.DownstreamServer
	var createdAt, updatedAt, args, caps, cacheCfg string
	err := row.Scan(
		&ds.ID, &ds.Name, &ds.Transport, &ds.Command, &args,
		&ds.URL, &ds.ToolNamespace, &ds.Discovery, &caps, &cacheCfg,
		&ds.IdleTimeoutSec, &ds.MaxInstances, &ds.RestartPolicy,
		&ds.Disabled, &ds.Source, &createdAt, &updatedAt,
		&ds.CallTimeoutSec,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ds.Args = json.RawMessage(args)
	ds.CapabilitiesCache = json.RawMessage(caps)
	ds.CacheConfig = json.RawMessage(cacheCfg)
	ds.CreatedAt = parseTime(createdAt)
	ds.UpdatedAt = parseTime(updatedAt)
	return &ds, nil
}

func scanDownstreamServerRow(row rowScanner) (*store.DownstreamServer, error) {
	var ds store.DownstreamServer
	var createdAt, updatedAt, args, caps, cacheCfg string
	err := row.Scan(
		&ds.ID, &ds.Name, &ds.Transport, &ds.Command, &args,
		&ds.URL, &ds.ToolNamespace, &ds.Discovery, &caps, &cacheCfg,
		&ds.IdleTimeoutSec, &ds.MaxInstances, &ds.RestartPolicy,
		&ds.Disabled, &ds.Source, &createdAt, &updatedAt,
		&ds.CallTimeoutSec,
	)
	if err != nil {
		return nil, err
	}
	ds.Args = json.RawMessage(args)
	ds.CapabilitiesCache = json.RawMessage(caps)
	ds.CacheConfig = json.RawMessage(cacheCfg)
	ds.CreatedAt = parseTime(createdAt)
	ds.UpdatedAt = parseTime(updatedAt)
	return &ds, nil
}
