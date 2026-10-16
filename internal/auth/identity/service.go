package identity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Options are what the identity service works with.
type Options struct {
	Store Store
	// Resources is the resource store, which keeps the sign-in policy,
	// accounts and identity providers.
	Resources store.Store
	// Secrets keeps authenticator app secrets and alert channel settings.
	Secrets    secrets.Store
	Events     events.Bus
	Audit      events.AuditLog
	Hooks      *hooks.Runner
	Authorizer auth.Authorizer
	// Hostname is the panel's name. Passkeys are bound to it, and alerts
	// link to it.
	Hostname string
	// Issuer names the panel in authenticator apps; Racksail by default.
	Issuer string
	Log    *slog.Logger
	Now    func() time.Time
}

// Service is the panel's identity: users and their roles, signing in,
// sessions, second steps, API tokens, SSH keys and impersonation. It
// authenticates every API request.
type Service struct {
	opts    Options
	limiter *attempts

	mu     sync.RWMutex
	policy PolicySpec

	challenges *challenges
	// setupMu keeps two first admins from being created at once.
	setupMu sync.Mutex
	// touched remembers when sessions and tokens were last written as
	// used, so busy ones aren't written on every request.
	touched sync.Map
}

// New returns the service. Run keeps its policy current and removes what
// expired.
func New(opts Options) *Service {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Issuer == "" {
		opts.Issuer = "Racksail"
	}
	return &Service{
		opts: opts, limiter: newAttempts(opts.Now), policy: DefaultPolicy(),
		challenges: newChallenges(opts.Now),
	}
}

func (s *Service) now() time.Time { return s.opts.Now().UTC() }

// Store returns where the service keeps its records.
func (s *Service) Store() Store { return s.opts.Store }

// Policy returns the sign-in policy in force.
func (s *Service) Policy() PolicySpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

// SetPolicy puts a policy in force, as Run does when the stored one
// changes.
func (s *Service) SetPolicy(p PolicySpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = p.effective()
}

// LoadPolicy reads the stored policy and puts it in force.
func (s *Service) LoadPolicy(ctx context.Context) error {
	if s.opts.Resources == nil {
		return nil
	}
	list, _, err := PolicyKind.List(ctx, s.opts.Resources, store.Query{Limit: 1})
	if err != nil {
		return err
	}
	p := DefaultPolicy()
	if len(list) > 0 && !list[0].Deleted() {
		p = list[0].Spec
	}
	s.SetPolicy(p)
	return nil
}

// cleanupEvery is how often what expired is removed.
const cleanupEvery = 10 * time.Minute

// Run keeps the policy current as it changes, removes the users of
// accounts that are gone, and clears out expired sessions, devices, links
// and tokens, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if err := s.LoadPolicy(ctx); err != nil {
		s.opts.Log.Warn("reading the sign-in policy", "error", err)
	}
	var notices <-chan store.Notice
	if s.opts.Resources != nil {
		w := s.opts.Resources.Watch(store.WatchFilter{Kinds: []string{PolicyKind.Name(), account.Kind.Name()}}, 64)
		defer w.Close()
		notices = w.C
	}
	tick := time.NewTicker(cleanupEvery)
	defer tick.Stop()
	s.cleanup(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-notices:
			if !ok {
				notices = nil
				continue
			}
			switch {
			case n.Kind == PolicyKind.Name():
				if err := s.LoadPolicy(ctx); err != nil {
					s.opts.Log.Warn("reading the sign-in policy", "error", err)
				}
			case n.Kind == account.Kind.Name() && n.Op == store.NoticePurged:
				s.removeAccountUsers(ctx, n.ID)
			}
		case <-tick.C:
			s.cleanup(ctx)
		}
	}
}

func (s *Service) cleanup(ctx context.Context) {
	if err := s.opts.Store.DeleteExpired(ctx, s.now()); err != nil {
		s.opts.Log.Warn("removing expired sessions", "error", err)
	}
	s.limiter.prune()
	s.challenges.prune()
	// Accounts removed while the panel wasn't watching still take their
	// users along.
	if s.opts.Resources == nil {
		return
	}
	homes := map[store.ID]bool{}
	q := UserQuery{Limit: store.MaxLimit}
	for {
		users, next, err := s.opts.Store.ListUsers(ctx, q)
		if err != nil {
			s.opts.Log.Warn("listing users", "error", err)
			return
		}
		for _, u := range users {
			if u.Home.Level == store.ScopeAccount {
				homes[u.Home.ID] = true
			}
		}
		if next == "" {
			break
		}
		q.Cursor = next
	}
	for id := range homes {
		if _, err := s.opts.Resources.Get(ctx, id); store.ErrNotFound.Is(err) {
			s.removeAccountUsers(ctx, id)
		}
	}
}

// removeAccountUsers deletes the users who belonged to an account that is
// gone, and the grants others held on it.
func (s *Service) removeAccountUsers(ctx context.Context, acct store.ID) {
	scope := store.AccountScope(acct)
	actor := auth.System("accounts")
	users, _, err := s.opts.Store.ListUsers(ctx, UserQuery{Homes: []store.Scope{scope}, Limit: store.MaxLimit})
	if err != nil {
		s.opts.Log.Warn("listing the users of a removed account", "account", acct, "error", err)
		return
	}
	for _, u := range users {
		if err := s.opts.Store.DeleteUser(ctx, u.ID); err != nil && !store.ErrNotFound.Is(err) {
			s.opts.Log.Warn("deleting the user of a removed account", "user", u.ID, "error", err)
			continue
		}
		s.forget(ctx, u.ID)
		s.audit(ctx, actor, "core.user.delete", u.ID, u.Home, events.OutcomeSuccess, "", "Delete "+u.Username+", whose account was removed")
		s.publish(ctx, UserDeleted, u, actor, u.Username+" was deleted with their account.", UserEvent{Username: u.Username})
	}
	granted, _, err := s.opts.Store.ListUsers(ctx, UserQuery{Grant: &scope, Limit: store.MaxLimit})
	if err != nil {
		return
	}
	for _, u := range granted {
		u.Grants = slices.DeleteFunc(u.Grants, func(g auth.Grant) bool { return g.Scope == scope })
		u.UpdatedAt = s.now()
		if err := s.opts.Store.UpdateUser(ctx, u, 0); err != nil {
			s.opts.Log.Warn("removing grants on a removed account", "user", u.ID, "error", err)
		}
	}
}

// forget drops what the service keeps in memory about a user's sessions.
func (s *Service) forget(ctx context.Context, user store.ID) {
	s.challenges.dropUser(user)
}

// audit records something done to identities.
func (s *Service) audit(ctx context.Context, actor *auth.Actor, action string, subject store.ID, scope store.Scope, outcome events.Outcome, reason, summary string) {
	if s.opts.Audit == nil {
		return
	}
	var a auth.Actor
	if actor != nil {
		a = *actor
	}
	kind := KindUser
	switch {
	case strings.HasPrefix(action, KindToken+"."):
		kind = KindToken
	case strings.HasPrefix(action, KindSession+"."):
		kind = KindSession
	}
	if scope.Level == "" {
		scope = store.ServerScope()
	}
	rec := events.AuditRecord{Actor: a, Action: action, Kind: kind, Subject: subject, Scope: scope, Outcome: outcome, Reason: reason, Summary: summary}
	if err := s.opts.Audit.Record(ctx, rec); err != nil {
		s.opts.Log.Error("writing an audit record", "action", action, "error", err)
	}
}

// publish puts an event about a user on the timeline, in the user's home
// scope so whoever manages them sees it.
func (s *Service) publish(ctx context.Context, t events.Type, u *User, actor *auth.Actor, message string, data any) {
	if s.opts.Events == nil {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		s.opts.Log.Error("encoding an event", "type", t.Name, "error", err)
		return
	}
	e := events.Event{
		Type: t.Name, Severity: t.Severity, Subject: u.ID, Kind: KindUser, Scope: u.Home, Actor: actor.Clone(),
		Message: message, Data: raw,
	}
	if err := s.opts.Events.Publish(context.WithoutCancel(ctx), e); err != nil {
		s.opts.Log.Error("publishing an event", "type", t.Name, "error", err)
	}
}

// authorize asks the authorizer whether actor may use perm on scope.
func (s *Service) authorize(ctx context.Context, actor *auth.Actor, perm auth.Permission, scope store.Scope) error {
	if s.opts.Authorizer == nil {
		return i18n.ErrForbidden.New("action", string(perm))
	}
	return s.opts.Authorizer.Authorize(ctx, actor, perm, auth.Target{Scope: scope})
}

func (s *Service) grantChecker() (auth.GrantChecker, error) {
	gc, ok := s.opts.Authorizer.(auth.GrantChecker)
	if !ok {
		return nil, errors.New("identity: the authorizer can't check grants")
	}
	return gc, nil
}

// covers reports whether a grant on outer reaches inner.
func (s *Service) covers(ctx context.Context, outer, inner store.Scope) (bool, error) {
	gc, err := s.grantChecker()
	if err != nil {
		return false, err
	}
	return gc.Covers(ctx, outer, inner)
}

// Directory returns what modules learn about users through the service.
func (s *Service) Directory() auth.Directory { return directory{s} }

type directory struct{ s *Service }

func directoryUser(u *User) auth.DirectoryUser {
	return auth.DirectoryUser{ID: u.ID, Username: u.Username, Name: u.Name, Email: u.Email, Disabled: u.Disabled, Grants: u.Clone().Grants}
}

func (d directory) User(ctx context.Context, id store.ID) (auth.DirectoryUser, error) {
	u, err := d.s.opts.Store.User(ctx, id)
	if err != nil {
		return auth.DirectoryUser{}, err
	}
	return directoryUser(u), nil
}

func (d directory) Members(ctx context.Context, acct store.ID) ([]auth.DirectoryUser, error) {
	scope := store.AccountScope(acct)
	seen := map[store.ID]bool{}
	var out []auth.DirectoryUser
	add := func(q UserQuery, keep func(*User) (bool, error)) error {
		q.Limit = store.MaxLimit
		for {
			users, next, err := d.s.opts.Store.ListUsers(ctx, q)
			if err != nil {
				return err
			}
			for _, u := range users {
				if seen[u.ID] {
					continue
				}
				ok, err := keep(u)
				if err != nil {
					return err
				}
				if ok {
					seen[u.ID] = true
					out = append(out, directoryUser(u))
				}
			}
			if next == "" {
				return nil
			}
			q.Cursor = next
		}
	}
	all := func(*User) (bool, error) { return true, nil }
	if err := add(UserQuery{Homes: []store.Scope{scope}}, all); err != nil {
		return nil, err
	}
	if err := add(UserQuery{Grant: &scope}, all); err != nil {
		return nil, err
	}
	// Reseller grants reach the account through whose customer it is.
	reaches := func(u *User) (bool, error) {
		for _, g := range u.Grants {
			if g.Scope.Level == store.ScopeReseller {
				ok, err := d.s.covers(ctx, g.Scope, scope)
				if err != nil || ok {
					return ok, err
				}
			}
		}
		return false, nil
	}
	if err := add(UserQuery{Role: auth.RoleReseller}, reaches); err != nil {
		return nil, err
	}
	return out, nil
}

func (d directory) SSHKeys(ctx context.Context, user store.ID) ([]auth.SSHKey, error) {
	keys, err := d.s.opts.Store.SSHKeys(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]auth.SSHKey, len(keys))
	for i, k := range keys {
		out[i] = *k
	}
	return out, nil
}
