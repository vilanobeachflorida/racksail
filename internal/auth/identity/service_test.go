package identity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// clock is a time the tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	t         *testing.T
	svc       *Service
	store     *Memory
	resources store.Store
	log       *events.Log
	hooks     *hooks.Registry
	owners    *account.Ownership
	clock     *clock
	vault     secrets.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{account.Kind, PolicyKind, ProviderKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	resources := store.NewMemory(kinds)
	owners := account.NewOwnership(resources, nil)
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	vault := secrets.NewMemory(box)
	log := events.NewMemory(1000)
	reg := hooks.NewRegistry()
	c := &clock{t: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)}
	st := NewMemory()
	svc := New(Options{
		Store: st, Resources: resources, Secrets: vault, Events: log, Audit: log,
		Hooks: &hooks.Runner{Registry: reg, Events: log}, Authorizer: auth.NewRoleAuthorizer(owners),
		Hostname: "panel.example.test", Log: slog.New(slog.DiscardHandler), Now: c.now,
	})
	return &harness{t: t, svc: svc, store: st, resources: resources, log: log, hooks: reg, owners: owners, clock: c, vault: vault}
}

var client = auth.Client{IP: "198.51.100.7", UserAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"}

const password = "tangerine kettle orbit"

// user adds a user straight to the store, with a password.
func (h *harness) user(name string, home store.Scope, grants ...auth.Grant) *User {
	h.t.Helper()
	ctx := context.Background()
	if grants == nil {
		grants = []auth.Grant{}
	}
	u := &User{ID: store.NewID(PrefixUser), Username: name, Name: strings.ToUpper(name[:1]) + name[1:], Home: home, Grants: grants, CreatedAt: h.clock.now(), UpdatedAt: h.clock.now()}
	if err := h.store.CreateUser(ctx, u); err != nil {
		h.t.Fatal(err)
	}
	hash, err := auth.HashPassword(ctx, password)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.SetPassword(ctx, u.ID, hash, false, h.clock.now()); err != nil {
		h.t.Fatal(err)
	}
	u.PasswordHash = hash
	return u
}

func (h *harness) admin(name string) *User {
	return h.user(name, store.ServerScope(), auth.Grant{Role: auth.RoleAdmin, Scope: store.ServerScope(), Permissions: []auth.Permission{auth.PermImpersonate}})
}

// signIn signs in with the password and returns the session's actor.
func (h *harness) signIn(name string) (*auth.Actor, string) {
	h.t.Helper()
	res, err := h.svc.SignIn(context.Background(), SignInInput{Username: name, Password: password}, "", client)
	if err != nil {
		h.t.Fatalf("signing in as %s: %v", name, err)
	}
	if res.Issued == nil {
		h.t.Fatalf("signing in as %s needs a second step: %v", name, res.Methods)
	}
	return h.actor(res.Issued.Secret), res.Issued.Secret
}

// actor authenticates a request carrying the session cookie.
func (h *harness) actor(secret string) *auth.Actor {
	h.t.Helper()
	a, err := h.authenticate(secret)
	if err != nil || a == nil {
		h.t.Fatalf("authenticating = %v, %v", a, err)
	}
	return a
}

func (h *harness) authenticate(secret string) (*auth.Actor, error) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: secret})
	ctx := auth.WithClient(context.Background(), client)
	return h.svc.Authenticate(ctx, r)
}

func (h *harness) bearer(secret string, ip string) (*auth.Actor, error) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("User-Agent", "racksail/1.0.0")
	ctx := auth.WithClient(context.Background(), auth.Client{IP: ip})
	return h.svc.Authenticate(ctx, r)
}

func (h *harness) audit(action string) []events.AuditRecord {
	h.t.Helper()
	records, _, err := h.log.QueryAudit(context.Background(), events.Query{Types: []string{action}, Limit: 1000})
	if err != nil {
		h.t.Fatal(err)
	}
	return records
}

func (h *harness) eventTypes() map[string]int {
	list, _, _ := h.log.QueryEvents(context.Background(), events.Query{Limit: 1000})
	out := map[string]int{}
	for _, e := range list {
		out[e.Type]++
	}
	return out
}

func TestSetupCode(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	if need, _ := h.svc.SetupRequired(ctx); !need {
		t.Fatal("an empty panel needs its first admin")
	}
	code, err := h.svc.Bootstrap(ctx, filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || len(code) != 23 {
		t.Fatalf("code = %q, %v", code, err)
	}
	in := SetupInput{Code: "WRONG-CODE", Username: "Ada", Name: "Ada Lovelace", Password: password}
	if _, err := h.svc.Setup(ctx, in, client); !auth.ErrBadSetupCode.Is(err) {
		t.Errorf("a wrong code = %v", err)
	}
	in.Code = strings.ToLower(code)
	in.Password = "short"
	if _, err := h.svc.Setup(ctx, in, client); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a weak password = %v", err)
	}
	in.Password = password
	issued, err := h.svc.Setup(ctx, in, client)
	if err != nil {
		t.Fatal(err)
	}
	actor := h.actor(issued.Secret)
	if actor.Name != "ada" || len(actor.Grants) != 1 || actor.Grants[0].Role != auth.RoleAdmin || actor.Grants[0].Permissions[0] != auth.PermImpersonate {
		t.Errorf("first admin = %+v", actor)
	}
	if _, err := h.svc.Setup(ctx, in, client); !auth.ErrSetupDone.Is(err) {
		t.Errorf("a second setup = %v", err)
	}
	if code, _ := h.svc.Bootstrap(ctx, ""); code != "" {
		t.Errorf("a panel with users gets no code: %q", code)
	}
	if left, _ := h.store.SetupCode(ctx); left != "" {
		t.Error("the code is gone once used")
	}
}

func TestInstallerAdmin(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	file := filepath.Join(t.TempDir(), "bootstrap.json")
	acct := InstallerAccount{
		Username: "root2", PasswordHash: "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1",
		SSHKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7 admin@laptop"},
	}
	raw, _ := json.Marshal(acct)
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	code, err := h.svc.Bootstrap(ctx, file)
	if err != nil || code != "" {
		t.Fatalf("bootstrap = %q, %v", code, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("the installer's account file is removed once used")
	}
	res, err := h.svc.SignIn(ctx, SignInInput{Username: "root2", Password: "Hello world!"}, "", client)
	if err != nil || res.Issued == nil {
		t.Fatalf("signing in with the installer's password = %+v, %v", res, err)
	}
	u, _ := h.store.UserByName(ctx, "root2")
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Errorf("the crypt hash is replaced after the first sign-in: %s", u.PasswordHash)
	}
	keys, _ := h.store.SSHKeys(ctx, u.ID)
	if len(keys) != 1 || keys[0].Name != "admin@laptop" {
		t.Errorf("keys = %+v", keys)
	}
}

func TestPasswordSignIn(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	acct := store.NewID("acct")
	owner := h.user("bob", store.AccountScope(acct), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(acct)})

	actor, secret := h.signIn("BOB")
	if actor.ID != owner.ID || actor.Type != auth.ActorUser || actor.Source != auth.SourceUI || actor.IP != client.IP || actor.Grants[0].Role != auth.RoleOwner {
		t.Errorf("actor = %+v", actor)
	}
	if !strings.HasPrefix(secret, auth.SessionPrefix) {
		t.Errorf("secret = %s", secret)
	}
	if len(h.audit("auth.sign_in")) != 1 || h.eventTypes()["core.user.signed_in"] != 1 {
		t.Error("a sign-in is audited and published")
	}

	// Wrong passwords and unknown names look the same.
	_, wrong := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: "not it at all"}, "", client)
	_, nobody := h.svc.SignIn(ctx, SignInInput{Username: "nobody", Password: password}, "", client)
	if !auth.ErrBadCredentials.Is(wrong) || !auth.ErrBadCredentials.Is(nobody) {
		t.Errorf("wrong = %v, nobody = %v", wrong, nobody)
	}

	// Enough failures lock the user out, even with the right password.
	for range DefaultPolicy().LockoutAttempts - 1 {
		h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: "still not it"}, "", client)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: password}, "", client); !auth.ErrLocked.Is(err) {
		t.Fatalf("locked = %v", err)
	}
	if h.eventTypes()["core.user.locked_out"] != 1 {
		t.Error("a lockout is published")
	}
	h.clock.add(16 * time.Minute)
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: password}, "", client); err != nil {
		t.Errorf("after the lockout = %v", err)
	}

	// A user turned off can't sign in, and loses their sessions.
	u, _ := h.store.User(ctx, owner.ID)
	u.Disabled = true
	h.store.UpdateUser(ctx, u, 0)
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: password}, "", client); !auth.ErrDisabled.Is(err) {
		t.Errorf("turned off = %v", err)
	}
	if _, err := h.authenticate(secret); !auth.ErrDisabled.Is(err) {
		t.Errorf("a turned off user's session = %v", err)
	}
}

func TestSignInLimits(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	var limited error
	for range attemptBurst + 1 {
		_, err := h.svc.SignIn(ctx, SignInInput{Username: "ghost", Password: "nope nope nope"}, "", client)
		if i18n.ErrRateLimited.Is(err) {
			limited = err
		}
	}
	if limited == nil {
		t.Error("an address trying too often is slowed down")
	}
	other := auth.Client{IP: "203.0.113.5"}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", other); err != nil {
		t.Errorf("other addresses aren't: %v", err)
	}

	// The policy's allowlist keeps everyone else out, but never the server
	// itself.
	h.svc.SetPolicy(PolicySpec{AllowFrom: []string{"192.0.2.0/24"}})
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", auth.Client{IP: "203.0.113.9"}); !auth.ErrAddressNotAllowed.Is(err) {
		t.Errorf("outside the allowlist = %v", err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", auth.Client{IP: "192.0.2.4"}); err != nil {
		t.Errorf("inside = %v", err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", auth.Client{IP: "127.0.0.1"}); err != nil {
		t.Errorf("from the server = %v", err)
	}
}

func TestLoginPolicies(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	err := h.hooks.Register(hooks.Registration{
		Point: hooks.AuthLoginValidate, Kind: hooks.Validate, Owner: "module:security", Security: true,
		OnValidate: func(ctx context.Context, c *hooks.Context) error {
			if a, ok := c.Data.(LoginAttempt); ok && a.IP == "203.0.113.66" {
				return auth.ErrAddressNotAllowed.New("address", a.IP, "what", "admins")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", auth.Client{IP: "203.0.113.66"}); !errors.Is(err, hooks.ErrDenied) {
		t.Errorf("refused by a login policy = %v", err)
	}
	if denied := h.audit("auth.sign_in"); len(denied) != 1 || denied[0].Outcome != events.OutcomeDenied {
		t.Errorf("audit = %+v", denied)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", client); err != nil {
		t.Errorf("elsewhere = %v", err)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	actor, secret := h.signIn("ada")
	_, other := h.signIn("ada")

	list, err := h.svc.MySessions(ctx, actor)
	if err != nil || len(list) != 2 {
		t.Fatalf("sessions = %+v, %v", list, err)
	}
	if !list[0].Current && !list[1].Current {
		t.Error("the current session is marked")
	}
	if list[0].Device != "Firefox on Linux" {
		t.Errorf("device = %s", list[0].Device)
	}

	// Idle sessions end.
	h.clock.add(59 * time.Minute)
	h.actor(secret)
	h.clock.add(30 * time.Minute)
	if _, err := h.authenticate(other); !auth.ErrSessionExpired.Is(err) {
		t.Errorf("an idle session = %v", err)
	}
	// So does one that lasted long enough, however active.
	for range 13 {
		h.clock.add(55 * time.Minute)
		if _, err := h.authenticate(secret); err != nil {
			if !auth.ErrSessionExpired.Is(err) {
				t.Fatal(err)
			}
			break
		}
	}
	if _, err := h.authenticate(secret); !auth.ErrSessionExpired.Is(err) {
		t.Errorf("a session past its lifetime = %v", err)
	}

	actor, secret = h.signIn("ada")
	_, other = h.signIn("ada")
	if n, err := h.svc.EndOtherSessions(ctx, actor); err != nil || n != 1 {
		t.Errorf("ending the others = %d, %v", n, err)
	}
	if _, err := h.authenticate(other); !auth.ErrSessionExpired.Is(err) {
		t.Error("the other session ended")
	}
	if err := h.svc.SignOut(ctx, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authenticate(secret); !auth.ErrSessionExpired.Is(err) {
		t.Error("signing out ends the session")
	}
	if a, err := h.authenticate("not-a-session"); err == nil || a != nil {
		t.Error("a made-up cookie signs nobody in")
	}
}

func TestStepUp(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	actor, secret := h.signIn("ada")
	if err := h.svc.RequireStepUp(actor); !auth.ErrStepUpRequired.Is(err) {
		t.Fatalf("before = %v", err)
	}
	if _, err := h.svc.StepUp(ctx, actor, StepUpInput{Password: "wrong one here"}, client, ""); !auth.ErrWrongPassword.Is(err) {
		t.Errorf("a wrong password = %v", err)
	}
	issued, err := h.svc.StepUp(ctx, actor, StepUpInput{Password: password}, client, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authenticate(secret); !auth.ErrSessionExpired.Is(err) {
		t.Error("stepping up gives the session a new secret")
	}
	fresh := h.actor(issued.Secret)
	if err := h.svc.RequireStepUp(fresh); err != nil {
		t.Errorf("after = %v", err)
	}
	h.clock.add(11 * time.Minute)
	if err := h.svc.RequireStepUp(h.actor(issued.Secret)); !auth.ErrStepUpRequired.Is(err) {
		t.Errorf("a confirmation lasts the policy's minutes: %v", err)
	}
	token := &auth.Actor{Type: auth.ActorToken, ID: store.NewID(PrefixToken)}
	if err := h.svc.RequireStepUp(token); !auth.ErrStepUpUnavailable.Is(err) {
		t.Errorf("tokens = %v", err)
	}
	if methods := h.svc.StepUpMethods(ctx, fresh); len(methods) != 1 || methods[0] != "password" {
		t.Errorf("methods = %v", methods)
	}
}

func TestTOTPAndRecovery(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	actor, _ := h.signIn("ada")

	key, err := h.svc.BeginTOTP(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key.URI, "ada%40panel.example.test") && !strings.Contains(key.URI, "ada@panel.example.test") {
		t.Errorf("uri = %s", key.URI)
	}
	if _, err := h.svc.ConfirmTOTP(ctx, actor, "000000"); !auth.ErrBadCode.Is(err) {
		t.Errorf("a wrong code = %v", err)
	}
	code, _ := auth.TOTPCode(key.Secret, h.clock.now())
	codes, err := h.svc.ConfirmTOTP(ctx, actor, code)
	if err != nil || len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("confirm = %v, %v", codes, err)
	}

	// Signing in now takes a second step.
	h.clock.add(time.Minute)
	res, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", client)
	if err != nil || res.Issued != nil || res.Challenge == "" {
		t.Fatalf("first step = %+v, %v", res, err)
	}
	if strings.Join(res.Methods, ",") != "totp,recovery" {
		t.Errorf("methods = %v", res.Methods)
	}
	code, _ = auth.TOTPCode(key.Secret, h.clock.now())
	done, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: res.Challenge, Method: "totp", Code: code, Trust: true}, client, "")
	if err != nil || done.Issued == nil || done.Issued.DeviceSecret == "" {
		t.Fatalf("second step = %+v, %v", done, err)
	}
	if _, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: res.Challenge, Method: "totp", Code: code}, client, ""); !auth.ErrChallengeExpired.Is(err) {
		t.Errorf("a challenge counts once: %v", err)
	}

	// The same code can't sign in twice.
	again, _ := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", client)
	if _, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: again.Challenge, Method: "totp", Code: code}, client, ""); !auth.ErrBadCode.Is(err) {
		t.Errorf("a code used twice = %v", err)
	}
	// A recovery code works once, and says so.
	if _, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: again.Challenge, Method: "recovery", Code: strings.ToLower(codes[0])}, client, ""); err != nil {
		t.Fatalf("recovery code = %v", err)
	}
	third, _ := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", client)
	if _, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: third.Challenge, Method: "recovery", Code: codes[0]}, client, ""); !auth.ErrBadCode.Is(err) {
		t.Errorf("a recovery code used twice = %v", err)
	}
	if h.eventTypes()["core.user.recovery_code_used"] != 1 {
		t.Error("using a recovery code is published")
	}

	// The trusted browser skips the second step.
	skip, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, done.Issued.DeviceSecret, client)
	if err != nil || skip.Issued == nil || skip.Issued.Session.SecondFactor != "device" {
		t.Fatalf("trusted browser = %+v, %v", skip, err)
	}
	me := h.actor(skip.Issued.Secret)
	devices, _ := h.svc.MyDevices(ctx, me, done.Issued.DeviceSecret)
	if len(devices) != 1 || !devices[0].Current {
		t.Errorf("devices = %+v", devices)
	}
	if err := h.svc.ForgetDevice(ctx, me, ""); err != nil {
		t.Fatal(err)
	}
	if res, _ := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, done.Issued.DeviceSecret, client); res == nil || res.Issued != nil {
		t.Error("a forgotten browser takes the second step again")
	}

	// Removing the app takes the second step, and the codes, away.
	if err := h.svc.RemoveTOTP(ctx, me); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.store.RecoveryCodesLeft(ctx, me.ID); n != 0 {
		t.Errorf("recovery codes left = %d", n)
	}
	if res, _ := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", client); res == nil || res.Issued == nil {
		t.Error("without a second step, the password is enough")
	}
}

func TestPendingSteps(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	acct := store.NewID("acct")
	h.user("bob", store.AccountScope(acct), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(acct)})
	h.svc.SetPolicy(PolicySpec{RequireTwoFactor: []auth.Role{auth.RoleOwner}})
	actor, secret := h.signIn("bob")
	if actor.Pending != PendingTwoFactor {
		t.Fatalf("pending = %q", actor.Pending)
	}
	key, err := h.svc.BeginTOTP(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := auth.TOTPCode(key.Secret, h.clock.now())
	if _, err := h.svc.ConfirmTOTP(ctx, actor, code); err != nil {
		t.Fatal(err)
	}
	if a := h.actor(secret); a.Pending != "" {
		t.Errorf("after setting up a second step = %q", a.Pending)
	}

	// A user who must choose a new password does that first.
	u, _ := h.store.UserByName(ctx, "bob")
	h.store.SetPassword(ctx, u.ID, u.PasswordHash, true, h.clock.now())
	res, _ := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: password}, "", client)
	code, _ = auth.TOTPCode(key.Secret, h.clock.now().Add(30*time.Second))
	h.clock.add(30 * time.Second)
	done, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: res.Challenge, Method: "totp", Code: code}, client, "")
	if err != nil {
		t.Fatal(err)
	}
	a := h.actor(done.Issued.Secret)
	if a.Pending != PendingPassword {
		t.Fatalf("pending = %q", a.Pending)
	}
	if err := h.svc.ChangePassword(ctx, a, PasswordInput{New: "another long passphrase"}); err != nil {
		t.Fatal(err)
	}
	if a := h.actor(done.Issued.Secret); a.Pending != "" {
		t.Errorf("after choosing a password = %q", a.Pending)
	}
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	actor, _ := h.signIn("ada")
	_, other := h.signIn("ada")
	if err := h.svc.ChangePassword(ctx, actor, PasswordInput{Current: "wrong", New: "another long passphrase"}); !auth.ErrWrongPassword.Is(err) {
		t.Errorf("wrong current = %v", err)
	}
	if err := h.svc.ChangePassword(ctx, actor, PasswordInput{Current: password, New: "short"}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("weak = %v", err)
	}
	if err := h.svc.ChangePassword(ctx, actor, PasswordInput{Current: password, New: "another long passphrase", EndOtherSessions: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authenticate(other); !auth.ErrSessionExpired.Is(err) {
		t.Error("the other sessions ended")
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: "another long passphrase"}, "", client); err != nil {
		t.Errorf("the new password = %v", err)
	}
}
