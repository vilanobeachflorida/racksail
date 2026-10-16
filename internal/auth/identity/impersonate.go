package identity

import (
	"context"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// impersonationLimit is the longest one can view the panel as someone else
// before signing back in as themselves.
const impersonationLimit = time.Hour

// Impersonate starts viewing the panel as another user. It needs
// core.impersonate where that user belongs and every grant they hold, and
// works for people in the panel only. Admins of the whole server can't be
// impersonated. Everything done meanwhile is recorded with both names, and
// the session ends after an hour at most.
func (s *Service) Impersonate(ctx context.Context, actor *auth.Actor, id store.ID, client auth.Client) (*Issued, error) {
	if actor == nil || actor.Type != auth.ActorUser || actor.SessionID == "" {
		return nil, auth.ErrPanelOnly.New()
	}
	if actor.Impersonator != "" {
		return nil, auth.ErrImpersonating.New("user", actor.Name, "action", "view the panel as someone else")
	}
	target, err := s.visible(ctx, actor, id, auth.PermUserRead)
	if err != nil {
		return nil, err
	}
	refuse := func(reason string) error {
		err := auth.ErrCannotImpersonate.New("user", target.Username, "reason", reason)
		s.audit(ctx, actor, string(auth.PermImpersonate), target.ID, target.Home, events.OutcomeDenied, err.Message, "View the panel as "+target.Username)
		return err
	}
	switch {
	case target.ID == actor.ID:
		return nil, refuse("that's you")
	case target.Disabled:
		return nil, refuse("the user is turned off")
	}
	for _, g := range target.Grants {
		if g.Scope.Level == store.ScopeServer {
			return nil, refuse("their grants cover the whole server")
		}
	}
	if err := s.authorize(ctx, actor, auth.PermImpersonate, target.Home); err != nil {
		s.audit(ctx, actor, string(auth.PermImpersonate), target.ID, target.Home, events.OutcomeDenied, i18n.From(err).Message, "View the panel as "+target.Username)
		return nil, err
	}
	gc, err := s.grantChecker()
	if err != nil {
		return nil, err
	}
	for _, g := range target.Grants {
		if err := gc.CanGrant(ctx, actor, g); err != nil {
			return nil, refuse("they may do things you may not")
		}
	}
	parent, err := s.SessionOf(ctx, actor)
	if err != nil {
		return nil, err
	}
	issued, err := s.issue(ctx, target, "impersonation", parent.SecondFactor, false, client, func(x *Session) {
		x.Impersonator, x.Parent = actor.ID, parent.ID
		x.ExpiresAt = minTime(x.ExpiresAt, x.CreatedAt.Add(impersonationLimit), parent.ExpiresAt)
		// What the user must do first is theirs, not the impersonator's.
		x.Pending = ""
	})
	if err != nil {
		return nil, err
	}
	seen := &auth.Actor{Type: auth.ActorUser, ID: target.ID, Name: target.Username, User: target.ID, Source: auth.SourceUI, IP: client.IP, Impersonator: actor.ID}
	s.audit(ctx, actor, string(auth.PermImpersonate), target.ID, target.Home, events.OutcomeSuccess, "", "View the panel as "+target.Username)
	s.publish(ctx, UserImpersonated, target, seen, actor.Name+" started viewing the panel as "+target.Username+".", UserEvent{Username: target.Username, Detail: actor.Name, IP: client.IP})
	return issued, nil
}

func minTime(ts ...time.Time) time.Time {
	m := ts[0]
	for _, t := range ts[1:] {
		if !t.IsZero() && t.Before(m) {
			m = t
		}
	}
	return m
}

// StopImpersonating ends viewing the panel as someone else and returns to
// the impersonator's own session, under a new secret.
func (s *Service) StopImpersonating(ctx context.Context, actor *auth.Actor) (*Issued, error) {
	if actor == nil || actor.Impersonator == "" {
		return nil, auth.ErrNotImpersonating.New()
	}
	x, err := s.SessionOf(ctx, actor)
	if err != nil {
		return nil, err
	}
	if err := s.opts.Store.DeleteSession(ctx, x.ID); err != nil && !store.ErrNotFound.Is(err) {
		return nil, err
	}
	parent, err := s.opts.Store.Session(ctx, x.Parent)
	if err != nil || s.now().After(parent.ExpiresAt) {
		return nil, auth.ErrSessionExpired.New()
	}
	now := s.now()
	issued, err := s.newSecret(ctx, parent, func(x *Session) { x.LastSeenAt = now })
	if err != nil {
		return nil, err
	}
	if target, err := s.User(ctx, x.User); err == nil {
		s.audit(ctx, actor, "core.impersonate.stop", target.ID, target.Home, events.OutcomeSuccess, "", "Stop viewing the panel as "+target.Username)
	}
	return issued, nil
}

// Impersonation says who is viewing the panel as the actor's user, for the
// banner the UI shows meanwhile.
type Impersonation struct {
	By        store.ID  `json:"by" doc:"The admin or reseller viewing the panel"`
	Name      string    `json:"name" doc:"Their name"`
	StartedAt time.Time `json:"startedAt" doc:"When they started viewing the panel as the user"`
	EndsAt    time.Time `json:"endsAt" doc:"When it ends at the latest"`
}

// ImpersonationOf returns who is viewing the panel as actor's user, or nil.
func (s *Service) ImpersonationOf(ctx context.Context, actor *auth.Actor) *Impersonation {
	if actor == nil || actor.Impersonator == "" {
		return nil
	}
	x, err := s.SessionOf(ctx, actor)
	if err != nil {
		return nil
	}
	by, err := s.User(ctx, actor.Impersonator)
	if err != nil {
		return nil
	}
	return &Impersonation{By: by.ID, Name: by.DisplayName(), StartedAt: x.CreatedAt, EndsAt: x.ExpiresAt}
}
