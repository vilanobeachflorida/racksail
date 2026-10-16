package auth

import (
	"net/http"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Errors of signing in, sessions, tokens and users. They live here, rather
// than next to the code that returns them, because identity providers and
// second-factor methods return some of them too.
var (
	ErrBadCredentials      = i18n.Define("auth.bad_credentials", http.StatusUnauthorized, "The username or password isn't right.")
	ErrWrongPassword       = i18n.Define("auth.wrong_password", http.StatusUnauthorized, "Your current password isn't right.")
	ErrOwnPassword         = i18n.Define("auth.own_password", http.StatusConflict, "Change your own password in your security settings, where your current one is asked for.")
	ErrLocked              = i18n.Define("auth.locked", http.StatusTooManyRequests, "Too many failed sign-ins for this user. Try again in {minutes, plural, one {# minute} other {# minutes}}, or sign in with a passkey.")
	ErrDisabled            = i18n.Define("auth.disabled", http.StatusForbidden, "This user is turned off. Ask an admin to turn it back on.")
	ErrBadCode             = i18n.Define("auth.bad_code", http.StatusUnauthorized, "That code isn't right, or it was used already.")
	ErrChallengeExpired    = i18n.Define("auth.challenge_expired", http.StatusUnauthorized, "The sign-in took too long. Start again.")
	ErrSessionExpired      = i18n.Define("auth.session_expired", http.StatusUnauthorized, "Your session ended. Sign in again.")
	ErrTokenInvalid        = i18n.Define("auth.token_invalid", http.StatusUnauthorized, "The API token isn't valid: it was revoked, it expired or it was mistyped.")
	ErrAddressNotAllowed   = i18n.Define("auth.address_not_allowed", http.StatusForbidden, "Signing in from {address} isn't allowed for {what}.")
	ErrStepUpRequired      = i18n.Define("auth.step_up_required", http.StatusForbidden, "Confirm it's you to continue: enter your password or a code from your authenticator app, or use a passkey.")
	ErrStepUpUnavailable   = i18n.Define("auth.step_up_unavailable", http.StatusForbidden, "This needs you to confirm it's you, which an API token can't do. Do it in the panel instead.")
	ErrPanelOnly           = i18n.Define("auth.panel_only", http.StatusForbidden, "Only you, signed in to the panel, can do this: API tokens can't change how you sign in. Do it in the panel instead.")
	ErrImpersonating       = i18n.Define("auth.impersonating", http.StatusForbidden, "You're viewing the panel as {user}. Stop viewing as them to {action}.")
	ErrCannotImpersonate   = i18n.Define("auth.cannot_impersonate", http.StatusForbidden, "You can't view the panel as {user}: {reason}.")
	ErrNotImpersonating    = i18n.Define("auth.not_impersonating", http.StatusConflict, "You aren't viewing the panel as someone else.")
	ErrSetupDone           = i18n.Define("auth.setup_done", http.StatusConflict, "This panel already has an admin. Sign in instead.")
	ErrBadSetupCode        = i18n.Define("auth.bad_setup_code", http.StatusUnauthorized, "The setup code isn't right. It's in the panel's log and on the server's console.")
	ErrInviteInvalid       = i18n.Define("auth.invite_invalid", http.StatusNotFound, "This sign-in link isn't valid any more. Ask for a new one.")
	ErrUsernameTaken       = i18n.Define("auth.username_taken", http.StatusConflict, "{value} is taken. Choose another username.")
	ErrLastAdmin           = i18n.Define("auth.last_admin", http.StatusConflict, "{user} is the last admin who can sign in. Make someone else an admin first.")
	ErrSelfDelete          = i18n.Define("auth.self_delete", http.StatusConflict, "You can't delete or turn off yourself. Ask another admin to do it.")
	ErrPasskeysUnavailable = i18n.Define("auth.passkeys_unavailable", http.StatusConflict, "Passkeys work when the panel is opened at its name, such as {hostname}, rather than at an address.")
	ErrPasskeyFailed       = i18n.Define("auth.passkey_failed", http.StatusUnauthorized, "The passkey couldn't be checked: {reason}.")
	ErrPasskeyUnknown      = i18n.Define("auth.passkey_unknown", http.StatusUnauthorized, "This passkey isn't registered here. Sign in another way and add it in your security settings.")
	ErrTOTPExists          = i18n.Define("auth.totp_exists", http.StatusConflict, "An authenticator app is already set up. Remove it first to set up another.")
	ErrTOTPNotStarted      = i18n.Define("auth.totp_not_started", http.StatusConflict, "Start setting up an authenticator app first.")
	ErrTwoFactorRequired   = i18n.Define("auth.two_factor_required", http.StatusForbidden, "Set up a second sign-in step, an authenticator app or a passkey, to continue. Your role needs one.")
	ErrPasswordChange      = i18n.Define("auth.password_change_required", http.StatusForbidden, "Choose a new password to continue.")
	ErrLastSignInMethod    = i18n.Define("auth.last_sign_in_method", http.StatusConflict, "This is your only way to sign in. Add a password or another passkey first.")
	ErrSSHKeyInvalid       = i18n.Define("auth.ssh_key_invalid", http.StatusUnprocessableEntity, "This isn't an SSH public key Racksail accepts: {reason}.")
	ErrSSHKeyExists        = i18n.Define("auth.ssh_key_exists", http.StatusConflict, "This key is already on the list, as {name}.")
	ErrProviderFailed      = i18n.Define("auth.provider_failed", http.StatusBadGateway, "{provider} couldn't answer: {reason}.")
	ErrIdentityUnlinked    = i18n.Define("auth.identity_unlinked", http.StatusForbidden, "Your {provider} account isn't linked to a user of this panel. Sign in another way and link it in your security settings, or ask an admin.")
	ErrLinkByHand          = i18n.Define("auth.link_by_hand", http.StatusForbidden, "Your {provider} account matches a user who manages the whole server, and those link their outside accounts themselves. Sign in another way and link it in your security settings.")
	ErrIdentityTaken       = i18n.Define("auth.identity_taken", http.StatusConflict, "That {provider} account already signs in as another user of this panel.")
	ErrProviderRefused     = i18n.Define("auth.provider_refused", http.StatusUnauthorized, "The sign-in didn't go through: {reason}.")
	ErrPending             = i18n.Define("auth.pending", http.StatusConflict, "Waiting for you to approve the sign-in.")
	ErrCrossSite           = i18n.Define("auth.cross_site", http.StatusForbidden, "The request came from another site, so it was refused.")
	ErrAllowlistLocksOut   = i18n.Define("auth.allowlist_locks_out", http.StatusUnprocessableEntity, "The allowlist leaves out {address}, where you're connected from, so it would lock you out. Add it first.")
	ErrTooMany             = i18n.Define("auth.too_many", http.StatusConflict, "You have {count} {what} already, which is as many as you can have. Remove one first.")
	ErrWeakPassword        = i18n.Define("auth.weak_password", http.StatusUnprocessableEntity, "Choose a stronger password: {reason}.")
	ErrGrantNotAllowed     = i18n.Define("auth.grant_not_allowed", http.StatusForbidden, "You can't give the {role} role on {scope}, because you don't have all of its permissions there yourself.")
)
