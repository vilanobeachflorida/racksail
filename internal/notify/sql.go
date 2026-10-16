package notify

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the migrations of the notification tables.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the notification tables' migrations run
// under.
const MigrationOwner = "core.notifications"

// ErrDeliveryNotFound is returned for a delivery that doesn't exist.
var ErrDeliveryNotFound = i18n.Define("notify.delivery_not_found", http.StatusNotFound, "There is no notification {delivery}.")

// NewSQL returns a service that keeps its deliveries, its place in the
// timeline and people's preferences in the panel database, after bringing
// its tables up to date.
func NewSQL(ctx context.Context, db *store.DB, opts Options) (*Service, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return newService(&sqlStorage{db: db}, opts), nil
}

type sqlStorage struct {
	db *store.DB
}

func optionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return store.FormatTime(*t)
}

func (s *sqlStorage) cursor(ctx context.Context) (int64, bool, error) {
	var seq int64
	err := s.db.Reader().QueryRowContext(ctx, `SELECT seq FROM notify_cursor WHERE id = 1`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return seq, err == nil, err
}

func insertDelivery(ctx context.Context, tx *sql.Tx, d *Notification) error {
	msg, err := json.Marshal(d.Message)
	if err != nil {
		return err
	}
	test := 0
	if d.Test {
		test = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notify_deliveries (id, channel, person, address, event, event_type, title, severity, state, test,
		attempt_count, next_attempt, last_error, summary, created_at, finished_at, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Channel, d.Person, d.Address, d.Event, d.EventType, d.Title, d.Severity, d.State, test,
		d.AttemptCount, optionalTime(d.NextAttempt), d.LastError, d.Summary, store.FormatTime(d.CreatedAt), optionalTime(d.FinishedAt), string(msg))
	return err
}

func (s *sqlStorage) enqueue(ctx context.Context, deliveries []*Notification, cursor int64) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, d := range deliveries {
			if err := insertDelivery(ctx, tx, d); err != nil {
				return fmt.Errorf("notify: storing a delivery: %w", err)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO notify_cursor (id, seq) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET seq = excluded.seq`, cursor)
		return err
	})
}

func (s *sqlStorage) insert(ctx context.Context, d *Notification) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error { return insertDelivery(ctx, tx, d) })
}

const deliveryColumns = `id, channel, person, address, event, event_type, title, severity, state, test, attempt_count, next_attempt, last_error, summary, created_at, finished_at`

func scanDelivery(sc interface{ Scan(...any) error }, withMessage bool) (*Notification, error) {
	var d Notification
	var next, finished sql.NullString
	var created, msg string
	dest := []any{&d.ID, &d.Channel, &d.Person, &d.Address, &d.Event, &d.EventType, &d.Title, &d.Severity, &d.State, &d.Test,
		&d.AttemptCount, &next, &d.LastError, &d.Summary, &created, &finished}
	if withMessage {
		dest = append(dest, &msg)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	var err error
	if d.CreatedAt, err = store.ParseTime(created); err != nil {
		return nil, err
	}
	for _, t := range []struct {
		src sql.NullString
		dst **time.Time
	}{{next, &d.NextAttempt}, {finished, &d.FinishedAt}} {
		if !t.src.Valid {
			continue
		}
		v, err := store.ParseTime(t.src.String)
		if err != nil {
			return nil, err
		}
		*t.dst = &v
	}
	if withMessage {
		var m Message
		if err := json.Unmarshal([]byte(msg), &m); err != nil {
			return nil, fmt.Errorf("notify: reading the message of %s: %w", d.ID, err)
		}
		d.Message = &m
	}
	return &d, nil
}

func (s *sqlStorage) query(ctx context.Context, withMessage bool, where string, args ...any) ([]*Notification, error) {
	cols := deliveryColumns
	if withMessage {
		cols += ", message"
	}
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT `+cols+` FROM notify_deliveries `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("notify: reading deliveries: %w", err)
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		d, err := scanDelivery(rows, withMessage)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *sqlStorage) due(ctx context.Context, now time.Time, limit int) ([]*Notification, error) {
	return s.query(ctx, true, `WHERE state = 'pending' AND next_attempt <= ? ORDER BY id LIMIT ?`, store.FormatTime(now), limit)
}

func (s *sqlStorage) heldDue(ctx context.Context, now time.Time) ([]*Notification, error) {
	return s.query(ctx, true, `WHERE state = 'held' AND next_attempt <= ? ORDER BY id LIMIT 1000`, store.FormatTime(now))
}

func (s *sqlStorage) nextDue(ctx context.Context) (time.Time, bool, error) {
	var next sql.NullString
	if err := s.db.Reader().QueryRowContext(ctx, `SELECT min(next_attempt) FROM notify_deliveries WHERE state IN ('pending', 'held')`).Scan(&next); err != nil || !next.Valid {
		return time.Time{}, false, err
	}
	t, err := store.ParseTime(next.String)
	return t, err == nil, err
}

func (s *sqlStorage) record(ctx context.Context, d *Notification) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET state = ?, attempt_count = ?, next_attempt = ?, last_error = ?, finished_at = ? WHERE id = ?`,
			d.State, d.AttemptCount, optionalTime(d.NextAttempt), d.LastError, optionalTime(d.FinishedAt), d.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrDeliveryNotFound.New("delivery", string(d.ID))
		}
		return nil
	})
}

// setState moves a held delivery on to be sent.
func (s *sqlStorage) setState(ctx context.Context, id store.ID, state NotificationState, next time.Time) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET state = ?, next_attempt = ? WHERE id = ?`, state, store.FormatTime(next), id)
		return err
	})
}

// merge stores a summary and marks the deliveries it sums up.
func (s *sqlStorage) merge(ctx context.Context, summary *Notification, merged []*Notification) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := insertDelivery(ctx, tx, summary); err != nil {
			return err
		}
		now := store.FormatTime(summary.CreatedAt)
		for _, d := range merged {
			if _, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET state = 'merged', summary = ?, next_attempt = NULL, finished_at = ? WHERE id = ?`,
				summary.ID, now, d.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *sqlStorage) get(ctx context.Context, id store.ID) (*Notification, error) {
	out, err := s.query(ctx, true, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrDeliveryNotFound.New("delivery", string(id))
	}
	return out[0], nil
}

func (s *sqlStorage) list(ctx context.Context, f DeliveryFilter) ([]*Notification, string, error) {
	where := `WHERE 1 = 1`
	var args []any
	if f.Channel != "" {
		where += ` AND channel = ?`
		args = append(args, f.Channel)
	}
	if f.Person != "" {
		where += ` AND person = ?`
		args = append(args, f.Person)
	}
	if f.State != "" {
		where += ` AND state = ?`
		args = append(args, f.State)
	}
	if f.Cursor != "" {
		before, err := store.DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where += ` AND id < ?`
		args = append(args, before)
	}
	out, err := s.query(ctx, false, where+` ORDER BY id DESC LIMIT ?`, append(args, f.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	if len(out) > f.Limit {
		out = out[:f.Limit]
		return out, store.EncodeCursor(out[f.Limit-1].ID), nil
	}
	return out, "", nil
}

func (s *sqlStorage) pending(ctx context.Context) (int, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx, `SELECT count(*) FROM notify_deliveries WHERE state IN ('pending', 'held')`).Scan(&n)
	return n, err
}

func (s *sqlStorage) prune(ctx context.Context, before time.Time) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM notify_deliveries WHERE created_at < ? AND state NOT IN ('pending', 'held')`, store.FormatTime(before))
		return err
	})
}

func (s *sqlStorage) preferences(ctx context.Context, person store.ID) (NotificationPreferences, error) {
	var raw string
	err := s.db.Reader().QueryRowContext(ctx, `SELECT preferences FROM notify_preferences WHERE person = ?`, person).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationPreferences{}, nil
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	var p NotificationPreferences
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return NotificationPreferences{}, fmt.Errorf("notify: reading the preferences of %s: %w", person, err)
	}
	return p, nil
}

func (s *sqlStorage) allPreferences(ctx context.Context) (map[store.ID]*NotificationPreferences, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT person, preferences FROM notify_preferences`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[store.ID]*NotificationPreferences{}
	for rows.Next() {
		var person, raw string
		if err := rows.Scan(&person, &raw); err != nil {
			return nil, err
		}
		var p NotificationPreferences
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("notify: reading the preferences of %s: %w", person, err)
		}
		out[store.ID(person)] = &p
	}
	return out, rows.Err()
}

func (s *sqlStorage) setPreferences(ctx context.Context, person store.ID, p NotificationPreferences) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO notify_preferences (person, preferences, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (person) DO UPDATE SET preferences = excluded.preferences, updated_at = excluded.updated_at`,
			person, string(raw), store.FormatTime(*p.UpdatedAt))
		return err
	})
}
