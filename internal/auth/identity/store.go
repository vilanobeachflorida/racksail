package identity

import (
	"context"
	"encoding/json"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Store keeps users' credentials and what belongs to them alone: sessions,
// trusted browsers, API tokens, passkeys, recovery codes, sign-in links,
// links to outside identities and second steps from add-ons. Who users are
// and what they may do is in their core.user resources. The SQL store keeps
// them in the panel database; Memory keeps them in memory and passes the
// same tests. Methods that find one record return store.ErrNotFound when
// there is none.
//
// Writing a user's credentials, or anything that belongs to them, makes
// the user's record when it isn't there yet.
type Store interface {
	// Credentials returns a user's credentials, or store.ErrNotFound when
	// nothing was ever kept for the user.
	Credentials(ctx context.Context, user store.ID) (*Credentials, error)
	// Users lists the users something is kept for, with when it was first.
	Users(ctx context.Context) (map[store.ID]time.Time, error)
	// DeleteUser removes everything kept for a user.
	DeleteUser(ctx context.Context, user store.ID) error
	// SetPassword replaces a user's password hash; an empty hash leaves the
	// user without one.
	SetPassword(ctx context.Context, user store.ID, hash string, mustChange bool, at time.Time) error
	// SetMustChangePassword says whether a user chooses a new password when
	// they next sign in.
	SetMustChangePassword(ctx context.Context, user store.ID, mustChange bool) error
	SetTOTP(ctx context.Context, user store.ID, enabled bool, step int64) error
	// UseTOTPStep records the time step of a code that was used. It fails
	// with ErrStepUsed when that step, or a later one, was used already,
	// so the same code can't count twice even when two requests race.
	UseTOTPStep(ctx context.Context, user store.ID, step int64) error
	// RecordSignIn keeps a successful sign-in, which clears the failures
	// and any lockout.
	RecordSignIn(ctx context.Context, user store.ID, at time.Time, ip string) error
	// RecordFailure counts a failed sign-in at now. The attempts-th failure
	// in a row locks the user out until now plus lockout; failures while a
	// lockout lasts don't count and never shorten it. It reports whether
	// this failure locked the user out, and until when they are.
	RecordFailure(ctx context.Context, user store.ID, attempts int, lockout time.Duration, now time.Time) (locked bool, until time.Time, err error)
	// Unlock clears a user's failures and lockout.
	Unlock(ctx context.Context, user store.ID) error
	SetPreferences(ctx context.Context, user store.ID, prefs json.RawMessage) error
	SetAlertChannel(ctx context.Context, user store.ID, channel string) error
	// SetSSHKeysSeen keeps the fingerprints of the SSH keys a user was told
	// about.
	SetSSHKeysSeen(ctx context.Context, user store.ID, fingerprints []string) error

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
	// TouchSession records when and from where a session was last used,
	// and nothing else, so it never undoes a new secret another request
	// gave the session meanwhile.
	TouchSession(ctx context.Context, id store.ID, at time.Time, ip string) error
	// SetSessionPending says what the session's person must do first.
	SetSessionPending(ctx context.Context, id store.ID, pending string) error
	// RotateSession gives a session a new secret: it keeps s's hash and
	// previous hash, when it was rotated, its grants' hash, when its person
	// last confirmed it's them, and when and from where it was last used.
	// It does so only while the session's secret is still the one hashed as
	// from, and fails with ErrSessionRotated otherwise, so two requests
	// can't each give a session a secret and leave one of them unknown.
	RotateSession(ctx context.Context, s *Session, from string) error
	Session(ctx context.Context, id store.ID) (*Session, error)
	// SessionByHash finds a session by the hash of its secret, or of the
	// secret it had before it was last given a new one.
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

	CreateInvite(ctx context.Context, inv *Invite) error
	InviteByHash(ctx context.Context, hash string) (*Invite, error)
	// UseInvite removes a sign-in link, failing with store.ErrNotFound when
	// someone used it first.
	UseInvite(ctx context.Context, id store.ID) error
	DeleteInvites(ctx context.Context, user store.ID) error

	// LinkIdentity links an outside identity to a user, replacing the
	// user's earlier link to the same provider; an identity linked to
	// another user already is auth.ErrIdentityTaken.
	LinkIdentity(ctx context.Context, l *Link) error
	// LinkedUser returns the user an outside identity is linked to.
	LinkedUser(ctx context.Context, provider store.ID, subject string) (store.ID, error)
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
