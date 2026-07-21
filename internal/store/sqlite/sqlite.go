package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	db  *sql.DB
	rdb *sql.DB   // concurrent read pool; see splitQueryable
	q   queryable // routes to db/rdb, or the active tx
}

// splitQueryable routes pure-read statements to a concurrent read pool and
// everything else (writes, pragmas, DDL) to the serialized writer. The
// writer must stay at MaxOpenConns(1) — modernc under WAL wants a single
// writer — but that pinned queue was also serializing every read behind
// in-flight writes, which under multi-agent load pushed p95 tool latency
// from milliseconds to seconds. WAL readers never block on the writer, so
// offloading SELECTs removes that queue entirely. Only SELECT-prefixed
// statements qualify: a SQLite SELECT cannot write, so misrouting a write
// to the read pool is impossible by construction; WITH-prefixed CTEs and
// anything else conservatively stay on the writer.
type splitQueryable struct {
	w, r *sql.DB
}

func (s splitQueryable) route(query string) *sql.DB {
	q := strings.TrimSpace(query)
	if len(q) >= 6 && strings.EqualFold(q[:6], "SELECT") {
		return s.r
	}
	return s.w
}

func (s splitQueryable) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.w.ExecContext(ctx, query, args...)
}

func (s splitQueryable) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.route(query).QueryContext(ctx, query, args...)
}

func (s splitQueryable) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.route(query).QueryRowContext(ctx, query, args...)
}

// New opens a SQLite database at the given path and runs migrations.
func New(ctx context.Context, path string) (*DB, error) {
	// modernc only recognizes _pragma=...(...) DSN params; the historical
	// mattn-style _journal_mode=WAL&_busy_timeout=5000 form was silently
	// ignored, which the old single-connection pool masked (one conn never
	// contends, so journal mode and busy timeout never mattered). With a
	// concurrent read pool they must actually apply — and via the DSN they
	// apply to EVERY pooled connection, unlike a one-off Exec'd PRAGMA
	// that is lost when the pool recycles its connection.
	// busy_timeout MUST be first: DSN pragmas run in order on every new
	// connection, and journal_mode(WAL) needs locks — with no timeout yet
	// set, a reader opening during a write burst would BUSY-fail at open.
	dsn := path + "?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Each plain :memory: connection owns a different private database. A
	// second read pool would therefore see none of the schema/data migrated on
	// the writer. Keep the historical single connection for in-memory callers
	// (predominantly tests); file-backed production databases still get the
	// independent WAL read pool below.
	if path == ":memory:" {
		return &DB{db: db, q: splitQueryable{w: db, r: db}}, nil
	}

	// Journal mode is database-wide and was already established by the writer.
	// Reusing the writer DSN here makes every lazily-opened reader connection
	// execute PRAGMA journal_mode=WAL; under concurrent writes that pragma can
	// itself return SQLITE_BUSY before the actual SELECT runs. Reader
	// connections only need their own busy timeout and synchronous setting.
	readDSN := path + "?_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	rdb, err := sql.Open("sqlite", readDSN)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite read pool: %w", err)
	}
	rdb.SetMaxOpenConns(4)
	rdb.SetMaxIdleConns(4)
	if err := rdb.PingContext(ctx); err != nil {
		_ = rdb.Close()
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite read pool: %w", err)
	}

	return &DB{db: db, rdb: rdb, q: splitQueryable{w: db, r: rdb}}, nil
}

// Tx executes fn within a database transaction.
func (d *DB) Tx(ctx context.Context, fn func(store.Store) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	txDB := &DB{db: d.db, rdb: d.rdb, q: tx}
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
	// Close the read pool first: a TRUNCATE checkpoint cannot complete
	// while reader connections hold WAL snapshots.
	if d.rdb != nil {
		_ = d.rdb.Close()
	}
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
