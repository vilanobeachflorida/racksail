package identity

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// UserView is a user as the API shows them: never their password or
// secrets.
type UserView struct {
	ID       store.ID     `json:"id"`
	Username string       `json:"username"`
	Name     string       `json:"name"`
	Email    string       `json:"email,omitempty"`
	Home     store.Scope  `json:"home" doc:"Where the user belongs, and so who manages them"`
	Grants   []auth.Grant `json:"grants"`
	Disabled bool         `json:"disabled,omitempty"`
	Locale   string       `json:"locale,omitempty"`
	// AllowFrom limits where the user may sign in from.
	AllowFrom          []string   `json:"allowFrom"`
	PasswordSet        bool       `json:"passwordSet" doc:"Whether the user has a password"`
	MustChangePassword bool       `json:"mustChangePassword,omitempty" doc:"Whether the user has to choose a new password when they next sign in"`
	TwoFactor          bool       `json:"twoFactor" doc:"Whether signing in takes a second step: an authenticator app, a passkey or a method from an add-on"`
	LockedUntil        *time.Time `json:"lockedUntil,omitempty" doc:"Until when failed sign-ins keep the user out"`
	LastSignInAt       *time.Time `json:"lastSignInAt,omitempty"`
	LastSignInIP       string     `json:"lastSignInIp,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	Revision           int64      `json:"revision" doc:"Send it back with changes, so nobody's changes are lost"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Service) view(u *User, twoFactor bool) UserView {
	c := u.Clone()
	v := UserView{
		ID: c.ID, Username: c.Username, Name: c.Name, Email: c.Email, Home: c.Home, Grants: c.Grants, Disabled: c.Disabled,
		Locale: c.Locale, AllowFrom: c.AllowFrom, PasswordSet: c.PasswordHash != "", MustChangePassword: c.MustChangePassword,
		TwoFactor: twoFactor, LastSignInAt: timePtr(c.LastSignInAt), LastSignInIP: c.LastSignInIP,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Revision: c.Revision,
	}
	if c.Locked(s.now()) {
		v.LockedUntil = timePtr(c.LockedUntil)
	}
	if v.AllowFrom == nil {
		v.AllowFrom = []string{}
	}
	if v.Grants == nil {
		v.Grants = []auth.Grant{}
	}
	return v
}

// SessionView is a session as its user sees it.
type SessionView struct {
	ID           store.ID  `json:"id"`
	Current      bool      `json:"current" doc:"Whether this is the session asking"`
	CreatedAt    time.Time `json:"createdAt"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	IP           string    `json:"ip,omitempty"`
	Device       string    `json:"device" doc:"The browser and system, as the browser describes itself"`
	Method       string    `json:"method" doc:"How the person signed in"`
	SecondFactor string    `json:"secondFactor,omitempty"`
	Impersonator store.ID  `json:"impersonator,omitempty" doc:"Who is viewing the panel as the user in this session"`
}

func sessionView(x *Session, current store.ID) SessionView {
	return SessionView{
		ID: x.ID, Current: x.ID == current, CreatedAt: x.CreatedAt, LastSeenAt: x.LastSeenAt, ExpiresAt: x.ExpiresAt, IP: x.IP,
		Device: DescribeAgent(x.UserAgent), Method: x.Method, SecondFactor: x.SecondFactor, Impersonator: x.Impersonator,
	}
}

// DeviceView is a trusted browser.
type DeviceView struct {
	ID         store.ID  `json:"id"`
	Name       string    `json:"name"`
	Current    bool      `json:"current" doc:"Whether it's the browser asking"`
	IP         string    `json:"ip,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// TokenView is an API token, without its secret.
type TokenView struct {
	ID          store.ID   `json:"id"`
	User        store.ID   `json:"user"`
	Name        string     `json:"name"`
	Hint        string     `json:"hint" doc:"How the token starts, to tell tokens apart"`
	Permissions []string   `json:"permissions" doc:"What the token may do, as permission patterns; empty means whatever its user may do"`
	Accounts    []store.ID `json:"accounts" doc:"The accounts the token may act on; empty means wherever its user may"`
	AllowFrom   []string   `json:"allowFrom" doc:"Where the token may be used from; empty means anywhere the sign-in policy allows"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	LastUsedIP  string     `json:"lastUsedIp,omitempty"`
}

func tokenView(t *Token) TokenView {
	v := TokenView{
		ID: t.ID, User: t.User, Name: t.Name, Hint: t.Hint, Permissions: t.Permissions, Accounts: t.Accounts, AllowFrom: t.AllowFrom,
		ExpiresAt: timePtr(t.ExpiresAt), CreatedAt: t.CreatedAt, LastUsedAt: timePtr(t.LastUsedAt), LastUsedIP: t.LastUsedIP,
	}
	if v.Permissions == nil {
		v.Permissions = []string{}
	}
	if v.Accounts == nil {
		v.Accounts = []store.ID{}
	}
	if v.AllowFrom == nil {
		v.AllowFrom = []string{}
	}
	return v
}

// PasskeyView is a passkey as its user sees it.
type PasskeyView struct {
	ID         store.ID   `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	// Synced passkeys live in a password manager or a platform account
	// rather than in one device.
	Synced bool `json:"synced" doc:"Whether the passkey is kept in a password manager or synced between devices"`
}

func passkeyView(p *Passkey) PasskeyView {
	v := PasskeyView{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, LastUsedAt: timePtr(p.LastUsedAt)}
	var cred webauthn.Credential
	if json.Unmarshal(p.Credential, &cred) == nil {
		v.Synced = cred.Flags.BackupEligible
	}
	return v
}

var (
	versionPattern = regexp.MustCompile(`racksail/([0-9][0-9A-Za-z.+-]*)`)
	agentNames     = []struct{ needle, name string }{
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	}
	systemNames = []struct{ needle, name string }{
		{"iPhone", "iOS"}, {"iPad", "iPadOS"}, {"Android", "Android"}, {"CrOS", "ChromeOS"}, {"Windows", "Windows"},
		{"Mac OS X", "macOS"}, {"Macintosh", "macOS"}, {"Linux", "Linux"},
	}
)

// DescribeAgent turns a user agent into a few words people recognize, such
// as "Firefox on Linux" or "racksail 1.2.0".
func DescribeAgent(ua string) string {
	switch {
	case ua == "":
		return "Unknown device"
	case strings.HasPrefix(ua, "racksail/"):
		if m := versionPattern.FindStringSubmatch(ua); m != nil {
			return "racksail " + m[1]
		}
		return "racksail"
	case strings.HasPrefix(ua, "curl/"):
		return "curl"
	}
	browser, system := "", ""
	for _, a := range agentNames {
		if strings.Contains(ua, a.needle) {
			browser = a.name
			break
		}
	}
	for _, s := range systemNames {
		if strings.Contains(ua, s.needle) {
			system = s.name
			break
		}
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return "A browser on " + system
	}
	if len(ua) > 40 {
		ua = ua[:40]
	}
	return ua
}
