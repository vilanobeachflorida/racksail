package identity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Options are what the identity service works with.
type Options struct {
	// Store keeps users' credentials, sessions and tokens.
	Store Store
	// Resources is the resource store, which keeps users, the sign-in
	// policy, accounts and identity providers.
	Resources store.Store
	// Pipeline makes the changes to users the service makes itself, such
	// as adding the first admin or someone's own SSH key.
	Pipeline changeset.Pipeline
	// Refresh asks for a user's resource to be reconciled again, so its
	// status says how they sign in now; the reconcile queue's Enqueue.
	Refresh func(id store.ID, reason string)
	// Secrets keeps authenticator app secrets and alert channel settings.
	Secrets secrets.Store
	// Channels returns what security alert channels are built with: the
	// notification service's Env, whose client reaches only where
	// notifications may go and whose relay sends email. Without it, alerts
	// reach public addresses only and can't go by email.
	Channels   func() notify.Env
	Events     events.Bus
	Audit      events.AuditLog
	Hooks      *hooks.Runner
	Authorizer auth.Authorizer
	// Hostname is the panel's name. Passkeys are bound to it, and alerts
	// link to it.
	Hostname string
	// Issuer names the panel in authenticator apps; Racksail by default.
	Issuer string
	// SetupFile is where the setup code is kept for the server's console,
	// which the service removes once the panel has its first admin.
	SetupFile string
	Log       *slog.Logger
	Now       func() time.Time
}

// Service is the panel's identity: users and their roles, signing in,
// sessions, second steps, API tokens, SSH keys and impersonation. It
// authenticates every API request.
type Service struct {
	opts    Options
	limiter *attempts

	mu     sync.RWMutex
	policy SignInPolicySpec

	challenges *challenges
	// setupMu keeps two first admins from being created at once.
	setupMu sync.Mutex
	// touched remembers when sessions and tokens were last written as
	// used, so busy ones aren't written on every request. Entries older
	// than touchEvery are pruned, so it only holds those in use lately.
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

// Store returns where the service keeps credentials.
func (s *Service) Store() Store { return s.opts.Store }

// Policy returns the sign-in policy in force.
func (s *Service) Policy() SignInPolicySpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

// SetPolicy puts a policy in force, as Run does when the stored one
// changes.
func (s *Service) SetPolicy(p SignInPolicySpec) {
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

// orphanAge is how old credentials of a user who isn't there must be
// before they're removed: the first admin's are written just before their
// user is.
const orphanAge = time.Hour

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
	now := s.now()
	if err := s.opts.Store.DeleteExpired(ctx, now); err != nil {
		s.opts.Log.Warn("removing expired sessions", "error", err)
	}
	s.limiter.prune()
	s.challenges.prune()
	s.touched.Range(func(key, value any) bool {
		if now.Sub(value.(time.Time)) >= touchEvery {
			s.touched.Delete(key)
		}
		return true
	})
	if s.opts.Resources == nil {
		return
	}
	// What a user left behind goes with them, also when the panel wasn't
	// running to see them go.
	kept, err := s.opts.Store.Users(ctx)
	if err != nil {
		s.opts.Log.Warn("listing users' credentials", "error", err)
		return
	}
	for id, since := range kept {
		if now.Sub(since) < orphanAge {
			continue
		}
		if _, err := s.opts.Resources.Get(ctx, id); store.ErrNotFound.Is(err) {
			s.forgetUser(ctx, id)
		}
	}
	// Accounts removed while the panel wasn't watching still take their
	// users along.
	homes := map[store.ID]bool{}
	err = s.eachUser(ctx, store.Query{}, func(u *store.Object[UserSpec, UserStatus]) error {
		if u.Scope.Level == store.ScopeAccount && !u.Deleted() {
			homes[u.Scope.ID] = true
		}
		return nil
	})
	if err != nil {
		s.opts.Log.Warn("listing users", "error", err)
		return
	}
	for id := range homes {
		if _, err := s.opts.Resources.Get(ctx, id); store.ErrNotFound.Is(err) {
			s.removeAccountUsers(ctx, id)
		}
	}
}

// forgetUser removes everything kept for a user who is gone: their
// credentials, sessions, tokens and secrets.
func (s *Service) forgetUser(ctx context.Context, id store.ID) {
	if err := s.opts.Store.DeleteUser(ctx, id); err != nil {
		s.opts.Log.Warn("removing a deleted user's credentials", "user", id, "error", err)
	}
	s.forget(ctx, id)
	if s.opts.Secrets != nil {
		for _, name := range userSecrets {
			if err := s.opts.Secrets.Delete(ctx, secretRef(id, name)); err != nil && !store.ErrNotFound.Is(err) {
				s.opts.Log.Warn("removing a deleted user's secret", "user", id, "error", err)
			}
		}
	}
}

// removeAccountUsers deletes the users who belonged to an account that is
// gone, and takes away the grants others held on it, as one change set.
func (s *Service) removeAccountUsers(ctx context.Context, acct store.ID) {
	scope := store.AccountScope(acct)
	err := s.retry(ctx, func() error {
		var changes []changeset.Change
		err := s.eachUser(ctx, store.Query{Scope: &scope}, func(u *store.Object[UserSpec, UserStatus]) error {
			changes = append(changes, changeset.Change{Op: changeset.OpDelete, Kind: KindUser, ID: u.ID})
			return nil
		})
		if err != nil {
			return err
		}
		err = s.eachUser(ctx, store.Query{Index: map[string]string{"grantOn": string(acct)}}, func(u *store.Object[UserSpec, UserStatus]) error {
			if u.Scope == scope {
				return nil
			}
			spec := u.Spec
			spec.Grants = slices.DeleteFunc(slices.Clone(spec.Grants), func(g auth.Grant) bool { return g.Scope == scope })
			raw, err := json.Marshal(spec)
			if err != nil {
				return err
			}
			changes = append(changes, changeset.Change{Op: changeset.OpUpdate, Kind: KindUser, ID: u.ID, Spec: raw, Revision: u.Revision})
			return nil
		})
		if err != nil || len(changes) == 0 {
			return err
		}
		_, err = s.submit(ctx, changeset.Request{Actor: auth.System("accounts"), Title: "Remove the users of a deleted account", Changes: changes})
		return err
	})
	if err != nil {
		s.opts.Log.Warn("removing the users of a deleted account", "account", acct, "error", err)
	}
}

// eachUser calls fn for every user the query selects, deleted ones left
// out.
func (s *Service) eachUser(ctx context.Context, q store.Query, fn func(u *store.Object[UserSpec, UserStatus]) error) error {
	if s.opts.Resources == nil {
		return nil
	}
	q.Limit = store.MaxLimit
	for {
		list, next, err := UserKind.List(ctx, s.opts.Resources, q)
		if err != nil {
			return err
		}
		for _, u := range list {
			if u.Deleted() {
				continue
			}
			if err := fn(u); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		q.Cursor = next
	}
}

// User returns a user: who they are and what they may do, with their
// credentials. A user being deleted is reported as not found.
func (s *Service) User(ctx context.Context, id store.ID) (*User, error) {
	if s.opts.Resources == nil {
		return nil, notFound(string(id))
	}
	obj, err := UserKind.Get(ctx, s.opts.Resources, id)
	if err != nil {
		return nil, err
	}
	if obj.Deleted() {
		return nil, notFound(string(id))
	}
	return s.withCredentials(ctx, obj)
}

func (s *Service) withCredentials(ctx context.Context, obj *store.Object[UserSpec, UserStatus]) (*User, error) {
	u := &User{
		ID: obj.ID, Username: obj.Spec.Username, Name: obj.Spec.Name, Email: obj.Spec.Email, Home: obj.Scope,
		Grants: obj.Spec.Grants, Disabled: obj.Spec.Disabled, Locale: obj.Spec.Locale, AllowFrom: obj.Spec.AllowFrom, SSHKeys: obj.Spec.SSHKeys,
		CreatedAt: obj.CreatedAt, UpdatedAt: obj.UpdatedAt, Revision: obj.Revision,
	}
	if u.Grants == nil {
		u.Grants = []auth.Grant{}
	}
	c, err := s.opts.Store.Credentials(ctx, obj.ID)
	switch {
	case err == nil:
		u.Credentials = *c
	case !store.ErrNotFound.Is(err):
		return nil, err
	}
	return u, nil
}

// userByName returns the user with a username.
func (s *Service) userByName(ctx context.Context, username string) (*User, error) {
	name := NormalizeUsername(username)
	var found *store.Object[UserSpec, UserStatus]
	err := s.eachUser(ctx, store.Query{Index: map[string]string{"username": name}}, func(u *store.Object[UserSpec, UserStatus]) error {
		found = u
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, notFound(name)
	}
	return s.withCredentials(ctx, found)
}

// conflictRetries is how often a change the service makes is planned again
// when something else changed the user meanwhile.
const conflictRetries = 3

// retry runs fn again when it fails because a resource changed between
// reading and writing it.
func (s *Service) retry(ctx context.Context, fn func() error) error {
	var err error
	for range conflictRetries {
		if err = fn(); !store.ErrConflict.Is(err) {
			return err
		}
	}
	return err
}

// submit sends a change the service makes through the change pipeline.
func (s *Service) submit(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error) {
	if s.opts.Pipeline == nil {
		return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
	}
	return s.opts.Pipeline.Submit(ctx, req)
}

// refresh asks for the user's status to be brought up to date with how
// they sign in now.
func (s *Service) refresh(id store.ID, reason string) {
	if s.opts.Refresh != nil {
		s.opts.Refresh(id, reason)
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

// setupDone removes the setup code once the panel has its first admin, and
// the copy the console shows.
func (s *Service) setupDone(ctx context.Context) error {
	if err := s.opts.Store.SetSetupCode(ctx, ""); err != nil {
		return err
	}
	if s.opts.SetupFile != "" {
		if err := os.Remove(s.opts.SetupFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.opts.Log.Warn("removing the used setup code", "file", s.opts.SetupFile, "error", err)
		}
	}
	return nil
}

// Directory returns what modules learn about users through the service.
func (s *Service) Directory() auth.Directory { return directory{s} }

type directory struct{ s *Service }

func directoryUser(u *store.Object[UserSpec, UserStatus]) auth.DirectoryUser {
	grants := make([]auth.Grant, len(u.Spec.Grants))
	for i, g := range u.Spec.Grants {
		grants[i] = g.Clone()
	}
	return auth.DirectoryUser{ID: u.ID, Username: u.Spec.Username, Name: u.Spec.Name, Email: u.Spec.Email, Disabled: u.Spec.Disabled, Grants: grants, Locale: u.Spec.Locale}
}

func (d directory) User(ctx context.Context, id store.ID) (auth.DirectoryUser, error) {
	if d.s.opts.Resources == nil {
		return auth.DirectoryUser{}, notFound(string(id))
	}
	u, err := UserKind.Get(ctx, d.s.opts.Resources, id)
	if err != nil {
		return auth.DirectoryUser{}, err
	}
	if u.Deleted() {
		return auth.DirectoryUser{}, notFound(string(id))
	}
	return directoryUser(u), nil
}

func (d directory) Members(ctx context.Context, acct store.ID) ([]auth.DirectoryUser, error) {
	scope := store.AccountScope(acct)
	seen := map[store.ID]bool{}
	var out []auth.DirectoryUser
	add := func(q store.Query, keep func(*store.Object[UserSpec, UserStatus]) (bool, error)) error {
		return d.s.eachUser(ctx, q, func(u *store.Object[UserSpec, UserStatus]) error {
			if seen[u.ID] {
				return nil
			}
			ok, err := keep(u)
			if err != nil || !ok {
				return err
			}
			seen[u.ID] = true
			out = append(out, directoryUser(u))
			return nil
		})
	}
	all := func(*store.Object[UserSpec, UserStatus]) (bool, error) { return true, nil }
	if err := add(store.Query{Scope: &scope}, all); err != nil {
		return nil, err
	}
	if err := add(store.Query{Index: map[string]string{"grantOn": string(acct)}}, all); err != nil {
		return nil, err
	}
	// Reseller grants reach the account through whose customer it is.
	reaches := func(u *store.Object[UserSpec, UserStatus]) (bool, error) {
		for _, g := range u.Spec.Grants {
			if g.Scope.Level == store.ScopeReseller {
				ok, err := d.s.covers(ctx, g.Scope, scope)
				if err != nil || ok {
					return ok, err
				}
			}
		}
		return false, nil
	}
	if err := add(store.Query{Index: map[string]string{"role": string(auth.RoleReseller)}}, reaches); err != nil {
		return nil, err
	}
	return out, nil
}

func (d directory) SSHKeys(ctx context.Context, user store.ID) ([]auth.SSHKey, error) {
	u, err := d.s.User(ctx, user)
	if err != nil {
		return nil, err
	}
	return sshKeyViews(u), nil
}
