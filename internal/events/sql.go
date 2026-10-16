package events

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

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the migrations of the timeline and audit tables.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the timeline's migrations run under.
const MigrationOwner = "core.events"

// NewSQL returns a log kept in the panel database, after bringing its
// tables up to date.
func NewSQL(ctx context.Context, db *store.DB, opts Options) (*Log, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return newLog(&sqlStorage{db: db}, opts), nil
}

type sqlStorage struct {
	db *store.DB
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *sqlStorage) appendEvent(ctx context.Context, e *Event) error {
	var actor string
	if e.Actor != nil {
		b, err := json.Marshal(e.Actor)
		if err != nil {
			return err
		}
		actor = string(b)
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO events (id, type, time, severity, severity_rank, subject, kind, scope_level, scope_id, actor, change_set, job, message, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.Type, store.FormatTime(e.Time), e.Severity, severityRank[e.Severity], e.Subject, e.Kind, e.Scope.Level, e.Scope.ID,
			nullText(actor), e.ChangeSet, e.Job, e.Message, nullText(string(e.Data)))
		if err != nil {
			return err
		}
		e.Seq, err = res.LastInsertId()
		return err
	})
}

func (s *sqlStorage) appendAudit(ctx context.Context, r *AuditRecord) error {
	actor, err := json.Marshal(r.Actor)
	if err != nil {
		return err
	}
	var last any
	if r.LastTime != nil {
		last = store.FormatTime(*r.LastTime)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO audit (id, time, actor, actor_id, actor_user, action, kind, subject, scope_level, scope_id, change_set, outcome, reason, summary, details, request_id, repeats, last_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, store.FormatTime(r.Time), string(actor), r.Actor.ID, r.Actor.User, r.Action, r.Kind, r.Subject, r.Scope.Level, r.Scope.ID,
		r.ChangeSet, r.Outcome, r.Reason, r.Summary, nullText(string(r.Details)), r.RequestID, r.Repeats, last)
	return err
}

// repeatAudit keeps the latest time of the repeats, which may commit out of
// order.
func (s *sqlStorage) repeatAudit(ctx context.Context, id store.ID, n int, last time.Time) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE audit SET repeats = repeats + ?, last_time = max(coalesce(last_time, ''), ?) WHERE id = ?`, n, store.FormatTime(last), id)
	if err != nil {
		return false, fmt.Errorf("events: counting a repeated refusal: %w", err)
	}
	changed, err := res.RowsAffected()
	return changed > 0, err
}

// where builds the conditions shared by event and audit queries.
type where struct {
	clauses []string
	args    []any
}

func (w *where) add(clause string, args ...any) {
	w.clauses = append(w.clauses, clause)
	w.args = append(w.args, args...)
}

func (w *where) sql() string {
	if len(w.clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.clauses, " AND ")
}

// common adds the conditions events and audit records share. column names
// the column type patterns match.
func (w *where) common(q Query, column string) {
	if before, _ := q.before(); before > 0 {
		w.add("seq < ?", before)
	}
	if !q.Since.IsZero() {
		w.add("time >= ?", store.FormatTime(q.Since))
	}
	if !q.Until.IsZero() {
		w.add("time < ?", store.FormatTime(q.Until))
	}
	if q.Subject != "" {
		w.add("subject = ?", q.Subject)
	}
	if q.Kind != "" {
		w.add("kind = ?", q.Kind)
	}
	if q.Scope != nil {
		w.add("scope_level = ? AND scope_id = ?", q.Scope.Level, q.Scope.ID)
	}
	if len(q.Scopes) > 0 {
		var alts []string
		for _, sc := range q.Scopes {
			alts = append(alts, "(scope_level = ? AND scope_id = ?)")
			w.args = append(w.args, sc.Level, sc.ID)
		}
		w.clauses = append(w.clauses, "("+strings.Join(alts, " OR ")+")")
	}
	if q.ChangeSet != "" {
		w.add("change_set = ?", q.ChangeSet)
	}
	if len(q.Types) > 0 {
		// GLOB matches the way path.Match does for names without slashes.
		var alts []string
		for _, p := range q.Types {
			alts = append(alts, column+" GLOB ?")
			w.args = append(w.args, p)
		}
		w.clauses = append(w.clauses, "("+strings.Join(alts, " OR ")+")")
	}
}

const eventColumns = `seq, id, type, time, severity, subject, kind, scope_level, scope_id, actor, change_set, job, message, data`

func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var e Event
	var when, level string
	var actor, data sql.NullString
	if err := row.Scan(&e.Seq, &e.ID, &e.Type, &when, &e.Severity, &e.Subject, &e.Kind, &level, &e.Scope.ID, &actor, &e.ChangeSet, &e.Job, &e.Message, &data); err != nil {
		return e, err
	}
	e.Scope.Level = store.ScopeLevel(level)
	t, err := store.ParseTime(when)
	if err != nil {
		return e, err
	}
	e.Time = t
	if actor.Valid {
		e.Actor = &auth.Actor{}
		if err := json.Unmarshal([]byte(actor.String), e.Actor); err != nil {
			return e, err
		}
	}
	if data.Valid {
		e.Data = json.RawMessage(data.String)
	}
	return e, nil
}

func (s *sqlStorage) queryEvents(ctx context.Context, q Query) ([]Event, string, error) {
	var w where
	w.common(q, "type")
	if q.Job != "" {
		w.add("job = ?", q.Job)
	}
	if q.MinSeverity != "" {
		w.add("severity_rank >= ?", severityRank[q.MinSeverity])
	}
	limit := q.limit()
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT `+eventColumns+` FROM events`+w.sql()+` ORDER BY seq DESC LIMIT ?`, append(w.args, limit+1)...)
	if err != nil {
		return nil, "", fmt.Errorf("events: querying the timeline: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		return out, encodeSeq(out[limit-1].Seq), nil
	}
	return out, "", nil
}

func (s *sqlStorage) queryAudit(ctx context.Context, q Query) ([]AuditRecord, string, error) {
	var w where
	w.common(q, "action")
	if q.Actor != "" {
		w.add("(actor_id = ? OR actor_user = ?)", q.Actor, q.Actor)
	}
	if q.Outcome != "" {
		w.add("outcome = ?", q.Outcome)
	}
	limit := q.limit()
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT seq, id, time, actor, action, kind, subject, scope_level, scope_id, change_set, outcome, reason, summary, details, request_id, repeats, last_time
		FROM audit`+w.sql()+` ORDER BY seq DESC LIMIT ?`, append(w.args, limit+1)...)
	if err != nil {
		return nil, "", fmt.Errorf("events: querying the audit log: %w", err)
	}
	defer rows.Close()
	var out []AuditRecord
	var seqs []int64
	for rows.Next() {
		var r AuditRecord
		var seq int64
		var when, actor, level string
		var details, last sql.NullString
		if err := rows.Scan(&seq, &r.ID, &when, &actor, &r.Action, &r.Kind, &r.Subject, &level, &r.Scope.ID, &r.ChangeSet, &r.Outcome, &r.Reason, &r.Summary, &details, &r.RequestID, &r.Repeats, &last); err != nil {
			return nil, "", err
		}
		if last.Valid {
			t, err := store.ParseTime(last.String)
			if err != nil {
				return nil, "", err
			}
			r.LastTime = &t
		}
		r.Scope.Level = store.ScopeLevel(level)
		if r.Time, err = store.ParseTime(when); err != nil {
			return nil, "", err
		}
		if err := json.Unmarshal([]byte(actor), &r.Actor); err != nil {
			return nil, "", err
		}
		if details.Valid {
			r.Details = json.RawMessage(details.String)
		}
		out = append(out, r)
		seqs = append(seqs, seq)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		return out[:limit], encodeSeq(seqs[limit-1]), nil
	}
	return out, "", nil
}

func (s *sqlStorage) eventsAfter(ctx context.Context, seq int64, limit int) ([]Event, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT `+eventColumns+` FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, seq, limit)
	if err != nil {
		return nil, fmt.Errorf("events: reading the timeline: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqlStorage) seqOf(ctx context.Context, id store.ID) (int64, error) {
	var seq int64
	err := s.db.Reader().QueryRowContext(ctx, `SELECT seq FROM events WHERE id = ?`, id).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrNotFound.New("what", "Event "+string(id))
	}
	return seq, err
}

func (s *sqlStorage) lastSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.Reader().QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM events`).Scan(&seq)
	return seq, err
}

func (s *sqlStorage) prune(ctx context.Context, r Retention, now time.Time) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []struct {
			query string
			args  []any
		}{
			{`DELETE FROM events WHERE time < ?`, []any{store.FormatTime(now.Add(-r.Events))}},
			{`DELETE FROM events WHERE seq <= (SELECT seq FROM events ORDER BY seq DESC LIMIT 1 OFFSET ?)`, []any{r.MaxEvents}},
			{`DELETE FROM audit WHERE time < ?`, []any{store.FormatTime(now.Add(-r.Audit))}},
			{`DELETE FROM audit WHERE seq <= (SELECT seq FROM audit ORDER BY seq DESC LIMIT 1 OFFSET ?)`, []any{r.MaxAudit}},
		} {
			if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
				return fmt.Errorf("events: pruning: %w", err)
			}
		}
		return nil
	})
}
