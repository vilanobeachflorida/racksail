package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// ldap checks passwords the way an LDAP add-on would.
type ldap struct{ secret string }

func (ldap) Method() auth.ProviderMethod { return auth.MethodPassword }

func (d ldap) CheckPassword(ctx context.Context, username, password string) (*auth.ExternalIdentity, error) {
	if password != "directory pass "+d.secret {
		return nil, auth.ErrBadCredentials.New()
	}
	return &auth.ExternalIdentity{Subject: "uid=" + username, Username: username, Email: username + "@corp.example", Name: "From the directory"}, nil
}

// redirector signs people in on its own page, as OpenID Connect does.
type redirector struct{}

func (redirector) Method() auth.ProviderMethod { return auth.MethodRedirect }

func (redirector) Begin(ctx context.Context, callback string) (string, []byte, error) {
	return "https://idp.example/authorize?redirect_uri=" + url.QueryEscape(callback), []byte("nonce-1"), nil
}

func (redirector) Complete(ctx context.Context, state []byte, callback string, q url.Values) (*auth.ExternalIdentity, error) {
	if string(state) != "nonce-1" || q.Get("code") != "good" {
		return nil, auth.ErrBadCredentials.New()
	}
	return &auth.ExternalIdentity{Subject: "sub-42", Email: "ada@example.test"}, nil
}

// push is a second step an add-on adds: approve on a phone.
type push struct {
	mu       sync.Mutex
	approved bool
}

func (p *push) Challenge(ctx context.Context, u auth.MFAUser) (*auth.MFAChallenge, error) {
	return &auth.MFAChallenge{Prompt: "Approve the sign-in on your phone", State: []byte("push:" + u.Username)}, nil
}

func (p *push) Verify(ctx context.Context, u auth.MFAUser, state []byte, response string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if string(state) != "push:"+u.Username {
		return auth.ErrBadCode.New()
	}
	if !p.approved {
		return auth.ErrPending.New()
	}
	return nil
}

func TestPasswordProviders(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	h.provide(hooks.AuthProvider, "ldap", func(s json.RawMessage) (any, error) {
		var settings struct {
			Secret string `json:"secret"`
		}
		json.Unmarshal(s, &settings)
		return ldap{secret: settings.Secret}, nil
	})
	if err := h.vault.Put(ctx, "signprov_01k7aaaaaaaaaaaaaaaaaaaaaa/secret", []byte("x")); err != nil {
		t.Fatal(err)
	}
	h.connect(ProviderSpec{
		Capability: "identity", Provider: "ldap", Title: "Company directory", Secret: "signprov_01k7aaaaaaaaaaaaaaaaaaaaaa/secret",
		Match: "username", NewUsers: true, NewUserGrants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}},
	})
	options, err := h.svc.SignInOptions(ctx)
	if err != nil || len(options) != 1 || options[0].Method != auth.MethodPassword || options[0].Title != "Company directory" {
		t.Fatalf("options = %+v, %v", options, err)
	}

	// Someone the directory knows is added on their first sign-in.
	res, err := h.svc.SignIn(ctx, SignInInput{Username: "carol", Password: "directory pass x"}, "", client)
	if err != nil || res.Issued == nil {
		t.Fatalf("directory sign-in = %+v, %v", res, err)
	}
	carol := h.actor(res.Issued.Secret)
	if carol.Name != "carol" || carol.Grants[0].Scope != store.AccountScope(shop) || res.Issued.Session.Method != "provider:ldap" {
		t.Errorf("carol = %+v", carol)
	}
	links, _ := h.svc.MyLinks(ctx, carol)
	if len(links) != 1 || links[0].Title != "Company directory" {
		t.Errorf("links = %+v", links)
	}
	if err := h.svc.UnlinkMine(ctx, carol, links[0].Provider); !auth.ErrLastSignInMethod.Is(err) {
		t.Errorf("unlinking the only way in = %v", err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "carol", Password: "wrong"}, "", client); !auth.ErrBadCredentials.Is(err) {
		t.Errorf("a wrong directory password = %v", err)
	}

	// A local user with the same name signs in with their own password,
	// not the directory's.
	h.admin("dave")
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "dave", Password: password}, "", client); err != nil {
		t.Errorf("a local password = %v", err)
	}
}

func TestRedirectProviders(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.provide(hooks.AuthProvider, "oidc", func(json.RawMessage) (any, error) { return redirector{}, nil })
	id := h.connect(ProviderSpec{Capability: "identity", Provider: "oidc", Title: "Single sign-on", Match: "email"})
	h.admin("root")
	shop := h.createAccount("shop", "")
	ada := h.user("ada", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	if _, err := h.update(root(), ada.ID, func(s *UserSpec) { s.Email = "ada@example.test" }); err != nil {
		t.Fatal(err)
	}

	callback := "https://panel.example.test/api/v1/auth/providers/" + string(id) + "/callback"
	to, state, err := h.svc.BeginProviderSignIn(ctx, id, callback, true, "", client)
	if err != nil || !strings.HasPrefix(to, "https://idp.example/authorize") || state == "" {
		t.Fatalf("begin = %q %q %v", to, state, err)
	}
	if _, err := h.svc.CompleteProviderSignIn(ctx, "made-up", callback, url.Values{"code": {"good"}}, "", client); !auth.ErrChallengeExpired.Is(err) {
		t.Errorf("a made-up state = %v", err)
	}
	res, err := h.svc.CompleteProviderSignIn(ctx, state, callback, url.Values{"code": {"good"}}, "", client)
	if err != nil || res.Issued == nil || !res.Issued.Session.Persistent {
		t.Fatalf("complete = %+v, %v", res, err)
	}
	if a := h.actor(res.Issued.Secret); a.ID != ada.ID {
		t.Errorf("signed in as %s, matched by email", a.Name)
	}
	if _, err := h.svc.CompleteProviderSignIn(ctx, state, callback, url.Values{"code": {"good"}}, "", client); !auth.ErrChallengeExpired.Is(err) {
		t.Errorf("a state counts once: %v", err)
	}
}

// A browser trusted to skip the second step skips it after a provider's
// page too. The way back from the provider's site doesn't carry the
// browser's SameSite=Strict cookie, but the start, from the panel's own
// page, does.
func TestRedirectProvidersKeepTrustedBrowsers(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.provide(hooks.AuthProvider, "oidc", func(json.RawMessage) (any, error) { return redirector{}, nil })
	id := h.connect(ProviderSpec{Capability: "identity", Provider: "oidc", Title: "Single sign-on", Match: "email"})
	h.admin("root")
	shop := h.createAccount("shop", "")
	ada := h.user("ada", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	if _, err := h.update(root(), ada.ID, func(s *UserSpec) { s.Email = "ada@example.test" }); err != nil {
		t.Fatal(err)
	}
	actor, _ := h.signIn("ada")
	key, err := h.svc.BeginTOTP(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := auth.TOTPCode(key.Secret, h.clock.now())
	if _, err := h.svc.ConfirmTOTP(ctx, actor, code); err != nil {
		t.Fatal(err)
	}
	h.clock.add(time.Minute)
	first, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", client)
	if err != nil {
		t.Fatal(err)
	}
	code, _ = auth.TOTPCode(key.Secret, h.clock.now())
	trusted, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: first.Challenge, Method: "totp", Code: code, Trust: true}, client, "")
	if err != nil || trusted.Issued.DeviceSecret == "" {
		t.Fatalf("trusting the browser = %+v, %v", trusted, err)
	}

	callback := "https://panel.example.test/api/v1/auth/providers/" + string(id) + "/callback"
	_, state, err := h.svc.BeginProviderSignIn(ctx, id, callback, false, "", client)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.svc.CompleteProviderSignIn(ctx, state, callback, url.Values{"code": {"good"}}, "", client)
	if err != nil || res.Issued != nil || res.Challenge == "" {
		t.Errorf("a browser nobody trusts = %+v, %v", res, err)
	}
	_, state, err = h.svc.BeginProviderSignIn(ctx, id, callback, false, trusted.Issued.DeviceSecret, client)
	if err != nil {
		t.Fatal(err)
	}
	res, err = h.svc.CompleteProviderSignIn(ctx, state, callback, url.Values{"code": {"good"}}, "", client)
	if err != nil || res.Issued == nil || res.Issued.Session.SecondFactor != "device" {
		t.Errorf("the trusted browser = %+v, %v", res, err)
	}
}

func TestMFAProviders(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p := &push{}
	h.provide(hooks.AuthMFA, "push", func(json.RawMessage) (any, error) { return p, nil })
	id := h.connect(ProviderSpec{Capability: "mfa", Provider: "push", Title: "Phone approval"})
	h.admin("ada")
	actor, _ := h.signIn("ada")

	options, err := h.svc.MFAOptions(ctx, actor)
	if err != nil || len(options) != 1 || options[0].Enabled {
		t.Fatalf("options = %+v, %v", options, err)
	}
	ceremony, ch, err := h.svc.BeginMFA(ctx, actor, id)
	if err != nil || ch.Prompt == "" {
		t.Fatalf("begin = %+v, %v", ch, err)
	}
	if err := h.svc.FinishMFA(ctx, actor, ceremony, ""); !auth.ErrPending.Is(err) {
		t.Errorf("before approving = %v", err)
	}
	p.approved = true
	if err := h.svc.FinishMFA(ctx, actor, ceremony, ""); err != nil {
		t.Fatal(err)
	}

	// Signing in now offers it as a second step.
	p.approved = false
	first, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", client)
	if err != nil || first.Challenge == "" || first.Methods[0] != "mfa:"+string(id) {
		t.Fatalf("first step = %+v, %v", first, err)
	}
	if first.Titles["mfa:"+string(id)] != "Phone approval" {
		t.Errorf("titles = %v", first.Titles)
	}
	if _, err := h.svc.BeginSecondStepMFA(ctx, first.Challenge, id); err != nil {
		t.Fatal(err)
	}
	step := SecondStepInput{Challenge: first.Challenge, Method: "mfa:" + string(id)}
	if _, err := h.svc.SecondStep(ctx, step, client, ""); !auth.ErrPending.Is(err) {
		t.Errorf("waiting = %v", err)
	}
	p.approved = true
	done, err := h.svc.SecondStep(ctx, step, client, "")
	if err != nil || done.Issued.Session.SecondFactor != "mfa:"+string(id) {
		t.Fatalf("approved = %+v, %v", done, err)
	}
	if err := h.svc.DisableMFA(ctx, h.actor(done.Issued.Secret), id); err != nil {
		t.Fatal(err)
	}
}

// memo is a notification channel that keeps what it was sent.
type memo struct {
	mu   sync.Mutex
	sent []notify.Message
}

func (m *memo) Type() string { return "memo" }

func (m *memo) Send(ctx context.Context, msg notify.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *memo) titles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.sent {
		out = append(out, s.Title)
	}
	return out
}

func TestSecurityAlerts(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	box := &memo{}
	h.provide(hooks.ProvideNotifier, "memo", func(s json.RawMessage) (any, error) {
		var settings struct {
			Room string `json:"room"`
		}
		if err := json.Unmarshal(s, &settings); err != nil || settings.Room == "" {
			return nil, auth.ErrProviderFailed.New("provider", "memo", "reason", "it needs a room")
		}
		return box, nil
	})
	h.admin("ada")
	actor, _ := h.signIn("ada")
	types := h.svc.AlertChannelTypes()
	if len(types) == 0 || types[len(types)-1].ID != "provider:memo" {
		t.Fatalf("types = %+v", types)
	}
	if err := h.svc.SetAlertChannel(ctx, actor, AlertChannelInput{Type: "provider:memo", Settings: json.RawMessage(`{}`)}); err == nil {
		t.Error("settings the channel refuses are refused")
	}
	if err := h.svc.SetAlertChannel(ctx, actor, AlertChannelInput{Type: "provider:memo", Settings: json.RawMessage(`{"room":"security"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.TestAlert(ctx, actor); err != nil {
		t.Fatal(err)
	}

	// A sign-in from a new device and address sends an alert.
	h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", auth.Client{IP: "203.0.113.200", UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Version/18.0 Mobile/15E148 Safari/604.1"})
	waitFor(t, func() bool { return len(box.titles()) >= 2 })
	titles := box.titles()
	if titles[0] != "Security alerts work" || titles[1] != "New sign-in to your account" {
		t.Errorf("alerts = %v", titles)
	}
	if err := h.svc.SetAlertChannel(ctx, actor, AlertChannelInput{}); err != nil {
		t.Fatal(err)
	}
	if me, _ := h.svc.MyProfile(ctx, actor); me.Security.AlertChannel != "" {
		t.Error("alerts turned off")
	}
}

func TestLockoutsCoverProviders(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	h.provide(hooks.AuthProvider, "ldap", func(json.RawMessage) (any, error) { return ldap{secret: "x"}, nil })
	h.connect(ProviderSpec{
		Capability: "identity", Provider: "ldap", Title: "Company directory",
		Match: "username", NewUsers: true, NewUserGrants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}},
	})
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "carol", Password: "directory pass x"}, "", client); err != nil {
		t.Fatal(err)
	}
	// Wrong passwords at the directory count as they would here, and the
	// lockout stops the right one too.
	for range DefaultPolicy().LockoutAttempts {
		if _, err := h.svc.SignIn(ctx, SignInInput{Username: "carol", Password: "guess"}, "", client); !auth.ErrBadCredentials.Is(err) {
			t.Fatalf("a wrong directory password = %v", err)
		}
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "carol", Password: "directory pass x"}, "", client); !auth.ErrLocked.Is(err) {
		t.Errorf("the right directory password while locked out = %v", err)
	}
	if h.eventTypes()["core.user.locked_out"] != 1 {
		t.Errorf("events = %v", h.eventTypes())
	}
	h.clock.add(time.Duration(DefaultPolicy().LockoutMinutes+1) * time.Minute)
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "carol", Password: "directory pass x"}, "", client); err != nil {
		t.Errorf("after the lockout = %v", err)
	}
}

func TestProvidersNeverMatchAdmins(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.provide(hooks.AuthProvider, "ldap", func(json.RawMessage) (any, error) { return ldap{secret: "x"}, nil })
	p := h.connect(ProviderSpec{Capability: "identity", Provider: "ldap", Title: "Company directory", Match: "username"})
	// erin manages the whole server and has no password here; someone who
	// calls themselves erin at the directory doesn't become her.
	erin, err := h.create(root(), store.ServerScope(), UserSpec{Username: "erin", Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "erin", Password: "directory pass x"}, "", client); !auth.ErrLinkByHand.Is(err) {
		t.Errorf("matching an admin by username = %v", err)
	}
	if links, _ := h.store.Links(ctx, erin); len(links) != 0 {
		t.Errorf("an admin was linked: %+v", links)
	}
	if denied := h.audit("auth.sign_in"); len(denied) == 0 || denied[len(denied)-1].Outcome != events.OutcomeDenied {
		t.Errorf("the refusal isn't audited: %+v", denied)
	}

	// Linked by hand from her own settings, the directory signs her in.
	h.admin("root")
	hash, _ := auth.HashPassword(ctx, password)
	h.store.SetPassword(ctx, erin, hash, false, h.clock.now())
	actor, _ := h.signIn("erin")
	if _, err := h.svc.LinkMine(ctx, actor, p, LinkInput{Username: "erin.d", Password: "nope"}, client); !auth.ErrBadCredentials.Is(err) {
		t.Errorf("linking with a wrong directory password = %v", err)
	}
	view, err := h.svc.LinkMine(ctx, actor, p, LinkInput{Username: "erin.d", Password: "directory pass x"}, client)
	if err != nil || view.Username != "erin.d" || view.Title != "Company directory" {
		t.Fatalf("linking = %+v, %v", view, err)
	}
	res, err := h.svc.SignIn(ctx, SignInInput{Username: "erin.d", Password: "directory pass x"}, "", client)
	if err != nil || res.Issued == nil || h.actor(res.Issued.Secret).ID != erin {
		t.Fatalf("signing in with the linked identity = %+v, %v", res, err)
	}
	// An identity signs in as one user only.
	other := h.user("fred", store.ServerScope(), auth.Grant{Role: auth.RoleReadOnly, Scope: store.ServerScope()})
	fred, _ := h.signIn(other.Username)
	if _, err := h.svc.LinkMine(ctx, fred, p, LinkInput{Username: "erin.d", Password: "directory pass x"}, client); !auth.ErrIdentityTaken.Is(err) {
		t.Errorf("linking someone else's identity = %v", err)
	}
	if err := h.svc.UnlinkMine(ctx, actor, p); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "erin.d", Password: "directory pass x"}, "", client); !auth.ErrIdentityUnlinked.Is(err) {
		t.Errorf("after unlinking = %v", err)
	}
}

func TestLinkingThroughARedirect(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.provide(hooks.AuthProvider, "oidc", func(json.RawMessage) (any, error) { return redirector{}, nil })
	id := h.connect(ProviderSpec{Capability: "identity", Provider: "oidc", Title: "Single sign-on"})
	ada := h.admin("ada")
	actor, _ := h.signIn("ada")
	callback := "https://panel.example.test/api/v1/auth/providers/" + string(id) + "/callback"

	// Without a link, the provider signs nobody in.
	to, state, err := h.svc.BeginProviderSignIn(ctx, id, callback, false, "", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CompleteProviderSignIn(ctx, state, callback, url.Values{"code": {"good"}}, "", client); !auth.ErrIdentityUnlinked.Is(err) {
		t.Errorf("an identity linked to nobody = %v (%s)", err, to)
	}

	_, state, err = h.svc.BeginLinkMine(ctx, actor, id, callback)
	if err != nil {
		t.Fatal(err)
	}
	ret, err := h.svc.CompleteProviderSignIn(ctx, state, callback, url.Values{"code": {"good"}}, "", client)
	if err != nil || ret.Linked == nil || ret.Linked.ID != id || ret.SignInResult != nil {
		t.Fatalf("linking = %+v, %v", ret, err)
	}
	_, state, _ = h.svc.BeginProviderSignIn(ctx, id, callback, false, "", client)
	res, err := h.svc.CompleteProviderSignIn(ctx, state, callback, url.Values{"code": {"good"}}, "", client)
	if err != nil || res.Issued == nil || h.actor(res.Issued.Secret).ID != ada.ID {
		t.Fatalf("signing in once linked = %+v, %v", res, err)
	}
	if h.eventTypes()["core.user.identity_linked"] != 1 {
		t.Errorf("events = %v", h.eventTypes())
	}
}

// remote is a provider that runs apart from the panel, as an add-on does:
// what it takes and answers goes through JSON, as over the add-on's socket.
type remote struct {
	mu      sync.Mutex
	methods []string
	answer  func(method string, in json.RawMessage) (string, error)
}

func (r *remote) Call(ctx context.Context, method string, in, out any) error {
	r.mu.Lock()
	r.methods = append(r.methods, method)
	r.mu.Unlock()
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	answer, err := r.answer(method, raw)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(answer), out)
}

func TestProvidersFromAddons(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	directory := &remote{answer: func(method string, in json.RawMessage) (string, error) {
		switch method {
		case auth.MethodDescribe:
			return `{"method":"password"}`, nil
		case auth.MethodCheckPassword:
			var check auth.PasswordCheck
			if err := json.Unmarshal(in, &check); err != nil {
				return "", err
			}
			if check.Password != "from the add-on" {
				return `{"refused":true,"reason":"wrong password"}`, nil
			}
			return `{"identity":{"subject":"guid-1","username":"` + check.Username + `","name":"Hana","email":"hana@corp.example"}}`, nil
		}
		return "", errors.New("no such method")
	}}
	h.provide(hooks.AuthProvider, "ldap-sign-in", func(json.RawMessage) (any, error) { return directory, nil })
	h.connect(ProviderSpec{Capability: "identity", Provider: "ldap-sign-in", Title: "Directory", NewUsers: true,
		NewUserGrants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}}})
	if options, err := h.svc.SignInOptions(ctx); err != nil || len(options) != 1 || options[0].Method != auth.MethodPassword {
		t.Fatalf("options = %+v, %v", options, err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "hana", Password: "wrong"}, "", client); !auth.ErrBadCredentials.Is(err) {
		t.Errorf("a refusal = %v", err)
	}
	res, err := h.svc.SignIn(ctx, SignInInput{Username: "hana", Password: "from the add-on"}, "", client)
	if err != nil || res.Issued == nil {
		t.Fatalf("signing in through the add-on = %+v, %v", res, err)
	}
	if u := h.get(h.actor(res.Issued.Secret).ID); u.Username != "hana" || u.Email != "hana@corp.example" || u.Home != store.AccountScope(shop) {
		t.Errorf("the user the add-on's identity added = %+v", u)
	}

	// A second step from an add-on asks, then hears whether it passed.
	approved := false
	phone := &remote{answer: func(method string, in json.RawMessage) (string, error) {
		var call struct {
			User  auth.MFAUser `json:"user"`
			State []byte       `json:"state"`
		}
		if err := json.Unmarshal(in, &call); err != nil || call.User.Username != "root" {
			return "", errors.New("the call doesn't say who")
		}
		switch method {
		case auth.MethodChallenge:
			return `{"prompt":"Approve it on your phone","input":false,"state":"cGhvbmU="}`, nil
		case auth.MethodVerify:
			switch {
			case string(call.State) != "phone":
				return `{"result":"failed"}`, nil
			case !approved:
				return `{"result":"pending"}`, nil
			}
			return `{"result":"passed"}`, nil
		}
		return "", errors.New("no such method")
	}}
	h.provide(hooks.AuthMFA, "push-approval", func(json.RawMessage) (any, error) { return phone, nil })
	mfa := h.connect(ProviderSpec{Capability: "mfa", Provider: "push-approval", Title: "Phone"})
	h.admin("root")
	actor, _ := h.signIn("root")
	ceremony, ch, err := h.svc.BeginMFA(ctx, actor, mfa)
	if err != nil || ch.Prompt != "Approve it on your phone" || ch.Input {
		t.Fatalf("begin = %+v, %v", ch, err)
	}
	if err := h.svc.FinishMFA(ctx, actor, ceremony, ""); !auth.ErrPending.Is(err) {
		t.Errorf("before approving = %v", err)
	}
	approved = true
	if err := h.svc.FinishMFA(ctx, actor, ceremony, ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(phone.methods, auth.MethodVerify) {
		t.Errorf("calls = %v", phone.methods)
	}
}

// A sign-in provider names what an add-on or a module provides, or the
// change that connects it is refused. One whose add-on goes later says so
// in its status rather than claim it's ready, until the add-on is back.
func TestProvidersNameWhatIsProvided(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	save := func(op changeset.Op, id store.ID, spec ProviderSpec) (*changeset.ChangeSet, error) {
		raw, _ := json.Marshal(spec)
		return h.submit(root(), "Connect "+spec.Title, changeset.Change{Op: op, Kind: ProviderKind.Name(), ID: id, Scope: store.ServerScope(), Spec: raw})
	}
	missing := func(what string, err error, provider string) {
		t.Helper()
		e := i18n.From(err)
		if !auth.ErrProviderMissing.Is(err) || len(e.Fields) != 1 || e.Fields[0].Path != "spec.provider" || !strings.Contains(e.Message, provider) {
			t.Errorf("%s = %v", what, err)
		}
	}
	_, err := save(changeset.OpCreate, "", ProviderSpec{Capability: "identity", Provider: "no-such-provider", Title: "Nobody"})
	missing("a provider nothing provides", err, "no-such-provider")
	h.provide(hooks.AuthProvider, "ldap-sign-in", func(json.RawMessage) (any, error) { return ldap{secret: "x"}, nil })
	_, err = save(changeset.OpCreate, "", ProviderSpec{Capability: "mfa", Provider: "ldap-sign-in", Title: "Directory"})
	missing("a second step that signs people in instead", err, "ldap-sign-in")
	spec := ProviderSpec{Capability: "identity", Provider: "ldap-sign-in", Title: "Directory"}
	cs, err := save(changeset.OpCreate, "", spec)
	if err != nil {
		t.Fatal(err)
	}
	id := cs.Changes[0].ID
	status := func() ProviderStatus {
		t.Helper()
		if err := h.runner.Reconcile(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		p, err := ProviderKind.Get(ctx, h.resources, id)
		if err != nil {
			t.Fatal(err)
		}
		return p.Status
	}
	if st := status(); st.Phase != store.PhaseReady || !st.IsTrue(reconcile.ConditionReady) {
		t.Errorf("a provider an add-on provides = %+v", st)
	}

	// The add-on goes: the provider says what's missing, the sign-in
	// screen leaves it out, and it can't be changed until the add-on is
	// back, only removed.
	var refreshed []store.ID
	h.svc.opts.Refresh = func(id store.ID, reason string) { refreshed = append(refreshed, id) }
	offered := h.svc.followOffers(ctx, "")
	h.hooks.Unregister("addon:ldap-sign-in")
	if offered = h.svc.followOffers(ctx, offered); !slices.Equal(refreshed, []store.ID{id, id}) {
		t.Errorf("reconciled again = %v", refreshed)
	}
	st := status()
	ready, ok := st.Condition(reconcile.ConditionReady)
	if st.Phase != store.PhaseDegraded || !ok || ready.Status != store.ConditionFalse || ready.Reason != "ProviderMissing" ||
		ready.Code != auth.ErrProviderMissing.Code || !strings.Contains(ready.Message, "ldap-sign-in") {
		t.Errorf("a provider whose add-on is gone = %+v", st)
	}
	if options, err := h.svc.SignInOptions(ctx); err != nil || len(options) != 0 {
		t.Errorf("sign-in options = %+v, %v", options, err)
	}
	spec.Title = "Company directory"
	_, err = save(changeset.OpUpdate, id, spec)
	missing("changing it", err, "ldap-sign-in")

	// It's back: following what add-ons provide has it reconciled again.
	refreshed = nil
	h.provide(hooks.AuthProvider, "ldap-sign-in", func(json.RawMessage) (any, error) { return ldap{secret: "x"}, nil })
	if offered = h.svc.followOffers(ctx, offered); !slices.Equal(refreshed, []store.ID{id}) {
		t.Errorf("reconciled again = %v", refreshed)
	}
	if h.svc.followOffers(ctx, offered); len(refreshed) != 1 {
		t.Errorf("nothing changed, yet reconciled again: %v", refreshed)
	}
	if st := status(); st.Phase != store.PhaseReady {
		t.Errorf("once the add-on is back = %+v", st)
	}
	if _, err := save(changeset.OpUpdate, id, spec); err != nil {
		t.Errorf("changing it now = %v", err)
	}
	h.hooks.Unregister("addon:ldap-sign-in")
	if _, err := h.submit(root(), "Remove it", changeset.Change{Op: changeset.OpDelete, Kind: ProviderKind.Name(), ID: id}); err != nil {
		t.Errorf("removing one nothing provides = %v", err)
	}
}
