package identity

import (
	"context"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// UserQuery selects users, ordered by username.
type UserQuery struct {
	// Homes keeps users who belong to one of these scopes; nil keeps
	// everyone.
	Homes []store.Scope
	// Grant keeps users with a grant on exactly this scope.
	Grant *store.Scope
	// Role keeps users with a grant of this role.
	Role auth.Role
	// Search keeps users whose username, name or email contains the text,
	// ignoring case.
	Search string
	// Cursor continues after the user it names, from UserPage.Next.
	Cursor string
	Limit  int
}

// Store keeps users and their credentials, sessions, tokens and keys. The
// SQL store keeps them in the panel database; Memory keeps them in memory
// and passes the same tests. Methods that find one record return
// store.ErrNotFound when there is none.
type Store interface {
	CountUsers(ctx context.Context) (int, error)
	CreateUser(ctx context.Context, u *User) error
	// UpdateUser replaces a user's details and grants. revision must match
	// the stored one, or be zero to skip the check; the new revision is set
	// on u.
	UpdateUser(ctx context.Context, u *User, revision int64) error
	// DeleteUser removes a user with everything that belongs to them.
	DeleteUser(ctx context.Context, id store.ID) error
	User(ctx context.Context, id store.ID) (*User, error)
	UserByName(ctx context.Context, username string) (*User, error)
	ListUsers(ctx context.Context, q UserQuery) ([]*User, string, error)
	// SetPassword replaces a user's password hash; an empty hash leaves the
	// user without one.
	SetPassword(ctx context.Context, id store.ID, hash string, mustChange bool, at time.Time) error
	SetTOTP(ctx context.Context, id store.ID, enabled bool, step int64) error
	// UseTOTPStep records the time step of a code that was used. It fails
	// with ErrStepUsed when that step, or a later one, was used already,
	// so the same code can't count twice even when two requests race.
	UseTOTPStep(ctx context.Context, id store.ID, step int64) error
	// RecordSignIn keeps the outcome of a sign-in attempt: failures and the
	// lockout they lead to, or when and where the last success came from.
	RecordSignIn(ctx context.Context, id store.ID, failures int, lockedUntil, at time.Time, ip string) error

	// ReplaceRecoveryCodes keeps a new set of recovery code hashes.
	ReplaceRecoveryCodes(ctx context.Context, user store.ID, hashes []string) error
	// UseRecoveryCode removes a recovery code and reports whether it was
	// there.
	UseRecoveryCode(ctx context.Context, user store.ID, hash string) (bool, error)
	RecoveryCodesLeft(ctx context.Context, user store.ID) (int, error)

	AddPasskey(ctx context.Context, p *Passkey) error
	UpdatePasskey(ctx context.Context, p *Passkey) error
	Passkeys(ctx context.Context, user store.ID) ([]*Passkey, error)
	PasskeyByCredential(ctx context.Context, credentialID string) (*Passkey, error)
	DeletePasskey(ctx context.Context, user, id store.ID) error

	CreateSession(ctx context.Context, s *Session) error
	// UpdateSession replaces what a session records, its hash included.
	UpdateSession(ctx context.Context, s *Session) error
	Session(ctx context.Context, id store.ID) (*Session, error)
	SessionByHash(ctx context.Context, hash string) (*Session, error)
	Sessions(ctx context.Context, user store.ID) ([]*Session, error)
	DeleteSession(ctx context.Context, id store.ID) error
	// DeleteSessions ends a user's sessions except keep, and returns how
	// many it ended.
	DeleteSessions(ctx context.Context, user, keep store.ID) (int, error)
	// DeleteExpired removes sessions, devices, sign-in links and tokens
	// that expired before now.
	DeleteExpired(ctx context.Context, now time.Time) error

	AddDevice(ctx context.Context, d *Device) error
	DeviceByHash(ctx context.Context, hash string) (*Device, error)
	TouchDevice(ctx context.Context, id store.ID, at time.Time, ip string) error
	Devices(ctx context.Context, user store.ID) ([]*Device, error)
	DeleteDevice(ctx context.Context, user, id store.ID) error
	DeleteDevices(ctx context.Context, user store.ID) error

	CreateToken(ctx context.Context, t *Token) error
	Token(ctx context.Context, id store.ID) (*Token, error)
	TokenByHash(ctx context.Context, hash string) (*Token, error)
	TouchToken(ctx context.Context, id store.ID, at time.Time, ip string) error
	Tokens(ctx context.Context, user store.ID) ([]*Token, error)
	DeleteToken(ctx context.Context, user, id store.ID) error
	DeleteTokens(ctx context.Context, user store.ID) (int, error)

	AddSSHKey(ctx context.Context, k *SSHKey) error
	SSHKeys(ctx context.Context, user store.ID) ([]*SSHKey, error)
	DeleteSSHKey(ctx context.Context, user, id store.ID) error

	CreateInvite(ctx context.Context, inv *Invite) error
	InviteByHash(ctx context.Context, hash string) (*Invite, error)
	// UseInvite removes a sign-in link, failing with store.ErrNotFound when
	// someone used it first.
	UseInvite(ctx context.Context, id store.ID) error
	DeleteInvites(ctx context.Context, user store.ID) error

	LinkIdentity(ctx context.Context, l *Link) error
	UserByIdentity(ctx context.Context, provider store.ID, subject string) (*User, error)
	Links(ctx context.Context, user store.ID) ([]*Link, error)
	UnlinkIdentity(ctx context.Context, user, provider store.ID) error

	EnableMFA(ctx context.Context, e *MFAEnrollment) error
	MFAMethods(ctx context.Context, user store.ID) ([]*MFAEnrollment, error)
	DisableMFA(ctx context.Context, user store.ID, method string) error

	// SetSetupCode keeps the hash of the one-time code that creates the
	// first admin; an empty hash removes it.
	SetSetupCode(ctx context.Context, hash string) error
	SetupCode(ctx context.Context) (string, error)
}
