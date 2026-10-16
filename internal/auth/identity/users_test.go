package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func TestManagingUsers(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	reseller := store.NewID("rsl")
	shop, other := h.createAccount("shop", reseller), h.createAccount("other", "")
	admin, _ := h.signIn(h.admin("ada").Username)
	h.user("rex", store.ResellerScope(reseller), auth.Grant{Role: auth.RoleReseller, Scope: store.ResellerScope(reseller)})
	res, _ := h.signIn("rex")

	// A reseller adds the owner of their customer's account, through the
	// change pipeline like any other change.
	owen, err := h.create(res, store.AccountScope(shop), UserSpec{
		Username: "Owen", Name: "Owen", Email: "owen@shop.example", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if u := h.get(owen); u.Username != "owen" || u.Home != store.AccountScope(shop) || u.PasswordHash != "" {
		t.Errorf("owner = %+v", u)
	}
	if _, err := h.create(res, store.AccountScope(other), UserSpec{Username: "intruder", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(other)}}}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("an account of someone else's = %v", err)
	}
	if _, err := h.create(res, store.ServerScope(), UserSpec{Username: "boss", Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("an admin by a reseller = %v", err)
	}
	if _, err := h.create(res, store.ResellerScope(reseller), UserSpec{Username: "boss", Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a grant beyond the reseller = %v", err)
	}
	if _, err := h.create(res, store.AccountScope(shop), UserSpec{Username: "OWEN", Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}}}); !auth.ErrUsernameTaken.Is(err) {
		t.Errorf("a taken name = %v", err)
	}

	// Whoever may help Owen sets his first password; he never sees the
	// reseller's choice, as he picks his own when he signs in.
	if err := h.svc.SetUserPassword(ctx, res, owen, PasswordSetting{Password: password, MustChange: true}); err != nil {
		t.Fatal(err)
	}
	owner, _ := h.signIn("owen")
	if owner.Pending != PendingPassword {
		t.Errorf("a password someone else chose = %q pending", owner.Pending)
	}
	if err := h.svc.ChangePassword(ctx, owner, PasswordInput{New: "a passphrase of his choosing"}); err != nil {
		t.Fatal(err)
	}
	owner = h.actor(h.mustSignIn("owen", "a passphrase of his choosing"))

	// The owner adds a collaborator on one site, but no more than they have.
	site := store.NewID("site")
	hal, err := h.create(owner, store.AccountScope(shop), UserSpec{
		Username: "hal", Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.AccountScope(shop), Object: site, Permissions: []auth.Permission{"web.site.deploy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.create(owner, store.ServerScope(), UserSpec{Username: "nope", Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.ServerScope()}}}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("a user of the server by an owner = %v", err)
	}
	if _, err := h.create(owner, store.AccountScope(shop), UserSpec{Username: "wide", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(other)}}}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a grant outside the user's home = %v", err)
	}
	if _, err := h.create(owner, store.AccountScope(shop), UserSpec{Username: "spy", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop), Permissions: []auth.Permission{auth.PermImpersonate}}}}); !auth.ErrGrantNotAllowed.Is(err) {
		t.Errorf("a special permission the owner lacks = %v", err)
	}

	// Each sees whom they manage.
	if _, err := h.svc.UserSSHKeys(ctx, owner, admin.ID); !store.ErrNotFound.Is(err) {
		t.Errorf("an owner looking at the admin = %v", err)
	}
	if _, err := h.svc.UserSSHKeys(ctx, owner, hal); err != nil {
		t.Errorf("an owner looking at their collaborator = %v", err)
	}

	// Changing grants takes the same rights as giving them, and a change
	// made from what someone saw fails once someone else changed it.
	obj, _ := UserKind.Get(ctx, h.resources, hal)
	promote := func(rev int64) error {
		spec := obj.Spec
		spec.Grants = []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}}
		_, err := h.submit(owner, "Make hal an owner", changeset.Change{Op: changeset.OpUpdate, Kind: KindUser, ID: hal, Spec: specJSON(t, spec), Revision: rev})
		return err
	}
	if err := promote(obj.Revision); err != nil {
		t.Fatal(err)
	}
	if u := h.get(hal); u.Grants[0].Role != auth.RoleOwner || u.Revision == obj.Revision {
		t.Errorf("promoted = %+v", u)
	}
	if err := promote(obj.Revision); !i18n.ErrConflict.Is(err) {
		t.Errorf("a stale change = %v", err)
	}

	// The last admin stays one, and nobody removes or turns off themselves.
	if _, err := h.update(admin, admin.ID, func(s *UserSpec) {
		s.Grants = []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.ServerScope()}}
	}); !auth.ErrLastAdmin.Is(err) {
		t.Errorf("demoting the last admin = %v", err)
	}
	if err := h.remove(admin, admin.ID); !auth.ErrSelfDelete.Is(err) {
		t.Errorf("deleting yourself = %v", err)
	}
	if _, err := h.update(admin, admin.ID, func(s *UserSpec) { s.Disabled = true }); !auth.ErrSelfDelete.Is(err) {
		t.Errorf("turning yourself off = %v", err)
	}
	bea := h.admin("bea")
	if _, err := h.update(admin, bea.ID, func(s *UserSpec) { s.Disabled = true }); err != nil {
		t.Errorf("turning another admin off = %v", err)
	}
	// With bea turned off, ada is the only admin who can sign in.
	if err := h.remove(root(), admin.ID); !auth.ErrLastAdmin.Is(err) {
		t.Errorf("removing the last admin who can sign in = %v", err)
	}

	// Removing a user takes their credentials along.
	if err := h.remove(res, owen); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.User(ctx, owen); !store.ErrNotFound.Is(err) {
		t.Errorf("the owner is still there: %v", err)
	}
	if _, err := h.store.Credentials(ctx, owen); !store.ErrNotFound.Is(err) {
		t.Errorf("the owner's credentials are still there: %v", err)
	}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "owen", Password: "a passphrase of his choosing"}, "", client); !auth.ErrBadCredentials.Is(err) {
		t.Errorf("signing in as a removed user = %v", err)
	}
	for _, action := range []string{"core.user.create", "core.user.update", "core.user.delete"} {
		if len(h.audit(action)) == 0 {
			t.Errorf("no %s in the audit log", action)
		}
	}
}

// mustSignIn signs in with a password and returns the session's secret.
func (h *harness) mustSignIn(username, pass string) string {
	h.t.Helper()
	res, err := h.svc.SignIn(h.ctx, SignInInput{Username: username, Password: pass}, "", client)
	if err != nil || res.Issued == nil {
		h.t.Fatalf("signing in as %s = %+v, %v", username, res, err)
	}
	return res.Issued.Secret
}

func TestUserChangesTakePartInThePipeline(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	ada, _ := h.signIn(h.admin("ada").Username)

	// Hooks and rules see users like any other kind.
	err := h.hooks.Register(hooks.Registration{
		Point: hooks.KindPoint(KindUser, "create", changeset.StageValidate), Kind: hooks.Validate, Owner: "module:naming",
		OnValidate: func(ctx context.Context, c *hooks.Context) error {
			var spec UserSpec
			if err := json.Unmarshal(c.Change.After, &spec); err != nil {
				return err
			}
			if strings.HasPrefix(spec.Username, "temp") {
				return i18n.ErrBadRequest.New("reason", "temporary users aren't allowed here")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.create(ada, store.AccountScope(shop), UserSpec{Username: "temp1", Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}}}); !errors.Is(err, hooks.ErrDenied) {
		t.Errorf("a user a hook refuses = %v", err)
	}

	// A preview shows the user as they'd be, without adding them.
	id := store.NewID(PrefixUser)
	preview, err := h.pipeline.Submit(ctx, changeset.Request{Actor: ada, DryRun: true, Changes: []changeset.Change{{
		Op: changeset.OpCreate, Kind: KindUser, ID: id, Scope: store.AccountScope(shop),
		Spec: specJSON(t, UserSpec{Username: " Bob ", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}}}),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Changes) != 1 || !strings.Contains(string(preview.Changes[0].After), `"username":"bob"`) || preview.Risk != store.RiskHigh {
		t.Errorf("preview = %+v", preview.Changes)
	}
	if _, err := h.svc.User(ctx, id); !store.ErrNotFound.Is(err) {
		t.Errorf("a preview added the user: %v", err)
	}

	// Applying it adds bob; undoing a change of his grants brings them
	// back as they were.
	applied, err := h.pipeline.Apply(ctx, changeset.ApplyRequest{Actor: ada, ChangeSet: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	h.settle(applied)
	bob := h.get(id)
	_, err = h.update(ada, bob.ID, func(s *UserSpec) {
		s.Grants = append(s.Grants, auth.Grant{Role: auth.RoleReadOnly, Scope: store.ServerScope()})
	})
	if !i18n.ErrInvalid.Is(err) {
		t.Errorf("a grant outside bob's account = %v", err)
	}
	promoted, err := h.update(ada, bob.ID, func(s *UserSpec) { s.Name, s.Email = "Bob", "bob@shop.example" })
	if err != nil {
		t.Fatal(err)
	}
	undo, err := h.pipeline.Undo(ctx, changeset.UndoRequest{Actor: ada, ChangeSet: promoted.ID})
	if err != nil {
		t.Fatal(err)
	}
	h.settle(undo)
	if u := h.get(bob.ID); u.Name != "" || u.Email != "" {
		t.Errorf("after the undo = %+v", u)
	}
	if h.eventTypes()["core.user.created"] != 2 || h.eventTypes()["core.user.updated"] < 2 {
		t.Errorf("events = %v", h.eventTypes())
	}
}

func TestChangesToOneUserDontLoseEachOther(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	bob := h.user("bob", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	bobActor, _ := h.signIn("bob")

	// An admin reads bob to change his grants; meanwhile bob adds a key.
	before, _ := UserKind.Get(ctx, h.resources, bob.ID)
	req, err := h.svc.AddSSHKey(bobActor, SSHKeyInput{PublicKey: keys[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.submitRequest(bobActor, req); err != nil {
		t.Fatal(err)
	}
	spec := before.Spec
	spec.Grants = []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)}}
	_, err = h.submit(root(), "Make bob read-only", changeset.Change{Op: changeset.OpUpdate, Kind: KindUser, ID: bob.ID, Spec: specJSON(t, spec), Revision: before.Revision})
	if !i18n.ErrConflict.Is(err) {
		t.Fatalf("a change from before bob's key = %v", err)
	}
	if u := h.get(bob.ID); len(u.SSHKeys) != 1 || u.Grants[0].Role != auth.RoleOwner {
		t.Errorf("bob = %+v", u)
	}

	// Changes made at the same moment each land, or are refused for the
	// caller to make again; none is silently undone by another.
	var wg sync.WaitGroup
	var mu sync.Mutex
	conflicts := 0
	for _, k := range keys[1:] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				req, err := h.svc.AddSSHKey(bobActor, SSHKeyInput{PublicKey: k})
				if err != nil {
					t.Error(err)
					return
				}
				cs, err := h.pipeline.Submit(ctx, changeset.Request{Actor: bobActor, Title: req.Title, Changes: req.Changes})
				if i18n.ErrConflict.Is(err) {
					mu.Lock()
					conflicts++
					mu.Unlock()
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				h.settle(cs)
				return
			}
			t.Error("a key never landed")
		}()
	}
	wg.Wait()
	if u := h.get(bob.ID); len(u.SSHKeys) != len(keys) {
		t.Errorf("bob has %d keys of %d, after %d conflicts", len(u.SSHKeys), len(keys), conflicts)
	}

	// Credentials aren't part of the user's settings, so changing these
	// never touches a password.
	if _, err := h.update(root(), bob.ID, func(s *UserSpec) { s.Name = "Robert" }); err != nil {
		t.Fatal(err)
	}
	h.mustSignIn("bob", password)
}

// keys are SSH public keys for the tests.
var keys = []string{
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7 bob@laptop",
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHaSw601QLuAPAILOu5mzYiHEjI06gxucUPArdc/9DHt one",
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID/EzP50WHDiwNmfcfMP8GVsje3UHMHX09N2sNvmheLz two",
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIItbnbDBPbJCVsgpqjZKqQxtLroxi5IypKuTE7lU01Vf three",
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIATvrwgPWj504cKdHKakhWk4LLvNMk6NWdK4PvIcA58A four",
}

func TestOwnChanges(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	box := &memo{}
	h.provide(hooks.ProvideNotifier, "memo", func(json.RawMessage) (any, error) { return box, nil })
	bob := h.user("bob", store.AccountScope(shop), auth.Grant{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)})
	actor, _ := h.signIn("bob")
	if err := h.svc.SetAlertChannel(ctx, actor, AlertChannelInput{Type: "provider:memo", Settings: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}

	// Everyone changes their own profile and keys, whatever their role.
	req, err := h.svc.UpdateProfile(actor, ProfileInput{Name: " Bob Builder ", Email: "bob@shop.example", Locale: "pt-BR"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.submitRequest(actor, req); err != nil {
		t.Fatal(err)
	}
	if u := h.get(bob.ID); u.Name != "Bob Builder" || u.Email != "bob@shop.example" || u.Locale != "pt-BR" {
		t.Errorf("profile = %+v", u)
	}
	if _, err := h.svc.UpdateProfile(actor, ProfileInput{Email: "not an address"}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a bad address = %v", err)
	}
	req, err = h.svc.AddSSHKey(actor, SSHKeyInput{PublicKey: keys[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.submitRequest(actor, req); err != nil {
		t.Fatal(err)
	}
	mine, _ := h.svc.MySSHKeys(ctx, actor)
	if len(mine) != 1 || mine[0].Name != "bob@laptop" || mine[0].Type != "ssh-ed25519" {
		t.Fatalf("keys = %+v", mine)
	}
	if _, err := h.submitRequest(actor, req); !i18n.ErrInvalid.Is(err) {
		t.Errorf("the same key twice = %v", err)
	}
	// A key someone else adds gets the same alert.
	addFor := changeset.Change{Op: changeset.OpAction, Kind: KindUser, ID: bob.ID, Action: "add_ssh_key", Input: json.RawMessage(`{"name":"from ops","publicKey":"` + keys[1] + `"}`)}
	if _, err := h.submit(root(), "Add a key for bob", addFor); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(box.titles()) >= 2 })
	if titles := box.titles(); titles[0] != "An SSH key was added" || titles[1] != "An SSH key was added" {
		t.Errorf("alerts = %v", titles)
	}

	// Nobody changes someone else's keys or profile without the right.
	other := h.user("cy", store.AccountScope(shop), auth.Grant{Role: auth.RoleReadOnly, Scope: store.AccountScope(shop)})
	cy, _ := h.signIn("cy")
	steal := changeset.Change{Op: changeset.OpAction, Kind: KindUser, ID: bob.ID, Action: "remove_ssh_key", Input: json.RawMessage(`{"id":"` + string(mine[0].ID) + `"}`)}
	if _, err := h.submit(cy, "Remove bob's key", steal); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("removing someone else's key = %v", err)
	}
	rename := changeset.Change{Op: changeset.OpAction, Kind: KindUser, ID: other.ID, Action: "update_profile", Input: json.RawMessage(`{"name":"Cy"}`)}
	if _, err := h.submit(actor, "Rename cy", rename); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("changing someone else's profile = %v", err)
	}
	// Nor their own grants, which aren't theirs to give.
	if _, err := h.update(actor, bob.ID, func(s *UserSpec) {
		s.Grants = []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}}
	}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("bob making himself an owner = %v", err)
	}

	req, err = h.svc.RemoveSSHKey(ctx, actor, mine[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.submitRequest(actor, req); err != nil {
		t.Fatal(err)
	}
	if left, _ := h.svc.MySSHKeys(ctx, actor); len(left) != 1 || left[0].Name != "from ops" {
		t.Errorf("after removing one = %+v", left)
	}
	if _, err := h.svc.RemoveSSHKey(ctx, actor, mine[0].ID); !store.ErrNotFound.Is(err) {
		t.Errorf("removing a key twice = %v", err)
	}

	// Someone viewing the panel as bob changes nothing that is his alone.
	impersonated := actor.Clone()
	impersonated.Impersonator = store.NewID(PrefixUser)
	if _, err := h.svc.AddSSHKey(impersonated, SSHKeyInput{PublicKey: keys[2]}); !auth.ErrImpersonating.Is(err) {
		t.Errorf("adding a key as someone = %v", err)
	}
	if _, err := h.submit(impersonated, "Rename bob", changeset.Change{Op: changeset.OpAction, Kind: KindUser, ID: bob.ID, Action: "update_profile", Input: json.RawMessage(`{"name":"Mallory"}`)}); err == nil {
		t.Error("someone viewing the panel as bob changed his profile")
	}
}

// How someone signs in changes only in their own session in the panel: a
// token of theirs, or someone viewing the panel as them, can't give them a
// new SSH key or email address, whatever their grants. Others can, with the
// permissions for it.
func TestOwnSignInTakesThePanel(t *testing.T) {
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	owen := h.user("owen", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	token := &auth.Actor{Type: auth.ActorToken, ID: store.NewID(PrefixToken), Name: "ci", User: owen.ID, Source: auth.SourceAPI,
		Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}}}
	viewing := &auth.Actor{Type: auth.ActorUser, ID: owen.ID, Name: "owen", User: owen.ID, Source: auth.SourceUI, Impersonator: store.NewID(PrefixUser),
		Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}}}
	addKey := changeset.Change{Op: changeset.OpAction, Kind: KindUser, ID: owen.ID, Action: "add_ssh_key",
		Input: json.RawMessage(`{"name":"ci","publicKey":"` + keys[0] + `"}`)}
	if _, err := h.submit(token, "Add a key", addKey); !auth.ErrPanelOnly.Is(err) {
		t.Errorf("a token adding its user a key = %v", err)
	}
	if _, err := h.submit(viewing, "Add a key", addKey); !auth.ErrImpersonating.Is(err) {
		t.Errorf("someone viewing as the user adding them a key = %v", err)
	}
	if _, err := h.update(token, owen.ID, func(s *UserSpec) { s.Email = "mallory@example.net" }); !auth.ErrPanelOnly.Is(err) {
		t.Errorf("a token changing its user's email address = %v", err)
	}
	if _, err := h.update(viewing, owen.ID, func(s *UserSpec) { s.AllowFrom = []string{"203.0.113.0/24"} }); !auth.ErrImpersonating.Is(err) {
		t.Errorf("someone viewing as the user changing where they sign in from = %v", err)
	}
	// Nor an address that only Unicode's case folding would count as the
	// one he has, which outside sign-in tells apart.
	if _, err := h.update(root(), owen.ID, func(s *UserSpec) { s.Email = "owen@shop.example" }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.update(token, owen.ID, func(s *UserSpec) { s.Email = "owen@\u017fhop.example" }); !auth.ErrPanelOnly.Is(err) {
		t.Errorf("a token giving its user an address with a long s = %v", err)
	}
	// What doesn't sign anyone in goes through, his address in capitals
	// included.
	if _, err := h.update(token, owen.ID, func(s *UserSpec) { s.Name, s.Email = "Owen Owner", "Owen@Shop.example" }); err != nil {
		t.Errorf("a token renaming its user = %v", err)
	}
	// Others with the permission add keys, and the user hears about it.
	cy := h.user("cy", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	theirs := &auth.Actor{Type: auth.ActorToken, ID: store.NewID(PrefixToken), Name: "ops", User: cy.ID, Source: auth.SourceAPI,
		Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(shop)}}}
	if _, err := h.submit(theirs, "Add a key for owen", addKey); err != nil {
		t.Errorf("another owner's token adding owen a key = %v", err)
	}
	if u := h.get(owen.ID); len(u.SSHKeys) != 1 || u.Name != "Owen Owner" {
		t.Errorf("owen = %+v", u)
	}
}

// What outside sign-in matches people by is a user's username and email
// address, in whatever case; their name, language and the rest aren't.
func TestSignInMatch(t *testing.T) {
	ada := UserSpec{Username: "ada", Name: "Ada", Email: "ada@example.com", Locale: "en"}
	for _, tc := range []struct {
		name    string
		change  func(*UserSpec)
		changed bool
	}{
		{"a new name and language", func(s *UserSpec) { s.Name, s.Locale = "Ada Lovelace", "pt-BR" }, false},
		{"the address in capitals", func(s *UserSpec) { s.Email = " ADA@Example.com" }, false},
		{"another address", func(s *UserSpec) { s.Email = "ada@example.org" }, true},
		{"no address", func(s *UserSpec) { s.Email = "" }, true},
		{"another username", func(s *UserSpec) { s.Username = "lovelace" }, true},
		{"new keys", func(s *UserSpec) { s.SSHKeys = []UserSSHKey{{Name: "laptop"}} }, false},
	} {
		next := ada
		tc.change(&next)
		if got := SignInMatchChanged(ada, next); got != tc.changed {
			t.Errorf("%s: changed = %v", tc.name, got)
		}
	}
	got, err := ApplyProfile(ada, json.RawMessage(`{"name":" Ada L. ","email":" ada@example.org ","locale":"en-GB"}`))
	if err != nil || got.Name != "Ada L." || got.Email != "ada@example.org" || got.Locale != "en-GB" || got.Username != "ada" {
		t.Errorf("a profile = %+v, %v", got, err)
	}
	if _, err := ApplyProfile(ada, json.RawMessage(`{"email":"not an address"}`)); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a bad address = %v", err)
	}
}

// A change counts as giving a user another address exactly when it changes
// whom the address finds: addresses are compared as the users' email index
// keeps them and outside sign-in looks them up, which tells apart letters
// that Unicode's case folding counts as one.
func TestSignInMatchFollowsTheIndex(t *testing.T) {
	h := newHarness(t)
	shop := h.createAccount("shop", "")
	sam := h.user("sam", store.AccountScope(shop), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(shop)})
	for _, tc := range []struct{ has, next string }{
		{"sam@example.com", "sam@example.com"},
		{"sam@example.com", " SAM@Example.COM "},
		{"sam@example.com", "\u017fam@example.com"},
		{"\u00b5@example.com", "\u03bc@example.com"},
		{"\u03c3@example.com", "\u03c2@example.com"},
		{"\u03c3@example.com", "\u03a3@example.com"},
		{"sam@example.com", "sam@example.org"},
	} {
		if _, err := h.update(root(), sam.ID, func(s *UserSpec) { s.Email = tc.has }); err != nil {
			t.Fatal(err)
		}
		u, err := h.svc.userByEmail(h.ctx, tc.next)
		finds := err == nil && u.ID == sam.ID
		if err != nil && !store.ErrNotFound.Is(err) {
			t.Fatal(err)
		}
		obj, err := UserKind.Get(h.ctx, h.resources, sam.ID)
		if err != nil {
			t.Fatal(err)
		}
		before, after := obj.Spec, obj.Spec
		after.Email = tc.next
		if changed := SignInMatchChanged(before, after); changed == finds {
			t.Errorf("%q after %q: changed = %t, but it finds sam = %t", tc.next, tc.has, changed, finds)
		}
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

	// An admin sets a password for bob, which signs out whoever had the
	// old one, and asks him to choose his own when he next signs in.
	_, keep := h.signIn("ada")
	bobSession = h.mustSignIn("bob", "a brand new passphrase")
	if err := h.svc.SetUserPassword(ctx, admin, bob.ID, PasswordSetting{Password: "short"}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a weak password = %v", err)
	}
	if err := h.svc.SetUserPassword(ctx, admin, bob.ID, PasswordSetting{Password: "one the admin chose", MustChange: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authenticate(bobSession); !auth.ErrSessionExpired.Is(err) {
		t.Errorf("bob's session after the admin set his password = %v", err)
	}
	h.actor(keep)
	if st := h.status(bob.ID); !st.PasswordSet || !st.MustChangePassword {
		t.Errorf("bob's status = %+v", st)
	}
	res, err = h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: "one the admin chose"}, "", client)
	if err != nil || res.Issued == nil || res.Issued.Session.Pending != PendingPassword {
		t.Fatalf("signing in with it = %+v, %v", res, err)
	}
	// Turning that off again lets him keep it; nobody sets their own this
	// way.
	if err := h.svc.SetUserPassword(ctx, admin, bob.ID, PasswordSetting{}); err != nil {
		t.Fatal(err)
	}
	if st := h.status(bob.ID); st.MustChangePassword {
		t.Errorf("after letting him keep it = %+v", st)
	}
	if err := h.svc.SetUserPassword(ctx, admin, admin.ID, PasswordSetting{Password: "a password of my own"}); !auth.ErrOwnPassword.Is(err) {
		t.Errorf("setting your own = %v", err)
	}
	if len(h.audit(string(auth.PermUserReset))) != 3 || h.eventTypes()["core.user.password_changed"] == 0 {
		t.Errorf("setting passwords is audited and published: %v", h.eventTypes())
	}

	// A new user starts with a sign-in link, which expires.
	nina, err := h.create(admin, store.AccountScope(acct), UserSpec{Username: "nina", Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(acct)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetUserPassword(ctx, admin, nina, PasswordSetting{MustChange: true}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("asking someone without a password to change it = %v", err)
	}
	link, err := h.svc.IssueLink(ctx, admin, nina)
	if err != nil || link.Purpose != InviteActivate {
		t.Fatalf("activation link = %+v, %v", link, err)
	}
	h.clock.add(8 * 24 * time.Hour)
	if _, err := h.svc.LookupLink(ctx, link.Code, client); !auth.ErrInviteInvalid.Is(err) {
		t.Errorf("an expired link = %v", err)
	}

	// An owner can't help an admin, whose grants they don't hold.
	again, err := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: "one the admin chose"}, "", client)
	if err != nil {
		t.Fatal(err)
	}
	owner := h.actor(again.Issued.Secret)
	if _, err := h.svc.ResetUser(ctx, owner, admin.ID, ResetInput{EndSessions: true}); !store.ErrNotFound.Is(err) {
		t.Errorf("an owner resetting an admin = %v", err)
	}
	if err := h.svc.SetUserPassword(ctx, owner, admin.ID, PasswordSetting{Password: "taking over the server"}); !store.ErrNotFound.Is(err) {
		t.Errorf("an owner setting an admin's password = %v", err)
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
	if actor.Type != auth.ActorToken || actor.User != rex.ID || actor.UserName != "rex" || actor.Name != "deploys" || actor.Source != auth.SourceCLI || len(actor.Grants) != 1 {
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
	h.svc.SetPolicy(SignInPolicySpec{TokenMaxDays: 30})
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

	bobsOwn, _ := h.signIn("bob")
	issued, err := h.svc.Impersonate(ctx, admin, bob.ID, client)
	if err != nil {
		t.Fatal(err)
	}
	as := h.actor(issued.Secret)
	if as.ID != bob.ID || as.Impersonator != admin.ID || as.ImpersonatorName != "ada" || as.Grants[0].Role != auth.RoleOwner {
		t.Fatalf("impersonating = %+v", as)
	}
	if info := h.svc.ImpersonationOf(ctx, as); info == nil || info.By != admin.ID || info.Name != "Ada" {
		t.Errorf("banner = %+v", info)
	}
	if err := h.svc.ChangePassword(ctx, as, PasswordInput{Current: password, New: "something else long"}); !auth.ErrImpersonating.Is(err) {
		t.Errorf("changing their password = %v", err)
	}
	// Signing bob out is out of reach too, one session at a time as much as
	// all of them.
	if err := h.svc.EndMySession(ctx, as, store.ID(bobsOwn.SessionID)); !auth.ErrImpersonating.Is(err) {
		t.Errorf("ending one of their sessions = %v", err)
	}
	if _, err := h.svc.EndOtherSessions(ctx, as); !auth.ErrImpersonating.Is(err) {
		t.Errorf("signing them out elsewhere = %v", err)
	}
	if err := h.svc.ForgetDevice(ctx, as, ""); !auth.ErrImpersonating.Is(err) {
		t.Errorf("forgetting their browsers = %v", err)
	}
	// So is everything else of how bob signs in, his passkeys' names
	// included.
	if _, err := h.svc.RenamePasskey(ctx, as, store.NewID(PrefixPasskey), "mine now"); !auth.ErrImpersonating.Is(err) {
		t.Errorf("renaming one of their passkeys = %v", err)
	}
	if err := h.svc.SetAlertChannel(ctx, as, AlertChannelInput{}); !auth.ErrImpersonating.Is(err) {
		t.Errorf("changing where their alerts go = %v", err)
	}
	if _, err := h.svc.NewRecoveryCodes(ctx, as); !auth.ErrImpersonating.Is(err) {
		t.Errorf("making them recovery codes = %v", err)
	}
	if err := h.svc.UnlinkMine(ctx, as, store.NewID(ProviderKind.Info().Prefix)); !auth.ErrImpersonating.Is(err) {
		t.Errorf("unlinking their outside sign-in = %v", err)
	}
	if err := h.svc.DisableMFA(ctx, as, store.NewID(ProviderKind.Info().Prefix)); !auth.ErrImpersonating.Is(err) {
		t.Errorf("turning off their second step = %v", err)
	}
	if _, err := h.svc.SessionOf(ctx, bobsOwn); err != nil {
		t.Errorf("bob's own session = %v", err)
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
	req, err := h.svc.AddSSHKey(actor, SSHKeyInput{PublicKey: keys[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.submitRequest(actor, req); err != nil {
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
	// A DSA key, written out by hand: only its type matters, and SSH reads
	// any key with a 1024-bit p and a 160-bit q.
	bits := func(n uint) *big.Int {
		v := new(big.Int).Lsh(big.NewInt(1), n-1)
		return v.Add(v, big.NewInt(1))
	}
	blob := ssh.Marshal(struct {
		Name       string
		P, Q, G, Y *big.Int
	}{"ssh-dss", bits(1024), bits(160), big.NewInt(2), big.NewInt(5)})
	dsaLine := "ssh-dss " + base64.StdEncoding.EncodeToString(blob) + " old"
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
	if _, err := h.svc.User(ctx, bob.ID); !store.ErrNotFound.Is(err) {
		t.Error("the account's own user went with it")
	}
	z, err := h.svc.User(ctx, zed.ID)
	if err != nil || len(z.Grants) != 1 || z.Grants[0].Scope != store.ServerScope() {
		t.Errorf("others lose their grants on it: %+v, %v", z, err)
	}
	removed := h.audit("core.user.delete")
	if len(removed) != 1 || removed[0].Actor.Name != "accounts" {
		t.Errorf("the account's users were removed through the change pipeline: %+v", removed)
	}
}

func TestOrphanCredentialsAreRemoved(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	// Credentials kept for a user who never got their resource, as when
	// adding the first admin failed halfway, go after a while.
	gone := store.NewID(PrefixUser)
	if err := h.store.SetPassword(ctx, gone, "$argon2id$x", false, h.clock.now()); err != nil {
		t.Fatal(err)
	}
	h.svc.cleanup(ctx)
	if _, err := h.store.Credentials(ctx, gone); err != nil {
		t.Errorf("fresh credentials were removed: %v", err)
	}
	h.clock.add(orphanAge + time.Minute)
	h.svc.cleanup(ctx)
	if _, err := h.store.Credentials(ctx, gone); !store.ErrNotFound.Is(err) {
		t.Errorf("old credentials of nobody = %v", err)
	}
	if _, err := h.store.Credentials(ctx, h.mustFind("ada")); err != nil {
		t.Errorf("ada's credentials went too: %v", err)
	}
}

func TestPolicyKind(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	spec, _ := json.Marshal(SignInPolicySpec{SessionIdleMinutes: 20, SessionLifetimeHours: 4, LockoutAttempts: 5, RequireTwoFactor: []auth.Role{auth.RoleAdmin}})
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
	obj := &store.Object[SignInPolicySpec, SignInPolicyStatus]{Spec: SignInPolicySpec{AllowFrom: []string{"192.0.2.0/24"}}}
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
