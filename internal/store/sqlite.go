package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

//go:embed migrations/*.sql
var coreMigrations embed.FS

// CoreMigrations returns the migrations of the resource tables, which
// NewSQLite applies under the owner "core".
func CoreMigrations() fs.FS {
	sub, err := fs.Sub(coreMigrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// SQLite is the panel's Store, kept in the panel database. Everything about
// how it behaves is the same as Memory; the conformance tests run against
// both.
type SQLite struct {
	db    *DB
	kinds *KindRegistry
	now   func() time.Time
	hub   hub
}

// NewSQLite returns the store kept in db, for the kinds in reg, after
// bringing its tables up to date.
func NewSQLite(ctx context.Context, db *DB, reg *KindRegistry, opts ...MemoryOption) (*SQLite, error) {
	if err := db.Migrate(ctx, "core", CoreMigrations()); err != nil {
		return nil, err
	}
	// Options are shared with Memory; apply them to a scratch one.
	m := &Memory{now: time.Now}
	for _, o := range opts {
		o(m)
	}
	s := &SQLite{db: db, kinds: reg, now: m.now}
	if err := s.fillSortKeys(ctx); err != nil {
		return nil, fmt.Errorf("store: filling in the sort keys of indexed values: %w", err)
	}
	return s, nil
}

// fillSortKeys gives the index values written before they had sort keys
// theirs, worked out from their resources' specs. Values of kinds this
// binary doesn't know sort as text.
func (s *SQLite) fillSortKeys(ctx context.Context) error {
	var one int
	err := s.db.Reader().QueryRowContext(ctx, `SELECT 1 FROM resource_index_values WHERE sort_key = '' LIMIT 1`).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		type pending struct {
			id   ID
			kind string
			spec string
		}
		rows, err := tx.QueryContext(ctx, `SELECT r.id, r.kind, r.spec FROM resources r
			WHERE EXISTS (SELECT 1 FROM resource_index_values v WHERE v.resource_id = r.id AND v.sort_key = '')`)
		if err != nil {
			return err
		}
		var todo []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.kind, &p.spec); err != nil {
				rows.Close()
				return err
			}
			todo = append(todo, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, p := range todo {
			k, ok := s.kinds.Get(p.kind)
			if !ok {
				continue
			}
			for _, e := range searchValues(k.Info(), json.RawMessage(p.spec)) {
				if _, err := tx.ExecContext(ctx, `UPDATE resource_index_values SET sort_key = ? WHERE resource_id = ? AND name = ? AND value = ? AND sort_key = ''`,
					e.sortKey, p.id, e.name, e.value); err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE resource_index_values SET sort_key = ? || value WHERE sort_key = ''`, textSortKey(""))
		return err
	})
}

// DB returns the database the store lives in.
func (s *SQLite) DB() *DB { return s.db }

// querier is what reads need: the reader pool or a write transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const resourceColumns = `r.id, r.kind, r.scope_level, r.scope_id, r.server, r.labels, r.spec, r.status,
	r.generation, r.revision, r.created_at, r.updated_at, r.deleted_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanResource(row scanner) (*Resource, error) {
	var r Resource
	var labels, spec string
	var status, deletedAt sql.NullString
	var created, updated string
	var level string
	err := row.Scan(&r.ID, &r.Kind, &level, &r.Scope.ID, &r.Server, &labels, &spec, &status,
		&r.Generation, &r.Revision, &created, &updated, &deletedAt)
	if err != nil {
		return nil, err
	}
	r.Scope.Level = ScopeLevel(level)
	if labels != "" && labels != "{}" {
		if err := json.Unmarshal([]byte(labels), &r.Labels); err != nil {
			return nil, fmt.Errorf("store: labels of %s: %w", r.ID, err)
		}
	}
	r.Spec = json.RawMessage(spec)
	if status.Valid {
		r.Status = json.RawMessage(status.String)
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if r.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	if deletedAt.Valid {
		t, err := parseTime(deletedAt.String)
		if err != nil {
			return nil, err
		}
		r.DeletedAt = &t
	}
	return &r, nil
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: reading a time: %w", err)
	}
	return t.UTC(), nil
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timeKey(*t)
}

func encodeLabels(labels map[string]string) (string, error) {
	if len(labels) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(labels)
	return string(b), err
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func getResource(ctx context.Context, q querier, id ID) (*Resource, error) {
	r, err := scanResource(q.QueryRowContext(ctx, `SELECT `+resourceColumns+` FROM resources r WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound.New("what", string(id))
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading %s: %w", id, err)
	}
	return r, nil
}

// Get implements Reader.
func (s *SQLite) Get(ctx context.Context, id ID) (*Resource, error) {
	return getResource(ctx, s.db.Reader(), id)
}

// List implements Reader.
func (s *SQLite) List(ctx context.Context, q Query) (*Page, error) {
	return listResources(ctx, s.db.Reader(), s.kinds, q)
}

// History implements Store.
func (s *SQLite) History(ctx context.Context, id ID) ([]Version, error) {
	out, err := history(ctx, s.db.Reader(), id)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound.New("what", string(id))
	}
	return out, nil
}

// history reads every recorded version of a resource, oldest first.
func history(ctx context.Context, q querier, id ID) ([]Version, error) {
	rows, err := q.QueryContext(ctx, `SELECT generation, kind, scope_level, scope_id, spec, labels, server, change_set, actor, time, deleted
		FROM resource_history WHERE resource_id = ? ORDER BY generation`, id)
	if err != nil {
		return nil, fmt.Errorf("store: reading the history of %s: %w", id, err)
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var level, spec, labels, when string
		if err := rows.Scan(&v.Generation, &v.Kind, &level, &v.Scope.ID, &spec, &labels, &v.Server, &v.ChangeSet, &v.Actor, &when, &v.Deleted); err != nil {
			return nil, err
		}
		v.Scope.Level = ScopeLevel(level)
		v.Spec = json.RawMessage(spec)
		if labels != "{}" {
			if err := json.Unmarshal([]byte(labels), &v.Labels); err != nil {
				return nil, err
			}
		}
		if v.Time, err = parseTime(when); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Write implements Store. Within the database's Atomic, the writes join its
// transaction, and watchers hear about them once it commits.
func (s *SQLite) Write(ctx context.Context, meta WriteMeta, fn func(tx Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var tx *sqliteTx
	err := s.db.Write(ctx, func(sqlTx *sql.Tx) error {
		tx = &sqliteTx{tx: sqlTx, kinds: s.kinds, meta: meta, now: s.now().UTC()}
		err := fn(tx)
		tx.closed = true
		return err
	})
	if err != nil {
		return err
	}
	changes := tx.changes
	s.db.AfterCommit(ctx, func() { s.hub.publish(changes) })
	return nil
}

// Watch implements Store.
func (s *SQLite) Watch(f WatchFilter, buffer int) *Watcher { return s.hub.watch(f, buffer) }

// Close implements Store. The database belongs to whoever opened it, so it
// stays open.
func (s *SQLite) Close() error { return nil }

type sqliteTx struct {
	tx      *sql.Tx
	kinds   *KindRegistry
	meta    WriteMeta
	now     time.Time
	closed  bool
	changes []Notice
}

func (tx *sqliteTx) Get(ctx context.Context, id ID) (*Resource, error) {
	if tx.closed {
		return nil, errTxClosed
	}
	return getResource(ctx, tx.tx, id)
}

func (tx *sqliteTx) List(ctx context.Context, q Query) (*Page, error) {
	if tx.closed {
		return nil, errTxClosed
	}
	return listResources(ctx, tx.tx, tx.kinds, q)
}

func (tx *sqliteTx) exec(ctx context.Context, query string, args ...any) error {
	_, err := tx.tx.ExecContext(ctx, query, args...)
	return err
}

func (tx *sqliteTx) Create(ctx context.Context, r *Resource) error {
	if tx.closed {
		return errTxClosed
	}
	k, ok := tx.kinds.Get(r.Kind)
	if !ok {
		return i18n.ErrUnknownName.New("what", "kind", "name", r.Kind)
	}
	info := k.Info()
	if err := checkNewID(info, r); err != nil {
		return err
	}
	var exists int
	switch err := tx.tx.QueryRowContext(ctx, `SELECT 1 FROM resources WHERE id = ?`, r.ID).Scan(&exists); {
	case err == nil:
		return ErrExists.New("what", string(r.ID))
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if err := checkMeta(info, r); err != nil {
		return err
	}
	if info.Singleton {
		err := tx.tx.QueryRowContext(ctx, `SELECT 1 FROM resources WHERE kind = ? AND scope_level = ? AND scope_id = ? AND deleted_at IS NULL LIMIT 1`,
			info.Name, r.Scope.Level, r.Scope.ID).Scan(&exists)
		switch {
		case err == nil:
			return ErrExists.New("what", info.Title)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	if len(r.Spec) == 0 {
		r.Spec = json.RawMessage("{}")
	}
	keys, err := uniqueKeys(info, r)
	if err != nil {
		return err
	}
	if err := tx.checkUnique(ctx, info, keys, r.ID); err != nil {
		return err
	}
	versions, err := history(ctx, tx.tx, r.ID)
	if err != nil {
		return err
	}
	if err := CheckReuse(versions, r, tx.meta.UndoOf); err != nil {
		return err
	}
	// A resource brought back under its ID, as when a delete is undone,
	// carries on from the generations it had.
	r.Generation, r.Revision = 1, 1
	if len(versions) > 0 {
		r.Generation = versions[len(versions)-1].Generation + 1
	}
	r.CreatedAt, r.UpdatedAt, r.DeletedAt = tx.now, tx.now, nil
	labels, err := encodeLabels(r.Labels)
	if err != nil {
		return err
	}
	err = tx.exec(ctx, `INSERT INTO resources (id, kind, scope_level, scope_id, server, labels, spec, status, generation, revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Kind, r.Scope.Level, r.Scope.ID, r.Server, labels, string(r.Spec), nullJSON(r.Status), r.Generation, r.Revision, timeKey(r.CreatedAt), timeKey(r.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: creating %s: %w", r.ID, err)
	}
	if err := tx.writeIndexes(ctx, info, r, keys); err != nil {
		return err
	}
	if err := tx.record(ctx, r, false); err != nil {
		return err
	}
	tx.changes = append(tx.changes, noticeOf(NoticeCreated, r, tx.meta.ChangeSet))
	return nil
}

func (tx *sqliteTx) Update(ctx context.Context, r *Resource) error {
	if tx.closed {
		return errTxClosed
	}
	cur, err := tx.live(ctx, r.ID)
	if err != nil {
		return err
	}
	if r.Revision != 0 && r.Revision != cur.Revision {
		return ErrConflict.New("what", string(r.ID))
	}
	if r.Kind != cur.Kind {
		return i18n.Invalid(i18n.Field("kind", i18n.ErrImmutable))
	}
	if r.Scope != cur.Scope {
		return i18n.Invalid(i18n.Field("scope", i18n.ErrImmutable))
	}
	k, ok := tx.kinds.Get(cur.Kind)
	if !ok {
		return i18n.ErrUnknownName.New("what", "kind", "name", cur.Kind)
	}
	info := k.Info()
	if err := checkMeta(info, r); err != nil {
		return err
	}
	if len(r.Spec) == 0 {
		r.Spec = json.RawMessage("{}")
	}
	keys, err := uniqueKeys(info, r)
	if err != nil {
		return err
	}
	if err := tx.checkUnique(ctx, info, keys, cur.ID); err != nil {
		return err
	}
	next := cur.Clone()
	specChanged := !jsonEqual(cur.Spec, r.Spec)
	placementChanged := cur.Server != r.Server
	next.Spec = slices.Clone(r.Spec)
	next.Labels = maps.Clone(r.Labels)
	next.Server = r.Server
	next.Revision++
	next.UpdatedAt = tx.now
	if specChanged || placementChanged {
		next.Generation++
	}
	labels, err := encodeLabels(next.Labels)
	if err != nil {
		return err
	}
	if err := tx.exec(ctx, `UPDATE resources SET server = ?, labels = ?, spec = ?, generation = ?, revision = ?, updated_at = ? WHERE id = ?`,
		next.Server, labels, string(next.Spec), next.Generation, next.Revision, timeKey(next.UpdatedAt), next.ID); err != nil {
		return fmt.Errorf("store: updating %s: %w", next.ID, err)
	}
	if err := tx.exec(ctx, `DELETE FROM resource_index_values WHERE resource_id = ?`, next.ID); err != nil {
		return err
	}
	if err := tx.exec(ctx, `DELETE FROM resource_unique_values WHERE resource_id = ?`, next.ID); err != nil {
		return err
	}
	if err := tx.writeIndexes(ctx, info, next, keys); err != nil {
		return err
	}
	if specChanged || placementChanged {
		if err := tx.record(ctx, next, false); err != nil {
			return err
		}
	}
	r.Meta = next.Meta
	r.Labels = maps.Clone(next.Labels)
	tx.changes = append(tx.changes, noticeOf(NoticeUpdated, next, tx.meta.ChangeSet))
	return nil
}

func (tx *sqliteTx) UpdateStatus(ctx context.Context, id ID, status json.RawMessage) error {
	if tx.closed {
		return errTxClosed
	}
	if len(status) > 0 && !json.Valid(status) {
		return i18n.Invalid(i18n.Field("status", i18n.ErrBadValue, "reason", "it isn't valid JSON"))
	}
	res, err := tx.tx.ExecContext(ctx, `UPDATE resources SET status = ? WHERE id = ?`, nullJSON(status), id)
	if err != nil {
		return fmt.Errorf("store: writing the status of %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrNotFound.New("what", string(id))
	}
	cur, err := getResource(ctx, tx.tx, id)
	if err != nil {
		return err
	}
	tx.changes = append(tx.changes, noticeOf(NoticeStatus, cur, tx.meta.ChangeSet))
	return nil
}

func (tx *sqliteTx) Delete(ctx context.Context, id ID, revision int64) error {
	if tx.closed {
		return errTxClosed
	}
	cur, err := tx.live(ctx, id)
	if err != nil {
		return err
	}
	if revision != 0 && revision != cur.Revision {
		return ErrConflict.New("what", string(id))
	}
	next := cur.Clone()
	now := tx.now
	next.DeletedAt = &now
	next.Generation++
	next.Revision++
	next.UpdatedAt = now
	if err := tx.exec(ctx, `UPDATE resources SET deleted_at = ?, generation = ?, revision = ?, updated_at = ? WHERE id = ?`,
		nullTime(next.DeletedAt), next.Generation, next.Revision, timeKey(next.UpdatedAt), id); err != nil {
		return fmt.Errorf("store: deleting %s: %w", id, err)
	}
	if err := tx.record(ctx, next, true); err != nil {
		return err
	}
	tx.changes = append(tx.changes, noticeOf(NoticeDeleted, next, tx.meta.ChangeSet))
	return nil
}

func (tx *sqliteTx) Purge(ctx context.Context, id ID) error {
	if tx.closed {
		return errTxClosed
	}
	cur, err := getResource(ctx, tx.tx, id)
	if err != nil {
		return err
	}
	// The index and unique values go with the row.
	if err := tx.exec(ctx, `DELETE FROM resources WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: purging %s: %w", id, err)
	}
	tx.changes = append(tx.changes, noticeOf(NoticePurged, cur, tx.meta.ChangeSet))
	return nil
}

// live returns the stored resource unless it is missing or deleted.
func (tx *sqliteTx) live(ctx context.Context, id ID) (*Resource, error) {
	cur, err := getResource(ctx, tx.tx, id)
	if err != nil {
		return nil, err
	}
	if cur.Deleted() {
		return nil, ErrNotFound.New("what", string(id))
	}
	return cur, nil
}

// checkUnique fails when another resource holds one of the keys.
func (tx *sqliteTx) checkUnique(ctx context.Context, info *KindInfo, keys []uniqueKey, id ID) error {
	for _, key := range keys {
		var owner ID
		err := tx.tx.QueryRowContext(ctx, `SELECT resource_id FROM resource_unique_values WHERE kind = ? AND scope = ? AND name = ? AND value = ?`,
			key.kind, key.scope, key.index, key.value).Scan(&owner)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case owner != id:
			return uniqueConflict(info, key)
		}
	}
	return nil
}

// writeIndexes stores the index, search and unique values of r.
func (tx *sqliteTx) writeIndexes(ctx context.Context, info *KindInfo, r *Resource, keys []uniqueKey) error {
	for _, e := range searchValues(info, r.Spec) {
		if err := tx.exec(ctx, `INSERT OR IGNORE INTO resource_index_values (resource_id, kind, name, value, sort_key) VALUES (?, ?, ?, ?, ?)`,
			r.ID, info.Name, e.name, e.value, e.sortKey); err != nil {
			return fmt.Errorf("store: indexing %s: %w", r.ID, err)
		}
	}
	for _, key := range keys {
		if err := tx.exec(ctx, `INSERT INTO resource_unique_values (kind, scope, name, value, resource_id) VALUES (?, ?, ?, ?, ?)`,
			key.kind, key.scope, key.index, key.value, r.ID); err != nil {
			return fmt.Errorf("store: indexing %s: %w", r.ID, err)
		}
	}
	return nil
}

func (tx *sqliteTx) record(ctx context.Context, r *Resource, deleted bool) error {
	labels, err := encodeLabels(r.Labels)
	if err != nil {
		return err
	}
	err = tx.exec(ctx, `INSERT INTO resource_history (resource_id, generation, kind, scope_level, scope_id, spec, labels, server, change_set, actor, time, deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Generation, r.Kind, r.Scope.Level, r.Scope.ID, string(r.Spec), labels, r.Server, tx.meta.ChangeSet, tx.meta.Actor, timeKey(tx.now), deleted)
	if err != nil {
		return fmt.Errorf("store: recording the history of %s: %w", r.ID, err)
	}
	return nil
}

// listResources builds and runs the query of a list. Filters, sorting and
// cursors mean exactly what they mean for Memory.
func listResources(ctx context.Context, q querier, reg *KindRegistry, query Query) (*Page, error) {
	info, s, err := checkQuery(reg, query)
	if err != nil {
		return nil, err
	}
	var where []string
	var args []any
	add := func(clause string, a ...any) {
		where = append(where, clause)
		args = append(args, a...)
	}
	add("r.kind = ?", query.Kind)
	if !query.IncludeDeleted && !query.Unreconciled {
		add("r.deleted_at IS NULL")
	}
	if query.Scope != nil {
		add("r.scope_level = ? AND r.scope_id = ?", query.Scope.Level, query.Scope.ID)
	}
	if len(query.Scopes) > 0 {
		var alts []string
		for _, sc := range query.Scopes {
			alts = append(alts, "(r.scope_level = ? AND r.scope_id = ?)")
			args = append(args, sc.Level, sc.ID)
		}
		where = append(where, "("+strings.Join(alts, " OR ")+")")
	}
	if query.Server != nil {
		add("r.server = ?", *query.Server)
	}
	for _, key := range slices.Sorted(maps.Keys(query.Labels)) {
		add("json_extract(r.labels, ?) = ?", `$."`+key+`"`, query.Labels[key])
	}
	for _, name := range slices.Sorted(maps.Keys(query.Index)) {
		value := query.Index[name]
		if findIndex(info, name).Fold {
			value = strings.ToLower(value)
		}
		add("EXISTS (SELECT 1 FROM resource_index_values v WHERE v.resource_id = r.id AND v.name = ? AND v.value = ?)", name, value)
	}
	if len(query.Phases) > 0 {
		add("r.phase IN ("+placeholders(len(query.Phases))+")", anySlice(query.Phases)...)
	}
	if query.Unreconciled {
		add("(r.deleted_at IS NOT NULL OR r.observed_generation < r.generation)")
	}
	if query.Search != "" {
		text := asciiLower(query.Search)
		add("(instr(lower(r.id), ?) > 0 OR EXISTS (SELECT 1 FROM resource_index_values v WHERE v.resource_id = r.id AND instr(lower(v.value), ?) > 0))", text, text)
	}
	if len(query.IDs) > 0 {
		add("r.id IN ("+placeholders(len(query.IDs))+")", anySlice(query.IDs)...)
	}
	sortExpr, sortArgs := sqlSortExpr(s)
	if query.Cursor != "" {
		after, err := s.decode(query.Cursor)
		if err != nil {
			return nil, err
		}
		cmp, idCmp := ">", ">"
		if s.desc {
			cmp, idCmp = "<", "<"
		}
		where = append(where, fmt.Sprintf("(%s %s ? OR (%s = ? AND r.id %s ?))", sortExpr, cmp, sortExpr, idCmp))
		args = append(args, sortArgs...)
		args = append(args, after.value)
		args = append(args, sortArgs...)
		args = append(args, after.value, after.id)
	}
	dir := "ASC"
	if s.desc {
		dir = "DESC"
	}
	limit := query.EffectiveLimit()
	stmt := `SELECT ` + resourceColumns + ` FROM resources r WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + sortExpr + ` ` + dir + `, r.id ` + dir + ` LIMIT ?`
	args = append(args, sortArgs...)
	args = append(args, limit+1)
	rows, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("store: listing %s: %w", query.Kind, err)
	}
	defer rows.Close()
	page := &Page{Items: []*Resource{}}
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = s.encode(s.positionOf(info, page.Items[limit-1]))
	}
	return page, nil
}

// sqlSortExpr is the SQL that computes what sortValue computes, and its
// arguments.
func sqlSortExpr(s sortSpec) (string, []any) {
	switch s.field {
	case SortCreatedAt:
		return "r.created_at", nil
	case SortUpdatedAt:
		return "r.updated_at", nil
	case SortGeneration:
		return "printf('%020d', r.generation)", nil
	case SortPhase:
		return "r.phase", nil
	case SortID:
		return "r.id", nil
	}
	return "coalesce((SELECT min(v.sort_key) FROM resource_index_values v WHERE v.resource_id = r.id AND v.name = ?), '')", []any{s.field}
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func anySlice[T any](items []T) []any {
	out := make([]any, len(items))
	for i, v := range items {
		out[i] = v
	}
	return out
}
