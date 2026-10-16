package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// ProviderSpec connects an outside service people sign in with, or a
// second step an add-on provides. The provider itself comes from an
// add-on, such as the LDAP and OpenID Connect add-ons of the official
// catalog, or a module.
type ProviderSpec struct {
	// Capability is identity for auth.provider providers and mfa for
	// auth.mfa ones.
	Capability string `json:"capability" enum:"identity,mfa" doc:"What it does: signs people in (identity), or adds a second sign-in step (mfa)"`
	// Provider is the provider's name in its capability.
	Provider string `json:"provider" minLength:"1" maxLength:"63" pattern:"^[a-z0-9][a-z0-9-]*$" doc:"The provider's name, as the add-on or module that provides it registers it" example:"ldap"`
	Title    string `json:"title" minLength:"1" maxLength:"64" doc:"What the sign-in screen calls it" example:"Company directory"`
	Disabled bool   `json:"disabled,omitempty" doc:"Turn it off without removing it"`
	// Settings are the provider's own, as its settings form asks for them.
	Settings map[string]any `json:"settings,omitempty" doc:"The provider's settings, as its form asks for them"`
	// Secret is a reference to the provider's one secret, such as a bind
	// password or a client secret, which it gets as settings.secret.
	Secret string `json:"secret,omitempty" doc:"A secret the provider needs, such as an LDAP bind password or an OpenID Connect client secret"`
	// Match says how someone signing in for the first time is matched to a
	// user here.
	Match string `json:"match,omitempty" enum:"username,email,none" doc:"How someone signing in for the first time is matched to an existing user: by username, by email, or not at all"`
	// NewUsers adds a user for someone who matches nobody, with
	// NewUserGrants.
	NewUsers      bool         `json:"newUsers,omitempty" doc:"Add a user for someone who signs in for the first time and matches nobody"`
	NewUserGrants []auth.Grant `json:"newUserGrants,omitempty" doc:"What users added this way may do"`
}

// ProviderStatus is what the reconciler last saw of a provider.
type ProviderStatus struct {
	store.Status
}

// ProviderKind is core.signin_provider.
var ProviderKind = store.Define[ProviderSpec, ProviderStatus](store.KindInfo{
	Name: "core.signin_provider", Prefix: "signprov", Scope: store.ScopeServer,
	Title: "Sign-in provider", Plural: "signin-providers",
	Description: "An outside service people sign in with, such as an LDAP directory or an OpenID Connect provider, or a second sign-in step " +
		"an add-on provides. Its provider comes from an add-on or a module; this connects it with its settings.",
	Risks: store.Risks{Create: store.RiskHigh, Update: store.RiskHigh, Delete: store.RiskMedium},
	UI: store.UIHints{
		Icon: "key-round", TitleField: "title", SubtitleField: "provider", Hidden: true,
		Fields: map[string]store.FieldHint{
			"secret":        {Sensitive: true, Widget: "password"},
			"settings":      {Widget: "code"},
			"newUserGrants": {Advanced: true},
		},
	},
}).WithValidator(validateProvider)

func init() {
	store.RegisterKind(ProviderKind)
}

func validateProvider(ctx context.Context, obj, old *store.Object[ProviderSpec, ProviderStatus]) error {
	var fields []i18n.FieldError
	spec := obj.Spec
	if spec.Capability == "mfa" && (spec.Match != "" || spec.NewUsers || len(spec.NewUserGrants) > 0) {
		fields = append(fields, i18n.Field("spec.match", i18n.ErrBadValue, "reason", "second steps don't sign anyone in, so they match and add nobody"))
	}
	if spec.NewUsers && len(spec.NewUserGrants) == 0 {
		fields = append(fields, i18n.Field("spec.newUserGrants", i18n.ErrRequired))
	}
	for i, g := range spec.NewUserGrants {
		if err := g.Validate(); err != nil {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.newUserGrants[%d]", i), i18n.ErrBadValue, "reason", i18n.From(err).Message))
		}
		if g.Role == auth.RoleAdmin {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.newUserGrants[%d].role", i), i18n.ErrBadValue, "reason", "admins are made by hand"))
		}
	}
	if raw, err := json.Marshal(spec.Settings); err == nil && len(raw) > 32<<10 {
		fields = append(fields, i18n.Field("spec.settings", i18n.ErrBadValue, "reason", "settings are at most 32 KiB"))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

type providerReconciler struct{}

func (providerReconciler) Plan(ctx context.Context, obj *store.Object[ProviderSpec, ProviderStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (providerReconciler) Apply(ctx context.Context, obj *store.Object[ProviderSpec, ProviderStatus], plan reconcile.Plan, ex reconcile.Executor) (ProviderStatus, error) {
	st := obj.Status
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	return st, err
}

// ProviderReconciler returns the reconciler of sign-in providers, which
// the panel applies as soon as they're saved.
func ProviderReconciler() reconcile.Reconciler {
	return reconcile.Typed(ProviderKind, providerReconciler{})
}

// SignInOption is an outside provider the sign-in screen offers.
type SignInOption struct {
	ID store.ID `json:"id"`
	// Title is what the button says.
	Title string `json:"title"`
	// Method is password for providers that check a username and password
	// typed on the panel's own screen, and redirect for those whose own page
	// people sign in on.
	Method auth.ProviderMethod `json:"method" enum:"password,redirect"`
}

// providers lists the connected providers of a capability that are on.
func (s *Service) providers(ctx context.Context, capability string) ([]*store.Object[ProviderSpec, ProviderStatus], error) {
	if s.opts.Resources == nil {
		return nil, nil
	}
	list, _, err := ProviderKind.List(ctx, s.opts.Resources, store.Query{Limit: store.MaxLimit})
	if err != nil {
		return nil, err
	}
	var out []*store.Object[ProviderSpec, ProviderStatus]
	for _, p := range list {
		if !p.Deleted() && !p.Spec.Disabled && p.Spec.Capability == capability {
			out = append(out, p)
		}
	}
	return out, nil
}

// settingsOf gives a provider its settings, with its secret resolved.
func (s *Service) settingsOf(ctx context.Context, p *store.Object[ProviderSpec, ProviderStatus]) (json.RawMessage, error) {
	settings := map[string]any{}
	for k, v := range p.Spec.Settings {
		settings[k] = v
	}
	if p.Spec.Secret != "" && s.opts.Secrets != nil {
		v, err := s.opts.Secrets.Get(ctx, secrets.Ref(p.Spec.Secret))
		if err != nil {
			return nil, err
		}
		settings["secret"] = string(v)
	}
	return json.Marshal(settings)
}

func (s *Service) registry() *hooks.Registry {
	if s.opts.Hooks == nil {
		return nil
	}
	return s.opts.Hooks.Registry
}

func (s *Service) openIdentity(ctx context.Context, p *store.Object[ProviderSpec, ProviderStatus]) (auth.IdentityProvider, error) {
	reg := s.registry()
	if reg == nil {
		return nil, hooks.ErrUnknownProvider.New("capability", string(hooks.AuthProvider), "provider", p.Spec.Provider)
	}
	settings, err := s.settingsOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return hooks.Open[auth.IdentityProvider](ctx, reg, hooks.AuthProvider, p.Spec.Provider, settings)
}

// SignInOptions lists the outside providers the sign-in screen offers.
// Providers whose add-on is gone are left out.
func (s *Service) SignInOptions(ctx context.Context) ([]SignInOption, error) {
	list, err := s.providers(ctx, "identity")
	if err != nil {
		return nil, err
	}
	out := []SignInOption{}
	for _, p := range list {
		ip, err := s.openIdentity(ctx, p)
		if err != nil {
			s.opts.Log.Warn("a sign-in provider can't be opened", "provider", p.ID, "error", err)
			continue
		}
		out = append(out, SignInOption{ID: p.ID, Title: p.Spec.Title, Method: ip.Method()})
	}
	return out, nil
}

// linkedUser is who an outside identity signs in as, and how.
type linkedUser struct {
	user   *User
	method string
}

// signInWithProviders tries the providers that check passwords, in turn.
// A wrong password everywhere is auth.ErrBadCredentials.
func (s *Service) signInWithProviders(ctx context.Context, username, password string, local *User) (*linkedUser, error) {
	list, err := s.providers(ctx, "identity")
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		ip, err := s.openIdentity(ctx, p)
		if err != nil {
			s.opts.Log.Warn("a sign-in provider can't be opened", "provider", p.ID, "error", err)
			continue
		}
		pp, ok := ip.(auth.PasswordProvider)
		if !ok || ip.Method() != auth.MethodPassword {
			continue
		}
		identity, err := pp.CheckPassword(ctx, username, password)
		if auth.ErrBadCredentials.Is(err) {
			continue
		}
		if err != nil {
			s.opts.Log.Warn("a sign-in provider couldn't answer", "provider", p.ID, "error", err)
			continue
		}
		u, err := s.userFor(ctx, p, identity)
		if err != nil {
			return nil, err
		}
		return &linkedUser{user: u, method: "provider:" + p.Spec.Provider}, nil
	}
	return nil, auth.ErrBadCredentials.New()
}

// userFor finds the user an outside identity signs in as: the one it is
// linked to, the one it matches the first time, or a new one when the
// provider adds users.
func (s *Service) userFor(ctx context.Context, p *store.Object[ProviderSpec, ProviderStatus], identity *auth.ExternalIdentity) (*User, error) {
	if identity == nil || identity.Subject == "" {
		return nil, auth.ErrProviderFailed.New("provider", p.Spec.Title, "reason", "it didn't say who signed in")
	}
	u, err := s.opts.Store.UserByIdentity(ctx, p.ID, identity.Subject)
	if err == nil {
		return u, nil
	}
	if !store.ErrNotFound.Is(err) {
		return nil, err
	}
	switch p.Spec.Match {
	case "", "username":
		if identity.Username != "" {
			u, err = s.opts.Store.UserByName(ctx, NormalizeUsername(identity.Username))
		}
	case "email":
		if identity.Email != "" {
			u, err = s.userByEmail(ctx, identity.Email)
		}
	}
	if err != nil && !store.ErrNotFound.Is(err) {
		return nil, err
	}
	if u == nil && p.Spec.NewUsers {
		if u, err = s.createProviderUser(ctx, p, identity); err != nil {
			return nil, err
		}
	}
	if u == nil {
		return nil, auth.ErrIdentityUnlinked.New("provider", p.Spec.Title)
	}
	link := &Link{User: u.ID, Provider: p.ID, Subject: identity.Subject, Username: identity.Username, LinkedAt: s.now()}
	if err := s.opts.Store.LinkIdentity(ctx, link); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) userByEmail(ctx context.Context, email string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	users, _, err := s.opts.Store.ListUsers(ctx, UserQuery{Search: email, Limit: store.MaxLimit})
	if err != nil {
		return nil, err
	}
	var found *User
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			if found != nil {
				// Two users with the address match neither.
				return nil, notFound(email)
			}
			found = u
		}
	}
	if found == nil {
		return nil, notFound(email)
	}
	return found, nil
}

var unsafeUsername = regexp.MustCompile(`[^a-z0-9._@-]+`)

func (s *Service) createProviderUser(ctx context.Context, p *store.Object[ProviderSpec, ProviderStatus], identity *auth.ExternalIdentity) (*User, error) {
	name := identity.Username
	if name == "" {
		name, _, _ = strings.Cut(identity.Email, "@")
	}
	name = strings.Trim(unsafeUsername.ReplaceAllString(NormalizeUsername(name), "-"), "-._@")
	if len(name) < 2 {
		return nil, auth.ErrProviderFailed.New("provider", p.Spec.Title, "reason", "it gave no username to add the user under")
	}
	in := UserInput{Username: name, Name: identity.Name, Email: identity.Email, Grants: p.Spec.NewUserGrants}
	home, err := homeOf(&in)
	if err != nil {
		home = store.ServerScope()
	}
	if fields := checkDetails(&in); len(fields) > 0 {
		return nil, i18n.Invalid(fields...)
	}
	now := s.now()
	u := &User{ID: store.NewID(PrefixUser), Username: in.Username, Name: in.Name, Email: in.Email, Home: home, Grants: in.Grants, CreatedAt: now, UpdatedAt: now}
	if err := s.opts.Store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	actor := auth.System("signin-provider")
	s.audit(ctx, actor, string(auth.PermUserCreate), u.ID, u.Home, events.OutcomeSuccess, "", "Add "+u.Username+" on their first sign-in with "+p.Spec.Title)
	s.publish(ctx, UserCreated, u, actor, u.Username+" was added on their first sign-in with "+p.Spec.Title+".", UserEvent{Username: u.Username, Detail: p.Spec.Title})
	return u, nil
}

func (s *Service) provider(ctx context.Context, id store.ID, capability string) (*store.Object[ProviderSpec, ProviderStatus], error) {
	if s.opts.Resources == nil {
		return nil, notFound(string(id))
	}
	p, err := ProviderKind.Get(ctx, s.opts.Resources, id)
	if err != nil {
		return nil, err
	}
	if p.Deleted() || p.Spec.Disabled || p.Spec.Capability != capability {
		return nil, notFound(string(id))
	}
	return p, nil
}

// BeginProviderSignIn starts signing in through a provider's own page. It
// returns where to send the browser, and a secret the browser keeps in a
// cookie until it comes back to callback.
func (s *Service) BeginProviderSignIn(ctx context.Context, id store.ID, callback string, remember bool, client auth.Client) (redirect, state string, err error) {
	if err := s.limiter.take(client.IP); err != nil {
		return "", "", err
	}
	p, err := s.provider(ctx, id, "identity")
	if err != nil {
		return "", "", err
	}
	ip, err := s.openIdentity(ctx, p)
	if err != nil {
		return "", "", err
	}
	rp, ok := ip.(auth.RedirectProvider)
	if !ok || ip.Method() != auth.MethodRedirect {
		return "", "", i18n.ErrBadRequest.New("reason", p.Spec.Title+" signs people in on this panel's own screen")
	}
	to, data, err := rp.Begin(ctx, callback)
	if err != nil {
		return "", "", auth.ErrProviderFailed.Wrap(err, "provider", p.Spec.Title, "reason", "it couldn't start the sign-in")
	}
	u, err := url.Parse(to)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", auth.ErrProviderFailed.New("provider", p.Spec.Title, "reason", "it gave an address the browser can't go to")
	}
	state = s.challenges.put(&challenge{purpose: "provider", method: string(p.ID), persistent: remember, data: data, expires: s.now().Add(10 * time.Minute)})
	return to, state, nil
}

// CompleteProviderSignIn finishes signing in through a provider's page,
// with the query the browser came back with.
func (s *Service) CompleteProviderSignIn(ctx context.Context, state, callback string, query url.Values, deviceSecret string, client auth.Client) (*SignInResult, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return nil, err
	}
	c, err := s.challenges.get(state, "provider")
	if err != nil {
		return nil, err
	}
	s.challenges.drop(state)
	p, err := s.provider(ctx, store.ID(c.method), "identity")
	if err != nil {
		return nil, err
	}
	ip, err := s.openIdentity(ctx, p)
	if err != nil {
		return nil, err
	}
	rp, ok := ip.(auth.RedirectProvider)
	if !ok {
		return nil, auth.ErrChallengeExpired.New()
	}
	identity, err := rp.Complete(ctx, c.data, callback, query)
	if err != nil {
		if i18n.From(err).Code != i18n.ErrInternal.Code {
			return nil, err
		}
		return nil, auth.ErrProviderFailed.Wrap(err, "provider", p.Spec.Title, "reason", "the sign-in didn't complete")
	}
	u, err := s.userFor(ctx, p, identity)
	if err != nil {
		return nil, err
	}
	return s.afterFirstStep(ctx, u, "provider:"+p.Spec.Provider, c.persistent, deviceSecret, client)
}

// LinkView is an outside identity linked to the actor's user.
type LinkView struct {
	Provider store.ID  `json:"provider"`
	Title    string    `json:"title"`
	Username string    `json:"username,omitempty"`
	LinkedAt time.Time `json:"linkedAt"`
}

// MyLinks lists the outside identities the actor signs in with.
func (s *Service) MyLinks(ctx context.Context, actor *auth.Actor) ([]LinkView, error) {
	links, err := s.opts.Store.Links(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	out := make([]LinkView, 0, len(links))
	for _, l := range links {
		v := LinkView{Provider: l.Provider, Title: string(l.Provider), Username: l.Username, LinkedAt: l.LinkedAt}
		if s.opts.Resources != nil {
			if p, err := ProviderKind.Get(ctx, s.opts.Resources, l.Provider); err == nil {
				v.Title = p.Spec.Title
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// UnlinkMine stops an outside identity signing in as the actor, unless it
// is the only way they have.
func (s *Service) UnlinkMine(ctx context.Context, actor *auth.Actor, provider store.ID) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "change how they sign in")
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	links, err := s.opts.Store.Links(ctx, u.ID)
	if err != nil {
		return err
	}
	keys, err := s.opts.Store.Passkeys(ctx, u.ID)
	if err != nil {
		return err
	}
	if u.PasswordHash == "" && len(keys) == 0 && len(links) == 1 {
		return auth.ErrLastSignInMethod.New()
	}
	if err := s.opts.Store.UnlinkIdentity(ctx, u.ID, provider); err != nil {
		return err
	}
	s.audit(ctx, actor, "core.user.unlink", u.ID, u.Home, events.OutcomeSuccess, "", "Stop signing in with "+string(provider))
	return nil
}

// MFAOption is a second step from an add-on that people can turn on.
type MFAOption struct {
	ID      store.ID `json:"id"`
	Title   string   `json:"title"`
	Enabled bool     `json:"enabled" doc:"Whether the person asking has it on"`
}

func (s *Service) openMFA(ctx context.Context, p *store.Object[ProviderSpec, ProviderStatus]) (auth.MFAMethod, error) {
	reg := s.registry()
	if reg == nil {
		return nil, hooks.ErrUnknownProvider.New("capability", string(hooks.AuthMFA), "provider", p.Spec.Provider)
	}
	settings, err := s.settingsOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return hooks.Open[auth.MFAMethod](ctx, reg, hooks.AuthMFA, p.Spec.Provider, settings)
}

func mfaUser(u *User) auth.MFAUser {
	return auth.MFAUser{ID: u.ID, Username: u.Username, Name: u.Name, Email: u.Email}
}

// MFAOptions lists the second steps from add-ons the actor can turn on.
func (s *Service) MFAOptions(ctx context.Context, actor *auth.Actor) ([]MFAOption, error) {
	list, err := s.providers(ctx, "mfa")
	if err != nil {
		return nil, err
	}
	on, err := s.opts.Store.MFAMethods(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	out := []MFAOption{}
	for _, p := range list {
		o := MFAOption{ID: p.ID, Title: p.Spec.Title}
		for _, m := range on {
			o.Enabled = o.Enabled || m.Method == string(p.ID)
		}
		out = append(out, o)
	}
	return out, nil
}

// BeginMFA starts a check with an add-on's second step: to turn it on for
// the actor, or as a sign-in's second step. It returns what to ask the
// person, and the ceremony that finishes it.
func (s *Service) BeginMFA(ctx context.Context, actor *auth.Actor, id store.ID) (string, *auth.MFAChallenge, error) {
	if err := requirePerson(actor); err != nil {
		return "", nil, err
	}
	if actor.Impersonator != "" {
		return "", nil, auth.ErrImpersonating.New("user", actor.Name, "action", "change their second steps")
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return "", nil, err
	}
	p, err := s.provider(ctx, id, "mfa")
	if err != nil {
		return "", nil, err
	}
	m, err := s.openMFA(ctx, p)
	if err != nil {
		return "", nil, err
	}
	ch, err := m.Challenge(ctx, mfaUser(u))
	if err != nil {
		return "", nil, auth.ErrProviderFailed.Wrap(err, "provider", p.Spec.Title, "reason", "it couldn't start the check")
	}
	ceremony := s.challenges.put(&challenge{user: u.ID, purpose: "mfa-enroll", method: string(p.ID), data: ch.State})
	return ceremony, ch, nil
}

// FinishMFA turns an add-on's second step on for the actor once a check
// with it passed.
func (s *Service) FinishMFA(ctx context.Context, actor *auth.Actor, ceremony, response string) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	c, err := s.challenges.get(ceremony, "mfa-enroll")
	if err != nil {
		return err
	}
	if c.user != actor.Person() {
		return auth.ErrChallengeExpired.New()
	}
	u, err := s.opts.Store.User(ctx, c.user)
	if err != nil {
		return err
	}
	p, err := s.provider(ctx, store.ID(c.method), "mfa")
	if err != nil {
		return err
	}
	m, err := s.openMFA(ctx, p)
	if err != nil {
		return err
	}
	if err := m.Verify(ctx, mfaUser(u), c.data, response); err != nil {
		if auth.ErrPending.Is(err) {
			return err
		}
		s.challenges.fail(ceremony)
		return auth.ErrBadCode.New()
	}
	s.challenges.drop(ceremony)
	if err := s.opts.Store.EnableMFA(ctx, &MFAEnrollment{User: u.ID, Method: string(p.ID), EnabledAt: s.now()}); err != nil {
		return err
	}
	if err := s.clearPending(ctx, actor, PendingTwoFactor, u); err != nil {
		return err
	}
	s.audit(ctx, actor, "core.user.enable_mfa", u.ID, u.Home, events.OutcomeSuccess, "", "Turn on "+p.Spec.Title)
	s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+" turned on "+p.Spec.Title+".", UserEvent{Username: u.Username, Detail: p.Spec.Title + " on"})
	return nil
}

// DisableMFA turns an add-on's second step off for the actor.
func (s *Service) DisableMFA(ctx context.Context, actor *auth.Actor, id store.ID) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "change their second steps")
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	if err := s.opts.Store.DisableMFA(ctx, u.ID, string(id)); err != nil {
		return err
	}
	s.audit(ctx, actor, "core.user.disable_mfa", u.ID, u.Home, events.OutcomeSuccess, "", "Turn off "+string(id))
	s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+" turned a second step off.", UserEvent{Username: u.Username, Detail: string(id) + " off"})
	return nil
}

// BeginSecondStepMFA starts an add-on's second step during a sign-in.
func (s *Service) BeginSecondStepMFA(ctx context.Context, challengeSecret string, id store.ID) (*auth.MFAChallenge, error) {
	c, err := s.challenges.get(challengeSecret, "signin")
	if err != nil {
		return nil, err
	}
	u, err := s.opts.Store.User(ctx, c.user)
	if err != nil {
		return nil, auth.ErrChallengeExpired.New()
	}
	enabled, err := s.opts.Store.MFAMethods(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	on := false
	for _, e := range enabled {
		on = on || e.Method == string(id)
	}
	if !on {
		return nil, notFound(string(id))
	}
	p, err := s.provider(ctx, id, "mfa")
	if err != nil {
		return nil, err
	}
	m, err := s.openMFA(ctx, p)
	if err != nil {
		return nil, err
	}
	ch, err := m.Challenge(ctx, mfaUser(u))
	if err != nil {
		return nil, auth.ErrProviderFailed.Wrap(err, "provider", p.Spec.Title, "reason", "it couldn't start the check")
	}
	state, err := json.Marshal(mfaState{Method: string(id), State: ch.State})
	if err != nil {
		return nil, err
	}
	c.data = state
	return ch, nil
}

type mfaState struct {
	Method string `json:"method"`
	State  []byte `json:"state"`
}

// verifyMFA checks the response to an add-on's second step started with
// BeginSecondStepMFA.
func (s *Service) verifyMFA(ctx context.Context, u *User, c *challenge, method, response string) error {
	var st mfaState
	if len(c.data) == 0 || json.Unmarshal(c.data, &st) != nil || st.Method != method {
		return auth.ErrChallengeExpired.New()
	}
	p, err := s.provider(ctx, store.ID(method), "mfa")
	if err != nil {
		return err
	}
	m, err := s.openMFA(ctx, p)
	if err != nil {
		return err
	}
	if err := m.Verify(ctx, mfaUser(u), st.State, response); err != nil {
		if auth.ErrPending.Is(err) {
			return err
		}
		return auth.ErrBadCode.New()
	}
	c.data = nil
	return nil
}
