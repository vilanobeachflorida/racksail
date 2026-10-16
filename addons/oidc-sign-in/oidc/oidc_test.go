package oidc_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/addons/oidc-sign-in/oidc"
	"github.com/vilanobeachflorida/racksail/addons/oidc-sign-in/oidc/oidctest"
)

func keyFor(t *testing.T, alg string) crypto.Signer {
	t.Helper()
	var key crypto.Signer
	var err error
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	case "ES256":
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "ES384":
		key, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "ES512":
		key, err = ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	case "EdDSA":
		_, key, err = ed25519.GenerateKey(rand.Reader)
	}
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func keySet(t *testing.T, jwks ...map[string]any) *oidc.KeySet {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"keys": jwks})
	if err != nil {
		t.Fatal(err)
	}
	ks, err := oidc.ParseKeySet(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

var claims = map[string]any{"iss": "https://id.example.com", "sub": "ada"}

func TestVerifyEveryAlgorithm(t *testing.T) {
	for _, alg := range oidc.Algorithms {
		t.Run(alg, func(t *testing.T) {
			key := keyFor(t, alg)
			ks := keySet(t, oidctest.PublicJWK(key, "k1", alg))
			token := oidctest.Sign(key, map[string]any{"alg": alg, "kid": "k1"}, claims)
			payload, err := ks.Verify(token, oidc.Algorithms)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(payload), `"sub":"ada"`) {
				t.Errorf("payload %s", payload)
			}
			parts := strings.Split(token, ".")
			other := oidctest.Sign(key, map[string]any{"alg": alg, "kid": "k1"}, map[string]any{"sub": "eve"})
			forged := parts[0] + "." + strings.Split(other, ".")[1] + "." + parts[2]
			if _, err := ks.Verify(forged, oidc.Algorithms); err == nil || errors.Is(err, oidc.ErrUnknownKey) {
				t.Errorf("claims under another token's signature: %v", err)
			}
		})
	}
}

func TestVerifyRefuses(t *testing.T) {
	rsaKey := keyFor(t, "RS256")
	ecKey := keyFor(t, "ES256")
	ks := keySet(t, oidctest.PublicJWK(rsaKey, "rsa", "RS256"), oidctest.PublicJWK(ecKey, "ec", "ES256"))
	for name, c := range map[string]struct {
		token string
		algs  []string
		want  error
	}{
		"no signature":           {oidctest.Sign(rsaKey, map[string]any{"alg": "none", "kid": "rsa"}, claims), oidc.Algorithms, nil},
		"the client's secret":    {oidctest.Sign(rsaKey, map[string]any{"alg": "HS256", "kid": "rsa"}, claims), []string{"HS256", "RS256"}, nil},
		"an algorithm not taken": {oidctest.Sign(rsaKey, map[string]any{"alg": "RS256", "kid": "rsa"}, claims), []string{"ES256"}, nil},
		"an unknown key":         {oidctest.Sign(rsaKey, map[string]any{"alg": "RS256", "kid": "gone"}, claims), oidc.Algorithms, oidc.ErrUnknownKey},
		"the wrong kind of key":  {oidctest.Sign(rsaKey, map[string]any{"alg": "ES256", "kid": "rsa"}, claims), oidc.Algorithms, oidc.ErrUnknownKey},
		"extensions":             {oidctest.Sign(rsaKey, map[string]any{"alg": "RS256", "kid": "rsa", "crit": []string{"exp"}}, claims), oidc.Algorithms, nil},
		"not a JWT":              {"a.b", oidc.Algorithms, nil},
		"not base64":             {"!!.e30.sig", oidc.Algorithms, nil},
	} {
		_, err := ks.Verify(c.token, c.algs)
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without a kid, the one key that fits is used.
	if _, err := ks.Verify(oidctest.Sign(ecKey, map[string]any{"alg": "ES256"}, claims), oidc.Algorithms); err != nil {
		t.Errorf("no kid and one EC key: %v", err)
	}
}

func TestParseKeySet(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oidc.ParseKeySet([]byte(`{"keys":[` + mustJSON(t, oidctest.PublicJWK(small, "small", "RS256")) + `]}`)); err == nil {
		t.Error("a 1024-bit RSA key was taken")
	}
	ec := oidctest.PublicJWK(keyFor(t, "ES256"), "ec", "ES256")
	ec["y"] = ec["x"]
	if _, err := oidc.ParseKeySet([]byte(`{"keys":[` + mustJSON(t, ec) + `]}`)); err == nil {
		t.Error("a point off its curve was taken")
	}
	enc := oidctest.PublicJWK(keyFor(t, "RS256"), "enc", "RS256")
	enc["use"] = "enc"
	ks, err := oidc.ParseKeySet([]byte(`{"keys":[` + mustJSON(t, enc) + `,{"kty":"oct","k":"c2VjcmV0"},{"kty":"OKP","crv":"X25519","x":"abc"}]}`))
	if err != nil || len(ks.Keys) != 0 {
		t.Errorf("keys that aren't for signatures: %v %v", ks, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCheckClaims(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	good := func() oidc.Claims {
		return oidc.Claims{
			"iss": "https://id.example.com", "aud": "racksail", "sub": "ada", "nonce": "n-1",
			"exp": json.Number("1800000300"), "iat": json.Number("1800000000"),
		}
	}
	if err := good().Check("https://id.example.com", "racksail", "n-1", now); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(oidc.Claims){
		"another issuer":         func(c oidc.Claims) { c["iss"] = "https://id.example.org" },
		"another audience":       func(c oidc.Claims) { c["aud"] = "other" },
		"several audiences":      func(c oidc.Claims) { c["aud"] = []any{"racksail", "other"} },
		"another party":          func(c oidc.Claims) { c["azp"] = "other" },
		"expired":                func(c oidc.Claims) { c["exp"] = json.Number("1799999000") },
		"no expiry":              func(c oidc.Claims) { delete(c, "exp") },
		"issued in the future":   func(c oidc.Claims) { c["iat"] = json.Number("1800001000") },
		"another nonce":          func(c oidc.Claims) { c["nonce"] = "n-2" },
		"nobody":                 func(c oidc.Claims) { delete(c, "sub") },
		"an expiry not a number": func(c oidc.Claims) { c["exp"] = "tomorrow" },
	} {
		c := good()
		change(c)
		if err := c.Check("https://id.example.com", "racksail", "n-1", now); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	c := good()
	c["aud"], c["azp"] = []any{"racksail", "other"}, "racksail"
	if err := c.Check("https://id.example.com", "racksail", "n-1", now); err != nil {
		t.Errorf("several audiences with this client as the party: %v", err)
	}
	if err := good().Check("https://id.example.com", "racksail", "", now); err == nil {
		t.Error("a sign-in without a nonce")
	}
	if err := good().Check("https://id.example.com", "racksail", "n-1", now.Add(301*time.Second+oidc.Leeway)); err == nil {
		t.Error("expired beyond the leeway")
	}
}

func TestDiscover(t *testing.T) {
	ctx := context.Background()
	p := oidctest.New(t)
	md, err := oidc.Discover(ctx, http.DefaultClient, p.Issuer)
	if err != nil {
		t.Fatal(err)
	}
	if md.TokenEndpoint != p.Issuer+"/token" || md.JWKSURI != p.Issuer+"/keys" {
		t.Errorf("metadata %+v", md)
	}
	if _, err := oidc.Discover(ctx, http.DefaultClient, p.Issuer+"/"); err == nil || !strings.Contains(err.Error(), "exactly") {
		t.Errorf("an issuer written differently: %v", err)
	}
	if _, err := oidc.Discover(ctx, http.DefaultClient, "http://id.example.com"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("http to another machine: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": "http://" + r.Host, "authorization_endpoint": "http://id.example.com/authorize",
			"token_endpoint": "http://" + r.Host + "/token", "jwks_uri": "http://" + r.Host + "/keys",
		})
	}))
	defer bad.Close()
	if _, err := oidc.Discover(ctx, http.DefaultClient, bad.URL); err == nil || !strings.Contains(err.Error(), "authorization endpoint") {
		t.Errorf("an endpoint over http elsewhere: %v", err)
	}
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html></html>"))
	}))
	defer html.Close()
	if _, err := oidc.Discover(ctx, http.DefaultClient, html.URL); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("a page instead of metadata: %v", err)
	}
}

func TestPKCE(t *testing.T) {
	// The example of RFC 7636, appendix B.
	if got := oidc.Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("challenge %s", got)
	}
	a, b := oidc.Random(), oidc.Random()
	if a == b || len(a) != 43 {
		t.Errorf("random values %q and %q", a, b)
	}
}
