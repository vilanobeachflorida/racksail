package identity

import (
	"context"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// What acts for a person after the request that signed them in, such as
// the frame of an add-on's screen, asks the directory whether that sign-in
// still works: it stops with the session, however the session ends, and
// with the token, once it is revoked or expired; and asking keeps a session
// from going idle.
func TestSignedIn(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	checker, ok := h.svc.Directory().(auth.SignInChecker)
	if !ok {
		t.Fatal("the directory of users doesn't say who is still signed in")
	}
	if checker.SignedIn(ctx, nil) {
		t.Error("nobody is signed in")
	}

	actor, secret := h.signIn("ada")
	if !checker.SignedIn(ctx, actor) {
		t.Fatal("a session just started")
	}
	// Asking counts as using the session, which then doesn't go idle.
	for range 3 {
		h.clock.add(25 * time.Minute)
		if !checker.SignedIn(ctx, actor) {
			t.Fatal("a session in use went idle")
		}
	}
	if _, err := h.authenticate(secret); err != nil {
		t.Errorf("the session after its uses: %v", err)
	}
	h.clock.add(time.Hour + time.Minute)
	if checker.SignedIn(ctx, actor) {
		t.Error("a session left idle")
	}

	actor, _ = h.signIn("ada")
	if err := h.svc.SignOut(ctx, actor); err != nil {
		t.Fatal(err)
	}
	if checker.SignedIn(ctx, actor) {
		t.Error("a session its person signed out of")
	}
	actor, _ = h.signIn("ada")
	other, _ := h.signIn("ada")
	if _, err := h.svc.EndOtherSessions(ctx, actor); err != nil {
		t.Fatal(err)
	}
	if checker.SignedIn(ctx, other) || !checker.SignedIn(ctx, actor) {
		t.Error("ending the other sessions ends only theirs")
	}

	created, err := h.svc.CreateToken(ctx, actor, TokenInput{Name: "deploys"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.bearer(created.Token, client.IP)
	if err != nil {
		t.Fatal(err)
	}
	if !checker.SignedIn(ctx, token) {
		t.Fatal("a token just made")
	}
	if err := h.svc.RevokeMyToken(ctx, actor, created.ID); err != nil {
		t.Fatal(err)
	}
	if checker.SignedIn(ctx, token) {
		t.Error("a revoked token")
	}
	soon := h.clock.now().Add(time.Hour)
	short, err := h.svc.CreateToken(ctx, actor, TokenInput{Name: "short", ExpiresAt: &soon})
	if err != nil {
		t.Fatal(err)
	}
	if token, err = h.bearer(short.Token, client.IP); err != nil {
		t.Fatal(err)
	}
	h.clock.add(2 * time.Hour)
	if checker.SignedIn(ctx, token) {
		t.Error("an expired token")
	}

	// Turned off, a person's sessions stop.
	h.user("bob", store.ServerScope(), auth.Grant{Role: auth.RoleReadOnly, Scope: store.ServerScope()})
	bob, _ := h.signIn("bob")
	if !checker.SignedIn(ctx, bob) {
		t.Fatal("bob's session")
	}
	if _, err := h.update(root(), bob.ID, func(s *UserSpec) { s.Disabled = true }); err != nil {
		t.Fatal(err)
	}
	if checker.SignedIn(ctx, bob) {
		t.Error("the session of someone turned off")
	}
	// Signed in without a session, as racksail dev signs its developer in,
	// there is nothing to sign out of.
	if !checker.SignedIn(ctx, &auth.Actor{Type: auth.ActorUser, ID: store.NewID(PrefixUser)}) {
		t.Error("a person signed in without a session")
	}
}
