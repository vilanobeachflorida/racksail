package webhook

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the migrations of the delivery tables.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the delivery tables' migrations run under.
const MigrationOwner = "core.webhooks"

// NewSQL returns a dispatcher that keeps its deliveries and its place in
// the timeline in the panel database, after bringing its tables up to date.
func NewSQL(ctx context.Context, db *store.DB, opts Options) (*Dispatcher, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return newDispatcher(&sqlStorage{db: db}, opts), nil
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *sqlStorage) cursor(ctx context.Context) (int64, bool, error) {
	var seq int64
	err := s.db.Reader().QueryRowContext(ctx, `SELECT seq FROM webhook_cursor WHERE id = 1`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return seq, err == nil, err
}

func insertDelivery(ctx context.Context, tx *sql.Tx, d *Delivery, payload json.RawMessage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO webhook_deliveries (id, webhook, event, event_type, state, test, redelivery_of, attempt_count,
		next_attempt, last_status, last_error, created_at, finished_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Webhook, d.Event, d.EventType, d.State, boolInt(d.Test), d.RedeliveryOf, d.AttemptCount,
		optionalTime(d.NextAttempt), d.LastStatus, d.LastError, store.FormatTime(d.CreatedAt), optionalTime(d.FinishedAt), string(payload))
	return err
}

func (s *sqlStorage) enqueue(ctx context.Context, deliveries []pending, cursor int64) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, p := range deliveries {
			if err := insertDelivery(ctx, tx, &p.delivery, p.payload); err != nil {
				return fmt.Errorf("webhook: storing a delivery: %w", err)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO webhook_cursor (id, seq) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET seq = excluded.seq`, cursor)
		return err
	})
}

func (s *sqlStorage) insert(ctx context.Context, d *Delivery) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error { return insertDelivery(ctx, tx, d, d.Payload) })
}

const deliveryColumns = `id, webhook, event, event_type, state, test, redelivery_of, attempt_count, next_attempt, last_status, last_error, created_at, finished_at`

func scanDelivery(sc interface{ Scan(...any) error }, payload *string) (*Delivery, error) {
	var d Delivery
	var next, finished sql.NullString
	var created string
	dest := []any{&d.ID, &d.Webhook, &d.Event, &d.EventType, &d.State, &d.Test, &d.RedeliveryOf, &d.AttemptCount, &next, &d.LastStatus, &d.LastError, &created, &finished}
	if payload != nil {
		dest = append(dest, payload)
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
	if payload != nil {
		d.Payload = json.RawMessage(*payload)
	}
	return &d, nil
}

func (s *sqlStorage) due(ctx context.Context, now time.Time, limit int) ([]*Delivery, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT `+deliveryColumns+`, payload FROM webhook_deliveries
		WHERE state = 'pending' AND next_attempt <= ? ORDER BY id LIMIT ?`, store.FormatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("webhook: reading deliveries: %w", err)
	}
	defer rows.Close()
	var out []*Delivery
	for rows.Next() {
		var payload string
		d, err := scanDelivery(rows, &payload)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *sqlStorage) nextDue(ctx context.Context) (time.Time, bool, error) {
	var next sql.NullString
	if err := s.db.Reader().QueryRowContext(ctx, `SELECT min(next_attempt) FROM webhook_deliveries WHERE state = 'pending'`).Scan(&next); err != nil || !next.Valid {
		return time.Time{}, false, err
	}
	t, err := store.ParseTime(next.String)
	return t, err == nil, err
}

func (s *sqlStorage) record(ctx context.Context, d *Delivery, a Attempt) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE webhook_deliveries SET state = ?, attempt_count = ?, next_attempt = ?, last_status = ?, last_error = ?, finished_at = ? WHERE id = ?`,
			d.State, d.AttemptCount, optionalTime(d.NextAttempt), d.LastStatus, d.LastError, optionalTime(d.FinishedAt), d.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrDeliveryNotFound.New("delivery", string(d.ID))
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO webhook_attempts (delivery, number, time, status, error, duration, response) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			d.ID, a.Number, store.FormatTime(a.Time), a.Status, a.Error, a.Duration, a.Response)
		return err
	})
}

func (s *sqlStorage) get(ctx context.Context, id store.ID) (*Delivery, error) {
	var payload string
	d, err := scanDelivery(s.db.Reader().QueryRowContext(ctx, `SELECT `+deliveryColumns+`, payload FROM webhook_deliveries WHERE id = ?`, id), &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryNotFound.New("delivery", string(id))
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT number, time, status, error, duration, response FROM webhook_attempts WHERE delivery = ? ORDER BY number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attempt
		var when string
		if err := rows.Scan(&a.Number, &when, &a.Status, &a.Error, &a.Duration, &a.Response); err != nil {
			return nil, err
		}
		if a.Time, err = store.ParseTime(when); err != nil {
			return nil, err
		}
		d.Attempts = append(d.Attempts, a)
	}
	return d, rows.Err()
}

func (s *sqlStorage) list(ctx context.Context, webhook store.ID, state DeliveryState, cursor string, limit int) ([]*Delivery, string, error) {
	query := `SELECT ` + deliveryColumns + ` FROM webhook_deliveries WHERE webhook = ?`
	args := []any{webhook}
	if state != "" {
		query += ` AND state = ?`
		args = append(args, state)
	}
	if cursor != "" {
		before, err := store.DecodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		query += ` AND id < ?`
		args = append(args, before)
	}
	rows, err := s.db.Reader().QueryContext(ctx, query+` ORDER BY id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", fmt.Errorf("webhook: listing deliveries: %w", err)
	}
	defer rows.Close()
	var out []*Delivery
	for rows.Next() {
		d, err := scanDelivery(rows, nil)
		if err != nil {
			return nil, "", err
		}
		out = append(out, d)
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

func (s *sqlStorage) prune(ctx context.Context, before time.Time, max int) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE state <> 'pending' AND created_at < ?`, store.FormatTime(before)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE state <> 'pending' AND id <= (
			SELECT id FROM webhook_deliveries WHERE state <> 'pending' ORDER BY id DESC LIMIT 1 OFFSET ?)`, max)
		return err
	})
}
