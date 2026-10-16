package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// authenticator is a software passkey: what a phone or a security key does
// when a browser asks it to make a credential or sign a challenge.
type authenticator struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	credID []byte
	user   []byte
	rpID   string
	origin string
}

func newAuthenticator(t *testing.T, rpID, origin string) *authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &authenticator{t: t, key: key, credID: id, rpID: rpID, origin: origin}
}

var b64 = base64.RawURLEncoding

// cbor writes the few CBOR shapes WebAuthn needs.
type cbor []byte

func (c cbor) head(major byte, n int) cbor {
	switch {
	case n < 24:
		return append(c, major<<5|byte(n))
	case n < 256:
		return append(c, major<<5|24, byte(n))
	default:
		return append(c, major<<5|25, byte(n>>8), byte(n))
	}
}

func (c cbor) int(n int) cbor {
	if n < 0 {
		return c.head(1, -1-n)
	}
	return c.head(0, n)
}

func (c cbor) bytes(b []byte) cbor { return append(c.head(2, len(b)), b...) }
func (c cbor) text(s string) cbor  { return append(c.head(3, len(s)), s...) }
func (c cbor) mapOf(n int) cbor    { return c.head(5, n) }

func (a *authenticator) coseKey() []byte {
	// The uncompressed point is 0x04, then X and Y.
	point, err := a.key.PublicKey.Bytes()
	if err != nil {
		panic(err)
	}
	x, y := point[1:33], point[33:65]
	return cbor(nil).mapOf(5).int(1).int(2).int(3).int(-7).int(-1).int(1).int(-2).bytes(x).int(-3).bytes(y)
}

func (a *authenticator) authData(flags byte, attested bool) []byte {
	rp := sha256.Sum256([]byte(a.rpID))
	out := append(rp[:], flags, 0, 0, 0, 0)
	if attested {
		out = append(out, make([]byte, 16)...)
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.credID)))
		out = append(out, a.credID...)
		out = append(out, a.coseKey()...)
	}
	return out
}

func (a *authenticator) clientData(typ, challenge string) []byte {
	raw, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.origin, "crossOrigin": false})
	return raw
}

// challengeOf reads the challenge and user handle out of the options a
// ceremony started with.
func challengeOf(t *testing.T, options json.RawMessage) (challenge string, user []byte) {
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &o); err != nil {
		t.Fatal(err)
	}
	if o.PublicKey.User.ID != "" {
		user, _ = b64.DecodeString(o.PublicKey.User.ID)
	}
	return o.PublicKey.Challenge, user
}

// create answers navigator.credentials.create().
func (a *authenticator) create(options json.RawMessage) json.RawMessage {
	challenge, user := challengeOf(a.t, options)
	a.user = user
	att := cbor(nil).mapOf(3).text("fmt").text("none").text("attStmt").mapOf(0).text("authData").bytes(a.authData(0x45, true))
	raw, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(a.clientData("webauthn.create", challenge)), "attestationObject": b64.EncodeToString(att),
			"transports": []string{"internal"},
		},
		"clientExtensionResults": map[string]any{}, "authenticatorAttachment": "platform",
	})
	return raw
}

// get answers navigator.credentials.get().
func (a *authenticator) get(options json.RawMessage) json.RawMessage {
	challenge, _ := challengeOf(a.t, options)
	data := a.authData(0x05, false)
	client := a.clientData("webauthn.get", challenge)
	sum := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, data...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(client), "authenticatorData": b64.EncodeToString(data),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(a.user),
		},
		"clientExtensionResults": map[string]any{},
	})
	return raw
}

const origin = "https://panel.example.test:8443"

func TestPasskeys(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	acct := h.createAccount("shop", "")
	bob := h.user("bob", store.AccountScope(acct), auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(acct)})
	actor, _ := h.signIn("bob")
	key := newAuthenticator(t, "panel.example.test", origin)

	ceremony, options, err := h.svc.BeginPasskeyRegistration(ctx, actor, origin)
	if err != nil {
		t.Fatal(err)
	}
	view, err := h.svc.FinishPasskeyRegistration(ctx, actor, ceremony, "Laptop", key.create(options), origin)
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "Laptop" || string(key.user) != string(bob.ID) {
		t.Errorf("passkey = %+v, handle %q", view, key.user)
	}
	if _, err := h.svc.FinishPasskeyRegistration(ctx, actor, ceremony, "Again", key.create(options), origin); !auth.ErrChallengeExpired.Is(err) {
		t.Errorf("a ceremony counts once: %v", err)
	}

	// Signing in with the passkey alone.
	ceremony, options, err = h.svc.BeginPasskeySignIn(ctx, origin, client)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.svc.FinishPasskeySignIn(ctx, ceremony, key.get(options), origin, false, client)
	if err != nil {
		t.Fatal(err)
	}
	if a := h.actor(res.Issued.Secret); a.ID != bob.ID || res.Issued.Session.Method != "passkey" {
		t.Errorf("signed in as %+v with %s", a, res.Issued.Session.Method)
	}

	// A passkey someone else made, or another site's, gets nowhere.
	stranger := newAuthenticator(t, "panel.example.test", origin)
	stranger.user = []byte(bob.ID)
	ceremony, options, _ = h.svc.BeginPasskeySignIn(ctx, origin, client)
	if _, err := h.svc.FinishPasskeySignIn(ctx, ceremony, stranger.get(options), origin, false, client); err == nil {
		t.Error("an unknown passkey signed in")
	}
	phish := newAuthenticator(t, "panel.example.test", "https://panel.example.test.evil.example")
	phish.key, phish.credID, phish.user = key.key, key.credID, key.user
	ceremony, options, _ = h.svc.BeginPasskeySignIn(ctx, origin, client)
	if _, err := h.svc.FinishPasskeySignIn(ctx, ceremony, phish.get(options), origin, false, client); err == nil {
		t.Error("an answer made for another origin signed in")
	}

	// As a second step after the password.
	first, err := h.svc.SignIn(ctx, SignInInput{Username: "bob", Password: password}, "", client)
	if err != nil || first.Challenge == "" || first.Methods[0] != "passkey" {
		t.Fatalf("first step = %+v, %v", first, err)
	}
	options, err = h.svc.BeginSecondStepPasskey(ctx, first.Challenge, origin)
	if err != nil {
		t.Fatal(err)
	}
	done, err := h.svc.SecondStep(ctx, SecondStepInput{Challenge: first.Challenge, Method: "passkey", Credential: key.get(options)}, client, origin)
	if err != nil || done.Issued.Session.SecondFactor != "passkey" {
		t.Fatalf("second step = %+v, %v", done, err)
	}

	// And to confirm it's them.
	me := h.actor(done.Issued.Secret)
	ceremony, options, err = h.svc.BeginStepUpPasskey(ctx, me, origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.StepUp(ctx, me, StepUpInput{Ceremony: ceremony, Credential: key.get(options)}, client, origin); err != nil {
		t.Fatalf("step-up = %v", err)
	}

	// Passkeys need the panel's name and HTTPS.
	for _, bad := range []string{"https://203.0.113.4:8443", "http://panel.example.test", "https://elsewhere.example"} {
		if err := h.svc.PasskeysAvailable(bad); !auth.ErrPasskeysUnavailable.Is(err) {
			t.Errorf("%s = %v", bad, err)
		}
	}
	if err := h.svc.PasskeysAvailable("http://localhost:7214"); err != nil {
		t.Errorf("localhost = %v", err)
	}

	keys, _ := h.svc.MyPasskeys(ctx, me)
	if len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Fatalf("passkeys = %+v", keys)
	}
	if _, err := h.svc.RenamePasskey(ctx, me, keys[0].ID, "Work laptop"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RemovePasskey(ctx, me, keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if h.eventTypes()["core.user.two_factor_changed"] != 2 {
		t.Errorf("events = %v", h.eventTypes())
	}
}

func TestLastSignInMethod(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.admin("ada")
	actor, _ := h.signIn("ada")
	key := newAuthenticator(t, "localhost", "http://localhost:7214")
	ceremony, options, err := h.svc.BeginPasskeyRegistration(ctx, actor, "http://localhost:7214")
	if err != nil {
		t.Fatal(err)
	}
	view, err := h.svc.FinishPasskeyRegistration(ctx, actor, ceremony, "", key.create(options), "http://localhost:7214")
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "Passkey" {
		t.Errorf("an unnamed passkey = %q", view.Name)
	}
	if err := h.store.SetPassword(ctx, actor.ID, "", false, h.clock.now()); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RemovePasskey(ctx, actor, view.ID); !auth.ErrLastSignInMethod.Is(err) {
		t.Errorf("removing the only way in = %v", err)
	}
}
