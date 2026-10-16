package identity

import (
	"context"
	"slices"
	"sort"
	"strings"
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
	users     map[store.ID]*User
	recovery  map[store.ID][]string
	passkeys  map[store.ID]*Passkey
	sessions  map[store.ID]*Session
	devices   map[store.ID]*Device
	tokens    map[store.ID]*Token
	sshKeys   map[store.ID]*SSHKey
	invites   map[store.ID]*Invite
	links     []*Link
	mfa       []*MFAEnrollment
	setupCode string
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{
		users: map[store.ID]*User{}, recovery: map[store.ID][]string{}, passkeys: map[store.ID]*Passkey{},
		sessions: map[store.ID]*Session{}, devices: map[store.ID]*Device{}, tokens: map[store.ID]*Token{},
		sshKeys: map[store.ID]*SSHKey{}, invites: map[store.ID]*Invite{},
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

func cloneUser(u *User) *User {
	c := u.Clone()
	if c.AllowFrom == nil {
		c.AllowFrom = []string{}
	}
	return c
}

// CountUsers implements Store.
func (m *Memory) CountUsers(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *Memory) nameTaken(username string, except store.ID) bool {
	for _, u := range m.users {
		if u.Username == username && u.ID != except {
			return true
		}
	}
	return false
}

// CreateUser implements Store.
func (m *Memory) CreateUser(ctx context.Context, u *User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTaken(u.Username, "") {
		return auth.ErrUsernameTaken.New("username", u.Username)
	}
	if _, ok := m.users[u.ID]; ok {
		return i18n.ErrAlreadyExists.New("what", string(u.ID))
	}
	if u.Revision == 0 {
		u.Revision = 1
	}
	m.users[u.ID] = cloneUser(u)
	return nil
}

// UpdateUser implements Store.
func (m *Memory) UpdateUser(ctx context.Context, u *User, revision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.users[u.ID]
	if !ok {
		return notFound(string(u.ID))
	}
	if revision != 0 && revision != cur.Revision {
		return i18n.ErrConflict.New("what", u.Username)
	}
	if m.nameTaken(u.Username, u.ID) {
		return auth.ErrUsernameTaken.New("username", u.Username)
	}
	next := cloneUser(u)
	// Credentials and sign-in records change through their own methods.
	next.PasswordHash, next.PasswordChangedAt, next.MustChangePassword = cur.PasswordHash, cur.PasswordChangedAt, cur.MustChangePassword
	next.TOTP, next.TOTPStep = cur.TOTP, cur.TOTPStep
	next.FailedSignIns, next.LockedUntil, next.LastSignInAt, next.LastSignInIP = cur.FailedSignIns, cur.LockedUntil, cur.LastSignInAt, cur.LastSignInIP
	next.CreatedAt = cur.CreatedAt
	next.Revision = cur.Revision + 1
	u.Revision = next.Revision
	m.users[u.ID] = next
	return nil
}

// DeleteUser implements Store.
func (m *Memory) DeleteUser(ctx context.Context, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[id]; !ok {
		return notFound(string(id))
	}
	delete(m.users, id)
	delete(m.recovery, id)
	maps(m.passkeys, func(v *Passkey) bool { return v.User == id })
	maps(m.sessions, func(v *Session) bool { return v.User == id })
	maps(m.devices, func(v *Device) bool { return v.User == id })
	maps(m.tokens, func(v *Token) bool { return v.User == id })
	maps(m.sshKeys, func(v *SSHKey) bool { return v.User == id })
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

// User implements Store.
func (m *Memory) User(ctx context.Context, id store.ID) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, notFound(string(id))
	}
	return cloneUser(u), nil
}

// UserByName implements Store.
func (m *Memory) UserByName(ctx context.Context, username string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := strings.ToLower(username)
	for _, u := range m.users {
		if u.Username == name {
			return cloneUser(u), nil
		}
	}
	return nil, notFound(username)
}

// ListUsers implements Store.
func (m *Memory) ListUsers(ctx context.Context, q UserQuery) ([]*User, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var after string
	if q.Cursor != "" {
		var err error
		if after, err = decodeCursor(q.Cursor); err != nil {
			return nil, "", err
		}
	}
	search := strings.ToLower(q.Search)
	var all []*User
	for _, u := range m.users {
		switch {
		case q.Homes != nil && !slices.Contains(q.Homes, u.Home):
			continue
		case q.Grant != nil && !slices.ContainsFunc(u.Grants, func(g auth.Grant) bool { return g.Scope == *q.Grant }):
			continue
		case q.Role != "" && !slices.ContainsFunc(u.Grants, func(g auth.Grant) bool { return g.Role == q.Role }):
			continue
		case search != "" && !strings.Contains(u.Username, search) && !strings.Contains(strings.ToLower(u.Name), search) &&
			!strings.Contains(strings.ToLower(u.Email), search):
			continue
		case after != "" && u.Username <= after:
			continue
		}
		all = append(all, u)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Username < all[j].Username })
	limit := limitOf(q.Limit)
	var next string
	if len(all) > limit {
		all = all[:limit]
		next = encodeCursor(all[limit-1].Username)
	}
	out := make([]*User, len(all))
	for i, u := range all {
		out[i] = cloneUser(u)
	}
	return out, next, nil
}

func (m *Memory) user(id store.ID) (*User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, notFound(string(id))
	}
	return u, nil
}

// SetPassword implements Store.
func (m *Memory) SetPassword(ctx context.Context, id store.ID, hash string, mustChange bool, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := m.user(id)
	if err != nil {
		return err
	}
	u.PasswordHash, u.MustChangePassword, u.PasswordChangedAt = hash, mustChange, at
	return nil
}

// SetTOTP implements Store.
func (m *Memory) SetTOTP(ctx context.Context, id store.ID, enabled bool, step int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := m.user(id)
	if err != nil {
		return err
	}
	u.TOTP, u.TOTPStep = enabled, step
	return nil
}

// UseTOTPStep implements Store.
func (m *Memory) UseTOTPStep(ctx context.Context, id store.ID, step int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok || u.TOTPStep >= step {
		return ErrStepUsed
	}
	u.TOTPStep = step
	return nil
}

// RecordSignIn implements Store.
func (m *Memory) RecordSignIn(ctx context.Context, id store.ID, failures int, lockedUntil, at time.Time, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, err := m.user(id)
	if err != nil {
		return err
	}
	u.FailedSignIns, u.LockedUntil = failures, lockedUntil
	if !at.IsZero() {
		u.LastSignInAt, u.LastSignInIP = at, ip
	}
	return nil
}

// ReplaceRecoveryCodes implements Store.
func (m *Memory) ReplaceRecoveryCodes(ctx context.Context, user store.ID, hashes []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	if _, err := m.user(s.User); err != nil {
		return err
	}
	m.sessions[s.ID] = copyOf(s)
	return nil
}

// UpdateSession implements Store.
func (m *Memory) UpdateSession(ctx context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.sessions[s.ID]
	if !ok {
		return notFound(string(s.ID))
	}
	// What a session is about never changes once it starts.
	next := copyOf(s)
	next.User, next.CreatedAt, next.Method, next.Impersonator, next.Parent = cur.User, cur.CreatedAt, cur.Method, cur.Impersonator, cur.Parent
	m.sessions[s.ID] = next
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
		if s.Hash == hash {
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
	if _, err := m.user(d.User); err != nil {
		return err
	}
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
	if _, err := m.user(t.User); err != nil {
		return err
	}
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

// AddSSHKey implements Store.
func (m *Memory) AddSSHKey(ctx context.Context, k *SSHKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.user(k.User); err != nil {
		return err
	}
	for _, other := range m.sshKeys {
		if other.User == k.User && other.Fingerprint == k.Fingerprint {
			return auth.ErrSSHKeyExists.New("name", other.Name)
		}
	}
	m.sshKeys[k.ID] = copyOf(k)
	return nil
}

// SSHKeys implements Store.
func (m *Memory) SSHKeys(ctx context.Context, user store.ID) ([]*SSHKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedBy(m.sshKeys, func(k *SSHKey) bool { return k.User == user }, func(a, b *SSHKey) bool {
		return a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID < b.ID)
	}), nil
}

// DeleteSSHKey implements Store.
func (m *Memory) DeleteSSHKey(ctx context.Context, user, id store.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.sshKeys[id]; !ok || k.User != user {
		return notFound(string(id))
	}
	delete(m.sshKeys, id)
	return nil
}

// CreateInvite implements Store.
func (m *Memory) CreateInvite(ctx context.Context, inv *Invite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.user(inv.User); err != nil {
		return err
	}
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
	if _, err := m.user(l.User); err != nil {
		return err
	}
	for _, other := range m.links {
		if other.Provider == l.Provider && other.Subject == l.Subject && other.User != l.User {
			return i18n.ErrAlreadyExists.New("what", "A link to this identity")
		}
	}
	m.links = slices.DeleteFunc(m.links, func(o *Link) bool { return o.User == l.User && o.Provider == l.Provider })
	m.links = append(m.links, copyOf(l))
	return nil
}

// UserByIdentity implements Store.
func (m *Memory) UserByIdentity(ctx context.Context, provider store.ID, subject string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.links {
		if l.Provider == provider && l.Subject == subject {
			if u, ok := m.users[l.User]; ok {
				return cloneUser(u), nil
			}
		}
	}
	return nil, notFound("The identity")
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
	if _, err := m.user(e.User); err != nil {
		return err
	}
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
