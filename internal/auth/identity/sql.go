package identity

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the migrations of the identity tables.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationOwner is the owner the identity tables' migrations run under.
const MigrationOwner = "core.identity"

// ErrStepUsed is returned by UseTOTPStep for a code whose time step, or a
// later one, was used already.
var ErrStepUsed = errors.New("identity: that code's time step was used already")

// SQL is the Store kept in the panel database.
type SQL struct {
	db *store.DB
}

var _ Store = (*SQL)(nil)

// NewSQL returns the identity store kept in db, after bringing its tables up
// to date.
func NewSQL(ctx context.Context, db *store.DB) (*SQL, error) {
	if err := db.Migrate(ctx, MigrationOwner, Migrations()); err != nil {
		return nil, err
	}
	return &SQL{db: db}, nil
}

func ftime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return store.FormatTime(t)
}

func ptime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := store.ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func notFound(what string) error { return store.ErrNotFound.New("what", what) }

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

const userColumns = `id, username, name, email, home_level, home_id, grants, disabled, locale, allow_from, preferences, alert_channel,
	password_hash, password_changed_at, must_change_password, totp, totp_step, failed_sign_ins, locked_until,
	last_sign_in_at, last_sign_in_ip, created_at, updated_at, revision`

type scanner interface{ Scan(dest ...any) error }

func scanUser(row scanner) (*User, error) {
	var u User
	var grants, allow string
	var prefs sql.NullString
	var disabled, mustChange, totp int
	var changed, locked, last, created, updated string
	err := row.Scan(&u.ID, &u.Username, &u.Name, &u.Email, &u.Home.Level, &u.Home.ID, &grants, &disabled, &u.Locale, &allow, &prefs,
		&u.AlertChannel, &u.PasswordHash, &changed, &mustChange, &totp, &u.TOTPStep, &u.FailedSignIns, &locked, &last, &u.LastSignInIP,
		&created, &updated, &u.Revision)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(grants), &u.Grants); err != nil {
		return nil, fmt.Errorf("identity: grants of %s: %w", u.ID, err)
	}
	if err := json.Unmarshal([]byte(allow), &u.AllowFrom); err != nil {
		return nil, fmt.Errorf("identity: allowlist of %s: %w", u.ID, err)
	}
	if prefs.Valid {
		u.Preferences = json.RawMessage(prefs.String)
	}
	u.Disabled, u.MustChangePassword, u.TOTP = disabled == 1, mustChange == 1, totp == 1
	u.PasswordChangedAt, u.LockedUntil, u.LastSignInAt = ptime(changed), ptime(locked), ptime(last)
	u.CreatedAt, u.UpdatedAt = ptime(created), ptime(updated)
	if u.Grants == nil {
		u.Grants = []auth.Grant{}
	}
	return &u, nil
}

func writeGrants(ctx context.Context, tx *sql.Tx, u *User) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM identity_grants WHERE user_id = ?`, u.ID); err != nil {
		return err
	}
	for _, g := range u.Grants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_grants (user_id, role, scope_level, scope_id) VALUES (?, ?, ?, ?)`,
			u.ID, g.Role, g.Scope.Level, g.Scope.ID); err != nil {
			return err
		}
	}
	return nil
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// CountUsers implements Store.
func (s *SQL) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx, `SELECT count(*) FROM identity_users`).Scan(&n)
	return n, err
}

// CreateUser implements Store.
func (s *SQL) CreateUser(ctx context.Context, u *User) error {
	if u.Revision == 0 {
		u.Revision = 1
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO identity_users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.ID, u.Username, u.Name, u.Email, u.Home.Level, u.Home.ID, jsonText(u.Grants), flag(u.Disabled), u.Locale, jsonText(u.AllowFrom),
			nullJSON(u.Preferences), u.AlertChannel, u.PasswordHash, ftime(u.PasswordChangedAt), flag(u.MustChangePassword), flag(u.TOTP),
			u.TOTPStep, u.FailedSignIns, ftime(u.LockedUntil), ftime(u.LastSignInAt), u.LastSignInIP, ftime(u.CreatedAt), ftime(u.UpdatedAt), u.Revision)
		if err != nil {
			return err
		}
		return writeGrants(ctx, tx, u)
	})
	if isUnique(err) {
		return auth.ErrUsernameTaken.New("username", u.Username)
	}
	return err
}

// UpdateUser implements Store.
func (s *SQL) UpdateUser(ctx context.Context, u *User, revision int64) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM identity_users WHERE id = ?`, u.ID).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound(string(u.ID))
			}
			return err
		}
		if revision != 0 && revision != current {
			return i18n.ErrConflict.New("what", u.Username)
		}
		_, err := tx.ExecContext(ctx, `UPDATE identity_users SET username = ?, name = ?, email = ?, home_level = ?, home_id = ?, grants = ?,
			disabled = ?, locale = ?, allow_from = ?, preferences = ?, alert_channel = ?, updated_at = ?, revision = ? WHERE id = ?`,
			u.Username, u.Name, u.Email, u.Home.Level, u.Home.ID, jsonText(u.Grants), flag(u.Disabled), u.Locale, jsonText(u.AllowFrom),
			nullJSON(u.Preferences), u.AlertChannel, ftime(u.UpdatedAt), current+1, u.ID)
		if err != nil {
			return err
		}
		u.Revision = current + 1
		return writeGrants(ctx, tx, u)
	})
	if isUnique(err) {
		return auth.ErrUsernameTaken.New("username", u.Username)
	}
	return err
}

// DeleteUser implements Store.
func (s *SQL) DeleteUser(ctx context.Context, id store.ID) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_users WHERE id = ?`, id)
	return affected(res, err, string(id))
}

func affected(res sql.Result, err error, what string) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound(what)
	}
	return nil
}

// User implements Store.
func (s *SQL) User(ctx context.Context, id store.ID) (*User, error) {
	u, err := scanUser(s.db.Reader().QueryRowContext(ctx, `SELECT `+userColumns+` FROM identity_users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(string(id))
	}
	return u, err
}

// UserByName implements Store.
func (s *SQL) UserByName(ctx context.Context, username string) (*User, error) {
	u, err := scanUser(s.db.Reader().QueryRowContext(ctx, `SELECT `+userColumns+` FROM identity_users WHERE username = ?`, strings.ToLower(username)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(username)
	}
	return u, err
}

func encodeCursor(username string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(username))
}

func decodeCursor(cursor string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", i18n.ErrBadRequest.New("reason", "the cursor is malformed")
	}
	return string(b), nil
}

func limitOf(n int) int {
	if n <= 0 || n > store.MaxLimit {
		return store.DefaultLimit
	}
	return n
}

// ListUsers implements Store.
func (s *SQL) ListUsers(ctx context.Context, q UserQuery) ([]*User, string, error) {
	var where []string
	var args []any
	if q.Homes != nil {
		if len(q.Homes) == 0 {
			return []*User{}, "", nil
		}
		var or []string
		for _, h := range q.Homes {
			or = append(or, "(home_level = ? AND home_id = ?)")
			args = append(args, h.Level, h.ID)
		}
		where = append(where, "("+strings.Join(or, " OR ")+")")
	}
	if q.Grant != nil {
		where = append(where, `id IN (SELECT user_id FROM identity_grants WHERE scope_level = ? AND scope_id = ?)`)
		args = append(args, q.Grant.Level, q.Grant.ID)
	}
	if q.Role != "" {
		where = append(where, `id IN (SELECT user_id FROM identity_grants WHERE role = ?)`)
		args = append(args, q.Role)
	}
	if q.Search != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(q.Search)) + "%"
		where = append(where, `(username LIKE ? ESCAPE '\' OR lower(name) LIKE ? ESCAPE '\' OR lower(email) LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if q.Cursor != "" {
		after, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = append(where, "username > ?")
		args = append(args, after)
	}
	query := `SELECT ` + userColumns + ` FROM identity_users`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := limitOf(q.Limit)
	query += " ORDER BY username LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(out[limit-1].Username)
	}
	return out, next, nil
}

// SetPassword implements Store.
func (s *SQL) SetPassword(ctx context.Context, id store.ID, hash string, mustChange bool, at time.Time) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_users SET password_hash = ?, must_change_password = ?, password_changed_at = ? WHERE id = ?`,
		hash, flag(mustChange), ftime(at), id)
	return affected(res, err, string(id))
}

// SetTOTP implements Store.
func (s *SQL) SetTOTP(ctx context.Context, id store.ID, enabled bool, step int64) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_users SET totp = ?, totp_step = ? WHERE id = ?`, flag(enabled), step, id)
	return affected(res, err, string(id))
}

// UseTOTPStep implements Store.
func (s *SQL) UseTOTPStep(ctx context.Context, id store.ID, step int64) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_users SET totp_step = ? WHERE id = ? AND totp_step < ?`, step, id, step)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return err
		}
		return ErrStepUsed
	}
	return nil
}

// RecordSignIn implements Store.
func (s *SQL) RecordSignIn(ctx context.Context, id store.ID, failures int, lockedUntil, at time.Time, ip string) error {
	var res sql.Result
	var err error
	if at.IsZero() {
		res, err = s.db.Exec(ctx, `UPDATE identity_users SET failed_sign_ins = ?, locked_until = ? WHERE id = ?`, failures, ftime(lockedUntil), id)
	} else {
		res, err = s.db.Exec(ctx, `UPDATE identity_users SET failed_sign_ins = ?, locked_until = ?, last_sign_in_at = ?, last_sign_in_ip = ? WHERE id = ?`,
			failures, ftime(lockedUntil), ftime(at), ip, id)
	}
	return affected(res, err, string(id))
}

// ReplaceRecoveryCodes implements Store.
func (s *SQL) ReplaceRecoveryCodes(ctx context.Context, user store.ID, hashes []string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_recovery_codes WHERE user_id = ?`, user); err != nil {
			return err
		}
		for _, h := range hashes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO identity_recovery_codes (user_id, hash) VALUES (?, ?)`, user, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// UseRecoveryCode implements Store.
func (s *SQL) UseRecoveryCode(ctx context.Context, user store.ID, hash string) (bool, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_recovery_codes WHERE user_id = ? AND hash = ?`, user, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RecoveryCodesLeft implements Store.
func (s *SQL) RecoveryCodesLeft(ctx context.Context, user store.ID) (int, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx, `SELECT count(*) FROM identity_recovery_codes WHERE user_id = ?`, user).Scan(&n)
	return n, err
}

const passkeyColumns = `id, user_id, name, credential_id, credential, created_at, last_used_at`

func scanPasskey(row scanner) (*Passkey, error) {
	var p Passkey
	var cred, created, used string
	if err := row.Scan(&p.ID, &p.User, &p.Name, &p.CredentialID, &cred, &created, &used); err != nil {
		return nil, err
	}
	p.Credential, p.CreatedAt, p.LastUsedAt = json.RawMessage(cred), ptime(created), ptime(used)
	return &p, nil
}

// AddPasskey implements Store.
func (s *SQL) AddPasskey(ctx context.Context, p *Passkey) error {
	_, err := s.db.Exec(ctx, `INSERT INTO identity_passkeys (`+passkeyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.User, p.Name, p.CredentialID, string(p.Credential), ftime(p.CreatedAt), ftime(p.LastUsedAt))
	if isUnique(err) {
		return i18n.ErrAlreadyExists.New("what", "This passkey")
	}
	return err
}

// UpdatePasskey implements Store.
func (s *SQL) UpdatePasskey(ctx context.Context, p *Passkey) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_passkeys SET name = ?, credential = ?, last_used_at = ? WHERE id = ? AND user_id = ?`,
		p.Name, string(p.Credential), ftime(p.LastUsedAt), p.ID, p.User)
	return affected(res, err, string(p.ID))
}

// Passkeys implements Store.
func (s *SQL) Passkeys(ctx context.Context, user store.ID) ([]*Passkey, error) {
	return queryAll(ctx, s.db, scanPasskey, `SELECT `+passkeyColumns+` FROM identity_passkeys WHERE user_id = ? ORDER BY created_at, id`, user)
}

// PasskeyByCredential implements Store.
func (s *SQL) PasskeyByCredential(ctx context.Context, credentialID string) (*Passkey, error) {
	p, err := scanPasskey(s.db.Reader().QueryRowContext(ctx, `SELECT `+passkeyColumns+` FROM identity_passkeys WHERE credential_id = ?`, credentialID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("The passkey")
	}
	return p, err
}

// DeletePasskey implements Store.
func (s *SQL) DeletePasskey(ctx context.Context, user, id store.ID) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_passkeys WHERE id = ? AND user_id = ?`, id, user)
	return affected(res, err, string(id))
}

func queryAll[T any](ctx context.Context, db *store.DB, scan func(scanner) (*T, error), query string, args ...any) ([]*T, error) {
	rows, err := db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

const sessionColumns = `id, user_id, hash, created_at, last_seen_at, expires_at, ip, user_agent, method, second_factor, stepped_up_at,
	impersonator, parent, persistent, pending`

func scanSession(row scanner) (*Session, error) {
	var x Session
	var created, seen, expires, stepped string
	var persistent int
	err := row.Scan(&x.ID, &x.User, &x.Hash, &created, &seen, &expires, &x.IP, &x.UserAgent, &x.Method, &x.SecondFactor, &stepped,
		&x.Impersonator, &x.Parent, &persistent, &x.Pending)
	if err != nil {
		return nil, err
	}
	x.CreatedAt, x.LastSeenAt, x.ExpiresAt, x.SteppedUpAt = ptime(created), ptime(seen), ptime(expires), ptime(stepped)
	x.Persistent = persistent == 1
	return &x, nil
}

// CreateSession implements Store.
func (s *SQL) CreateSession(ctx context.Context, x *Session) error {
	_, err := s.db.Exec(ctx, `INSERT INTO identity_sessions (`+sessionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.User, x.Hash, ftime(x.CreatedAt), ftime(x.LastSeenAt), ftime(x.ExpiresAt), x.IP, x.UserAgent, x.Method, x.SecondFactor,
		ftime(x.SteppedUpAt), x.Impersonator, x.Parent, flag(x.Persistent), x.Pending)
	return err
}

// UpdateSession implements Store.
func (s *SQL) UpdateSession(ctx context.Context, x *Session) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_sessions SET hash = ?, last_seen_at = ?, expires_at = ?, ip = ?, user_agent = ?,
		second_factor = ?, stepped_up_at = ?, persistent = ?, pending = ? WHERE id = ?`,
		x.Hash, ftime(x.LastSeenAt), ftime(x.ExpiresAt), x.IP, x.UserAgent, x.SecondFactor, ftime(x.SteppedUpAt), flag(x.Persistent), x.Pending, x.ID)
	return affected(res, err, string(x.ID))
}

// Session implements Store.
func (s *SQL) Session(ctx context.Context, id store.ID) (*Session, error) {
	x, err := scanSession(s.db.Reader().QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM identity_sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(string(id))
	}
	return x, err
}

// SessionByHash implements Store.
func (s *SQL) SessionByHash(ctx context.Context, hash string) (*Session, error) {
	x, err := scanSession(s.db.Reader().QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM identity_sessions WHERE hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("The session")
	}
	return x, err
}

// Sessions implements Store.
func (s *SQL) Sessions(ctx context.Context, user store.ID) ([]*Session, error) {
	return queryAll(ctx, s.db, scanSession, `SELECT `+sessionColumns+` FROM identity_sessions WHERE user_id = ? ORDER BY last_seen_at DESC, id`, user)
}

// DeleteSession implements Store.
func (s *SQL) DeleteSession(ctx context.Context, id store.ID) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_sessions WHERE id = ?`, id)
	return affected(res, err, string(id))
}

// DeleteSessions implements Store.
func (s *SQL) DeleteSessions(ctx context.Context, user, keep store.ID) (int, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_sessions WHERE user_id = ? AND id <> ?`, user, keep)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteExpired implements Store.
func (s *SQL) DeleteExpired(ctx context.Context, now time.Time) error {
	t := ftime(now)
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM identity_sessions WHERE expires_at < ?`,
			`DELETE FROM identity_devices WHERE expires_at < ?`,
			`DELETE FROM identity_invites WHERE expires_at < ?`,
			`DELETE FROM identity_tokens WHERE expires_at <> '' AND expires_at < ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, t); err != nil {
				return err
			}
		}
		return nil
	})
}

const deviceColumns = `id, user_id, hash, name, ip, created_at, last_used_at, expires_at`

func scanDevice(row scanner) (*Device, error) {
	var d Device
	var created, used, expires string
	if err := row.Scan(&d.ID, &d.User, &d.Hash, &d.Name, &d.IP, &created, &used, &expires); err != nil {
		return nil, err
	}
	d.CreatedAt, d.LastUsedAt, d.ExpiresAt = ptime(created), ptime(used), ptime(expires)
	return &d, nil
}

// AddDevice implements Store.
func (s *SQL) AddDevice(ctx context.Context, d *Device) error {
	_, err := s.db.Exec(ctx, `INSERT INTO identity_devices (`+deviceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.User, d.Hash, d.Name, d.IP, ftime(d.CreatedAt), ftime(d.LastUsedAt), ftime(d.ExpiresAt))
	return err
}

// DeviceByHash implements Store.
func (s *SQL) DeviceByHash(ctx context.Context, hash string) (*Device, error) {
	d, err := scanDevice(s.db.Reader().QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM identity_devices WHERE hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("The device")
	}
	return d, err
}

// TouchDevice implements Store.
func (s *SQL) TouchDevice(ctx context.Context, id store.ID, at time.Time, ip string) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_devices SET last_used_at = ?, ip = ? WHERE id = ?`, ftime(at), ip, id)
	return affected(res, err, string(id))
}

// Devices implements Store.
func (s *SQL) Devices(ctx context.Context, user store.ID) ([]*Device, error) {
	return queryAll(ctx, s.db, scanDevice, `SELECT `+deviceColumns+` FROM identity_devices WHERE user_id = ? ORDER BY last_used_at DESC, id`, user)
}

// DeleteDevice implements Store.
func (s *SQL) DeleteDevice(ctx context.Context, user, id store.ID) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_devices WHERE id = ? AND user_id = ?`, id, user)
	return affected(res, err, string(id))
}

// DeleteDevices implements Store.
func (s *SQL) DeleteDevices(ctx context.Context, user store.ID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM identity_devices WHERE user_id = ?`, user)
	return err
}

const tokenColumns = `id, user_id, name, hash, hint, permissions, accounts, allow_from, expires_at, created_at, last_used_at, last_used_ip`

func scanToken(row scanner) (*Token, error) {
	var t Token
	var perms, accounts, allow, expires, created, used string
	if err := row.Scan(&t.ID, &t.User, &t.Name, &t.Hash, &t.Hint, &perms, &accounts, &allow, &expires, &created, &used, &t.LastUsedIP); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		text string
		into any
	}{{perms, &t.Permissions}, {accounts, &t.Accounts}, {allow, &t.AllowFrom}} {
		if err := json.Unmarshal([]byte(f.text), f.into); err != nil {
			return nil, fmt.Errorf("identity: token %s: %w", t.ID, err)
		}
	}
	t.ExpiresAt, t.CreatedAt, t.LastUsedAt = ptime(expires), ptime(created), ptime(used)
	return &t, nil
}

// CreateToken implements Store.
func (s *SQL) CreateToken(ctx context.Context, t *Token) error {
	_, err := s.db.Exec(ctx, `INSERT INTO identity_tokens (`+tokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.User, t.Name, t.Hash, t.Hint, jsonText(t.Permissions), jsonText(t.Accounts), jsonText(t.AllowFrom), ftime(t.ExpiresAt),
		ftime(t.CreatedAt), ftime(t.LastUsedAt), t.LastUsedIP)
	return err
}

// Token implements Store.
func (s *SQL) Token(ctx context.Context, id store.ID) (*Token, error) {
	t, err := scanToken(s.db.Reader().QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM identity_tokens WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(string(id))
	}
	return t, err
}

// TokenByHash implements Store.
func (s *SQL) TokenByHash(ctx context.Context, hash string) (*Token, error) {
	t, err := scanToken(s.db.Reader().QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM identity_tokens WHERE hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("The token")
	}
	return t, err
}

// TouchToken implements Store.
func (s *SQL) TouchToken(ctx context.Context, id store.ID, at time.Time, ip string) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, ftime(at), ip, id)
	return affected(res, err, string(id))
}

// Tokens implements Store.
func (s *SQL) Tokens(ctx context.Context, user store.ID) ([]*Token, error) {
	return queryAll(ctx, s.db, scanToken, `SELECT `+tokenColumns+` FROM identity_tokens WHERE user_id = ? ORDER BY created_at DESC, id`, user)
}

// DeleteToken implements Store.
func (s *SQL) DeleteToken(ctx context.Context, user, id store.ID) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_tokens WHERE id = ? AND user_id = ?`, id, user)
	return affected(res, err, string(id))
}

// DeleteTokens implements Store.
func (s *SQL) DeleteTokens(ctx context.Context, user store.ID) (int, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_tokens WHERE user_id = ?`, user)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

const sshKeyColumns = `id, user_id, name, type, bits, fingerprint, public_key, created_at`

func scanSSHKey(row scanner) (*SSHKey, error) {
	var k SSHKey
	var created string
	if err := row.Scan(&k.ID, &k.User, &k.Name, &k.Type, &k.Bits, &k.Fingerprint, &k.PublicKey, &created); err != nil {
		return nil, err
	}
	k.CreatedAt = ptime(created)
	return &k, nil
}

// AddSSHKey implements Store.
func (s *SQL) AddSSHKey(ctx context.Context, k *SSHKey) error {
	_, err := s.db.Exec(ctx, `INSERT INTO identity_ssh_keys (`+sshKeyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.User, k.Name, k.Type, k.Bits, k.Fingerprint, k.PublicKey, ftime(k.CreatedAt))
	if isUnique(err) {
		var name string
		if s.db.Reader().QueryRowContext(ctx, `SELECT name FROM identity_ssh_keys WHERE user_id = ? AND fingerprint = ?`, k.User, k.Fingerprint).Scan(&name) != nil {
			name = k.Fingerprint
		}
		return auth.ErrSSHKeyExists.New("name", name)
	}
	return err
}

// SSHKeys implements Store.
func (s *SQL) SSHKeys(ctx context.Context, user store.ID) ([]*SSHKey, error) {
	return queryAll(ctx, s.db, scanSSHKey, `SELECT `+sshKeyColumns+` FROM identity_ssh_keys WHERE user_id = ? ORDER BY created_at, id`, user)
}

// DeleteSSHKey implements Store.
func (s *SQL) DeleteSSHKey(ctx context.Context, user, id store.ID) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_ssh_keys WHERE id = ? AND user_id = ?`, id, user)
	return affected(res, err, string(id))
}

const inviteColumns = `id, user_id, hash, purpose, created_by, created_at, expires_at`

func scanInvite(row scanner) (*Invite, error) {
	var inv Invite
	var created, expires string
	if err := row.Scan(&inv.ID, &inv.User, &inv.Hash, &inv.Purpose, &inv.CreatedBy, &created, &expires); err != nil {
		return nil, err
	}
	inv.CreatedAt, inv.ExpiresAt = ptime(created), ptime(expires)
	return &inv, nil
}

// CreateInvite implements Store.
func (s *SQL) CreateInvite(ctx context.Context, inv *Invite) error {
	_, err := s.db.Exec(ctx, `INSERT INTO identity_invites (`+inviteColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.User, inv.Hash, inv.Purpose, inv.CreatedBy, ftime(inv.CreatedAt), ftime(inv.ExpiresAt))
	return err
}

// InviteByHash implements Store.
func (s *SQL) InviteByHash(ctx context.Context, hash string) (*Invite, error) {
	inv, err := scanInvite(s.db.Reader().QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM identity_invites WHERE hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("The sign-in link")
	}
	return inv, err
}

// UseInvite implements Store.
func (s *SQL) UseInvite(ctx context.Context, id store.ID) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_invites WHERE id = ?`, id)
	return affected(res, err, string(id))
}

// DeleteInvites implements Store.
func (s *SQL) DeleteInvites(ctx context.Context, user store.ID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM identity_invites WHERE user_id = ?`, user)
	return err
}

// LinkIdentity implements Store.
func (s *SQL) LinkIdentity(ctx context.Context, l *Link) error {
	_, err := s.db.Exec(ctx, `INSERT INTO identity_links (user_id, provider, subject, username, linked_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, provider) DO UPDATE SET subject = excluded.subject, username = excluded.username, linked_at = excluded.linked_at`,
		l.User, l.Provider, l.Subject, l.Username, ftime(l.LinkedAt))
	if isUnique(err) {
		return i18n.ErrAlreadyExists.New("what", "A link to this identity")
	}
	return err
}

// UserByIdentity implements Store.
func (s *SQL) UserByIdentity(ctx context.Context, provider store.ID, subject string) (*User, error) {
	u, err := scanUser(s.db.Reader().QueryRowContext(ctx, `SELECT `+prefixed("u.", userColumns)+` FROM identity_users u
		JOIN identity_links l ON l.user_id = u.id WHERE l.provider = ? AND l.subject = ?`, provider, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("The identity")
	}
	return u, err
}

func prefixed(prefix, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// Links implements Store.
func (s *SQL) Links(ctx context.Context, user store.ID) ([]*Link, error) {
	return queryAll(ctx, s.db, func(row scanner) (*Link, error) {
		var l Link
		var at string
		if err := row.Scan(&l.User, &l.Provider, &l.Subject, &l.Username, &at); err != nil {
			return nil, err
		}
		l.LinkedAt = ptime(at)
		return &l, nil
	}, `SELECT user_id, provider, subject, username, linked_at FROM identity_links WHERE user_id = ? ORDER BY linked_at`, user)
}

// UnlinkIdentity implements Store.
func (s *SQL) UnlinkIdentity(ctx context.Context, user, provider store.ID) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_links WHERE user_id = ? AND provider = ?`, user, provider)
	return affected(res, err, string(provider))
}

// EnableMFA implements Store.
func (s *SQL) EnableMFA(ctx context.Context, e *MFAEnrollment) error {
	_, err := s.db.Exec(ctx, `INSERT INTO identity_mfa (user_id, method, enabled_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id, method) DO UPDATE SET enabled_at = excluded.enabled_at`, e.User, e.Method, ftime(e.EnabledAt))
	return err
}

// MFAMethods implements Store.
func (s *SQL) MFAMethods(ctx context.Context, user store.ID) ([]*MFAEnrollment, error) {
	return queryAll(ctx, s.db, func(row scanner) (*MFAEnrollment, error) {
		var e MFAEnrollment
		var at string
		if err := row.Scan(&e.User, &e.Method, &at); err != nil {
			return nil, err
		}
		e.EnabledAt = ptime(at)
		return &e, nil
	}, `SELECT user_id, method, enabled_at FROM identity_mfa WHERE user_id = ? ORDER BY method`, user)
}

// DisableMFA implements Store.
func (s *SQL) DisableMFA(ctx context.Context, user store.ID, method string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM identity_mfa WHERE user_id = ? AND method = ?`, user, method)
	return affected(res, err, method)
}

// SetSetupCode implements Store.
func (s *SQL) SetSetupCode(ctx context.Context, hash string) error {
	if hash == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM identity_setup`)
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO identity_setup (id, hash) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET hash = excluded.hash`, hash)
	return err
}

// SetupCode implements Store.
func (s *SQL) SetupCode(ctx context.Context) (string, error) {
	var hash string
	err := s.db.Reader().QueryRowContext(ctx, `SELECT hash FROM identity_setup WHERE id = 1`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hash, err
}
