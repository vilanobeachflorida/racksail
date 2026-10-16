package identity

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
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

func TestCredentials(t *testing.T) {
	stores(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ada, bob := store.NewID(PrefixUser), store.NewID(PrefixUser)
		if _, err := s.Credentials(ctx, ada); !store.ErrNotFound.Is(err) {
			t.Errorf("credentials of nobody yet = %v", err)
		}
		// Writing anything of a user's makes their record.
		if err := s.SetPassword(ctx, ada, "$argon2id$x", true, epoch); err != nil {
			t.Fatal(err)
		}
		if err := s.SetPreferences(ctx, bob, json.RawMessage(`{"mode":"dark"}`)); err != nil {
			t.Fatal(err)
		}
		if users, _ := s.Users(ctx); len(users) != 2 || users[ada].IsZero() {
			t.Errorf("users = %v", users)
		}
		if err := s.SetTOTP(ctx, ada, true, 10); err != nil {
			t.Fatal(err)
		}
		if err := s.UseTOTPStep(ctx, ada, 10); !errors.Is(err, ErrStepUsed) {
			t.Errorf("a used step = %v", err)
		}
		if err := s.UseTOTPStep(ctx, ada, 9); !errors.Is(err, ErrStepUsed) {
			t.Errorf("an earlier step = %v", err)
		}
		if err := s.UseTOTPStep(ctx, ada, 11); err != nil {
			t.Errorf("a new step = %v", err)
		}
		if err := s.SetAlertChannel(ctx, ada, "provider:memo"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSSHKeysSeen(ctx, ada, []string{"SHA256:a", "SHA256:b"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordSignIn(ctx, ada, epoch.Add(time.Minute), "192.0.2.7"); err != nil {
			t.Fatal(err)
		}
		c, err := s.Credentials(ctx, ada)
		if err != nil || c.PasswordHash != "$argon2id$x" || !c.MustChangePassword || !c.PasswordChangedAt.Equal(epoch) || !c.TOTP || c.TOTPStep != 11 ||
			c.AlertChannel != "provider:memo" || len(c.SSHKeysSeen) != 2 || !c.LastSignInAt.Equal(epoch.Add(time.Minute)) || c.LastSignInIP != "192.0.2.7" {
			t.Errorf("credentials = %+v, %v", c, err)
		}
		if err := s.SetMustChangePassword(ctx, ada, false); err != nil {
			t.Fatal(err)
		}
		if c, _ := s.Credentials(ctx, ada); c.MustChangePassword || c.PasswordHash != "$argon2id$x" {
			t.Errorf("only must-change changed: %+v", c)
		}
		if c, _ := s.Credentials(ctx, bob); string(c.Preferences) != `{"mode":"dark"}` || c.PasswordHash != "" {
			t.Errorf("bob = %+v", c)
		}

		if err := s.DeleteUser(ctx, bob); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Credentials(ctx, bob); !store.ErrNotFound.Is(err) {
			t.Errorf("after deleting = %v", err)
		}
		if err := s.DeleteUser(ctx, bob); err != nil {
			t.Errorf("deleting what isn't there = %v", err)
		}
	})
}

func TestLockouts(t *testing.T) {
	stores(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u := store.NewID(PrefixUser)
		for i := 1; i < 3; i++ {
			if locked, _, err := s.RecordFailure(ctx, u, 3, time.Hour, epoch); err != nil || locked {
				t.Fatalf("failure %d = %v, %v", i, locked, err)
			}
		}
		locked, until, err := s.RecordFailure(ctx, u, 3, time.Hour, epoch.Add(time.Minute))
		if err != nil || !locked || !until.Equal(epoch.Add(time.Hour+time.Minute)) {
			t.Fatalf("the third failure = %v, %v, %v", locked, until, err)
		}
		// Failures while locked out neither count nor push the end back.
		if locked, until, _ := s.RecordFailure(ctx, u, 3, time.Hour, epoch.Add(30*time.Minute)); locked || !until.Equal(epoch.Add(time.Hour+time.Minute)) {
			t.Errorf("a failure during the lockout = %v, %v", locked, until)
		}
		c, _ := s.Credentials(ctx, u)
		if c.FailedSignIns != 0 || !c.LockedUntil.Equal(epoch.Add(time.Hour+time.Minute)) {
			t.Errorf("credentials = %+v", c)
		}
		if err := s.Unlock(ctx, u); err != nil {
			t.Fatal(err)
		}
		if c, _ := s.Credentials(ctx, u); !c.LockedUntil.IsZero() || c.FailedSignIns != 0 {
			t.Errorf("unlocked = %+v", c)
		}
		s.RecordFailure(ctx, u, 3, time.Hour, epoch.Add(2*time.Hour))
		if err := s.RecordSignIn(ctx, u, epoch.Add(2*time.Hour), "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
		if c, _ := s.Credentials(ctx, u); c.FailedSignIns != 0 {
			t.Errorf("a sign-in clears the failures: %+v", c)
		}

		// Failures at the same moment each count, and one locks out.
		var wg sync.WaitGroup
		var mu sync.Mutex
		lockouts := 0
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				locked, _, err := s.RecordFailure(ctx, u, 5, time.Hour, epoch.Add(3*time.Hour))
				if err != nil {
					t.Error(err)
				}
				if locked {
					mu.Lock()
					lockouts++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if lockouts != 1 {
			t.Errorf("%d failures at once locked out %d times", 12, lockouts)
		}
	})
}

func TestCredentialsAndSessions(t *testing.T) {
	stores(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u := &User{ID: store.NewID(PrefixUser)}

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
		if err := s.SetSessionPending(ctx, sess.ID, PendingPassword); err != nil {
			t.Fatal(err)
		}
		stepped := *sess
		stepped.Hash, stepped.SteppedUpAt, stepped.LastSeenAt = "h3", epoch.Add(time.Second), epoch.Add(2*time.Minute)
		if err := s.RotateSession(ctx, &stepped, "h1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SessionByHash(ctx, "h1"); !store.ErrNotFound.Is(err) {
			t.Error("a session's replaced hash finds nothing")
		}
		// A secret is given once: a request that read the session before
		// it changed doesn't give it another, and using it later doesn't
		// bring the old hash back.
		late := *sess
		late.Hash = "h5"
		if err := s.RotateSession(ctx, &late, "h1"); !errors.Is(err, ErrSessionRotated) {
			t.Errorf("a second new secret from the same one = %v", err)
		}
		if err := s.TouchSession(ctx, sess.ID, epoch.Add(150*time.Second), "192.0.2.7"); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Session(ctx, sess.ID); err != nil || got.Hash != "h3" || got.IP != "192.0.2.7" || !got.LastSeenAt.Equal(epoch.Add(150*time.Second)) {
			t.Errorf("after the touch = %+v, %v", got, err)
		}
		// A session given a new secret for new grants is still found by the
		// one before, for requests already on their way.
		rotated := stepped
		rotated.PreviousHash, rotated.Hash, rotated.RotatedAt, rotated.GrantsHash = "h3", "h4", epoch.Add(3*time.Minute), "g2"
		rotated.LastSeenAt, rotated.IP = epoch.Add(3*time.Minute), "192.0.2.8"
		if err := s.RotateSession(ctx, &rotated, "h3"); err != nil {
			t.Fatal(err)
		}
		for _, hash := range []string{"h3", "h4"} {
			if got, err := s.SessionByHash(ctx, hash); err != nil || got.ID != sess.ID || got.Hash != "h4" || got.PreviousHash != "h3" ||
				got.GrantsHash != "g2" || !got.RotatedAt.Equal(epoch.Add(3*time.Minute)) || got.IP != "192.0.2.8" {
				t.Errorf("by %s = %+v, %v", hash, got, err)
			}
		}
		got, err := s.SessionByHash(ctx, "h4")
		if err != nil || got.ID != sess.ID || got.Method != "password" || got.SecondFactor != "totp" || !got.Persistent || got.Pending != PendingPassword ||
			!got.SteppedUpAt.Equal(epoch.Add(time.Second)) || !got.ExpiresAt.Equal(sess.ExpiresAt) {
			t.Errorf("session = %+v, %v", got, err)
		}
		gone := Session{ID: store.NewID(PrefixSession), Hash: "h6"}
		if err := s.RotateSession(ctx, &gone, "h0"); !store.ErrNotFound.Is(err) {
			t.Errorf("a session that isn't there = %v", err)
		}
		if err := s.TouchSession(ctx, gone.ID, epoch, ""); !store.ErrNotFound.Is(err) {
			t.Errorf("touching a session that isn't there = %v", err)
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

		provider := store.NewID("signprov")
		if err := s.LinkIdentity(ctx, &Link{User: u.ID, Provider: provider, Subject: "uid=eve", Username: "eve", LinkedAt: epoch}); err != nil {
			t.Fatal(err)
		}
		if got, err := s.LinkedUser(ctx, provider, "uid=eve"); err != nil || got != u.ID {
			t.Errorf("by identity = %v, %v", got, err)
		}
		someone := store.NewID(PrefixUser)
		if err := s.LinkIdentity(ctx, &Link{User: someone, Provider: provider, Subject: "uid=eve", LinkedAt: epoch}); !auth.ErrIdentityTaken.Is(err) {
			t.Errorf("an identity links to one user: %v", err)
		}
		// Linking again replaces the user's link to the provider.
		if err := s.LinkIdentity(ctx, &Link{User: u.ID, Provider: provider, Subject: "uid=eve2", Username: "eve2", LinkedAt: epoch}); err != nil {
			t.Fatal(err)
		}
		if links, _ := s.Links(ctx, u.ID); len(links) != 1 || links[0].Subject != "uid=eve2" {
			t.Errorf("links = %v", links)
		}
		if _, err := s.LinkedUser(ctx, provider, "uid=eve"); !store.ErrNotFound.Is(err) {
			t.Errorf("the replaced link = %v", err)
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
