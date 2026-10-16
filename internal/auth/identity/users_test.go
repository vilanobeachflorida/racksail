package identity

import (
	"context"
	"crypto/dsa"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// createAccount puts an account straight in the resource store.
func (h *harness) createAccount(slug string, reseller store.ID) store.ID {
	h.t.Helper()
	ctx := context.Background()
	id := store.NewID("acct")
	spec, _ := json.Marshal(account.AccountSpec{Slug: slug, Name: slug, Reseller: reseller})
	r := &store.Resource{Meta: store.Meta{ID: id, Kind: account.Kind.Name(), Scope: store.AccountScope(id)}, Spec: spec}
	if err := h.resources.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, r) }); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func TestManagingUsers(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	reseller := store.NewID("rsl")
	shop, other := h.createAccount("shop", reseller), h.createAccount("other", "")
	admin, _ := h.signIn(h.admin("ada").Username)
	h.user("rex", store.ResellerScope(reseller), auth.Grant{Role: auth.RoleReseller, Scope: store.ResellerScope(reseller)})
	res, _ := h.signIn("rex")

	// A reseller adds the owner of their customer's account.
	ownerView, err := h.svc.CreateUser(ctx, res, UserInput{
		Username: "Owen", Name: "Owen", Email: "owen@shop.example", Password: password,
		Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ownerView.Username != "owen" || ownerView.Home != store.AccountScope(shop) || !ownerView.PasswordSet {
		t.Errorf("owner = %+v", ownerView)
	}
	if _, err := h.svc.CreateUser(ctx, res, UserInput{Username: "intruder", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(other)}}}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("an account of someone else's = %v", err)
	}
	if _, err := h.svc.CreateUser(ctx, res, UserInput{Username: "boss", Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("an admin by a reseller = %v", err)
	}
	if _, err := h.svc.CreateUser(ctx, res, UserInput{Username: "owen", Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}}}); !auth.ErrUsernameTaken.Is(err) {
		t.Errorf("a taken name = %v", err)
	}

	// The owner adds a collaborator on one site, but no more than they have.
	owner, _ := h.signIn("owen")
	site := store.NewID("site")
	helper, err := h.svc.CreateUser(ctx, owner, UserInput{
		Username: "hal", Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.AccountScope(shop), Object: site, Permissions: []auth.Permission{"web.site.deploy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if helper.PasswordSet {
		t.Error("a user added without a password chooses one through a link")
	}
	_, err = h.svc.CreateUser(ctx, owner, UserInput{Username: "nope", Home: ptr(store.ServerScope()), Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.ServerScope()}}})
	if !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("a user of the server by an owner = %v", err)
	}
	_, err = h.svc.CreateUser(ctx, owner, UserInput{Username: "wide", Home: ptr(store.AccountScope(shop)), Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(other)}}})
	if !i18n.ErrInvalid.Is(err) {
		t.Errorf("a grant outside the user's home = %v", err)
	}

	// Each sees whom they manage.
	names := func(views []UserView) (out []string) {
		for _, v := range views {
			out = append(out, v.Username)
		}
		return out
	}
	all, _, _ := h.svc.ListUsers(ctx, admin, UserQuery{})
	mine, _, _ := h.svc.ListUsers(ctx, owner, UserQuery{})
	theirs, _, _ := h.svc.ListUsers(ctx, res, UserQuery{})
	if len(all) != 4 || !slices.Equal(names(mine), []string{"hal", "owen"}) || !slices.Equal(names(theirs), []string{"hal", "owen", "rex"}) {
		t.Errorf("admin %v, owner %v, reseller %v", names(all), names(mine), names(theirs))
	}
	if _, err := h.svc.GetUser(ctx, owner, admin.ID); !store.ErrNotFound.Is(err) {
		t.Errorf("an owner looking at the admin = %v", err)
	}
	if v, err := h.svc.GetUser(ctx, owner, owner.ID); err != nil || v.Username != "owen" {
		t.Errorf("themselves = %+v, %v", v, err)
	}

	// Changing grants takes the same rights as giving them.
	in := UserInput{Username: "hal", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}}}
	updated, err := h.svc.UpdateUser(ctx, owner, helper.ID, in, helper.Revision)
	if err != nil || updated.Grants[0].Role != auth.RoleOwner || updated.Revision != 2 {
		t.Fatalf("promote = %+v, %v", updated, err)
	}
	if _, err := h.svc.UpdateUser(ctx, owner, helper.ID, in, helper.Revision); !i18n.ErrConflict.Is(err) {
		t.Errorf("a stale change = %v", err)
	}
	in.Home = ptr(store.ServerScope())
	if _, err := h.svc.UpdateUser(ctx, admin, helper.ID, in, 0); !i18n.ErrInvalid.Is(err) {
		t.Errorf("moving a user = %v", err)
	}

	// The last admin stays one.
	adaIn := UserInput{Username: "ada", Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.ServerScope()}}}
	if _, err := h.svc.UpdateUser(ctx, admin, admin.ID, adaIn, 0); !auth.ErrLastAdmin.Is(err) {
		t.Errorf("demoting the last admin = %v", err)
	}
	if err := h.svc.DeleteUser(ctx, admin, admin.ID); !auth.ErrSelfDelete.Is(err) {
		t.Errorf("deleting yourself = %v", err)
	}
	second := h.admin("bea")
	if _, err := h.svc.UpdateUser(ctx, admin, second.ID, UserInput{Username: "bea", Disabled: true, Grants: second.Grants}, 0); err != nil {
		t.Errorf("turning another admin off = %v", err)
	}
	if err := h.svc.DeleteUser(ctx, res, ownerView.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.User(ctx, ownerView.ID); !store.ErrNotFound.Is(err) {
		t.Error("the owner is gone")
	}
	if n := len(h.audit("core.user.*")); n < 5 {
		t.Errorf("user changes are audited: %d", n)
	}
}

func TestHelpingUsersBackIn(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	acct := h.createAccount("shop", "")
	admin, _ := h.signIn(h.admin("ada").Username)
	bob := h.user("bob", store.AccountScope(acct), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(acct)})
	bobActor, bobSession := h.signIn("bob")
	key, _ := h.svc.BeginTOTP(ctx, bobActor)
	code, _ := auth.TOTPCode(key.Secret, h.clock.now())
	h.svc.ConfirmTOTP(ctx, bobActor, code)
	tok, err := h.svc.CreateToken(ctx, bobActor, TokenInput{Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}

	out, err := h.svc.ResetUser(ctx, admin, bob.ID, ResetInput{Link: true, RemoveTwoFactor: true, EndSessions: true, RevokeTokens: true, Unlock: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Link == nil || out.Link.URL != "https://panel.example.test/activate#"+out.Link.Code || out.SessionsEnded != 1 || out.TokensRevoked != 1 {
		t.Errorf("reset = %+v", out)
	}
	if _, err := h.authenticate(bobSession); !auth.ErrSessionExpired.Is(err) {
		t.Error("the sessions ended")
	}
	if _, err := h.bearer(tok.Token, client.IP); !auth.ErrTokenInvalid.Is(err) {
		t.Error("the tokens were revoked")
	}
	info, err := h.svc.LookupLink(ctx, out.Link.Code, client)
	if err != nil || info.Username != "bob" || info.Purpose != InviteReset {
		t.Errorf("link = %+v, %v", info, err)
	}
	res, err := h.svc.UseLink(ctx, UseLinkInput{Code: out.Link.Code, Password: "a brand new passphrase"}, client)
	if err != nil || res.Issued == nil {
		t.Fatalf("using the link = %+v, %v", res, err)
	}
	if _, err := h.svc.UseLink(ctx, UseLinkInput{Code: out.Link.Code, Password: "a brand new passphrase"}, client); !auth.ErrInviteInvalid.Is(err) {
		t.Errorf("a link works once: %v", err)
	}

	// A new user starts with a sign-in link, which expires.
	newbie, _ := h.svc.CreateUser(ctx, admin, UserInput{Username: "nina", Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(acct)}}})
	link, err := h.svc.IssueLink(ctx, admin, newbie.ID)
	if err != nil || link.Purpose != InviteActivate {
		t.Fatalf("activation link = %+v, %v", link, err)
	}
	h.clock.add(8 * 24 * time.Hour)
	if _, err := h.svc.LookupLink(ctx, link.Code, client); !auth.ErrInviteInvalid.Is(err) {
		t.Errorf("an expired link = %v", err)
	}

	// An owner can't help an admin, whose grants they don't hold.
	again, err := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: "a brand new passphrase"}, "", client)
	if err != nil {
		t.Fatal(err)
	}
	owner := h.actor(again.Issued.Secret)
	if _, err := h.svc.ResetUser(ctx, owner, admin.ID, ResetInput{EndSessions: true}); !store.ErrNotFound.Is(err) {
		t.Errorf("an owner resetting an admin = %v", err)
	}
}

func TestTokens(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	reseller := store.NewID("rsl")
	shop, other := h.createAccount("shop", reseller), h.createAccount("other", reseller)
	h.user("rex", store.ResellerScope(reseller), auth.Grant{Role: auth.RoleReseller, Scope: store.ResellerScope(reseller)})
	rex, _ := h.signIn("rex")

	created, err := h.svc.CreateToken(ctx, rex, TokenInput{Name: "deploys", Permissions: []string{"web.*"}, Accounts: []store.ID{shop}, AllowFrom: []string{"198.51.100.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Token[:4] != auth.TokenPrefix || created.Hint != created.Token[:8] || created.Accounts[0] != shop {
		t.Errorf("token = %+v", created)
	}
	actor, err := h.bearer(created.Token, "198.51.100.20")
	if err != nil {
		t.Fatal(err)
	}
	if actor.Type != auth.ActorToken || actor.User != rex.ID || actor.Source != auth.SourceCLI || len(actor.Grants) != 1 {
		t.Fatalf("token actor = %+v", actor)
	}
	az := h.svc.opts.Authorizer
	for perm, target := range map[auth.Permission]store.Scope{"web.site.deploy": store.AccountScope(shop)} {
		if err := az.Authorize(ctx, actor, perm, auth.Target{Scope: target}); err != nil {
			t.Errorf("%s on its account = %v", perm, err)
		}
	}
	if err := az.Authorize(ctx, actor, "web.site.deploy", auth.Target{Scope: store.AccountScope(other)}); err == nil {
		t.Error("a token narrowed to one account reached another")
	}
	if err := az.Authorize(ctx, actor, "dns.zone.update", auth.Target{Scope: store.AccountScope(shop)}); err == nil {
		t.Error("a token narrowed to web.* changed a zone")
	}
	if _, err := h.bearer(created.Token, "203.0.113.1"); !auth.ErrAddressNotAllowed.Is(err) {
		t.Errorf("outside its allowlist = %v", err)
	}
	if _, err := h.bearer("rsk_madeup", client.IP); !auth.ErrTokenInvalid.Is(err) {
		t.Errorf("a made-up token = %v", err)
	}

	if _, err := h.svc.CreateToken(ctx, rex, TokenInput{Name: "elsewhere", Accounts: []store.ID{store.NewID("acct")}}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("an account the user doesn't reach = %v", err)
	}
	if _, err := h.svc.CreateToken(ctx, rex, TokenInput{Name: "special", Permissions: []string{"core.impersonate"}}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a special permission the user lacks = %v", err)
	}
	if _, err := h.svc.CreateToken(ctx, actor, TokenInput{Name: "from a token"}); !auth.ErrPanelOnly.Is(err) {
		t.Errorf("a token making tokens = %v", err)
	}

	// Tokens expire, and the policy can cap how long they last.
	h.svc.SetPolicy(PolicySpec{TokenMaxDays: 30})
	if _, err := h.svc.CreateToken(ctx, rex, TokenInput{Name: "forever"}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a token without an end = %v", err)
	}
	soon := h.clock.now().Add(48 * time.Hour)
	short, err := h.svc.CreateToken(ctx, rex, TokenInput{Name: "short", ExpiresAt: &soon})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.bearer(short.Token, client.IP); err != nil {
		t.Errorf("before it expires = %v", err)
	}
	h.clock.add(49 * time.Hour)
	if _, err := h.bearer(short.Token, client.IP); !auth.ErrTokenInvalid.Is(err) {
		t.Errorf("after = %v", err)
	}

	list, _ := h.svc.MyTokens(ctx, rex)
	if len(list) != 2 || list[1].LastUsedAt == nil {
		t.Errorf("tokens = %+v", list)
	}
	if err := h.svc.RevokeMyToken(ctx, rex, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.bearer(created.Token, "198.51.100.20"); !auth.ErrTokenInvalid.Is(err) {
		t.Errorf("a revoked token = %v", err)
	}
	if h.eventTypes()["core.token.created"] != 2 || h.eventTypes()["core.token.revoked"] != 1 {
		t.Errorf("events = %v", h.eventTypes())
	}
}

func TestImpersonation(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	acct := h.createAccount("shop", "")
	h.admin("ada")
	admin, adminSession := h.signIn("ada")
	bob := h.user("bob", store.AccountScope(acct), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(acct)})
	viewer := h.user("vic", store.ServerScope(), auth.Grant{Role: auth.RoleReadOnly, Scope: store.ServerScope()})

	issued, err := h.svc.Impersonate(ctx, admin, bob.ID, client)
	if err != nil {
		t.Fatal(err)
	}
	as := h.actor(issued.Secret)
	if as.ID != bob.ID || as.Impersonator != admin.ID || as.Grants[0].Role != auth.RoleOwner {
		t.Fatalf("impersonating = %+v", as)
	}
	if info := h.svc.ImpersonationOf(ctx, as); info == nil || info.By != admin.ID || info.Name != "Ada" {
		t.Errorf("banner = %+v", info)
	}
	if err := h.svc.ChangePassword(ctx, as, PasswordInput{Current: password, New: "something else long"}); !auth.ErrImpersonating.Is(err) {
		t.Errorf("changing their password = %v", err)
	}
	if _, err := h.svc.Impersonate(ctx, as, viewer.ID, client); !auth.ErrImpersonating.Is(err) {
		t.Errorf("impersonating twice = %v", err)
	}
	if _, err := h.svc.Impersonate(ctx, admin, viewer.ID, client); !auth.ErrCannotImpersonate.Is(err) {
		t.Errorf("a server-wide user = %v", err)
	}
	back, err := h.svc.StopImpersonating(ctx, as)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authenticate(issued.Secret); !auth.ErrSessionExpired.Is(err) {
		t.Error("the impersonating session ended")
	}
	if _, err := h.authenticate(adminSession); !auth.ErrSessionExpired.Is(err) {
		t.Error("the admin's session has a new secret")
	}
	if a := h.actor(back.Secret); a.ID != admin.ID || a.Impersonator != "" {
		t.Errorf("back = %+v", a)
	}

	// Without core.impersonate, nobody impersonates.
	h.admin("bea")
	plain := h.user("cal", store.ServerScope(), auth.Grant{Role: auth.RoleAdmin, Scope: store.ServerScope()})
	calActor, _ := h.signIn(plain.Username)
	if _, err := h.svc.Impersonate(ctx, calActor, bob.ID, client); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("an admin without the permission = %v", err)
	}
	if records := h.audit(string(auth.PermImpersonate)); len(records) < 3 {
		t.Errorf("attempts are audited: %+v", records)
	}
}

func TestDirectory(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	reseller := store.NewID("rsl")
	shop := h.createAccount("shop", reseller)
	h.admin("ada")
	h.user("rex", store.ResellerScope(reseller), auth.Grant{Role: auth.RoleReseller, Scope: store.ResellerScope(reseller)})
	bob := h.user("bob", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	h.user("zed", store.ServerScope(), auth.Grant{Role: auth.RoleCollaborator, Scope: store.AccountScope(shop), Permissions: []auth.Permission{"files.*"}})
	actor, _ := h.signIn("bob")
	if _, err := h.svc.AddSSHKey(ctx, actor, SSHKeyInput{PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7 bob@laptop"}); err != nil {
		t.Fatal(err)
	}
	dir := h.svc.Directory()
	members, err := dir.Members(ctx, shop)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range members {
		names = append(names, m.Username)
	}
	if !slices.Equal(names, []string{"bob", "zed", "rex"}) {
		t.Errorf("members = %v", names)
	}
	keys, err := dir.SSHKeys(ctx, bob.ID)
	if err != nil || len(keys) != 1 || keys[0].Type != "ssh-ed25519" {
		t.Errorf("keys = %+v, %v", keys, err)
	}
}

func TestSSHKeys(t *testing.T) {
	var params dsa.Parameters
	if err := dsa.GenerateParameters(&params, rand.Reader, dsa.L1024N160); err != nil {
		t.Fatal(err)
	}
	priv := &dsa.PrivateKey{PublicKey: dsa.PublicKey{Parameters: params}}
	if err := dsa.GenerateKey(priv, rand.Reader); err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	dsaLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " old"
	for _, tc := range []struct {
		key    string
		ok     bool
		reason string
	}{
		{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7 a@b", true, ""},
		{`command="/bin/sh" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7`, false, "options"},
		{dsaLine, false, "aren't accepted"},
		{"not a key", false, "doesn't look"},
		{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7", false, "one key"},
	} {
		k, comment, err := ParseSSHKey(tc.key)
		if (err == nil) != tc.ok {
			t.Errorf("%.30s: %v", tc.key, err)
			continue
		}
		if err != nil && !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("%.30s: %v, want %q", tc.key, err, tc.reason)
		}
		if err == nil && (k.Fingerprint[:7] != "SHA256:" || comment != "a@b" || k.PublicKey[:11] != "ssh-ed25519") {
			t.Errorf("parsed = %+v %q", k, comment)
		}
	}
}

func TestAccountsTakeTheirUsersAlong(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	bob := h.user("bob", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	zed := h.user("zed", store.ServerScope(), auth.Grant{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}, auth.Grant{Role: auth.RoleReadOnly, Scope: store.ServerScope()})
	if err := h.resources.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error {
		if err := tx.Delete(ctx, shop, 0); err != nil {
			return err
		}
		return tx.Purge(ctx, shop)
	}); err != nil {
		t.Fatal(err)
	}
	h.svc.cleanup(ctx)
	if _, err := h.store.User(ctx, bob.ID); !store.ErrNotFound.Is(err) {
		t.Error("the account's own user went with it")
	}
	z, err := h.store.User(ctx, zed.ID)
	if err != nil || len(z.Grants) != 1 || z.Grants[0].Scope != store.ServerScope() {
		t.Errorf("others lose their grants on it: %+v, %v", z, err)
	}
}

func TestPolicyKind(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	spec, _ := json.Marshal(PolicySpec{SessionIdleMinutes: 20, SessionLifetimeHours: 4, LockoutAttempts: 5, RequireTwoFactor: []auth.Role{auth.RoleAdmin}})
	r := &store.Resource{Meta: store.Meta{ID: store.NewID("signin"), Kind: PolicyKind.Name(), Scope: store.ServerScope()}, Spec: spec}
	if err := h.resources.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, r) }); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.LoadPolicy(ctx); err != nil {
		t.Fatal(err)
	}
	p := h.svc.Policy()
	if p.Idle() != 20*time.Minute || p.Lifetime() != 4*time.Hour || p.LockoutAttempts != 5 || p.StepUpMinutes != 10 || p.SignInAlerts != "new-device" {
		t.Errorf("policy = %+v", p)
	}

	// The policy refuses an allowlist that would lock out whoever sets it.
	actor := &auth.Actor{Type: auth.ActorUser, IP: "203.0.113.4"}
	obj := &store.Object[PolicySpec, PolicyStatus]{Spec: PolicySpec{AllowFrom: []string{"192.0.2.0/24"}}}
	if err := validatePolicy(auth.WithActor(ctx, actor), obj, nil); !i18n.ErrInvalid.Is(err) {
		t.Errorf("locking yourself out = %v", err)
	}
	actor.IP = "127.0.0.1"
	if err := validatePolicy(auth.WithActor(ctx, actor), obj, nil); err != nil {
		t.Errorf("from the server = %v", err)
	}
	obj.Spec.AllowFrom = []string{"not a network"}
	if err := validatePolicy(ctx, obj, nil); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a bad network = %v", err)
	}
}
