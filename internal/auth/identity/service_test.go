package identity

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

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
	// The console's copy of the code goes once it is used.
	h.svc.opts.SetupFile = filepath.Join(t.TempDir(), "setup-code")
	if err := os.WriteFile(h.svc.opts.SetupFile, []byte(code+"\n"), 0o600); err != nil {
		t.Fatal(err)
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
	if _, err := os.Stat(h.svc.opts.SetupFile); !os.IsNotExist(err) {
		t.Errorf("the console's copy of the code is still there: %v", err)
	}
	// The first admin is a user like any other, added through the change
	// pipeline, with its record in the audit log.
	created := h.audit("core.user.create")
	if len(created) != 1 || created[0].Actor.Name != "setup" || created[0].Subject != actor.ID {
		t.Errorf("the first admin's audit record = %+v", created)
	}
	if st := h.status(actor.ID); !st.PasswordSet || st.LastSignInAt == nil {
		t.Errorf("the first admin's status = %+v", st)
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
	u, err := h.svc.userByName(ctx, "root2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Errorf("the crypt hash is replaced after the first sign-in: %s", u.PasswordHash)
	}
	if len(u.SSHKeys) != 1 || u.SSHKeys[0].Name != "admin@laptop" || !u.Admin() {
		t.Errorf("the installer's admin = %+v", u)
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
	actor, secret = h.signIn("bob")
	if _, err := h.update(root(), owner.ID, func(s *UserSpec) { s.Disabled = true }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: password}, "", client); !auth.ErrDisabled.Is(err) {
		t.Errorf("turned off = %v", err)
	}
	if _, err := h.authenticate(secret); !auth.ErrSessionExpired.Is(err) {
		t.Errorf("a turned off user's session = %v", err)
	}
	if list, _ := h.store.Sessions(ctx, actor.ID); len(list) != 0 {
		t.Errorf("turning bob off left %d sessions", len(list))
	}
}

func TestLockoutsAreCountedOnce(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	attempts := DefaultPolicy().LockoutAttempts
	// Wrong passwords at the same moment each count, and exactly one of
	// them locks the user out.
	var wg sync.WaitGroup
	for range attempts * 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: "not the password"}, "", auth.Client{IP: "192.0.2." + itoa(rand.N(200)+1)})
		}()
	}
	wg.Wait()
	if n := h.eventTypes()["core.user.locked_out"]; n != 1 {
		t.Errorf("locked out %d times", n)
	}
	if st := h.status(h.get(h.mustFind("ada")).ID); st.LockedUntil == nil {
		t.Error("the status doesn't say ada is locked out")
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
	h.svc.SetPolicy(SignInPolicySpec{AllowFrom: []string{"192.0.2.0/24"}})
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
	// Tokens are scoped instead, and can't change how anyone signs in.
	token := &auth.Actor{Type: auth.ActorToken, ID: store.NewID(PrefixToken), User: actor.User}
	if err := h.svc.RequireStepUp(token); err != nil {
		t.Errorf("tokens = %v", err)
	}
	if _, err := h.svc.BeginTOTP(ctx, token); !auth.ErrPanelOnly.Is(err) {
		t.Errorf("a token setting up an authenticator app = %v", err)
	}
	if _, err := h.svc.AddSSHKey(token, SSHKeyInput{Name: "laptop", PublicKey: "ssh-ed25519 AAAA"}); !auth.ErrPanelOnly.Is(err) {
		t.Errorf("a token adding an SSH key = %v", err)
	}
	if _, err := h.svc.UpdateProfile(token, ProfileInput{Name: "Ada", Email: "someone@example.net"}); !auth.ErrPanelOnly.Is(err) {
		t.Errorf("a token changing the email address sign-ins match = %v", err)
	}
	if err := h.svc.SetPreferences(ctx, token, json.RawMessage(`{"theme":"dark"}`)); !auth.ErrPanelOnly.Is(err) {
		t.Errorf("a token changing display preferences = %v", err)
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
	// The codes' hashes are keyed with a secret of the user's, which the
	// secrets store keeps, so the database alone can't be guessed against.
	if used, _ := h.store.UseRecoveryCode(ctx, actor.ID, auth.HashCode(codes[1])); used {
		t.Error("a recovery code is kept as its plain hash")
	}
	if key, err := h.vault.Get(ctx, secretRef(actor.ID, recoverySecret)); err != nil || len(key) != auth.CodeKeySize {
		t.Errorf("the recovery codes' key = %x, %v", key, err)
	}
	fourth, _ := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", client)
	if _, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: fourth.Challenge, Method: "recovery", Code: codes[1]}, client, ""); err != nil {
		t.Errorf("another recovery code = %v", err)
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
	h.svc.SetPolicy(SignInPolicySpec{RequireTwoFactor: []auth.Role{auth.RoleOwner}})
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
	u, _ := h.svc.userByName(ctx, "bob")
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

// mustFind returns the ID of the user with a username.
func (h *harness) mustFind(username string) store.ID {
	h.t.Helper()
	u, err := h.svc.userByName(h.ctx, username)
	if err != nil {
		h.t.Fatal(err)
	}
	return u.ID
}

func TestSessionsFollowGrantChanges(t *testing.T) {
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	bob := h.user("bob", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	_, secret := h.signIn("bob")
	if _, issued, err := h.authenticateWith(secret); err != nil || issued != nil {
		t.Fatalf("a session whose grants didn't change = %v, %v", issued, err)
	}

	// Taking bob's owner role away gives his session a new secret, carrying
	// what he may do now.
	if _, err := h.update(root(), bob.ID, func(s *UserSpec) {
		s.Grants = []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}}
	}); err != nil {
		t.Fatal(err)
	}
	a, issued, err := h.authenticateWith(secret)
	if err != nil || issued == nil || issued.Secret == secret {
		t.Fatalf("after the grants changed = %v, %v", issued, err)
	}
	if a.Grants[0].Role != auth.RoleReadOnly {
		t.Errorf("the session acts with %+v", a.Grants)
	}
	// Requests already on their way with the old secret still work for a
	// moment, without giving out yet another one.
	if _, again, err := h.authenticateWith(secret); err != nil || again != nil {
		t.Errorf("the old secret within the grace = %v, %v", again, err)
	}
	h.clock.add(rotationGrace + time.Second)
	if _, err := h.authenticate(secret); !auth.ErrSessionExpired.Is(err) {
		t.Errorf("the old secret after the grace = %v", err)
	}
	if a := h.actor(issued.Secret); a.Grants[0].Role != auth.RoleReadOnly {
		t.Errorf("the new secret acts with %+v", a.Grants)
	}
}

// heldReads holds every session read by hash until release is closed, so
// tests can line requests up as they would race on a busy panel.
type heldReads struct {
	Store
	read    sync.WaitGroup
	release chan struct{}
}

func (h *heldReads) SessionByHash(ctx context.Context, hash string) (*Session, error) {
	x, err := h.Store.SessionByHash(ctx, hash)
	h.read.Done()
	<-h.release
	return x, err
}

// Requests that read a session before either gives it a new secret give it
// one between them, so the browser never keeps a cookie the panel lost.
func TestSessionsGetOneNewSecretAtATime(t *testing.T) {
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	bob := h.user("bob", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	_, secret := h.signIn("bob")
	if _, err := h.update(root(), bob.ID, func(s *UserSpec) {
		s.Grants = []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}}
	}); err != nil {
		t.Fatal(err)
	}
	const requests = 4
	held := &heldReads{Store: h.svc.opts.Store, release: make(chan struct{})}
	held.read.Add(requests)
	h.svc.opts.Store = held
	var wg sync.WaitGroup
	issued := make([]*Issued, requests)
	errs := make([]error, requests)
	for i := range requests {
		wg.Go(func() { _, issued[i], errs[i] = h.authenticateWith(secret) })
	}
	held.read.Wait()
	close(held.release)
	wg.Wait()
	h.svc.opts.Store = held.Store
	var fresh []string
	for i := range requests {
		if errs[i] != nil {
			t.Fatalf("request %d = %v", i, errs[i])
		}
		if issued[i] != nil {
			fresh = append(fresh, issued[i].Secret)
		}
	}
	if len(fresh) != 1 {
		t.Fatalf("%d requests gave the session a new secret", len(fresh))
	}
	h.clock.add(rotationGrace + time.Second)
	if a := h.actor(fresh[0]); a.Grants[0].Role != auth.RoleReadOnly {
		t.Errorf("the new secret acts with %+v", a.Grants)
	}
}

// A request with the cookie from before a step-up records that the session
// was used without putting that cookie's secret back.
func TestUsingASessionKeepsTheSecretAStepUpGave(t *testing.T) {
	h := newHarness(t)
	h.admin("ada")
	actor, secret := h.signIn("ada")
	held := &heldReads{Store: h.svc.opts.Store, release: make(chan struct{})}
	held.read.Add(1)
	h.svc.opts.Store = held
	done := make(chan error)
	go func() {
		_, err := h.authenticate(secret)
		done <- err
	}()
	held.read.Wait()
	h.svc.opts.Store = held.Store
	issued, err := h.svc.StepUp(h.ctx, actor, StepUpInput{Password: password}, client, "")
	if err != nil {
		t.Fatal(err)
	}
	// The request was read before the step-up and is due to record its use.
	h.clock.add(touchEvery + time.Second)
	close(held.release)
	if err := <-done; err != nil {
		t.Fatalf("the request on its way = %v", err)
	}
	stepped := h.actor(issued.Secret)
	if !h.svc.SteppedUp(stepped) {
		t.Error("the step-up was undone")
	}
	if _, err := h.authenticate(secret); !auth.ErrSessionExpired.Is(err) {
		t.Errorf("the secret before the step-up = %v", err)
	}
}

// A wrong password for someone without one, or for a name nobody has, takes
// as long as one checked against a hash, so how long an answer takes
// doesn't say which names exist.
func TestSignInTimingDoesntTellNamesApart(t *testing.T) {
	h := newHarness(t)
	h.admin("ada")
	if _, err := h.create(root(), store.ServerScope(), UserSpec{Username: "newcomer"}); err != nil {
		t.Fatal(err)
	}
	fastest := func(name string) time.Duration {
		best := time.Duration(math.MaxInt64)
		for range 3 {
			start := time.Now()
			_, err := h.svc.SignIn(h.ctx, SignInInput{Username: name, Password: "not the password at all"}, "", client)
			if !auth.ErrBadCredentials.Is(err) {
				t.Fatalf("signing in as %s = %v", name, err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	checked := fastest("ada")
	for _, name := range []string{"newcomer", "nobody"} {
		if took := fastest(name); took*3 < checked {
			t.Errorf("a wrong password for %s took %s, and one checked against a hash %s", name, took, checked)
		}
	}
}

func TestCleanupForgetsWhatWasTouchedLongAgo(t *testing.T) {
	h := newHarness(t)
	h.admin("ada")
	_, secret := h.signIn("ada")
	h.actor(secret)
	count := func() (n int) {
		h.svc.touched.Range(func(any, any) bool { n++; return true })
		return n
	}
	if count() == 0 {
		t.Fatal("using a session isn't remembered")
	}
	h.svc.cleanup(h.ctx)
	if count() == 0 {
		t.Error("a session used just now was forgotten")
	}
	h.clock.add(touchEvery + time.Second)
	h.svc.cleanup(h.ctx)
	if n := count(); n != 0 {
		t.Errorf("%d sessions used long ago are still remembered", n)
	}
}
