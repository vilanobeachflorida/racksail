package identity

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// stores runs a test against both stores, which must behave the same.
func stores(t *testing.T, test func(t *testing.T, s Store)) {
	t.Run("memory", func(t *testing.T) { test(t, NewMemory()) })
	t.Run("sql", func(t *testing.T) {
		ctx := context.Background()
		db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		s, err := NewSQL(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		test(t, s)
	})
}

var epoch = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newUser(name string, home store.Scope, grants ...auth.Grant) *User {
	if grants == nil {
		grants = []auth.Grant{}
	}
	return &User{
		ID: store.NewID(PrefixUser), Username: name, Name: "Person " + name, Email: name + "@example.test", Home: home, Grants: grants,
		CreatedAt: epoch, UpdatedAt: epoch,
	}
}

func TestUsers(t *testing.T) {
	stores(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		acct, other := store.NewID("acct"), store.NewID("acct")
		admin := newUser("ada", store.ServerScope(), auth.Grant{Role: auth.RoleAdmin, Scope: store.ServerScope()})
		owner := newUser("bob", store.AccountScope(acct), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(acct)})
		helper := newUser("cy", store.AccountScope(acct), auth.Grant{Role: auth.RoleCollaborator, Scope: store.AccountScope(acct), Object: store.NewID("site"), Permissions: []auth.Permission{"web.site.deploy"}})
		stranger := newUser("dee", store.AccountScope(other), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(other)})
		for _, u := range []*User{helper, admin, stranger, owner} {
			if err := s.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		if n, _ := s.CountUsers(ctx); n != 4 {
			t.Errorf("count = %d", n)
		}
		dup := newUser("bob", store.ServerScope())
		if err := s.CreateUser(ctx, dup); !auth.ErrUsernameTaken.Is(err) {
			t.Errorf("duplicate username = %v", err)
		}

		got, err := s.UserByName(ctx, "BOB")
		if err != nil || got.ID != owner.ID || got.Grants[0].Role != auth.RoleOwner || got.Revision != 1 || got.AllowFrom == nil {
			t.Fatalf("by name = %+v, %v", got, err)
		}
		got, _ = s.User(ctx, helper.ID)
		if got.Grants[0].Object != helper.Grants[0].Object || got.Grants[0].Permissions[0] != "web.site.deploy" {
			t.Errorf("grants = %+v", got.Grants)
		}
		if _, err := s.User(ctx, store.NewID(PrefixUser)); !store.ErrNotFound.Is(err) {
			t.Errorf("missing user = %v", err)
		}

		names := func(us []*User) (out []string) {
			for _, u := range us {
				out = append(out, u.Username)
			}
			return out
		}
		all, next, err := s.ListUsers(ctx, UserQuery{})
		if err != nil || next != "" || len(all) != 4 || all[0].Username != "ada" {
			t.Errorf("all = %v %q %v", names(all), next, err)
		}
		page, next, _ := s.ListUsers(ctx, UserQuery{Limit: 3})
		rest, last, _ := s.ListUsers(ctx, UserQuery{Limit: 3, Cursor: next})
		if len(page) != 3 || next == "" || len(rest) != 1 || rest[0].Username != "dee" || last != "" {
			t.Errorf("pages = %v %v", names(page), names(rest))
		}
		inAcct, _, _ := s.ListUsers(ctx, UserQuery{Homes: []store.Scope{store.AccountScope(acct)}})
		if len(inAcct) != 2 {
			t.Errorf("homes = %v", names(inAcct))
		}
		if none, _, _ := s.ListUsers(ctx, UserQuery{Homes: []store.Scope{}}); len(none) != 0 {
			t.Errorf("no homes = %v", names(none))
		}
		granted, _, _ := s.ListUsers(ctx, UserQuery{Grant: ptr(store.AccountScope(acct))})
		if len(granted) != 2 {
			t.Errorf("grant = %v", names(granted))
		}
		admins, _, _ := s.ListUsers(ctx, UserQuery{Role: auth.RoleAdmin})
		if len(admins) != 1 || admins[0].ID != admin.ID {
			t.Errorf("admins = %v", names(admins))
		}
		found, _, _ := s.ListUsers(ctx, UserQuery{Search: "PERSON D"})
		if len(found) != 1 || found[0].Username != "dee" {
			t.Errorf("search = %v", names(found))
		}
		if pct, _, _ := s.ListUsers(ctx, UserQuery{Search: "%"}); len(pct) != 0 {
			t.Errorf("search for %% = %v", names(pct))
		}

		// Updates replace details and grants, guarded by the revision.
		edit, _ := s.User(ctx, owner.ID)
		edit.Name, edit.Disabled, edit.Grants = "Robert", true, []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(acct)}}
		edit.AllowFrom, edit.Preferences, edit.Locale = []string{"192.0.2.0/24"}, json.RawMessage(`{"mode":"dark"}`), "de"
		if err := s.UpdateUser(ctx, edit, 1); err != nil || edit.Revision != 2 {
			t.Fatalf("update = %v, revision %d", err, edit.Revision)
		}
		if err := s.UpdateUser(ctx, edit, 1); !i18n.ErrConflict.Is(err) {
			t.Errorf("stale update = %v", err)
		}
		got, _ = s.User(ctx, owner.ID)
		if got.Name != "Robert" || !got.Disabled || got.Grants[0].Role != auth.RoleReadOnly || got.Locale != "de" ||
			string(got.Preferences) != `{"mode":"dark"}` || got.AllowFrom[0] != "192.0.2.0/24" || got.Revision != 2 {
			t.Errorf("updated = %+v", got)
		}
		if owners, _, _ := s.ListUsers(ctx, UserQuery{Role: auth.RoleOwner}); len(owners) != 1 {
			t.Errorf("grants follow updates: %v", names(owners))
		}
		edit.Username = "ada"
		if err := s.UpdateUser(ctx, edit, 0); !auth.ErrUsernameTaken.Is(err) {
			t.Errorf("renaming onto another = %v", err)
		}

		// Credentials change on their own, without touching the revision.
		if err := s.SetPassword(ctx, admin.ID, "$argon2id$x", true, epoch); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTOTP(ctx, admin.ID, true, 10); err != nil {
			t.Fatal(err)
		}
		if err := s.UseTOTPStep(ctx, admin.ID, 10); !errors.Is(err, ErrStepUsed) {
			t.Errorf("a used step = %v", err)
		}
		if err := s.UseTOTPStep(ctx, admin.ID, 11); err != nil {
			t.Errorf("a new step = %v", err)
		}
		locked := epoch.Add(time.Hour)
		if err := s.RecordSignIn(ctx, admin.ID, 3, locked, time.Time{}, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordSignIn(ctx, admin.ID, 0, time.Time{}, epoch, "192.0.2.7"); err != nil {
			t.Fatal(err)
		}
		got, _ = s.User(ctx, admin.ID)
		if got.PasswordHash != "$argon2id$x" || !got.MustChangePassword || !got.PasswordChangedAt.Equal(epoch) || !got.TOTP || got.TOTPStep != 11 ||
			got.FailedSignIns != 0 || !got.LockedUntil.IsZero() || !got.LastSignInAt.Equal(epoch) || got.LastSignInIP != "192.0.2.7" || got.Revision != 1 {
			t.Errorf("credentials = %+v", got)
		}
		got.Name = "Ada L."
		if err := s.UpdateUser(ctx, got, got.Revision); err != nil {
			t.Fatal(err)
		}
		if again, _ := s.User(ctx, admin.ID); again.PasswordHash != "$argon2id$x" || !again.TOTP {
			t.Error("updating details kept the credentials")
		}

		if err := s.DeleteUser(ctx, stranger.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUser(ctx, stranger.ID); !store.ErrNotFound.Is(err) {
			t.Errorf("deleting twice = %v", err)
		}
	})
}

func ptr[T any](v T) *T { return &v }

func TestCredentialsAndSessions(t *testing.T) {
	stores(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u := newUser("eve", store.ServerScope())
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}

		if err := s.ReplaceRecoveryCodes(ctx, u.ID, []string{"a", "b", "c"}); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.UseRecoveryCode(ctx, u.ID, "b"); !ok {
			t.Error("a recovery code works once")
		}
		if ok, _ := s.UseRecoveryCode(ctx, u.ID, "b"); ok {
			t.Error("and only once")
		}
		if n, _ := s.RecoveryCodesLeft(ctx, u.ID); n != 2 {
			t.Errorf("left = %d", n)
		}

		pk := &Passkey{ID: store.NewID(PrefixPasskey), User: u.ID, Name: "Laptop", CredentialID: "Y3JlZA", Credential: json.RawMessage(`{"id":"x"}`), CreatedAt: epoch}
		if err := s.AddPasskey(ctx, pk); err != nil {
			t.Fatal(err)
		}
		if err := s.AddPasskey(ctx, &Passkey{ID: store.NewID(PrefixPasskey), User: u.ID, Name: "Again", CredentialID: "Y3JlZA", Credential: json.RawMessage(`{}`), CreatedAt: epoch}); err == nil {
			t.Error("a credential registers once")
		}
		pk.Name, pk.LastUsedAt, pk.Credential = "Work laptop", epoch.Add(time.Minute), json.RawMessage(`{"id":"y"}`)
		if err := s.UpdatePasskey(ctx, pk); err != nil {
			t.Fatal(err)
		}
		found, err := s.PasskeyByCredential(ctx, "Y3JlZA")
		if err != nil || found.Name != "Work laptop" || string(found.Credential) != `{"id":"y"}` || !found.LastUsedAt.Equal(epoch.Add(time.Minute)) {
			t.Errorf("passkey = %+v, %v", found, err)
		}
		if list, _ := s.Passkeys(ctx, u.ID); len(list) != 1 {
			t.Errorf("passkeys = %v", list)
		}
		if err := s.DeletePasskey(ctx, store.NewID(PrefixUser), pk.ID); !store.ErrNotFound.Is(err) {
			t.Errorf("someone else's passkey = %v", err)
		}
		if err := s.DeletePasskey(ctx, u.ID, pk.ID); err != nil {
			t.Fatal(err)
		}

		sess := &Session{ID: store.NewID(PrefixSession), User: u.ID, Hash: "h1", CreatedAt: epoch, LastSeenAt: epoch, ExpiresAt: epoch.Add(time.Hour),
			IP: "192.0.2.1", UserAgent: "Firefox", Method: "password", SecondFactor: "totp", Persistent: true}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		other := &Session{ID: store.NewID(PrefixSession), User: u.ID, Hash: "h2", CreatedAt: epoch, LastSeenAt: epoch.Add(time.Minute), ExpiresAt: epoch.Add(2 * time.Hour), Method: "passkey"}
		if err := s.CreateSession(ctx, other); err != nil {
			t.Fatal(err)
		}
		sess.Hash, sess.SteppedUpAt, sess.LastSeenAt, sess.Pending = "h3", epoch.Add(time.Second), epoch.Add(2*time.Minute), PendingPassword
		sess.Method = "changed"
		if err := s.UpdateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SessionByHash(ctx, "h1"); !store.ErrNotFound.Is(err) {
			t.Error("a rotated session's old hash finds nothing")
		}
		got, err := s.SessionByHash(ctx, "h3")
		if err != nil || got.ID != sess.ID || got.Method != "password" || got.SecondFactor != "totp" || !got.Persistent || got.Pending != PendingPassword ||
			!got.SteppedUpAt.Equal(epoch.Add(time.Second)) {
			t.Errorf("session = %+v, %v", got, err)
		}
		list, _ := s.Sessions(ctx, u.ID)
		if len(list) != 2 || list[0].ID != sess.ID {
			t.Errorf("sessions, latest first = %v", list)
		}
		if n, err := s.DeleteSessions(ctx, u.ID, sess.ID); n != 1 || err != nil {
			t.Errorf("ending the others = %d, %v", n, err)
		}
		if err := s.DeleteExpired(ctx, epoch.Add(3*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Session(ctx, sess.ID); !store.ErrNotFound.Is(err) {
			t.Error("expired sessions are removed")
		}

		dev := &Device{ID: store.NewID(PrefixDevice), User: u.ID, Hash: "d1", Name: "Firefox on Linux", CreatedAt: epoch, LastUsedAt: epoch, ExpiresAt: epoch.Add(30 * 24 * time.Hour)}
		if err := s.AddDevice(ctx, dev); err != nil {
			t.Fatal(err)
		}
		if err := s.TouchDevice(ctx, dev.ID, epoch.Add(time.Hour), "192.0.2.9"); err != nil {
			t.Fatal(err)
		}
		if got, err := s.DeviceByHash(ctx, "d1"); err != nil || got.IP != "192.0.2.9" {
			t.Errorf("device = %+v, %v", got, err)
		}
		if err := s.DeleteDevice(ctx, u.ID, dev.ID); err != nil {
			t.Fatal(err)
		}
		if list, _ := s.Devices(ctx, u.ID); len(list) != 0 {
			t.Errorf("devices = %v", list)
		}

		tok := &Token{ID: store.NewID(PrefixToken), User: u.ID, Name: "deploys", Hash: "t1", Hint: "rsk_abcd", Permissions: []string{"web.*"},
			Accounts: []store.ID{store.NewID("acct")}, AllowFrom: []string{"198.51.100.0/24"}, ExpiresAt: epoch.Add(24 * time.Hour), CreatedAt: epoch}
		if err := s.CreateToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
		forever := &Token{ID: store.NewID(PrefixToken), User: u.ID, Name: "backups", Hash: "t2", CreatedAt: epoch.Add(time.Second)}
		if err := s.CreateToken(ctx, forever); err != nil {
			t.Fatal(err)
		}
		if err := s.TouchToken(ctx, tok.ID, epoch.Add(time.Minute), "198.51.100.4"); err != nil {
			t.Fatal(err)
		}
		got2, err := s.TokenByHash(ctx, "t1")
		if err != nil || got2.Permissions[0] != "web.*" || got2.Accounts[0] != tok.Accounts[0] || got2.AllowFrom[0] != "198.51.100.0/24" ||
			got2.LastUsedIP != "198.51.100.4" || !got2.ExpiresAt.Equal(tok.ExpiresAt) {
			t.Errorf("token = %+v, %v", got2, err)
		}
		if f, _ := s.Token(ctx, forever.ID); f.Permissions == nil || f.Accounts == nil || !f.ExpiresAt.IsZero() {
			t.Errorf("a token without limits = %+v", f)
		}
		if list, _ := s.Tokens(ctx, u.ID); len(list) != 2 || list[0].ID != forever.ID {
			t.Errorf("tokens, newest first = %v", list)
		}
		if err := s.DeleteExpired(ctx, epoch.Add(48*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Token(ctx, tok.ID); !store.ErrNotFound.Is(err) {
			t.Error("expired tokens are removed")
		}
		if _, err := s.Token(ctx, forever.ID); err != nil {
			t.Error("tokens without an expiry stay")
		}
		if n, _ := s.DeleteTokens(ctx, u.ID); n != 1 {
			t.Errorf("revoked %d", n)
		}

		key := &SSHKey{ID: store.NewID(PrefixSSHKey), User: u.ID, Name: "laptop", Type: "ssh-ed25519", Fingerprint: "SHA256:abc", PublicKey: "ssh-ed25519 AAAA", CreatedAt: epoch}
		if err := s.AddSSHKey(ctx, key); err != nil {
			t.Fatal(err)
		}
		again := *key
		again.ID, again.Name = store.NewID(PrefixSSHKey), "copy"
		if err := s.AddSSHKey(ctx, &again); !auth.ErrSSHKeyExists.Is(err) || !strings.Contains(err.Error(), "laptop") {
			t.Errorf("the same key twice = %v", err)
		}
		if keys, _ := s.SSHKeys(ctx, u.ID); len(keys) != 1 || keys[0].Fingerprint != "SHA256:abc" {
			t.Errorf("keys = %v", keys)
		}
		if err := s.DeleteSSHKey(ctx, u.ID, key.ID); err != nil {
			t.Fatal(err)
		}

		inv := &Invite{ID: store.NewID(PrefixInvite), User: u.ID, Hash: "i1", Purpose: InviteReset, CreatedAt: epoch, ExpiresAt: epoch.Add(time.Hour)}
		if err := s.CreateInvite(ctx, inv); err != nil {
			t.Fatal(err)
		}
		if got, err := s.InviteByHash(ctx, "i1"); err != nil || got.Purpose != InviteReset {
			t.Errorf("invite = %+v, %v", got, err)
		}
		if err := s.UseInvite(ctx, inv.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.UseInvite(ctx, inv.ID); !store.ErrNotFound.Is(err) {
			t.Errorf("a link works once: %v", err)
		}

		provider := store.NewID("idp")
		if err := s.LinkIdentity(ctx, &Link{User: u.ID, Provider: provider, Subject: "uid=eve", Username: "eve", LinkedAt: epoch}); err != nil {
			t.Fatal(err)
		}
		if got, err := s.UserByIdentity(ctx, provider, "uid=eve"); err != nil || got.ID != u.ID {
			t.Errorf("by identity = %v, %v", got, err)
		}
		someone := newUser("fay", store.ServerScope())
		s.CreateUser(ctx, someone)
		if err := s.LinkIdentity(ctx, &Link{User: someone.ID, Provider: provider, Subject: "uid=eve", LinkedAt: epoch}); err == nil {
			t.Error("an identity links to one user")
		}
		if links, _ := s.Links(ctx, u.ID); len(links) != 1 {
			t.Errorf("links = %v", links)
		}
		if err := s.UnlinkIdentity(ctx, u.ID, provider); err != nil {
			t.Fatal(err)
		}

		if err := s.EnableMFA(ctx, &MFAEnrollment{User: u.ID, Method: "push", EnabledAt: epoch}); err != nil {
			t.Fatal(err)
		}
		if list, _ := s.MFAMethods(ctx, u.ID); len(list) != 1 || list[0].Method != "push" {
			t.Errorf("mfa = %v", list)
		}
		if err := s.DisableMFA(ctx, u.ID, "push"); err != nil {
			t.Fatal(err)
		}

		if code, _ := s.SetupCode(ctx); code != "" {
			t.Errorf("setup code = %q", code)
		}
		s.SetSetupCode(ctx, "abc")
		s.SetSetupCode(ctx, "def")
		if code, _ := s.SetupCode(ctx); code != "def" {
			t.Errorf("setup code = %q", code)
		}
		s.SetSetupCode(ctx, "")
		if code, _ := s.SetupCode(ctx); code != "" {
			t.Errorf("cleared setup code = %q", code)
		}

		// Deleting a user takes everything of theirs along.
		s.CreateSession(ctx, &Session{ID: store.NewID(PrefixSession), User: u.ID, Hash: "h9", CreatedAt: epoch, LastSeenAt: epoch, ExpiresAt: epoch.Add(time.Hour), Method: "password"})
		s.CreateToken(ctx, &Token{ID: store.NewID(PrefixToken), User: u.ID, Name: "x", Hash: "t9", CreatedAt: epoch})
		if err := s.DeleteUser(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SessionByHash(ctx, "h9"); !store.ErrNotFound.Is(err) {
			t.Error("the user's sessions went with them")
		}
		if _, err := s.TokenByHash(ctx, "t9"); !store.ErrNotFound.Is(err) {
			t.Error("the user's tokens went with them")
		}
	})
}
