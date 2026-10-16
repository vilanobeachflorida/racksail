package auth

import (
	"context"
	"net/url"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The identity capabilities. Providers attach to hooks.AuthProvider and
// hooks.AuthMFA with provide hooks: modules from their HookProvider, and
// add-ons, such as the LDAP and OpenID Connect add-ons of the official
// catalog, from their manifests. Their Open functions return values that
// implement these interfaces.

// ProviderMethod says how people sign in with an identity provider.
type ProviderMethod string

// Sign-in methods of identity providers.
const (
	// MethodPassword providers check a username and password, as an LDAP
	// directory does. Their users type both on the panel's sign-in screen.
	MethodPassword ProviderMethod = "password"
	// MethodRedirect providers send the browser to the outside service and
	// back, as OpenID Connect does.
	MethodRedirect ProviderMethod = "redirect"
)

// IdentityProvider is the auth.provider capability: signing people in with
// an outside service. A provider implements PasswordProvider or
// RedirectProvider as its Method says.
type IdentityProvider interface {
	Method() ProviderMethod
}

// PasswordProvider checks credentials with the outside service.
type PasswordProvider interface {
	IdentityProvider
	// CheckPassword returns who the credentials belong to. A wrong
	// password is ErrBadCredentials; any other error means the service
	// couldn't answer.
	CheckPassword(ctx context.Context, username, password string) (*ExternalIdentity, error)
}

// RedirectProvider signs people in through the outside service's own page.
type RedirectProvider interface {
	IdentityProvider
	// Begin returns the address to send the browser to and state the panel
	// keeps until the browser comes back to callback.
	Begin(ctx context.Context, callback string) (redirect string, state []byte, err error)
	// Complete finishes the sign-in with the query the browser came back
	// with.
	Complete(ctx context.Context, state []byte, callback string, query url.Values) (*ExternalIdentity, error)
}

// ExternalIdentity is who an outside service says someone is.
type ExternalIdentity struct {
	// Subject identifies the person at the provider and never changes,
	// unlike a username or an email address.
	Subject  string   `json:"subject"`
	Username string   `json:"username,omitempty"`
	Name     string   `json:"name,omitempty"`
	Email    string   `json:"email,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

// MFAUser is who a second-factor method checks.
type MFAUser struct {
	ID       store.ID `json:"id"`
	Username string   `json:"username"`
	Name     string   `json:"name,omitempty"`
	Email    string   `json:"email,omitempty"`
}

// MFAChallenge is what a second-factor method asks of the person.
type MFAChallenge struct {
	// Prompt tells the person what to do, such as "Approve the sign-in on
	// your phone".
	Prompt string `json:"prompt"`
	// Input says the person types a response, such as a code sent to them;
	// otherwise the panel asks the method again until it says yes or no.
	Input bool `json:"input"`
	// State is kept by the panel until Verify, never shown.
	State []byte `json:"-"`
}

// MFAMethod is the auth.mfa capability: a second factor beyond the built-in
// authenticator apps, passkeys and recovery codes.
type MFAMethod interface {
	// Challenge starts a check of the user.
	Challenge(ctx context.Context, user MFAUser) (*MFAChallenge, error)
	// Verify checks the person's response, empty for methods without
	// input. ErrPending says the person hasn't answered yet.
	Verify(ctx context.Context, user MFAUser, state []byte, response string) error
}

// DirectoryUser is what modules learn about a user.
type DirectoryUser struct {
	ID       store.ID `json:"id"`
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Email    string   `json:"email,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
	Grants   []Grant  `json:"grants"`
	// Locale is the language the person chose for the panel, such as
	// pt-BR; empty when they follow their browser.
	Locale string `json:"locale,omitempty"`
}

// SSHKey is one of a user's SSH public keys.
type SSHKey struct {
	ID          store.ID `json:"id" doc:"The key's ID, which starts with sshkey_ and comes from its fingerprint"`
	User        store.ID `json:"user" doc:"The user it belongs to"`
	Name        string   `json:"name" doc:"What the key is for, such as the laptop it's on"`
	Type        string   `json:"type" doc:"The key's algorithm, such as ssh-ed25519"`
	Bits        int      `json:"bits,omitempty" doc:"The key's size, for RSA and ECDSA keys"`
	Fingerprint string   `json:"fingerprint" doc:"The SHA256 fingerprint ssh-keygen -l shows"`
	PublicKey   string   `json:"publicKey" doc:"The key as an authorized_keys line, without options"`
}

// Directory answers what modules need to know about the panel's users,
// such as which SSH keys may reach an account's files.
type Directory interface {
	// User describes one user.
	User(ctx context.Context, id store.ID) (DirectoryUser, error)
	// Members lists the users whose grants reach an account, the account's
	// own people first.
	Members(ctx context.Context, account store.ID) ([]DirectoryUser, error)
	// SSHKeys lists a user's SSH keys.
	SSHKeys(ctx context.Context, user store.ID) ([]SSHKey, error)
}
