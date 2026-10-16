package identity

import (
	"context"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// visible reads a user actor may see: themselves, or one who belongs where
// actor holds perm. Anyone else is reported as not found, so users of
// other accounts don't reveal that they exist.
func (s *Service) visible(ctx context.Context, actor *auth.Actor, id store.ID, perm auth.Permission) (*User, error) {
	if actor == nil {
		return nil, i18n.ErrUnauthenticated.New()
	}
	u, err := s.User(ctx, id)
	if err != nil {
		return nil, err
	}
	if actor.Person() == u.ID && perm == auth.PermUserRead && auth.SelfAllowed(actor, perm) {
		return u, nil
	}
	if err := s.authorize(ctx, actor, auth.PermUserRead, u.Home); err != nil {
		return nil, notFound(string(id))
	}
	if perm != auth.PermUserRead {
		if err := s.authorize(ctx, actor, perm, u.Home); err != nil {
			s.audit(ctx, actor, string(perm), u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, "Help "+u.Username+" back in")
			return nil, err
		}
	}
	return u, nil
}

// ReadOwn checks that the actor may see their own user's records, as
// everyone may, unless they're an API token narrowed to permissions that
// leave their user out or to some accounts: then their grants decide.
func (s *Service) ReadOwn(ctx context.Context, actor *auth.Actor) error {
	if actor == nil {
		return i18n.ErrUnauthenticated.New()
	}
	if auth.SelfAllowed(actor, auth.PermUserRead) {
		return nil
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	if s.opts.Authorizer == nil {
		return i18n.ErrForbidden.New("action", string(auth.PermUserRead))
	}
	return s.opts.Authorizer.Authorize(ctx, actor, auth.PermUserRead, auth.Target{Scope: u.Home, Object: u.ID})
}

// helps checks that actor may help a user back in: they need every grant
// the user holds, and can't do it while viewing the panel as someone.
func (s *Service) helps(ctx context.Context, actor *auth.Actor, u *User, perm auth.Permission, summary string) error {
	gc, err := s.grantChecker()
	if err != nil {
		return err
	}
	for _, g := range u.Grants {
		if err := gc.CanGrant(ctx, actor, g); err != nil {
			s.audit(ctx, actor, string(perm), u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, summary)
			return err
		}
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "help users back in")
	}
	return nil
}

// hasTwoFactor reports whether signing in as u takes a second step.
func (s *Service) hasTwoFactor(ctx context.Context, u *User) bool {
	return len(s.secondFactors(ctx, u)) > 0
}

// ResetInput says how to help a user back in.
type ResetInput struct {
	// Link makes a sign-in link the user chooses a new password with.
	Link bool `json:"link,omitempty" doc:"Make a sign-in link for choosing a new password; it is shown once"`
	// RemoveTwoFactor removes the user's authenticator app, passkeys,
	// recovery codes, trusted browsers and add-on methods.
	RemoveTwoFactor bool `json:"removeTwoFactor,omitempty" doc:"Remove their authenticator app, passkeys, recovery codes, trusted browsers and methods from add-ons"`
	EndSessions     bool `json:"endSessions,omitempty" doc:"Sign them out everywhere"`
	RevokeTokens    bool `json:"revokeTokens,omitempty" doc:"Revoke all their API tokens"`
	Unlock          bool `json:"unlock,omitempty" doc:"Lift a lockout after failed sign-ins"`
}

// ResetResult is what helping a user back in did.
type ResetResult struct {
	Link          *IssuedLink `json:"link,omitempty" doc:"The sign-in link made, shown once"`
	SessionsEnded int         `json:"sessionsEnded" doc:"How many sessions were ended"`
	TokensRevoked int         `json:"tokensRevoked" doc:"How many API tokens were revoked"`
}

// ResetUser helps a user back in. actor needs core.user.reset where the
// user belongs and every grant the user holds.
func (s *Service) ResetUser(ctx context.Context, actor *auth.Actor, id store.ID, in ResetInput) (ResetResult, error) {
	var out ResetResult
	u, err := s.visible(ctx, actor, id, auth.PermUserReset)
	if err != nil {
		return out, err
	}
	if err := s.helps(ctx, actor, u, auth.PermUserReset, "Help "+u.Username+" back in"); err != nil {
		return out, err
	}
	var did []string
	if in.RemoveTwoFactor {
		if err := s.removeTwoFactor(ctx, u); err != nil {
			return out, err
		}
		did = append(did, "removed their second steps")
		s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+"'s second sign-in steps were removed.", UserEvent{Username: u.Username, Detail: "removed"})
	}
	if in.EndSessions {
		n, err := s.opts.Store.DeleteSessions(ctx, u.ID, "")
		if err != nil {
			return out, err
		}
		s.forget(ctx, u.ID)
		out.SessionsEnded = n
		did = append(did, "ended "+count(n, "session", "sessions"))
	}
	if in.RevokeTokens {
		n, err := s.opts.Store.DeleteTokens(ctx, u.ID)
		if err != nil {
			return out, err
		}
		out.TokensRevoked = n
		did = append(did, "revoked "+count(n, "API token", "API tokens"))
	}
	if in.Unlock {
		if err := s.opts.Store.Unlock(ctx, u.ID); err != nil {
			return out, err
		}
		did = append(did, "lifted the lockout")
	}
	if in.Link {
		link, err := s.issueLink(ctx, actor, u, InviteReset)
		if err != nil {
			return out, err
		}
		out.Link = &link
		did = append(did, "made a sign-in link")
	}
	if len(did) == 0 {
		return out, i18n.ErrBadRequest.New("reason", "choose something to do")
	}
	s.refresh(u.ID, "reset")
	s.audit(ctx, actor, string(auth.PermUserReset), u.ID, u.Home, events.OutcomeSuccess, "", "Help "+u.Username+" back in: "+strings.Join(did, ", "))
	return out, nil
}

// PasswordSetting sets a user's password for them, or only asks them to
// choose a new one.
type PasswordSetting struct {
	// Password is the user's new password; leave it out to only change
	// whether they must choose one.
	Password string `json:"password,omitempty" maxLength:"1024" format:"password" doc:"The user's new password; leave it out to only say whether they choose a new one when they next sign in"`
	// MustChange asks the user to choose their own password when they next
	// sign in.
	MustChange bool `json:"mustChange,omitempty" doc:"Ask the user to choose a new password when they next sign in"`
}

// SetUserPassword sets a user's password, which ends their sessions, or
// only says whether they must choose a new one when they next sign in. It
// needs core.user.reset where the user belongs and every grant they hold.
func (s *Service) SetUserPassword(ctx context.Context, actor *auth.Actor, id store.ID, in PasswordSetting) error {
	u, err := s.visible(ctx, actor, id, auth.PermUserReset)
	if err != nil {
		return err
	}
	summary := "Set a password for " + u.Username
	if in.Password == "" {
		summary = "Ask " + u.Username + " to choose a new password"
		if !in.MustChange {
			summary = "Let " + u.Username + " keep their password"
		}
	}
	if err := s.helps(ctx, actor, u, auth.PermUserReset, summary); err != nil {
		return err
	}
	if actor.Person() == u.ID {
		return auth.ErrOwnPassword.New()
	}
	if in.Password == "" {
		if in.MustChange && u.PasswordHash == "" {
			return i18n.Invalid(i18n.Field("password", i18n.ErrRequired))
		}
		if err := s.opts.Store.SetMustChangePassword(ctx, u.ID, in.MustChange); err != nil {
			return err
		}
		s.refresh(u.ID, "password")
		s.audit(ctx, actor, string(auth.PermUserReset), u.ID, u.Home, events.OutcomeSuccess, "", summary)
		return nil
	}
	if err := auth.CheckPasswordStrength(in.Password, u.Username); err != nil {
		return i18n.Invalid(i18n.Field("password", auth.ErrWeakPassword, "reason", i18n.From(err).Params["reason"]))
	}
	hash, err := auth.HashPassword(ctx, in.Password)
	if err != nil {
		return err
	}
	if err := s.opts.Store.SetPassword(ctx, u.ID, hash, in.MustChange, s.now()); err != nil {
		return err
	}
	// Whoever had the old password is signed out.
	if _, err := s.opts.Store.DeleteSessions(ctx, u.ID, ""); err != nil {
		return err
	}
	s.forget(ctx, u.ID)
	s.refresh(u.ID, "password")
	s.audit(ctx, actor, string(auth.PermUserReset), u.ID, u.Home, events.OutcomeSuccess, "", summary)
	s.publish(ctx, UserPasswordChanged, u, actor, actor.Name+" set a new password for "+u.Username+".", UserEvent{Username: u.Username, Detail: "set by " + actor.Name, IP: actor.IP})
	s.alert(ctx, u, "Your password was changed", "The password of "+u.Username+" was set by "+actor.Name+
		". If you didn't ask for it, ask an admin what happened.", events.SeverityNotice)
	return nil
}

// removeTwoFactor takes away every second step a user has.
func (s *Service) removeTwoFactor(ctx context.Context, u *User) error {
	st := s.opts.Store
	if err := st.SetTOTP(ctx, u.ID, false, 0); err != nil {
		return err
	}
	if s.opts.Secrets != nil {
		s.opts.Secrets.Delete(ctx, secretRef(u.ID, totpSecret))
		s.opts.Secrets.Delete(ctx, secretRef(u.ID, totpPending))
	}
	keys, err := st.Passkeys(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := st.DeletePasskey(ctx, u.ID, k.ID); err != nil && !store.ErrNotFound.Is(err) {
			return err
		}
	}
	if err := st.ReplaceRecoveryCodes(ctx, u.ID, nil); err != nil {
		return err
	}
	if err := st.DeleteDevices(ctx, u.ID); err != nil {
		return err
	}
	methods, err := st.MFAMethods(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, m := range methods {
		if err := st.DisableMFA(ctx, u.ID, m.Method); err != nil && !store.ErrNotFound.Is(err) {
			return err
		}
	}
	return nil
}
