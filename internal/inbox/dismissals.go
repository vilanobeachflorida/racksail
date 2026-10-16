package inbox

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"maps"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Dismissals keeps what people dismissed or snoozed.
type Dismissals interface {
	// Of returns a person's dismissals by item ID.
	Of(ctx context.Context, person string) (map[string]Dismissal, error)
	// Put records a dismissal, replacing the person's earlier one of the
	// item.
	Put(ctx context.Context, person, item string, d Dismissal) error
	// Delete takes a dismissal back and reports whether there was one.
	Delete(ctx context.Context, person, item string) (bool, error)
	// Prune deletes snoozes that ended by now and dismissals made before
	// before, and reports how many it deleted.
	Prune(ctx context.Context, now, before time.Time) (int, error)
}

// Memory keeps dismissals in memory, for tests and tools.
type Memory struct {
	mu sync.Mutex
	by map[string]map[string]Dismissal
}

// NewMemory returns dismissals kept in memory.
func NewMemory() *Memory { return &Memory{by: map[string]map[string]Dismissal{}} }

// Of implements Dismissals.
func (m *Memory) Of(ctx context.Context, person string) (map[string]Dismissal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.by[person]), nil
}

// Put implements Dismissals.
func (m *Memory) Put(ctx context.Context, person, item string, d Dismissal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.by[person] == nil {
		m.by[person] = map[string]Dismissal{}
	}
	m.by[person][item] = d
	return nil
}

// Delete implements Dismissals.
func (m *Memory) Delete(ctx context.Context, person, item string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.by[person][item]
	delete(m.by[person], item)
	return ok, nil
}

// Prune implements Dismissals.
func (m *Memory) Prune(ctx context.Context, now, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, items := range m.by {
		for id, d := range items {
			if (d.Until != nil && !d.Until.After(now)) || d.At.Before(before) {
				delete(items, id)
				n++
			}
		}
	}
	return n, nil
}

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the migrations of the dismissals table.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the inbox's migrations run under.
const MigrationOwner = "core.inbox"

// SQL keeps dismissals in the panel database.
type SQL struct {
	db *store.DB
}

// NewSQL returns dismissals kept in the panel database, after bringing its
// table up to date.
func NewSQL(ctx context.Context, db *store.DB) (*SQL, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return &SQL{db: db}, nil
}

// Of implements Dismissals.
func (s *SQL) Of(ctx context.Context, person string) (map[string]Dismissal, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT item, version, at, until FROM inbox_dismissals WHERE person = ?`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Dismissal{}
	for rows.Next() {
		var item, version, at string
		var until sql.NullString
		if err := rows.Scan(&item, &version, &at, &until); err != nil {
			return nil, err
		}
		d := Dismissal{Version: version}
		if d.At, err = store.ParseTime(at); err != nil {
			return nil, err
		}
		if until.Valid {
			t, err := store.ParseTime(until.String)
			if err != nil {
				return nil, err
			}
			d.Until = &t
		}
		out[item] = d
	}
	return out, rows.Err()
}

// Put implements Dismissals.
func (s *SQL) Put(ctx context.Context, person, item string, d Dismissal) error {
	var until any
	if d.Until != nil {
		until = store.FormatTime(*d.Until)
	}
	_, err := s.db.Exec(ctx, `INSERT INTO inbox_dismissals (person, item, version, at, until) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (person, item) DO UPDATE SET version = excluded.version, at = excluded.at, until = excluded.until`,
		person, item, d.Version, store.FormatTime(d.At), until)
	return err
}

// Delete implements Dismissals.
func (s *SQL) Delete(ctx context.Context, person, item string) (bool, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM inbox_dismissals WHERE person = ? AND item = ?`, person, item)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Prune implements Dismissals.
func (s *SQL) Prune(ctx context.Context, now, before time.Time) (int, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM inbox_dismissals WHERE (until IS NOT NULL AND until <= ?) OR at < ?`,
		store.FormatTime(now), store.FormatTime(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
