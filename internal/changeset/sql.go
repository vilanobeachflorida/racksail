package changeset

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the migrations of the change set tables.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the change set tables' migrations run under.
const MigrationOwner = "core.changesets"

// SQLStorage keeps change sets in the panel database.
type SQLStorage struct {
	db *store.DB
}

// NewSQLStorage returns the change set storage kept in db, after bringing
// its tables up to date.
func NewSQLStorage(ctx context.Context, db *store.DB) (*SQLStorage, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return &SQLStorage{db: db}, nil
}

var _ Storage = (*SQLStorage)(nil)

func actorKeys(cs *ChangeSet) (string, string) {
	if cs.Actor == nil {
		return "", ""
	}
	return string(cs.Actor.ID), string(cs.Actor.User)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// write stores a change set and the resources it touched within tx.
func (s *SQLStorage) write(ctx context.Context, tx *sql.Tx, cs *ChangeSet, request *StoredRequest, keepRequest bool) error {
	body, err := json.Marshal(cs)
	if err != nil {
		return err
	}
	actorID, actorUser := actorKeys(cs)
	if keepRequest {
		_, err = tx.ExecContext(ctx, `UPDATE change_sets SET title = ?, state = ?, dry_run = ?, actor_id = ?, actor_user = ?, undo_of = ?, created_at = ?, body = ? WHERE id = ?`,
			cs.Title, cs.State, boolInt(cs.DryRun), actorID, actorUser, cs.UndoOf, store.FormatTime(cs.CreatedAt), string(body), cs.ID)
	} else {
		var req any
		if request != nil {
			raw, err := json.Marshal(request)
			if err != nil {
				return err
			}
			req = string(raw)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO change_sets (id, title, state, dry_run, actor_id, actor_user, undo_of, created_at, body, request)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET title = excluded.title, state = excluded.state, dry_run = excluded.dry_run,
				actor_id = excluded.actor_id, actor_user = excluded.actor_user, undo_of = excluded.undo_of,
				created_at = excluded.created_at, body = excluded.body, request = excluded.request`,
			cs.ID, cs.Title, cs.State, boolInt(cs.DryRun), actorID, actorUser, cs.UndoOf, store.FormatTime(cs.CreatedAt), string(body), req)
	}
	if err != nil {
		return fmt.Errorf("changeset: storing %s: %w", cs.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM change_set_resources WHERE change_set = ?`, cs.ID); err != nil {
		return err
	}
	for _, c := range cs.Changes {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO change_set_resources (change_set, resource, scope_level, scope_id) VALUES (?, ?, ?, ?)`,
			cs.ID, c.ID, c.Scope.Level, c.Scope.ID); err != nil {
			return err
		}
	}
	return nil
}

// Save implements Storage.
func (s *SQLStorage) Save(ctx context.Context, cs *ChangeSet, request *StoredRequest) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error { return s.write(ctx, tx, cs, request, false) })
}

func decodeSet(body string) (*ChangeSet, error) {
	var cs ChangeSet
	if err := json.Unmarshal([]byte(body), &cs); err != nil {
		return nil, fmt.Errorf("changeset: reading a change set: %w", err)
	}
	return &cs, nil
}

// Get implements Storage.
func (s *SQLStorage) Get(ctx context.Context, id store.ID) (*ChangeSet, error) {
	var body string
	err := s.db.Reader().QueryRowContext(ctx, `SELECT body FROM change_sets WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(id)
	}
	if err != nil {
		return nil, err
	}
	return decodeSet(body)
}

// Request implements Storage.
func (s *SQLStorage) Request(ctx context.Context, id store.ID) (*StoredRequest, error) {
	var raw sql.NullString
	err := s.db.Reader().QueryRowContext(ctx, `SELECT request FROM change_sets WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !raw.Valid) {
		return nil, notFound(id)
	}
	if err != nil {
		return nil, err
	}
	var r StoredRequest
	if err := json.Unmarshal([]byte(raw.String), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Update implements Storage.
func (s *SQLStorage) Update(ctx context.Context, id store.ID, fn func(cs *ChangeSet) error) (*ChangeSet, error) {
	var out *ChangeSet
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var body string
		err := tx.QueryRowContext(ctx, `SELECT body FROM change_sets WHERE id = ?`, id).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound(id)
		}
		if err != nil {
			return err
		}
		cs, err := decodeSet(body)
		if err != nil {
			return err
		}
		if err := fn(cs); err != nil {
			return err
		}
		out = cs
		return s.write(ctx, tx, cs, nil, true)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// List implements Storage.
func (s *SQLStorage) List(ctx context.Context, q ListQuery) ([]*ChangeSet, string, error) {
	where := []string{"dry_run = ?"}
	args := []any{boolInt(q.DryRuns)}
	if q.Cursor != "" {
		before, err := store.DecodeCursor(q.Cursor)
		if err != nil {
			return nil, "", err
		}
		where, args = append(where, "id < ?"), append(args, before)
	}
	if q.State != "" {
		where, args = append(where, "state = ?"), append(args, q.State)
	}
	if q.UndoOf != "" {
		where, args = append(where, "undo_of = ?"), append(args, q.UndoOf)
	}
	if q.Actor != "" {
		where, args = append(where, "(actor_id = ? OR actor_user = ?)"), append(args, q.Actor, q.Actor)
	}
	if q.Resource != "" {
		where, args = append(where, "id IN (SELECT change_set FROM change_set_resources WHERE resource = ?)"), append(args, q.Resource)
	}
	if v := q.Visible; v != nil {
		// Their own, or every resource is in reach.
		var reach []string
		var reachArgs []any
		for _, sc := range v.Scopes {
			reach, reachArgs = append(reach, "(r.scope_level = ? AND r.scope_id = ?)"), append(reachArgs, sc.Level, sc.ID)
		}
		if len(v.Objects) > 0 {
			reach = append(reach, "r.resource IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(v.Objects)), ", ")+")")
			for _, id := range v.Objects {
				reachArgs = append(reachArgs, id)
			}
		}
		inReach := "0"
		if len(reach) > 0 {
			inReach = strings.Join(reach, " OR ")
		}
		where = append(where, `((? <> '' AND CASE WHEN actor_user <> '' THEN actor_user ELSE actor_id END = ?)
			OR (EXISTS (SELECT 1 FROM change_set_resources r WHERE r.change_set = change_sets.id)
				AND NOT EXISTS (SELECT 1 FROM change_set_resources r WHERE r.change_set = change_sets.id AND NOT (`+inReach+`))))`)
		args = append(append(args, v.Person, v.Person), reachArgs...)
	}
	limit := q.limit()
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT body FROM change_sets WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", fmt.Errorf("changeset: listing: %w", err)
	}
	defer rows.Close()
	var out []*ChangeSet
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, "", err
		}
		cs, err := decodeSet(body)
		if err != nil {
			return nil, "", err
		}
		out = append(out, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		return out, store.EncodeCursor(out[limit-1].ID), nil
	}
	return out, "", nil
}

// Prune implements Storage.
func (s *SQLStorage) Prune(ctx context.Context, r Retention, now time.Time, keep func(id store.ID) bool) ([]*ChangeSet, error) {
	var evicted []*ChangeSet
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		evicted = nil
		for _, group := range []struct {
			where string
			ttl   time.Duration
			most  int
		}{
			{`dry_run = 1`, r.Previews, r.MaxPreviews},
			{`dry_run = 0 AND state IN ('applied', 'failed', 'undone')`, r.Finished, r.MaxFinished},
		} {
			// What has expired, and what is beyond the newest the cap keeps.
			// Usually neither finds anything, so only IDs are read here.
			rows, err := tx.QueryContext(ctx, `SELECT id FROM change_sets WHERE `+group.where+` AND created_at < ?
				UNION SELECT id FROM (SELECT id FROM change_sets WHERE `+group.where+` ORDER BY id DESC LIMIT -1 OFFSET ?)
				ORDER BY id`, store.FormatTime(now.Add(-group.ttl)), group.most)
			if err != nil {
				return err
			}
			var ids []store.ID
			for rows.Next() {
				var id store.ID
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				if !keep(id) {
					ids = append(ids, id)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, id := range ids {
				var body string
				if err := tx.QueryRowContext(ctx, `SELECT body FROM change_sets WHERE id = ?`, id).Scan(&body); err != nil {
					return err
				}
				cs, err := decodeSet(body)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM change_sets WHERE id = ?`, id); err != nil {
					return err
				}
				evicted = append(evicted, cs)
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE time < ?
			OR (change_set <> '' AND change_set NOT IN (SELECT id FROM change_sets))`, store.FormatTime(now.Add(-IdempotencyWindow)))
		return err
	})
	return evicted, err
}

// Mentions implements Storage.
func (s *SQLStorage) Mentions(ctx context.Context, text string) (bool, error) {
	var one int
	err := s.db.Reader().QueryRowContext(ctx, `SELECT 1 FROM change_sets WHERE dry_run = 0 AND instr(body, ?) > 0 LIMIT 1`, text).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ReserveKey implements Storage.
func (s *SQLStorage) ReserveKey(ctx context.Context, key string, now time.Time) (store.ID, bool, error) {
	var earlier store.ID
	var busy bool
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if earlier, busy, err = lookupKey(ctx, tx, key, now); err != nil || earlier != "" || busy {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_keys (key, change_set, time) VALUES (?, '', ?)
			ON CONFLICT (key) DO UPDATE SET change_set = '', time = excluded.time`, key, store.FormatTime(now))
		return err
	})
	return earlier, busy, err
}

// LookupKey implements Storage.
func (s *SQLStorage) LookupKey(ctx context.Context, key string, now time.Time) (store.ID, bool, error) {
	return lookupKey(ctx, s.db.Reader(), key, now)
}

// lookupKey returns the change set a request with the key produced, as long
// as it is kept, or whether one is still being processed.
func lookupKey(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, key string, now time.Time) (store.ID, bool, error) {
	var set, when string
	err := q.QueryRowContext(ctx, `SELECT change_set, time FROM idempotency_keys WHERE key = ?`, key).Scan(&set, &when)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	t, err := store.ParseTime(when)
	if err != nil {
		return "", false, err
	}
	switch {
	case now.Sub(t) > IdempotencyWindow:
		return "", false, nil
	case set == "":
		return "", now.Sub(t) <= reservationTimeout, nil
	}
	var one int
	err = q.QueryRowContext(ctx, `SELECT 1 FROM change_sets WHERE id = ?`, set).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return store.ID(set), false, nil
}

// SettleKey implements Storage.
func (s *SQLStorage) SettleKey(ctx context.Context, key string, id store.ID, now time.Time) error {
	if id == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM idempotency_keys WHERE key = ?`, key)
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO idempotency_keys (key, change_set, time) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET change_set = excluded.change_set, time = excluded.time`, key, id, store.FormatTime(now))
	return err
}
