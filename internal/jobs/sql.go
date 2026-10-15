package jobs

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

// Migrations returns the migrations of the job tables.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the job queue's migrations run under.
const MigrationOwner = "core.jobs"

// NewSQL returns a queue kept in the panel database, after bringing its
// tables up to date. Jobs stop when ctx is done; call Start once every job
// type is registered.
func NewSQL(ctx context.Context, db *store.DB, opts ManagerOptions) (*Manager, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return newManager(ctx, &sqlStorage{db: db}, opts), nil
}

type sqlStorage struct {
	db *store.DB
}

const jobColumns = `id, type, title, state, progress, actor, scope_level, scope_id, change_set, resource, attempt, retry_of,
	error, result, args, run_after, created_at, started_at, finished_at`

// row is a job as its columns hold it.
type row struct {
	progress, args              string
	actor, failure, result      sql.NullString
	runAfter, started, finished sql.NullString
	created, level              string
}

func jsonText(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func optionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return store.FormatTime(*t)
}

// values returns the columns of a job after id, in the order of jobColumns.
func values(j *Job) ([]any, error) {
	progress, err := json.Marshal(j.Progress)
	if err != nil {
		return nil, err
	}
	var actor any
	if j.Actor != nil {
		if actor, err = jsonText(j.Actor); err != nil {
			return nil, err
		}
	}
	var failure any
	if j.Error != nil {
		if failure, err = jsonText(j.Error); err != nil {
			return nil, err
		}
	}
	var result any
	if len(j.Result) > 0 {
		result = string(j.Result)
	}
	args := string(j.Args)
	if args == "" {
		args = "null"
	}
	return []any{
		j.Type, j.Title, j.State, string(progress), actor, j.Scope.Level, j.Scope.ID, j.ChangeSet, j.Resource, j.Attempt, j.RetryOf,
		failure, result, args, optionalTime(j.RunAfter), store.FormatTime(j.CreatedAt), optionalTime(j.StartedAt), optionalTime(j.FinishedAt),
	}, nil
}

func actorIDs(j *Job) (string, string) {
	if j.Actor == nil {
		return "", ""
	}
	return string(j.Actor.ID), string(j.Actor.User)
}

func (s *sqlStorage) insert(ctx context.Context, j *Job) error {
	vals, err := values(j)
	if err != nil {
		return err
	}
	actorID, actorUser := actorIDs(j)
	_, err = s.db.Exec(ctx, `INSERT INTO jobs (id, actor_id, actor_user, `+strings.Replace(jobColumns, "id, ", "", 1)+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, append([]any{j.ID, actorID, actorUser}, vals...)...)
	return err
}

func (s *sqlStorage) update(ctx context.Context, j *Job) error {
	vals, err := values(j)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE jobs SET type = ?, title = ?, state = ?, progress = ?, actor = ?, scope_level = ?, scope_id = ?,
		change_set = ?, resource = ?, attempt = ?, retry_of = ?, error = ?, result = ?, args = ?, run_after = ?, created_at = ?,
		started_at = ?, finished_at = ? WHERE id = ?`, append(vals, j.ID)...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return notFound(j.ID)
	}
	return nil
}

func scanJob(sc interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var r row
	err := sc.Scan(&j.ID, &j.Type, &j.Title, &j.State, &r.progress, &r.actor, &r.level, &j.Scope.ID, &j.ChangeSet, &j.Resource,
		&j.Attempt, &j.RetryOf, &r.failure, &r.result, &r.args, &r.runAfter, &r.created, &r.started, &r.finished)
	if err != nil {
		return nil, err
	}
	j.Scope.Level = store.ScopeLevel(r.level)
	if err := json.Unmarshal([]byte(r.progress), &j.Progress); err != nil {
		return nil, err
	}
	if r.actor.Valid {
		j.Actor = &auth.Actor{}
		if err := json.Unmarshal([]byte(r.actor.String), j.Actor); err != nil {
			return nil, err
		}
	}
	if r.failure.Valid {
		j.Error = &Failure{}
		if err := json.Unmarshal([]byte(r.failure.String), j.Error); err != nil {
			return nil, err
		}
	}
	if r.result.Valid {
		j.Result = json.RawMessage(r.result.String)
	}
	j.Args = json.RawMessage(r.args)
	if j.CreatedAt, err = store.ParseTime(r.created); err != nil {
		return nil, err
	}
	for _, t := range []struct {
		src sql.NullString
		dst **time.Time
	}{{r.runAfter, &j.RunAfter}, {r.started, &j.StartedAt}, {r.finished, &j.FinishedAt}} {
		if !t.src.Valid {
			continue
		}
		v, err := store.ParseTime(t.src.String)
		if err != nil {
			return nil, err
		}
		*t.dst = &v
	}
	return &j, nil
}

func (s *sqlStorage) get(ctx context.Context, id store.ID) (*Job, error) {
	j, err := scanJob(s.db.Reader().QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(id)
	}
	return j, err
}

func (s *sqlStorage) query(ctx context.Context, query string, args ...any) ([]*Job, error) {
	rows, err := s.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobs: reading the queue: %w", err)
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *sqlStorage) list(ctx context.Context, f Filter) ([]*Job, string, error) {
	var where []string
	var args []any
	add := func(clause string, a ...any) {
		where = append(where, clause)
		args = append(args, a...)
	}
	if f.Cursor != "" {
		before, err := store.DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		add("id < ?", before)
	}
	if f.State != "" {
		add("state = ?", f.State)
	}
	if f.Type != "" {
		add("type = ?", f.Type)
	}
	if f.ChangeSet != "" {
		add("change_set = ?", f.ChangeSet)
	}
	if f.Resource != "" {
		add("resource = ?", f.Resource)
	}
	if len(f.Scopes) > 0 {
		var alts []string
		for _, sc := range f.Scopes {
			alts = append(alts, "(scope_level = ? AND scope_id = ?)")
			args = append(args, sc.Level, sc.ID)
		}
		where = append(where, "("+strings.Join(alts, " OR ")+")")
	}
	if f.Actor != "" {
		add("(actor_id = ? OR actor_user = ?)", f.Actor, f.Actor)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.limit()
	out, err := s.query(ctx, `SELECT `+jobColumns+` FROM jobs`+clause+` ORDER BY id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		return out, store.EncodeCursor(out[limit-1].ID), nil
	}
	return out, "", nil
}

func (s *sqlStorage) runnable(ctx context.Context, now time.Time, limit int) ([]*Job, error) {
	return s.query(ctx, `SELECT `+jobColumns+` FROM jobs WHERE state = 'queued' AND (run_after IS NULL OR run_after <= ?) ORDER BY id LIMIT ?`,
		store.FormatTime(now), limit)
}

func (s *sqlStorage) nextRunAfter(ctx context.Context, now time.Time) (time.Time, bool, error) {
	var next sql.NullString
	err := s.db.Reader().QueryRowContext(ctx, `SELECT min(run_after) FROM jobs WHERE state = 'queued' AND run_after > ?`, store.FormatTime(now)).Scan(&next)
	if err != nil || !next.Valid {
		return time.Time{}, false, err
	}
	t, err := store.ParseTime(next.String)
	return t, err == nil, err
}

func (s *sqlStorage) interrupted(ctx context.Context) ([]*Job, error) {
	return s.query(ctx, `SELECT `+jobColumns+` FROM jobs WHERE state = 'running' ORDER BY id`)
}

func (s *sqlStorage) appendLog(ctx context.Context, id store.ID, line *LogLine) error {
	attrs, err := jsonText(line.Attrs)
	if err != nil {
		return err
	}
	if line.Attrs == nil {
		attrs = nil
	}
	_, err = s.db.Exec(ctx, `INSERT INTO job_logs (job_id, seq, time, level, message, attrs) VALUES (?, ?, ?, ?, ?, ?)`,
		id, line.Seq, store.FormatTime(line.Time), line.Level, line.Message, attrs)
	return err
}

func (s *sqlStorage) lastLogSeq(ctx context.Context, id store.ID) (int64, error) {
	var seq int64
	err := s.db.Reader().QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM job_logs WHERE job_id = ?`, id).Scan(&seq)
	return seq, err
}

func (s *sqlStorage) logs(ctx context.Context, id store.ID, after int64, limit int) ([]LogLine, error) {
	if _, err := s.get(ctx, id); err != nil {
		return nil, err
	}
	query := `SELECT seq, time, level, message, attrs FROM job_logs WHERE job_id = ? AND seq > ? ORDER BY seq`
	args := []any{id, after}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobs: reading a log: %w", err)
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		var when string
		var attrs sql.NullString
		if err := rows.Scan(&l.Seq, &when, &l.Level, &l.Message, &attrs); err != nil {
			return nil, err
		}
		if l.Time, err = store.ParseTime(when); err != nil {
			return nil, err
		}
		if attrs.Valid {
			if err := json.Unmarshal([]byte(attrs.String), &l.Attrs); err != nil {
				return nil, err
			}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *sqlStorage) prune(ctx context.Context, r Retention, now time.Time) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE finished_at IS NOT NULL AND finished_at < ?`, store.FormatTime(now.Add(-r.Finished))); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE finished_at IS NOT NULL AND id <= (
			SELECT id FROM jobs WHERE finished_at IS NOT NULL ORDER BY id DESC LIMIT 1 OFFSET ?)`, r.MaxFinished)
		return err
	})
}
