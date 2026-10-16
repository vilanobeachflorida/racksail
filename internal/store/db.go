package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	// The pure Go SQLite driver, registered as "sqlite".
	_ "modernc.org/sqlite"
)

// DB is the panel database: one SQLite file in WAL mode that the resource
// store and the other parts of the core that keep state share, each with
// tables and migrations of its own.
//
// SQLite lets one connection write at a time, so DB keeps a single writing
// connection, whose transactions start IMMEDIATE and so never fail halfway
// for lack of the write lock, and a pool of read-only connections, which in
// WAL mode never wait for the writer.
type DB struct {
	path   string
	writer *sql.DB
	reader *sql.DB
}

// Connection settings. The busy timeout covers other processes, such as a
// backup reading the file; within the panel the single writer never waits.
const (
	commonParams = "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_synchronous=NORMAL"
	writerParams = commonParams + "&_txlock=immediate&_journal_mode=WAL"
	readerParams = commonParams + "&_query_only=1"
)

// OpenDB opens the database at path, creating it, readable by its owner
// only, when it doesn't exist yet.
func OpenDB(ctx context.Context, path string) (*DB, error) {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: the database path %q must be absolute and can't contain ? or #", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: creating the database directory: %w", err)
	}
	// SQLite gives the WAL and shared-memory files the mode of the database
	// file, so creating it first keeps all three private.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: opening the database: %w", err)
	}
	f.Close()

	writer, err := sql.Open("sqlite", path+"?"+writerParams)
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)
	var mode string
	if err := writer.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		writer.Close()
		return nil, fmt.Errorf("store: opening %s: %w", path, err)
	}
	if !strings.EqualFold(mode, "wal") {
		writer.Close()
		return nil, fmt.Errorf("store: %s can't use write-ahead logging (journal mode %s)", path, mode)
	}
	reader, err := sql.Open("sqlite", path+"?"+readerParams)
	if err != nil {
		writer.Close()
		return nil, err
	}
	readers := max(4, runtime.GOMAXPROCS(0))
	reader.SetMaxOpenConns(readers)
	reader.SetMaxIdleConns(readers)
	reader.SetConnMaxIdleTime(5 * time.Minute)
	if err := reader.PingContext(ctx); err != nil {
		writer.Close()
		reader.Close()
		return nil, fmt.Errorf("store: opening %s for reading: %w", path, err)
	}
	return &DB{path: path, writer: writer, reader: reader}, nil
}

// Path returns where the database file is.
func (db *DB) Path() string { return db.path }

// Reader returns the pool of read-only connections. Reads see what was
// committed when they started; they never see a transaction in progress.
func (db *DB) Reader() *sql.DB { return db.reader }

// Write runs fn in a write transaction, committing it when fn returns nil.
// Writes are serialized, so fn must not call Write itself. Inside Atomic,
// with its context, fn runs in Atomic's transaction instead, as a savepoint
// that is undone on its own when fn fails.
func (db *DB) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if st := db.joined(ctx); st != nil {
		return st.savepoint(ctx, fn)
	}
	tx, err := db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: starting a transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing: %w", err)
	}
	return nil
}

// Exec runs one statement in a write transaction of its own, or in Atomic's
// when ctx carries it.
func (db *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		res, err = tx.ExecContext(ctx, query, args...)
		return err
	})
	return res, err
}

// ErrOtherTransaction refuses Atomic while ctx carries a transaction of
// another database, whose writes couldn't commit together with this one's.
var ErrOtherTransaction = errors.New("store: a transaction of another database is in progress")

// txState is a transaction Atomic started. Writes made with a context that
// carries it join it.
type txState struct {
	db *DB
	tx *sql.Tx

	mu    sync.Mutex
	depth int
	after []func()
	// done is set once the transaction committed or rolled back, after
	// which contexts that still carry it write on their own again.
	done bool
}

type txKey struct{}

// current returns the transaction ctx carries, while it is going on.
func current(ctx context.Context) *txState {
	st, ok := ctx.Value(txKey{}).(*txState)
	if !ok {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return nil
	}
	return st
}

// joined returns the transaction of db that ctx carries, while it is going
// on.
func (db *DB) joined(ctx context.Context) *txState {
	if st := current(ctx); st != nil && st.db == db {
		return st
	}
	return nil
}

// Atomic runs fn in one write transaction of the database. Every write made
// with fn's context, or a context derived from it, through Write and Exec
// joins that transaction, so what the store, the job queue, the change
// pipeline and the audit log write there commits together or not at all.
// Each write joins as a savepoint: one that fails is undone on its own, and
// its caller decides whether to go on. Reads through Reader don't see the
// transaction's writes until it commits, and what waits for them
// (AfterCommit) runs once it has.
//
// The transaction holds the only writing connection, so every write fn makes
// must use its context: one made with another context would wait for the
// transaction to end, which never happens. For the same reason fn must not
// publish events, since publishing takes a lock that a writer waiting for
// the connection may hold.
func (db *DB) Atomic(ctx context.Context, fn func(ctx context.Context) error) error {
	if st := current(ctx); st != nil {
		if st.db != db {
			return ErrOtherTransaction
		}
		return fn(ctx)
	}
	tx, err := db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: starting a transaction: %w", err)
	}
	st := &txState{db: db, tx: tx}
	err = fn(context.WithValue(ctx, txKey{}, st))
	if err == nil {
		if err = tx.Commit(); err != nil {
			err = fmt.Errorf("store: committing: %w", err)
		}
	} else if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
		err = errors.Join(err, rbErr)
	}
	st.mu.Lock()
	after := st.after
	st.after, st.done = nil, true
	st.mu.Unlock()
	if err != nil {
		return err
	}
	for _, f := range after {
		f()
	}
	return nil
}

// AfterCommit runs fn once the writes made to the database with ctx are
// committed: when Atomic's transaction commits, if ctx carries it, and at
// once otherwise. fn never runs when the transaction rolls back. Work that
// others must only see once it is in the database, such as telling watchers
// about it or starting a job, waits for the commit this way.
func (db *DB) AfterCommit(ctx context.Context, fn func()) {
	st := db.joined(ctx)
	if st == nil {
		fn()
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.after = append(st.after, fn)
}

// savepoint runs fn inside the transaction, undoing what fn wrote when it
// fails. Savepoints nest, so a write within another one works too.
func (st *txState) savepoint(ctx context.Context, fn func(tx *sql.Tx) error) error {
	st.mu.Lock()
	st.depth++
	name := "write_" + strconv.Itoa(st.depth)
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.depth--
		st.mu.Unlock()
	}()
	if _, err := st.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return fmt.Errorf("store: starting a savepoint: %w", err)
	}
	// Undoing goes ahead even when ctx has ended.
	cleanup := context.WithoutCancel(ctx)
	if err := fn(st.tx); err != nil {
		if _, rbErr := st.tx.ExecContext(cleanup, "ROLLBACK TO "+name); rbErr != nil {
			return errors.Join(err, rbErr)
		}
		if _, relErr := st.tx.ExecContext(cleanup, "RELEASE "+name); relErr != nil {
			return errors.Join(err, relErr)
		}
		return err
	}
	if _, err := st.tx.ExecContext(cleanup, "RELEASE "+name); err != nil {
		return fmt.Errorf("store: ending a savepoint: %w", err)
	}
	return nil
}

// Checkpoint copies the write-ahead log into the database file and empties
// it, as before a backup takes a copy of the file.
func (db *DB) Checkpoint(ctx context.Context) error {
	_, err := db.writer.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Close closes every connection.
func (db *DB) Close() error {
	return errors.Join(db.reader.Close(), db.writer.Close())
}

// Migrate brings the tables of owner up to date. It applies the migrations
// in fsys that haven't run yet, in order, each in a transaction of its own
// that also records it. It refuses to go on when a migration that already
// ran has changed since, or when the database knows migrations this binary
// doesn't, as after going back to an older release.
func (db *DB) Migrate(ctx context.Context, owner string, fsys fs.FS) error {
	migrations, err := LoadMigrations(owner, fsys)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		owner      TEXT NOT NULL,
		version    INTEGER NOT NULL,
		name       TEXT NOT NULL,
		checksum   TEXT NOT NULL,
		applied_at TEXT NOT NULL,
		PRIMARY KEY (owner, version)
	) STRICT`); err != nil {
		return fmt.Errorf("store: creating the migrations table: %w", err)
	}
	applied, err := db.applied(ctx, owner)
	if err != nil {
		return err
	}
	for version, sum := range applied {
		if version > len(migrations) {
			return fmt.Errorf("store: the database has migration %04d of %s, which this version of Racksail doesn't know; it was written by a newer release", version, owner)
		}
		if m := migrations[version-1]; sum != checksum(m.SQL) {
			return fmt.Errorf("store: migration %04d_%s of %s changed after it ran; add a new migration instead", m.Version, m.Name, owner)
		}
	}
	for _, m := range migrations {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		err := db.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (owner, version, name, checksum, applied_at) VALUES (?, ?, ?, ?, ?)`,
				owner, m.Version, m.Name, checksum(m.SQL), timeKey(time.Now()))
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migration %04d_%s of %s: %w", m.Version, m.Name, owner, err)
		}
	}
	return nil
}

// applied returns the checksums of the migrations of owner that have run,
// by version.
func (db *DB) applied(ctx context.Context, owner string) (map[int]string, error) {
	rows, err := db.reader.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations WHERE owner = ?`, owner)
	if err != nil {
		return nil, fmt.Errorf("store: reading the migrations of %s: %w", owner, err)
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var version int
		var sum string
		if err := rows.Scan(&version, &sum); err != nil {
			return nil, err
		}
		out[version] = sum
	}
	return out, rows.Err()
}

func checksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}
