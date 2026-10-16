package hooks

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// DisabledHook is a hook the circuit breaker switched off. It stays off,
// across restarts of the panel too, until an admin switches it back on.
type DisabledHook struct {
	Owner string `json:"owner" doc:"Who registered the hook, such as addon:site-guard"`
	// Point is the point or pattern the hook was attached to.
	Point    Point     `json:"point" doc:"The hook point, or pattern of points, the hook is attached to"`
	Failures int       `json:"failures" doc:"How many times in a row it failed before it was switched off"`
	Reason   string    `json:"reason" doc:"Why it failed the last time"`
	At       time.Time `json:"at" doc:"When it was switched off"`
}

// BreakerStore keeps the hooks the circuit breaker switched off.
type BreakerStore interface {
	Disabled(ctx context.Context) ([]DisabledHook, error)
	Disable(ctx context.Context, d DisabledHook) error
	Enable(ctx context.Context, owner string, p Point) error
}

// MemoryBreakers keeps switched-off hooks in memory, for tests and tools.
type MemoryBreakers struct {
	mu sync.Mutex
	m  map[string]DisabledHook
}

// NewMemoryBreakers returns an empty in-memory BreakerStore.
func NewMemoryBreakers() *MemoryBreakers { return &MemoryBreakers{m: map[string]DisabledHook{}} }

// Disabled implements BreakerStore.
func (s *MemoryBreakers) Disabled(ctx context.Context) ([]DisabledHook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DisabledHook, 0, len(s.m))
	for _, d := range s.m {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// Disable implements BreakerStore.
func (s *MemoryBreakers) Disable(ctx context.Context, d DisabledHook) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[breakerKey(d.Owner, d.Point)] = d
	return nil
}

// Enable implements BreakerStore.
func (s *MemoryBreakers) Enable(ctx context.Context, owner string, p Point) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, breakerKey(owner, p))
	return nil
}

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the migrations of the hooks' tables.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the hooks' migrations run under.
const MigrationOwner = "core.hooks"

// SQLBreakers keeps switched-off hooks in the panel database.
type SQLBreakers struct {
	db *store.DB
}

// NewSQLBreakers brings the hooks' tables up to date and returns a
// BreakerStore kept in them.
func NewSQLBreakers(ctx context.Context, db *store.DB) (*SQLBreakers, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return &SQLBreakers{db: db}, nil
}

// Disabled implements BreakerStore.
func (s *SQLBreakers) Disabled(ctx context.Context) ([]DisabledHook, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT owner, point, failures, reason, disabled_at FROM hook_breakers ORDER BY disabled_at, owner, point`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DisabledHook
	for rows.Next() {
		var d DisabledHook
		var at string
		if err := rows.Scan(&d.Owner, &d.Point, &d.Failures, &d.Reason, &at); err != nil {
			return nil, err
		}
		if d.At, err = store.ParseTime(at); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Disable implements BreakerStore.
func (s *SQLBreakers) Disable(ctx context.Context, d DisabledHook) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO hook_breakers (owner, point, failures, reason, disabled_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (owner, point) DO UPDATE SET failures = excluded.failures, reason = excluded.reason, disabled_at = excluded.disabled_at`,
			d.Owner, string(d.Point), d.Failures, d.Reason, store.FormatTime(d.At))
		return err
	})
}

// Enable implements BreakerStore.
func (s *SQLBreakers) Enable(ctx context.Context, owner string, p Point) error {
	_, err := s.db.Exec(ctx, `DELETE FROM hook_breakers WHERE owner = ? AND point = ?`, owner, string(p))
	return err
}
