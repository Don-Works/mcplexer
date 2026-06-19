package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	_ "modernc.org/sqlite"
	// vec registers the sqlite-vec extension functions + vec0 virtual
	// table module on package init. Used by migration 058 (memories_vec).
	_ "modernc.org/sqlite/vec"
)

// Compile-time check that DB satisfies store.Store.
var _ store.Store = (*DB)(nil)

// queryable abstracts *sql.DB and *sql.Tx for shared query code.
type queryable interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB is the SQLite-backed store implementation.
type DB struct {
	db *sql.DB
	q  queryable // points to db or active tx
}

// New opens a SQLite database at the given path and runs migrations.
func New(ctx context.Context, path string) (*DB, error) {
	dsn := path + "?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &DB{db: db, q: db}, nil
}

// Tx executes fn within a database transaction.
func (d *DB) Tx(ctx context.Context, fn func(store.Store) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	txDB := &DB{db: d.db, q: tx}
	if err := fn(txDB); err != nil {
		return err
	}

	return tx.Commit()
}

// Ping checks database connectivity.
func (d *DB) Ping(ctx context.Context) error {
	return d.db.PingContext(ctx)
}

// withTx runs fn inside a transaction. If d.q is already a *sql.Tx (e.g.
// when called from config.Apply's transaction), it reuses that tx to avoid
// deadlocking on MaxOpenConns(1). Otherwise it starts a new transaction.
func (d *DB) withTx(ctx context.Context, fn func(q queryable) error) error {
	if tx, ok := d.q.(*sql.Tx); ok {
		return fn(tx)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the database connection. It is safe to call on a nil
// receiver or when the underlying db is nil (both return nil).
// On a live handle it performs a best-effort WAL checkpoint (TRUNCATE)
// before closing the *sql.DB to improve durability for orderly shutdowns.
// The checkpoint is advisory; any error is swallowed so that a failing
// checkpoint does not mask a real close error (primary contract is Close
// error from the sql.DB if any).
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	// Best-effort: move WAL contents into the main DB file and truncate
	// the WAL. This makes subsequent reopen see a clean state and reduces
	// reliance on WAL replay for committed data after graceful close.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = d.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return d.db.Close()
}

// Raw returns the underlying *sql.DB. Callers should treat it as
// read-mostly: MaxOpenConns is 1, so blocking queries on this handle will
// stall the entire daemon. Intended for narrow use cases (e.g. the p2p
// SQLPeerLookup adapter) that need direct table access without a full
// store.Store interface entry.
func (d *DB) Raw() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}
