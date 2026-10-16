package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
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
	// user here. Users with a grant on the whole server are never matched:
	// they link their outside identity themselves.
	Match string `json:"match,omitempty" enum:"none,username,email" doc:"How someone signing in for the first time is matched to an existing user: not at all, so people link it themselves in their security settings, by username, or by email. Users with a grant on the whole server are never matched"`
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
	Risks:   store.Risks{Create: store.RiskHigh, Update: store.RiskHigh, Delete: store.RiskMedium},
	Confirm: true,
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
	// Whoever connects a provider that adds users gives those users their
	// grants, so they must be able to give them.
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		return nil
	}
	az, ok := auth.AuthorizerFrom(ctx)
	if !ok {
		return nil
	}
	gc, ok := az.(auth.GrantChecker)
	if !ok {
		return fmt.Errorf("identity: the authorizer can't check grants")
	}
	for i, g := range spec.NewUserGrants {
		if err := gc.CanGrant(ctx, actor, g); err != nil {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.newUserGrants[%d]", i), i18n.ErrBadValue, "reason", i18n.From(err).Message))
		}
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
	ID store.ID `json:"id" doc:"The sign-in provider"`
	// Title is what the button says.
	Title string `json:"title" doc:"What the button says"`
	// Method is password for providers that check a username and password
	// typed on the panel's own screen, and redirect for those whose own page
	// people sign in on.
	Method auth.ProviderMethod `json:"method" enum:"password,redirect" doc:"password when the panel's own screen takes the username and password, redirect when people sign in on the provider's page"`
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

// openProvider connects the provider of a capability a sign-in provider
// names, with its settings.
func (s *Service) openProvider(ctx context.Context, point hooks.Point, p *store.Object[ProviderSpec, ProviderStatus]) (any, error) {
	reg := s.registry()
	if reg == nil {
		return nil, hooks.ErrUnknownProvider.New("capability", string(point), "provider", p.Spec.Provider)
	}
	found, ok := reg.Provider(point, p.Spec.Provider)
	if !ok {
		return nil, hooks.ErrUnknownProvider.New("capability", string(point), "provider", p.Spec.Provider)
	}
	settings, err := s.settingsOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return found.Provider.Open(ctx, settings)
}

// openIdentity connects an identity provider. Providers that run apart
// from the panel, as add-ons do, answer the capability's methods instead
// of implementing its interface here.
func (s *Service) openIdentity(ctx context.Context, p *store.Object[ProviderSpec, ProviderStatus]) (auth.IdentityProvider, error) {
	v, err := s.openProvider(ctx, hooks.AuthProvider, p)
	if err != nil {
		return nil, err
	}
	switch ip := v.(type) {
	case auth.IdentityProvider:
		return ip, nil
	case auth.Caller:
		return auth.RemoteIdentityProvider(ctx, ip)
	}
	return nil, fmt.Errorf("identity: the provider %s gave %T, which signs nobody in", p.Spec.Provider, v)
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
	id, err := s.opts.Store.LinkedUser(ctx, p.ID, identity.Subject)
	if err == nil {
		return s.User(ctx, id)
	}
	if !store.ErrNotFound.Is(err) {
		return nil, err
	}
	var u *User
	switch p.Spec.Match {
	case "username":
		if identity.Username != "" {
			u, err = s.userByName(ctx, identity.Username)
		}
	case "email":
		if identity.Email != "" {
			u, err = s.userByEmail(ctx, identity.Email)
		}
	}
	if err != nil && !store.ErrNotFound.Is(err) {
		return nil, err
	}
	if u != nil && serverWide(u) {
		// Usernames and email addresses are often the person's own choice
		// at the provider, so nobody becomes an admin by picking one.
		s.audit(ctx, auth.System("signin-provider"), "auth.sign_in", u.ID, u.Home, events.OutcomeDenied, "matched a user who manages the whole server",
			"Sign in with "+p.Spec.Title+" as "+u.Username)
		return nil, auth.ErrLinkByHand.New("provider", p.Spec.Title)
	}
	if u == nil && p.Spec.NewUsers {
		if u, err = s.createProviderUser(ctx, p, identity); err != nil {
			return nil, err
		}
	}
	if u == nil {
		return nil, auth.ErrIdentityUnlinked.New("provider", p.Spec.Title)
	}
	if err := s.link(ctx, auth.System("signin-provider"), u, p, identity); err != nil {
		return nil, err
	}
	return u, nil
}

// serverWide reports whether one of u's grants is on the whole server.
func serverWide(u *User) bool {
	return slices.ContainsFunc(u.Grants, func(g auth.Grant) bool { return g.Scope.Level == store.ScopeServer })
}

// link connects an outside identity to u, so it signs in as them.
func (s *Service) link(ctx context.Context, actor *auth.Actor, u *User, p *store.Object[ProviderSpec, ProviderStatus], identity *auth.ExternalIdentity) error {
	l := &Link{User: u.ID, Provider: p.ID, Subject: identity.Subject, Username: identity.Username, LinkedAt: s.now()}
	if err := s.opts.Store.LinkIdentity(ctx, l); err != nil {
		if auth.ErrIdentityTaken.Is(err) {
			return auth.ErrIdentityTaken.New("provider", p.Spec.Title)
		}
		return err
	}
	who := identity.Username
	if who == "" {
		who = identity.Subject
	}
	s.audit(ctx, actor, "core.user.link", u.ID, u.Home, events.OutcomeSuccess, "", "Link "+who+" at "+p.Spec.Title+" to "+u.Username)
	s.publish(ctx, UserIdentityLinked, u, actor, who+" at "+p.Spec.Title+" now signs in as "+u.Username+".", UserEvent{Username: u.Username, Detail: p.Spec.Title})
	s.alert(ctx, u, "An outside sign-in was linked", who+" at "+p.Spec.Title+" can now sign in as "+u.Username+
		". If you didn't link it, stop signing in with it in your security settings.", events.SeverityNotice)
	return nil
}

func (s *Service) userByEmail(ctx context.Context, email string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var found []*store.Object[UserSpec, UserStatus]
	err := s.eachUser(ctx, store.Query{Index: map[string]string{"email": email}}, func(u *store.Object[UserSpec, UserStatus]) error {
		found = append(found, u)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Two users with the address match neither.
	if len(found) != 1 {
		return nil, notFound(email)
	}
	return s.withCredentials(ctx, found[0])
}

var unsafeUsername = regexp.MustCompile(`[^a-z0-9._@-]+`)

// providerActor is the panel adding users for a provider that adds them:
// it gives what the admin who connected the provider chose, which never
// includes an admin's role or core.override.
func providerActor() *auth.Actor {
	a := auth.System("signin-provider")
	a.Grants = []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope(), Permissions: []auth.Permission{auth.PermImpersonate, auth.PermSecretsRead}}}
	return a
}

func (s *Service) createProviderUser(ctx context.Context, p *store.Object[ProviderSpec, ProviderStatus], identity *auth.ExternalIdentity) (*User, error) {
	name := identity.Username
	if name == "" {
		name, _, _ = strings.Cut(identity.Email, "@")
	}
	name = strings.Trim(unsafeUsername.ReplaceAllString(NormalizeUsername(name), "-"), "-._@")
	if len(name) < 2 {
		return nil, auth.ErrProviderFailed.New("provider", p.Spec.Title, "reason", "it gave no username to add the user under")
	}
	home := store.ServerScope()
	if len(p.Spec.NewUserGrants) > 0 {
		home = p.Spec.NewUserGrants[0].Scope
		for _, g := range p.Spec.NewUserGrants[1:] {
			if g.Scope != home {
				home = store.ServerScope()
			}
		}
	}
	spec := UserSpec{Username: name, Name: identity.Name, Email: identity.Email, Grants: p.Spec.NewUserGrants}
	normalize(&spec)
	if !emailPattern.MatchString(spec.Email) {
		spec.Email = ""
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	id := store.NewID(PrefixUser)
	_, err = s.submit(ctx, changeset.Request{
		Actor: providerActor(), Title: "Add " + spec.Username + " on their first sign-in with " + p.Spec.Title,
		Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: KindUser, ID: id, Scope: home, Spec: raw}},
	})
	if err != nil {
		return nil, err
	}
	return s.User(ctx, id)
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
// cookie until it comes back to callback. deviceSecret is the browser's
// trusted-device cookie, if it has one, which skips the second step once
// the browser is back.
func (s *Service) BeginProviderSignIn(ctx context.Context, id store.ID, callback string, remember bool, deviceSecret string, client auth.Client) (redirect, state string, err error) {
	if err := s.limiter.take(client.IP); err != nil {
		return "", "", err
	}
	return s.beginRedirect(ctx, id, callback, &challenge{purpose: "provider", persistent: remember, device: deviceSecret})
}

// beginRedirect sends the browser to a redirect provider's page, keeping
// what the provider needs when it comes back with c.
func (s *Service) beginRedirect(ctx context.Context, id store.ID, callback string, c *challenge) (string, string, error) {
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
	c.method, c.data, c.expires = string(p.ID), data, s.now().Add(10*time.Minute)
	return to, s.challenges.put(c), nil
}

// ProviderReturn is how coming back from a provider's page went: signed
// in, waiting for a second step, or an outside identity linked to the user
// who started from their security settings.
type ProviderReturn struct {
	*SignInResult
	// Linked is the provider whose identity was linked, when the browser
	// came back from linking one.
	Linked *store.Object[ProviderSpec, ProviderStatus]
}

// CompleteProviderSignIn finishes signing in through a provider's page,
// or linking an identity there, with the query the browser came back with.
func (s *Service) CompleteProviderSignIn(ctx context.Context, state, callback string, query url.Values, deviceSecret string, client auth.Client) (*ProviderReturn, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return nil, err
	}
	purpose := s.challenges.purposeOf(state)
	if purpose != "provider-link" {
		purpose = "provider"
	}
	c, err := s.challenges.get(state, purpose)
	if err != nil {
		return nil, err
	}
	if !s.challenges.consume(state) {
		return nil, auth.ErrChallengeExpired.New()
	}
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
	if purpose == "provider-link" {
		u, err := s.User(ctx, c.user)
		if err != nil {
			return nil, auth.ErrChallengeExpired.New()
		}
		actor := signInActor(u, u.Username, client)
		if err := s.linkChecked(ctx, actor, u, p, identity); err != nil {
			return nil, err
		}
		return &ProviderReturn{Linked: p}, nil
	}
	u, err := s.userFor(ctx, p, identity)
	if err != nil {
		return nil, err
	}
	if deviceSecret == "" {
		deviceSecret = c.device
	}
	res, err := s.afterFirstStep(ctx, u, "provider:"+p.Spec.Provider, c.persistent, deviceSecret, client)
	if err != nil {
		return nil, err
	}
	return &ProviderReturn{SignInResult: res}, nil
}

// linkChecked links an outside identity to u, unless it signs in as
// someone else already.
func (s *Service) linkChecked(ctx context.Context, actor *auth.Actor, u *User, p *store.Object[ProviderSpec, ProviderStatus], identity *auth.ExternalIdentity) error {
	if identity == nil || identity.Subject == "" {
		return auth.ErrProviderFailed.New("provider", p.Spec.Title, "reason", "it didn't say who signed in")
	}
	if other, err := s.opts.Store.LinkedUser(ctx, p.ID, identity.Subject); err == nil && other != u.ID {
		return auth.ErrIdentityTaken.New("provider", p.Spec.Title)
	}
	return s.link(ctx, actor, u, p, identity)
}

// LinkInput links an identity at a provider that checks passwords, such as
// an LDAP directory, to the actor's own user.
type LinkInput struct {
	Username string `json:"username" maxLength:"254" doc:"Your username at the provider"`
	Password string `json:"password" maxLength:"1024" format:"password" doc:"Your password at the provider, which the panel checks with it and doesn't keep"`
}

// linking checks that the actor may link an outside identity to their own
// user, in the panel.
func (s *Service) linking(ctx context.Context, actor *auth.Actor) (*User, error) {
	if err := ownChange(actor, "change how they sign in"); err != nil {
		return nil, err
	}
	return s.User(ctx, actor.Person())
}

// LinkMine links the identity a password provider checks the credentials
// of to the actor's own user, so it signs them in from then on.
func (s *Service) LinkMine(ctx context.Context, actor *auth.Actor, id store.ID, in LinkInput, client auth.Client) (LinkView, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return LinkView{}, err
	}
	u, err := s.linking(ctx, actor)
	if err != nil {
		return LinkView{}, err
	}
	p, err := s.provider(ctx, id, "identity")
	if err != nil {
		return LinkView{}, err
	}
	ip, err := s.openIdentity(ctx, p)
	if err != nil {
		return LinkView{}, err
	}
	pp, ok := ip.(auth.PasswordProvider)
	if !ok || ip.Method() != auth.MethodPassword {
		return LinkView{}, i18n.ErrBadRequest.New("reason", p.Spec.Title+" signs people in on its own page; start linking it there")
	}
	identity, err := pp.CheckPassword(ctx, in.Username, in.Password)
	if auth.ErrBadCredentials.Is(err) {
		s.audit(ctx, actor, "core.user.link", u.ID, u.Home, events.OutcomeFailed, "wrong username or password at "+p.Spec.Title, "Link an identity at "+p.Spec.Title)
		s.failed(ctx, u, client)
		return LinkView{}, err
	}
	if err != nil {
		return LinkView{}, auth.ErrProviderFailed.Wrap(err, "provider", p.Spec.Title, "reason", "it couldn't check the password")
	}
	if err := s.linkChecked(ctx, actor, u, p, identity); err != nil {
		return LinkView{}, err
	}
	return LinkView{Provider: p.ID, Title: p.Spec.Title, Username: identity.Username, LinkedAt: s.now()}, nil
}

// BeginLinkMine starts linking an identity at a provider with its own
// page to the actor's user: the browser goes there, and back to callback.
func (s *Service) BeginLinkMine(ctx context.Context, actor *auth.Actor, id store.ID, callback string) (redirect, state string, err error) {
	u, err := s.linking(ctx, actor)
	if err != nil {
		return "", "", err
	}
	return s.beginRedirect(ctx, id, callback, &challenge{user: u.ID, purpose: "provider-link"})
}

// LinkView is an outside identity linked to the actor's user.
type LinkView struct {
	Provider store.ID  `json:"provider" doc:"The sign-in provider"`
	Title    string    `json:"title" doc:"What the provider is called"`
	Username string    `json:"username,omitempty" doc:"Who you are at the provider"`
	LinkedAt time.Time `json:"linkedAt" doc:"When your user was linked to it"`
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
	u, err := s.linking(ctx, actor)
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
	ID      store.ID `json:"id" doc:"The second step, as setting it up names it"`
	Title   string   `json:"title" doc:"What it is called"`
	Enabled bool     `json:"enabled" doc:"Whether the person asking has it on"`
}

// openMFA connects a second step an add-on or a module provides.
func (s *Service) openMFA(ctx context.Context, p *store.Object[ProviderSpec, ProviderStatus]) (auth.MFAMethod, error) {
	v, err := s.openProvider(ctx, hooks.AuthMFA, p)
	if err != nil {
		return nil, err
	}
	switch m := v.(type) {
	case auth.MFAMethod:
		return m, nil
	case auth.Caller:
		return auth.RemoteMFAMethod(m), nil
	}
	return nil, fmt.Errorf("identity: the second step %s gave %T, which checks nobody", p.Spec.Provider, v)
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

// BeginMFA starts a check with an add-on's second step, to turn it on for
// the actor. It returns what to ask the person, and the ceremony that
// finishes it.
func (s *Service) BeginMFA(ctx context.Context, actor *auth.Actor, id store.ID) (string, *auth.MFAChallenge, error) {
	if err := ownChange(actor, "change their second steps"); err != nil {
		return "", nil, err
	}
	u, err := s.User(ctx, actor.Person())
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
	if err := ownChange(actor, "change their second steps"); err != nil {
		return err
	}
	c, err := s.challenges.get(ceremony, "mfa-enroll")
	if err != nil {
		return err
	}
	if c.user != actor.Person() {
		return auth.ErrChallengeExpired.New()
	}
	u, err := s.User(ctx, c.user)
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
	if !s.challenges.consume(ceremony) {
		return auth.ErrChallengeExpired.New()
	}
	if err := s.opts.Store.EnableMFA(ctx, &MFAEnrollment{User: u.ID, Method: string(p.ID), EnabledAt: s.now()}); err != nil {
		return err
	}
	s.refresh(u.ID, "two-factor")
	if err := s.clearPending(ctx, actor, PendingTwoFactor, u); err != nil {
		return err
	}
	s.audit(ctx, actor, "core.user.enable_mfa", u.ID, u.Home, events.OutcomeSuccess, "", "Turn on "+p.Spec.Title)
	s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+" turned on "+p.Spec.Title+".", UserEvent{Username: u.Username, Detail: p.Spec.Title + " on"})
	return nil
}

// DisableMFA turns an add-on's second step off for the actor.
func (s *Service) DisableMFA(ctx context.Context, actor *auth.Actor, id store.ID) error {
	if err := ownChange(actor, "change their second steps"); err != nil {
		return err
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	if err := s.opts.Store.DisableMFA(ctx, u.ID, string(id)); err != nil {
		return err
	}
	s.refresh(u.ID, "two-factor")
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
	u, err := s.User(ctx, c.user)
	if err != nil {
		return nil, auth.ErrChallengeExpired.New()
	}
	enabled, err := s.opts.Store.MFAMethods(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(enabled, func(e *MFAEnrollment) bool { return e.Method == string(id) }) {
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
	if err := s.challenges.setData(challengeSecret, "signin", state); err != nil {
		return nil, err
	}
	return ch, nil
}

type mfaState struct {
	Method string `json:"method"`
	State  []byte `json:"state"`
}

// verifyMFA checks the response to an add-on's second step started with
// BeginSecondStepMFA. The check stays open while the person hasn't
// answered, for the screen to ask again.
func (s *Service) verifyMFA(ctx context.Context, u *User, secret, method, response string) error {
	c, err := s.challenges.get(secret, "signin")
	if err != nil {
		return err
	}
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
	return nil
}
