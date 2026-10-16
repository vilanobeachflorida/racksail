package identity

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

const credentialColumns = `password_hash, password_changed_at, must_change_password, totp, totp_step, failed_sign_ins, locked_until,
	last_sign_in_at, last_sign_in_ip, preferences, alert_channel, ssh_keys_seen`

type scanner interface{ Scan(dest ...any) error }

func scanCredentials(row scanner) (*Credentials, error) {
	var c Credentials
	var prefs sql.NullString
	var mustChange, totp int
	var changed, locked, last, seen string
	err := row.Scan(&c.PasswordHash, &changed, &mustChange, &totp, &c.TOTPStep, &c.FailedSignIns, &locked, &last, &c.LastSignInIP,
		&prefs, &c.AlertChannel, &seen)
	if err != nil {
		return nil, err
	}
	if prefs.Valid {
		c.Preferences = json.RawMessage(prefs.String)
	}
	if err := json.Unmarshal([]byte(seen), &c.SSHKeysSeen); err != nil {
		return nil, fmt.Errorf("identity: SSH keys seen: %w", err)
	}
	c.MustChangePassword, c.TOTP = mustChange == 1, totp == 1
	c.PasswordChangedAt, c.LockedUntil, c.LastSignInAt = ptime(changed), ptime(locked), ptime(last)
	return &c, nil
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// ensureUser makes the record a user's credentials and everything else
// kept for them hang off, when there isn't one yet.
func ensureUser(ctx context.Context, tx *sql.Tx, user store.ID) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO identity_users (id, created_at) VALUES (?, ?) ON CONFLICT (id) DO NOTHING`, user, ftime(time.Now().UTC()))
	return err
}

// update changes a user's record, making it first when there is none.
func (s *SQL) update(ctx context.Context, user store.ID, query string, args ...any) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureUser(ctx, tx, user); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, query, append(args, user)...)
		return err
	})
}

// insert adds a row of something that belongs to a user.
func (s *SQL) insert(ctx context.Context, user store.ID, query string, args ...any) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureUser(ctx, tx, user); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// Credentials implements Store.
func (s *SQL) Credentials(ctx context.Context, user store.ID) (*Credentials, error) {
	c, err := scanCredentials(s.db.Reader().QueryRowContext(ctx, `SELECT `+credentialColumns+` FROM identity_users WHERE id = ?`, user))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound(string(user))
	}
	return c, err
}

// Users implements Store.
func (s *SQL) Users(ctx context.Context) (map[store.ID]time.Time, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT id, created_at FROM identity_users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[store.ID]time.Time{}
	for rows.Next() {
		var id store.ID
		var created string
		if err := rows.Scan(&id, &created); err != nil {
			return nil, err
		}
		out[id] = ptime(created)
	}
	return out, rows.Err()
}

// DeleteUser implements Store.
func (s *SQL) DeleteUser(ctx context.Context, user store.ID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM identity_users WHERE id = ?`, user)
	return err
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

// SetPassword implements Store.
func (s *SQL) SetPassword(ctx context.Context, user store.ID, hash string, mustChange bool, at time.Time) error {
	return s.update(ctx, user, `UPDATE identity_users SET password_hash = ?, must_change_password = ?, password_changed_at = ? WHERE id = ?`,
		hash, flag(mustChange), ftime(at))
}

// SetMustChangePassword implements Store.
func (s *SQL) SetMustChangePassword(ctx context.Context, user store.ID, mustChange bool) error {
	return s.update(ctx, user, `UPDATE identity_users SET must_change_password = ? WHERE id = ?`, flag(mustChange))
}

// SetTOTP implements Store.
func (s *SQL) SetTOTP(ctx context.Context, user store.ID, enabled bool, step int64) error {
	return s.update(ctx, user, `UPDATE identity_users SET totp = ?, totp_step = ? WHERE id = ?`, flag(enabled), step)
}

// UseTOTPStep implements Store.
func (s *SQL) UseTOTPStep(ctx context.Context, user store.ID, step int64) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_users SET totp_step = ? WHERE id = ? AND totp_step < ?`, step, user, step)
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
func (s *SQL) RecordSignIn(ctx context.Context, user store.ID, at time.Time, ip string) error {
	return s.update(ctx, user, `UPDATE identity_users SET failed_sign_ins = 0, locked_until = '', last_sign_in_at = ?, last_sign_in_ip = ? WHERE id = ?`,
		ftime(at), ip)
}

// RecordFailure implements Store.
func (s *SQL) RecordFailure(ctx context.Context, user store.ID, attempts int, lockout time.Duration, now time.Time) (bool, time.Time, error) {
	var locked bool
	var until time.Time
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureUser(ctx, tx, user); err != nil {
			return err
		}
		var failures int
		var current string
		if err := tx.QueryRowContext(ctx, `SELECT failed_sign_ins, locked_until FROM identity_users WHERE id = ?`, user).Scan(&failures, &current); err != nil {
			return err
		}
		failures, until, locked = nextFailure(failures, ptime(current), attempts, lockout, now)
		_, err := tx.ExecContext(ctx, `UPDATE identity_users SET failed_sign_ins = ?, locked_until = ? WHERE id = ?`, failures, ftime(until), user)
		return err
	})
	return locked, until, err
}

// nextFailure works out what one more failed sign-in makes of a user's
// failures and lockout, the same way in both stores.
func nextFailure(failures int, lockedUntil time.Time, attempts int, lockout time.Duration, now time.Time) (int, time.Time, bool) {
	if now.Before(lockedUntil) {
		return failures, lockedUntil, false
	}
	failures++
	if failures >= attempts {
		return 0, now.Add(lockout), true
	}
	return failures, time.Time{}, false
}

// Unlock implements Store.
func (s *SQL) Unlock(ctx context.Context, user store.ID) error {
	_, err := s.db.Exec(ctx, `UPDATE identity_users SET failed_sign_ins = 0, locked_until = '' WHERE id = ?`, user)
	return err
}

// SetPreferences implements Store.
func (s *SQL) SetPreferences(ctx context.Context, user store.ID, prefs json.RawMessage) error {
	return s.update(ctx, user, `UPDATE identity_users SET preferences = ? WHERE id = ?`, nullJSON(prefs))
}

// SetAlertChannel implements Store.
func (s *SQL) SetAlertChannel(ctx context.Context, user store.ID, channel string) error {
	return s.update(ctx, user, `UPDATE identity_users SET alert_channel = ? WHERE id = ?`, channel)
}

// SetSSHKeysSeen implements Store.
func (s *SQL) SetSSHKeysSeen(ctx context.Context, user store.ID, fingerprints []string) error {
	return s.update(ctx, user, `UPDATE identity_users SET ssh_keys_seen = ? WHERE id = ?`, jsonText(fingerprints))
}

// ReplaceRecoveryCodes implements Store.
func (s *SQL) ReplaceRecoveryCodes(ctx context.Context, user store.ID, hashes []string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureUser(ctx, tx, user); err != nil {
			return err
		}
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
	err := s.insert(ctx, p.User, `INSERT INTO identity_passkeys (`+passkeyColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
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

const sessionColumns = `id, user_id, hash, previous_hash, rotated_at, grants_hash, created_at, last_seen_at, expires_at, ip, user_agent,
	method, second_factor, stepped_up_at, impersonator, parent, persistent, pending`

func scanSession(row scanner) (*Session, error) {
	var x Session
	var rotated, created, seen, expires, stepped string
	var persistent int
	err := row.Scan(&x.ID, &x.User, &x.Hash, &x.PreviousHash, &rotated, &x.GrantsHash, &created, &seen, &expires, &x.IP, &x.UserAgent,
		&x.Method, &x.SecondFactor, &stepped, &x.Impersonator, &x.Parent, &persistent, &x.Pending)
	if err != nil {
		return nil, err
	}
	x.RotatedAt, x.CreatedAt, x.LastSeenAt, x.ExpiresAt, x.SteppedUpAt = ptime(rotated), ptime(created), ptime(seen), ptime(expires), ptime(stepped)
	x.Persistent = persistent == 1
	return &x, nil
}

// CreateSession implements Store.
func (s *SQL) CreateSession(ctx context.Context, x *Session) error {
	return s.insert(ctx, x.User, `INSERT INTO identity_sessions (`+sessionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.User, x.Hash, x.PreviousHash, ftime(x.RotatedAt), x.GrantsHash, ftime(x.CreatedAt), ftime(x.LastSeenAt), ftime(x.ExpiresAt),
		x.IP, x.UserAgent, x.Method, x.SecondFactor, ftime(x.SteppedUpAt), x.Impersonator, x.Parent, flag(x.Persistent), x.Pending)
}

// UpdateSession implements Store.
func (s *SQL) UpdateSession(ctx context.Context, x *Session) error {
	res, err := s.db.Exec(ctx, `UPDATE identity_sessions SET hash = ?, previous_hash = ?, rotated_at = ?, grants_hash = ?, last_seen_at = ?,
		expires_at = ?, ip = ?, user_agent = ?, second_factor = ?, stepped_up_at = ?, persistent = ?, pending = ? WHERE id = ?`,
		x.Hash, x.PreviousHash, ftime(x.RotatedAt), x.GrantsHash, ftime(x.LastSeenAt), ftime(x.ExpiresAt), x.IP, x.UserAgent,
		x.SecondFactor, ftime(x.SteppedUpAt), flag(x.Persistent), x.Pending, x.ID)
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
	x, err := scanSession(s.db.Reader().QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM identity_sessions WHERE hash = ?
		UNION ALL SELECT `+sessionColumns+` FROM identity_sessions WHERE previous_hash = ? AND previous_hash <> '' LIMIT 1`, hash, hash))
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
	return s.insert(ctx, d.User, `INSERT INTO identity_devices (`+deviceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.User, d.Hash, d.Name, d.IP, ftime(d.CreatedAt), ftime(d.LastUsedAt), ftime(d.ExpiresAt))
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
	return s.insert(ctx, t.User, `INSERT INTO identity_tokens (`+tokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.User, t.Name, t.Hash, t.Hint, jsonText(t.Permissions), jsonText(t.Accounts), jsonText(t.AllowFrom), ftime(t.ExpiresAt),
		ftime(t.CreatedAt), ftime(t.LastUsedAt), t.LastUsedIP)
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
	return s.insert(ctx, inv.User, `INSERT INTO identity_invites (`+inviteColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.User, inv.Hash, inv.Purpose, inv.CreatedBy, ftime(inv.CreatedAt), ftime(inv.ExpiresAt))
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
	err := s.insert(ctx, l.User, `INSERT INTO identity_links (user_id, provider, subject, username, linked_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, provider) DO UPDATE SET subject = excluded.subject, username = excluded.username, linked_at = excluded.linked_at`,
		l.User, l.Provider, l.Subject, l.Username, ftime(l.LinkedAt))
	if isUnique(err) {
		return auth.ErrIdentityTaken.New("provider", string(l.Provider))
	}
	return err
}

// LinkedUser implements Store.
func (s *SQL) LinkedUser(ctx context.Context, provider store.ID, subject string) (store.ID, error) {
	var id store.ID
	err := s.db.Reader().QueryRowContext(ctx, `SELECT user_id FROM identity_links WHERE provider = ? AND subject = ?`, provider, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("The identity")
	}
	return id, err
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
	return s.insert(ctx, e.User, `INSERT INTO identity_mfa (user_id, method, enabled_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id, method) DO UPDATE SET enabled_at = excluded.enabled_at`, e.User, e.Method, ftime(e.EnabledAt))
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
