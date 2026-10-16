package identity

import (
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The kind names identity events and audit records are about. Users and
// tokens aren't resources, but they're named like kinds so filters and
// webhooks treat them the same way.
const (
	KindUser    = "core.user"
	KindToken   = "core.token"
	KindSession = "core.session"
)

// UserEvent is the data of user events.
type UserEvent struct {
	Username string `json:"username"`
	// Detail says more, such as how someone signed in or which passkey was
	// added.
	Detail string `json:"detail,omitempty"`
	IP     string `json:"ip,omitempty"`
	Device string `json:"device,omitempty" doc:"The browser or program, as its user agent describes it"`
}

// SignInEvent is the data of core.user.signed_in.
type SignInEvent struct {
	Username     string `json:"username"`
	Method       string `json:"method" doc:"How they signed in: password, passkey, provider:<name>, setup or invite"`
	SecondFactor string `json:"secondFactor,omitempty" doc:"What confirmed it was them: totp, passkey, recovery, device or mfa:<name>"`
	IP           string `json:"ip,omitempty"`
	Device       string `json:"device,omitempty"`
	NewDevice    bool   `json:"newDevice" doc:"Whether they signed in from a browser and address not seen before"`
}

// TokenEvent is the data of API token events.
type TokenEvent struct {
	Name string   `json:"name"`
	User store.ID `json:"user"`
}

// Event types of users, sessions and tokens.
var (
	UserCreated          = events.Type{Name: "core.user.created", Description: "A user was added.", Severity: events.SeverityInfo, Data: UserEvent{}}
	UserUpdated          = events.Type{Name: "core.user.updated", Description: "A user's details or roles changed.", Severity: events.SeverityInfo, Data: UserEvent{}}
	UserDeleted          = events.Type{Name: "core.user.deleted", Description: "A user was deleted.", Severity: events.SeverityNotice, Data: UserEvent{}}
	UserSignedIn         = events.Type{Name: "core.user.signed_in", Description: "Someone signed in.", Severity: events.SeverityInfo, Data: SignInEvent{}}
	UserLockedOut        = events.Type{Name: "core.user.locked_out", Description: "Too many failed sign-ins locked a user out for a while.", Severity: events.SeverityWarning, Data: UserEvent{}}
	UserPasswordChanged  = events.Type{Name: "core.user.password_changed", Description: "A user's password changed.", Severity: events.SeverityNotice, Data: UserEvent{}}
	UserTwoFactorChanged = events.Type{Name: "core.user.two_factor_changed", Description: "A user's second sign-in steps changed: an authenticator app, a passkey, recovery codes or a method from an add-on was added or removed.", Severity: events.SeverityNotice, Data: UserEvent{}}
	UserRecoveryUsed     = events.Type{Name: "core.user.recovery_code_used", Description: "A user signed in with one of their recovery codes.", Severity: events.SeverityWarning, Data: UserEvent{}}
	UserImpersonated     = events.Type{Name: "core.user.impersonated", Description: "An admin or reseller started viewing the panel as a user.", Severity: events.SeverityNotice, Data: UserEvent{}}
	UserLinkIssued       = events.Type{Name: "core.user.link_issued", Description: "A sign-in link was made for a user, to set a password.", Severity: events.SeverityNotice, Data: UserEvent{}}
	UserSSHKeyChanged    = events.Type{Name: "core.user.ssh_keys_changed", Description: "A user's SSH keys changed.", Severity: events.SeverityInfo, Data: UserEvent{}}
	SessionsEnded        = events.Type{Name: "core.session.ended", Description: "Sessions were ended before they expired.", Severity: events.SeverityInfo, Data: UserEvent{}}
	TokenCreated         = events.Type{Name: "core.token.created", Description: "An API token was created.", Severity: events.SeverityNotice, Data: TokenEvent{}}
	TokenRevoked         = events.Type{Name: "core.token.revoked", Description: "An API token was revoked.", Severity: events.SeverityInfo, Data: TokenEvent{}}
)

// EventTypes lists the identity event types.
var EventTypes = []events.Type{
	UserCreated, UserUpdated, UserDeleted, UserSignedIn, UserLockedOut, UserPasswordChanged, UserTwoFactorChanged,
	UserRecoveryUsed, UserImpersonated, UserLinkIssued, UserSSHKeyChanged, SessionsEnded, TokenCreated, TokenRevoked,
}

func init() {
	for _, t := range EventTypes {
		events.RegisterType(t)
	}
}
