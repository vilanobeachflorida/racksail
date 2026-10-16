package identity

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// TypeID prefixes of identity records. Users are core.user resources;
// passkeys, devices, SSH keys and sign-in links aren't resources, so their
// prefixes are reserved; sessions and tokens use the store's own.
const (
	PrefixUser    = "user"
	PrefixPasskey = "passkey"
	PrefixDevice  = "device"
	PrefixSSHKey  = "sshkey"
	PrefixInvite  = "invite"
	PrefixSession = store.PrefixSession
	PrefixToken   = store.PrefixToken
)

func init() {
	for prefix, purpose := range map[string]string{
		PrefixPasskey: "passkeys", PrefixDevice: "trusted devices", PrefixSSHKey: "SSH keys", PrefixInvite: "sign-in links",
	} {
		store.ReservePrefix(prefix, purpose)
	}
}

// User is someone who signs in to the panel, as the service works with
// them: who they are and what they may do, from their core.user resource,
// and how they sign in, from their credentials.
type User struct {
	ID       store.ID
	Username string
	Name     string
	Email    string
	// Home is where the user belongs, and so who manages them: the server,
	// a reseller or an account. Their grants never reach outside it.
	Home      store.Scope
	Grants    []auth.Grant
	Disabled  bool
	Locale    string
	AllowFrom []string
	SSHKeys   []UserSSHKey

	Credentials

	CreatedAt time.Time
	UpdatedAt time.Time
	// Revision is the resource's, which changes with every change of who
	// the user is or what they may do.
	Revision int64
}

// Credentials are how a user signs in and what the panel keeps for them
// alone. They aren't part of the user's resource, so they never reach
// change sets, history, hooks or previews, and change only through the
// identity service.
type Credentials struct {
	PasswordHash       string
	PasswordChangedAt  time.Time
	MustChangePassword bool
	TOTP               bool
	// TOTPStep is the time step of the last code used, so no code counts
	// twice.
	TOTPStep      int64
	FailedSignIns int
	LockedUntil   time.Time
	LastSignInAt  time.Time
	LastSignInIP  string
	Preferences   json.RawMessage
	// AlertChannel is the type of channel security alerts go to, empty for
	// none. Its settings are kept as a secret.
	AlertChannel string
	// SSHKeysSeen are the fingerprints of the SSH keys the user was last
	// told about, so a key someone adds gets an alert.
	SSHKeysSeen []string
}

// Clone returns a copy of u whose lists can be changed without affecting u.
func (u *User) Clone() *User {
	c := *u
	c.Grants = make([]auth.Grant, len(u.Grants))
	for i, g := range u.Grants {
		c.Grants[i] = g.Clone()
	}
	c.AllowFrom = slices.Clone(u.AllowFrom)
	c.SSHKeys = slices.Clone(u.SSHKeys)
	c.Preferences = slices.Clone(u.Preferences)
	c.SSHKeysSeen = slices.Clone(u.SSHKeysSeen)
	return &c
}

// DisplayName is the user's name, or their username when they have none.
func (u *User) DisplayName() string {
	if u.Name != "" {
		return u.Name
	}
	return u.Username
}

// Locked reports whether failed sign-ins locked the user out at now.
func (u *User) Locked(now time.Time) bool { return now.Before(u.LockedUntil) }

// Admin reports whether the user is an admin of the whole server.
func (u *User) Admin() bool { return adminGrants(u.Grants) }

func adminGrants(grants []auth.Grant) bool {
	return slices.ContainsFunc(grants, func(g auth.Grant) bool {
		return g.Role == auth.RoleAdmin && g.Scope.Level == store.ScopeServer && g.Object == "" && len(g.Limit) == 0
	})
}

// Session is someone signed in to the panel in one browser.
type Session struct {
	ID   store.ID
	User store.ID
	// Hash is the SHA-256 of the cookie's secret.
	Hash string
	// PreviousHash is the secret the session had before it was last given
	// a new one because the user's roles changed, which requests already
	// on their way may still carry until RotatedAt plus rotationGrace.
	PreviousHash string
	RotatedAt    time.Time
	// GrantsHash says which grants the secret was issued for; when the
	// user's grants change, the session gets a new secret.
	GrantsHash string
	CreatedAt  time.Time
	LastSeenAt time.Time
	// ExpiresAt ends the session however active it is.
	ExpiresAt time.Time
	IP        string
	UserAgent string
	// Method is how the person signed in: password, passkey, provider:<name>,
	// setup, invite or impersonation.
	Method string
	// SecondFactor is what confirmed it was them: totp, passkey, recovery,
	// device or mfa:<name>; empty when nothing did.
	SecondFactor string
	SteppedUpAt  time.Time
	// Impersonator and Parent are set while an admin or reseller views the
	// panel as the user: who they are and their own session, which they go
	// back to.
	Impersonator store.ID
	Parent       store.ID
	// Persistent sessions keep their cookie when the browser closes.
	Persistent bool
	// Pending is what the person must do before anything else: two-factor
	// when their role needs a second step they haven't set up, password when
	// they must choose a new one.
	Pending string
}

// Things a session must do before anything else.
const (
	PendingTwoFactor = "two-factor"
	PendingPassword  = "password"
)

// Device is a browser someone chose to trust, which skips the second
// sign-in step until it expires.
type Device struct {
	ID         store.ID
	User       store.ID
	Hash       string
	Name       string
	IP         string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// Token is an API token. It acts for its user with at most the user's own
// grants, narrowed to its permissions and accounts.
type Token struct {
	ID   store.ID
	User store.ID
	Name string
	Hash string
	// Hint is the start of the secret, so people can tell tokens apart.
	Hint string
	// Permissions narrow what the token may do, as patterns; empty means
	// whatever its user may do.
	Permissions []string
	// Accounts narrow the token to some accounts; empty means wherever its
	// user's grants reach.
	Accounts   []store.ID
	AllowFrom  []string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastUsedAt time.Time
	LastUsedIP string
}

// Expired reports whether the token has expired at now.
func (t *Token) Expired(now time.Time) bool { return !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt) }

// Passkey is a WebAuthn credential that signs a user in.
type Passkey struct {
	ID   store.ID
	User store.ID
	Name string
	// CredentialID is the credential's ID, base64url-encoded.
	CredentialID string
	// Credential is the webauthn.Credential, as JSON.
	Credential json.RawMessage
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// SSHKey is one of a user's SSH public keys, as the panel shows it.
type SSHKey = auth.SSHKey

// Invite is a link that lets someone choose a password: to start using a
// new user, or to get back in after losing theirs.
type Invite struct {
	ID        store.ID
	User      store.ID
	Hash      string
	Purpose   string
	CreatedBy store.ID
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Purposes of sign-in links.
const (
	InviteActivate = "activate"
	InviteReset    = "reset"
)

// Link connects a user to an identity at an outside provider.
type Link struct {
	User store.ID
	// Provider is the identity provider resource the identity comes from.
	Provider store.ID
	Subject  string
	Username string
	LinkedAt time.Time
}

// MFAEnrollment is a second-factor method from an add-on that a user turned
// on.
type MFAEnrollment struct {
	User      store.ID
	Method    string
	EnabledAt time.Time
}
