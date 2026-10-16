package identity

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Memory is a Store kept in memory, for tests and tools. It behaves as the
// SQL store does.
type Memory struct {
	mu        sync.Mutex
	users     map[store.ID]*memoryUser
	recovery  map[store.ID][]string
	passkeys  map[store.ID]*Passkey
	sessions  map[store.ID]*Session
	devices   map[store.ID]*Device
	tokens    map[store.ID]*Token
	invites   map[store.ID]*Invite
	links     []*Link
	mfa       []*MFAEnrollment
	setupCode string
	now       func() time.Time
}

type memoryUser struct {
	creds   Credentials
	created time.Time
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{
		users: map[store.ID]*memoryUser{}, recovery: map[store.ID][]string{}, passkeys: map[store.ID]*Passkey{},
		sessions: map[store.ID]*Session{}, devices: map[store.ID]*Device{}, tokens: map[store.ID]*Token{},
		invites: map[store.ID]*Invite{}, now: time.Now,
	}
}

func copyOf[T any](v *T) *T {
	c := *v
	return &c
}

func cloneToken(t *Token) *Token {
	c := *t
	c.Permissions, c.Accounts, c.AllowFrom = slices.Clone(t.Permissions), slices.Clone(t.Accounts), slices.Clone(t.AllowFrom)
	if c.Permissions == nil {
		c.Permissions = []string{}
	}
	if c.Accounts == nil {
		c.Accounts = []store.ID{}
	}
	if c.AllowFrom == nil {
		c.AllowFrom = []string{}
	}
	return &c
}

func cloneCredentials(c Credentials) *Credentials {
	c.Preferences = slices.Clone(c.Preferences)
	c.SSHKeysSeen = slices.Clone(c.SSHKeysSeen)
	if c.SSHKeysSeen == nil {
		c.SSHKeysSeen = []string{}
	}
	return &c
}

// user returns a user's record, making it when there is none, as the SQL
// store does whenever something is kept for them.
func (m *Memory) user(id store.ID) *memoryUser {
	u, ok := m.users[id]
	if !ok {
		u = &memoryUser{created: m.now().UTC()}
		m.users[id] = u
	}
	return u
}

// Credentials implements Store.
func (m *Memory) Credentials(ctx context.Context, user store.ID) (*Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[user]
	if !ok {
		return nil, notFound(string(user))
	}
	return cloneCredentials(u.creds), nil
}

// Users implements Store.
func (m *Memory) Users(ctx context.Context) (map[store.ID]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[store.ID]time.Time{}
	for id, u := range m.users {
		out[id] = u.created
	}
	return out, nil
}

// DeleteUser implements Store.
func (m *Memory) DeleteUser(ctx context.Context, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.users, id)
	delete(m.recovery, id)
	maps(m.passkeys, func(v *Passkey) bool { return v.User == id })
	maps(m.sessions, func(v *Session) bool { return v.User == id })
	maps(m.devices, func(v *Device) bool { return v.User == id })
	maps(m.tokens, func(v *Token) bool { return v.User == id })
	maps(m.invites, func(v *Invite) bool { return v.User == id })
	m.links = slices.DeleteFunc(m.links, func(l *Link) bool { return l.User == id })
	m.mfa = slices.DeleteFunc(m.mfa, func(e *MFAEnrollment) bool { return e.User == id })
	return nil
}

// maps removes the entries drop selects.
func maps[T any](m map[store.ID]*T, drop func(*T) bool) {
	for k, v := range m {
		if drop(v) {
			delete(m, k)
		}
	}
}

// change changes a user's credentials, making their record when needed.
func (m *Memory) change(id store.ID, fn func(c *Credentials)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.user(id).creds)
	return nil
}

// SetPassword implements Store.
func (m *Memory) SetPassword(ctx context.Context, id store.ID, hash string, mustChange bool, at time.Time) error {
	return m.change(id, func(c *Credentials) { c.PasswordHash, c.MustChangePassword, c.PasswordChangedAt = hash, mustChange, at })
}

// SetMustChangePassword implements Store.
func (m *Memory) SetMustChangePassword(ctx context.Context, id store.ID, mustChange bool) error {
	return m.change(id, func(c *Credentials) { c.MustChangePassword = mustChange })
}

// SetTOTP implements Store.
func (m *Memory) SetTOTP(ctx context.Context, id store.ID, enabled bool, step int64) error {
	return m.change(id, func(c *Credentials) { c.TOTP, c.TOTPStep = enabled, step })
}

// UseTOTPStep implements Store.
func (m *Memory) UseTOTPStep(ctx context.Context, id store.ID, step int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok || u.creds.TOTPStep >= step {
		return ErrStepUsed
	}
	u.creds.TOTPStep = step
	return nil
}

// RecordSignIn implements Store.
func (m *Memory) RecordSignIn(ctx context.Context, id store.ID, at time.Time, ip string) error {
	return m.change(id, func(c *Credentials) {
		c.FailedSignIns, c.LockedUntil, c.LastSignInAt, c.LastSignInIP = 0, time.Time{}, at, ip
	})
}

// RecordFailure implements Store.
func (m *Memory) RecordFailure(ctx context.Context, id store.ID, attempts int, lockout time.Duration, now time.Time) (bool, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := &m.user(id).creds
	var locked bool
	c.FailedSignIns, c.LockedUntil, locked = nextFailure(c.FailedSignIns, c.LockedUntil, attempts, lockout, now)
	return locked, c.LockedUntil, nil
}

// Unlock implements Store.
func (m *Memory) Unlock(ctx context.Context, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[id]; ok {
		u.creds.FailedSignIns, u.creds.LockedUntil = 0, time.Time{}
	}
	return nil
}

// SetPreferences implements Store.
func (m *Memory) SetPreferences(ctx context.Context, id store.ID, prefs json.RawMessage) error {
	return m.change(id, func(c *Credentials) { c.Preferences = slices.Clone(prefs) })
}

// SetAlertChannel implements Store.
func (m *Memory) SetAlertChannel(ctx context.Context, id store.ID, channel string) error {
	return m.change(id, func(c *Credentials) { c.AlertChannel = channel })
}

// SetSSHKeysSeen implements Store.
func (m *Memory) SetSSHKeysSeen(ctx context.Context, id store.ID, fingerprints []string) error {
	return m.change(id, func(c *Credentials) { c.SSHKeysSeen = slices.Clone(fingerprints) })
}

// ReplaceRecoveryCodes implements Store.
func (m *Memory) ReplaceRecoveryCodes(ctx context.Context, user store.ID, hashes []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.user(user)
	m.recovery[user] = slices.Clone(hashes)
	return nil
}

// UseRecoveryCode implements Store.
func (m *Memory) UseRecoveryCode(ctx context.Context, user store.ID, hash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	codes := m.recovery[user]
	i := slices.Index(codes, hash)
	if i < 0 {
		return false, nil
	}
	m.recovery[user] = slices.Delete(codes, i, i+1)
	return true, nil
}

// RecoveryCodesLeft implements Store.
func (m *Memory) RecoveryCodesLeft(ctx context.Context, user store.ID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.recovery[user]), nil
}

// AddPasskey implements Store.
func (m *Memory) AddPasskey(ctx context.Context, p *Passkey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, other := range m.passkeys {
		if other.CredentialID == p.CredentialID {
			return i18n.ErrAlreadyExists.New("what", "This passkey")
		}
	}
	m.user(p.User)
	m.passkeys[p.ID] = copyOf(p)
	return nil
}

// UpdatePasskey implements Store.
func (m *Memory) UpdatePasskey(ctx context.Context, p *Passkey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.passkeys[p.ID]
	if !ok || cur.User != p.User {
		return notFound(string(p.ID))
	}
	cur.Name, cur.Credential, cur.LastUsedAt = p.Name, slices.Clone(p.Credential), p.LastUsedAt
	return nil
}

func sortedBy[T any](m map[store.ID]*T, keep func(*T) bool, less func(a, b *T) bool) []*T {
	out := []*T{}
	for _, v := range m {
		if keep(v) {
			out = append(out, copyOf(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// Passkeys implements Store.
func (m *Memory) Passkeys(ctx context.Context, user store.ID) ([]*Passkey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedBy(m.passkeys, func(p *Passkey) bool { return p.User == user }, func(a, b *Passkey) bool {
		return a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID < b.ID)
	}), nil
}

// PasskeyByCredential implements Store.
func (m *Memory) PasskeyByCredential(ctx context.Context, credentialID string) (*Passkey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.passkeys {
		if p.CredentialID == credentialID {
			return copyOf(p), nil
		}
	}
	return nil, notFound("The passkey")
}

// DeletePasskey implements Store.
func (m *Memory) DeletePasskey(ctx context.Context, user, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.passkeys[id]; !ok || p.User != user {
		return notFound(string(id))
	}
	delete(m.passkeys, id)
	return nil
}

// CreateSession implements Store.
func (m *Memory) CreateSession(ctx context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.user(s.User)
	m.sessions[s.ID] = copyOf(s)
	return nil
}

// TouchSession implements Store.
func (m *Memory) TouchSession(ctx context.Context, id store.ID, at time.Time, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return notFound(string(id))
	}
	s.LastSeenAt, s.IP = at, ip
	return nil
}

// SetSessionPending implements Store.
func (m *Memory) SetSessionPending(ctx context.Context, id store.ID, pending string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return notFound(string(id))
	}
	s.Pending = pending
	return nil
}

// RotateSession implements Store.
func (m *Memory) RotateSession(ctx context.Context, x *Session, from string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[x.ID]
	switch {
	case !ok:
		return notFound(string(x.ID))
	case s.Hash != from:
		return ErrSessionRotated
	}
	s.Hash, s.PreviousHash, s.RotatedAt, s.GrantsHash = x.Hash, x.PreviousHash, x.RotatedAt, x.GrantsHash
	s.SteppedUpAt, s.LastSeenAt, s.IP = x.SteppedUpAt, x.LastSeenAt, x.IP
	return nil
}

// Session implements Store.
func (m *Memory) Session(ctx context.Context, id store.ID) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, notFound(string(id))
	}
	return copyOf(s), nil
}

// SessionByHash implements Store.
func (m *Memory) SessionByHash(ctx context.Context, hash string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.Hash == hash || (s.PreviousHash != "" && s.PreviousHash == hash) {
			return copyOf(s), nil
		}
	}
	return nil, notFound("The session")
}

// Sessions implements Store.
func (m *Memory) Sessions(ctx context.Context, user store.ID) ([]*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedBy(m.sessions, func(s *Session) bool { return s.User == user }, func(a, b *Session) bool {
		return a.LastSeenAt.After(b.LastSeenAt) || (a.LastSeenAt.Equal(b.LastSeenAt) && a.ID < b.ID)
	}), nil
}

// DeleteSession implements Store.
func (m *Memory) DeleteSession(ctx context.Context, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return notFound(string(id))
	}
	delete(m.sessions, id)
	return nil
}

// DeleteSessions implements Store.
func (m *Memory) DeleteSessions(ctx context.Context, user, keep store.ID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.sessions {
		if s.User == user && id != keep {
			delete(m.sessions, id)
			n++
		}
	}
	return n, nil
}

// DeleteExpired implements Store.
func (m *Memory) DeleteExpired(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps(m.sessions, func(s *Session) bool { return s.ExpiresAt.Before(now) })
	maps(m.devices, func(d *Device) bool { return d.ExpiresAt.Before(now) })
	maps(m.invites, func(i *Invite) bool { return i.ExpiresAt.Before(now) })
	maps(m.tokens, func(t *Token) bool { return !t.ExpiresAt.IsZero() && t.ExpiresAt.Before(now) })
	return nil
}

// AddDevice implements Store.
func (m *Memory) AddDevice(ctx context.Context, d *Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.user(d.User)
	m.devices[d.ID] = copyOf(d)
	return nil
}

// DeviceByHash implements Store.
func (m *Memory) DeviceByHash(ctx context.Context, hash string) (*Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.devices {
		if d.Hash == hash {
			return copyOf(d), nil
		}
	}
	return nil, notFound("The device")
}

// TouchDevice implements Store.
func (m *Memory) TouchDevice(ctx context.Context, id store.ID, at time.Time, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return notFound(string(id))
	}
	d.LastUsedAt, d.IP = at, ip
	return nil
}

// Devices implements Store.
func (m *Memory) Devices(ctx context.Context, user store.ID) ([]*Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedBy(m.devices, func(d *Device) bool { return d.User == user }, func(a, b *Device) bool {
		return a.LastUsedAt.After(b.LastUsedAt) || (a.LastUsedAt.Equal(b.LastUsedAt) && a.ID < b.ID)
	}), nil
}

// DeleteDevice implements Store.
func (m *Memory) DeleteDevice(ctx context.Context, user, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.devices[id]; !ok || d.User != user {
		return notFound(string(id))
	}
	delete(m.devices, id)
	return nil
}

// DeleteDevices implements Store.
func (m *Memory) DeleteDevices(ctx context.Context, user store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps(m.devices, func(d *Device) bool { return d.User == user })
	return nil
}

// CreateToken implements Store.
func (m *Memory) CreateToken(ctx context.Context, t *Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.user(t.User)
	m.tokens[t.ID] = cloneToken(t)
	return nil
}

// Token implements Store.
func (m *Memory) Token(ctx context.Context, id store.ID) (*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return nil, notFound(string(id))
	}
	return cloneToken(t), nil
}

// TokenByHash implements Store.
func (m *Memory) TokenByHash(ctx context.Context, hash string) (*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.Hash == hash {
			return cloneToken(t), nil
		}
	}
	return nil, notFound("The token")
}

// TouchToken implements Store.
func (m *Memory) TouchToken(ctx context.Context, id store.ID, at time.Time, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return notFound(string(id))
	}
	t.LastUsedAt, t.LastUsedIP = at, ip
	return nil
}

// Tokens implements Store.
func (m *Memory) Tokens(ctx context.Context, user store.ID) ([]*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Token{}
	for _, t := range m.tokens {
		if t.User == user {
			out = append(out, cloneToken(t))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt) || (out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID)
	})
	return out, nil
}

// DeleteToken implements Store.
func (m *Memory) DeleteToken(ctx context.Context, user, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[id]; !ok || t.User != user {
		return notFound(string(id))
	}
	delete(m.tokens, id)
	return nil
}

// DeleteTokens implements Store.
func (m *Memory) DeleteTokens(ctx context.Context, user store.ID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, t := range m.tokens {
		if t.User == user {
			delete(m.tokens, id)
			n++
		}
	}
	return n, nil
}

// CreateInvite implements Store.
func (m *Memory) CreateInvite(ctx context.Context, inv *Invite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.user(inv.User)
	m.invites[inv.ID] = copyOf(inv)
	return nil
}

// InviteByHash implements Store.
func (m *Memory) InviteByHash(ctx context.Context, hash string) (*Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.invites {
		if inv.Hash == hash {
			return copyOf(inv), nil
		}
	}
	return nil, notFound("The sign-in link")
}

// UseInvite implements Store.
func (m *Memory) UseInvite(ctx context.Context, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.invites[id]; !ok {
		return notFound(string(id))
	}
	delete(m.invites, id)
	return nil
}

// DeleteInvites implements Store.
func (m *Memory) DeleteInvites(ctx context.Context, user store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps(m.invites, func(i *Invite) bool { return i.User == user })
	return nil
}

// LinkIdentity implements Store.
func (m *Memory) LinkIdentity(ctx context.Context, l *Link) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, other := range m.links {
		if other.Provider == l.Provider && other.Subject == l.Subject && other.User != l.User {
			return auth.ErrIdentityTaken.New("provider", string(l.Provider))
		}
	}
	m.user(l.User)
	m.links = slices.DeleteFunc(m.links, func(o *Link) bool { return o.User == l.User && o.Provider == l.Provider })
	m.links = append(m.links, copyOf(l))
	return nil
}

// LinkedUser implements Store.
func (m *Memory) LinkedUser(ctx context.Context, provider store.ID, subject string) (store.ID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.links {
		if l.Provider == provider && l.Subject == subject {
			return l.User, nil
		}
	}
	return "", notFound("The identity")
}

// Links implements Store.
func (m *Memory) Links(ctx context.Context, user store.ID) ([]*Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Link{}
	for _, l := range m.links {
		if l.User == user {
			out = append(out, copyOf(l))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LinkedAt.Before(out[j].LinkedAt) })
	return out, nil
}

// UnlinkIdentity implements Store.
func (m *Memory) UnlinkIdentity(ctx context.Context, user, provider store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.links)
	m.links = slices.DeleteFunc(m.links, func(l *Link) bool { return l.User == user && l.Provider == provider })
	if len(m.links) == n {
		return notFound(string(provider))
	}
	return nil
}

// EnableMFA implements Store.
func (m *Memory) EnableMFA(ctx context.Context, e *MFAEnrollment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.user(e.User)
	m.mfa = slices.DeleteFunc(m.mfa, func(o *MFAEnrollment) bool { return o.User == e.User && o.Method == e.Method })
	m.mfa = append(m.mfa, copyOf(e))
	return nil
}

// MFAMethods implements Store.
func (m *Memory) MFAMethods(ctx context.Context, user store.ID) ([]*MFAEnrollment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*MFAEnrollment{}
	for _, e := range m.mfa {
		if e.User == user {
			out = append(out, copyOf(e))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out, nil
}

// DisableMFA implements Store.
func (m *Memory) DisableMFA(ctx context.Context, user store.ID, method string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.mfa)
	m.mfa = slices.DeleteFunc(m.mfa, func(e *MFAEnrollment) bool { return e.User == user && e.Method == method })
	if len(m.mfa) == n {
		return notFound(method)
	}
	return nil
}

// SetSetupCode implements Store.
func (m *Memory) SetSetupCode(ctx context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setupCode = hash
	return nil
}

// SetupCode implements Store.
func (m *Memory) SetupCode(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.setupCode, nil
}
