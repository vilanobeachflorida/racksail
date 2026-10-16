package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/addons/oidc-sign-in/oidc/oidctest"
)

const callback = "https://panel.example.com:8443/api/v1/auth/providers/signprov_01/callback"

// panel plays the panel: it calls the add-on's methods over its protocol
// and keeps what begin returns until the browser comes back.
type panel struct {
	t       *testing.T
	handler http.Handler
	idp     *oidctest.Server
	extra   map[string]any
}

func newPanel(t *testing.T, idp *oidctest.Server, settings map[string]any) *panel {
	return &panel{t: t, handler: addon(slog.New(slog.DiscardHandler)).Handler(), idp: idp, extra: settings}
}

func (p *panel) settings() map[string]any {
	s := map[string]any{"issuer": p.idp.Issuer, "clientId": p.idp.ClientID, "secret": p.idp.ClientSecret}
	if p.idp.CA != nil {
		s["caCertificate"] = string(p.idp.CA)
	}
	for k, v := range p.extra {
		if v == nil {
			delete(s, k)
		} else {
			s[k] = v
		}
	}
	return s
}

func (p *panel) call(method string, input any) (int, map[string]any) {
	p.t.Helper()
	body, err := json.Marshal(map[string]any{"input": map[string]any{"settings": p.settings(), "input": input}})
	if err != nil {
		p.t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/provide/auth-provider/"+method, bytes.NewReader(body)))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		p.t.Fatalf("%s answered %q", method, rec.Body.String())
	}
	return rec.Code, out
}

// begin starts a sign-in and returns where the browser goes and the state
// the panel keeps.
func (p *panel) begin() (string, string) {
	p.t.Helper()
	code, out := p.call("begin", map[string]string{"callback": callback})
	if code != http.StatusOK {
		p.t.Fatalf("begin: %d %v", code, out)
	}
	return out["redirect"].(string), out["state"].(string)
}

func (p *panel) complete(state string, query url.Values) (int, map[string]any) {
	p.t.Helper()
	return p.call("complete", map[string]any{"state": state, "callback": callback, "query": query})
}

// signIn goes through a whole sign-in as sub.
func (p *panel) signIn(sub string) (int, map[string]any) {
	p.t.Helper()
	redirect, state := p.begin()
	return p.complete(state, p.idp.SignIn(p.t, redirect, sub))
}

func TestDescribe(t *testing.T) {
	p := newPanel(t, oidctest.New(t), nil)
	if code, out := p.call("describe", map[string]any{}); code != http.StatusOK || out["method"] != "redirect" {
		t.Errorf("describe: %d %v", code, out)
	}
}

func TestSignIn(t *testing.T) {
	idp := oidctest.NewTLS(t)
	idp.Claims = map[string]any{
		"preferred_username": "ada", "name": "Ada Lovelace", "email": "ada@example.com", "email_verified": true,
		"groups": []string{"staff", "engines"},
	}
	p := newPanel(t, idp, map[string]any{"scopes": []string{"profile", "email", "groups"}, "prompt": "login"})
	redirect, state := p.begin()
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": "racksail", "redirect_uri": callback, "scope": "openid profile email groups",
		"code_challenge_method": "S256", "prompt": "login", "tenant": "example",
	} {
		if q.Get(k) != want {
			t.Errorf("%s is %q, want %q", k, q.Get(k), want)
		}
	}
	if len(q.Get("state")) < 43 || len(q.Get("nonce")) < 43 || len(q.Get("code_challenge")) != 43 {
		t.Errorf("state, nonce and challenge: %v", q)
	}
	if strings.Contains(redirect, "verifier") {
		t.Error("the PKCE verifier went to the browser")
	}

	code, out := p.complete(state, idp.SignIn(t, redirect, "user-1"))
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	want := map[string]any{"subject": "user-1", "username": "ada", "name": "Ada Lovelace", "email": "ada@example.com", "groups": []any{"staff", "engines"}}
	if !reflect.DeepEqual(out["identity"], want) {
		t.Errorf("identity %v, want %v", out["identity"], want)
	}

	// A code is only good once, and a state only for its own sign-in.
	again := idp.SignIn(t, redirect, "user-1")
	if code, out := p.complete(state, again); code != http.StatusOK {
		t.Errorf("a new code for the same sign-in: %d %v", code, out)
	}
	if code, out := p.complete(state, again); code != http.StatusBadGateway || !strings.Contains(out["detail"].(string), "didn't take the code") {
		t.Errorf("a code used twice: %d %v", code, out)
	}
	_, other := p.begin()
	if code, out := p.complete(other, idp.SignIn(t, redirect, "user-1")); code != http.StatusBadGateway || !strings.Contains(out["detail"].(string), "another sign-in") {
		t.Errorf("a response for another sign-in: %d %v", code, out)
	}
}

func TestPublicClientsAndPostedSecrets(t *testing.T) {
	idp := oidctest.New(t)
	idp.AuthMethods = []string{"client_secret_post"}
	if code, out := newPanel(t, idp, nil).signIn("user-1"); code != http.StatusOK || out["identity"] == nil {
		t.Errorf("a secret in the form: %d %v", code, out)
	}
	idp.AuthMethods = []string{"none"}
	if code, out := newPanel(t, idp, map[string]any{"secret": nil}).signIn("user-1"); code != http.StatusOK || out["identity"] == nil {
		t.Errorf("a public client: %d %v", code, out)
	}
}

func TestRefusals(t *testing.T) {
	idp := oidctest.New(t)
	idp.Claims = map[string]any{"preferred_username": "ada", "email": "ada@example.org", "email_verified": true, "groups": []string{"staff"}}
	p := newPanel(t, idp, nil)
	if code, out := p.signIn(""); code != http.StatusOK || out["refused"] != true || out["reason"] != "The person turned the sign-in down." {
		t.Errorf("turned down at the provider: %d %v", code, out)
	}
	p.extra = map[string]any{"allowedDomains": []string{"@Example.com"}}
	if code, out := p.signIn("user-1"); code != http.StatusOK || out["refused"] != true || !strings.Contains(out["reason"].(string), "domain") {
		t.Errorf("an address at another domain: %d %v", code, out)
	}
	p.extra = map[string]any{"allowedGroups": []string{"admins"}}
	if code, out := p.signIn("user-1"); code != http.StatusOK || out["refused"] != true || !strings.Contains(out["reason"].(string), "group") {
		t.Errorf("outside the allowed groups: %d %v", code, out)
	}
	p.extra = map[string]any{"allowedGroups": []string{"staff"}, "allowedDomains": []string{"example.org"}}
	if code, out := p.signIn("user-1"); code != http.StatusOK || out["identity"] == nil {
		t.Errorf("in an allowed group at an allowed domain: %d %v", code, out)
	}
}

func TestUnverifiedEmail(t *testing.T) {
	idp := oidctest.New(t)
	idp.Claims = map[string]any{"preferred_username": "ada", "name": "Ada", "email": "admin@example.com", "email_verified": false}
	p := newPanel(t, idp, nil)
	_, out := p.signIn("user-1")
	if id := out["identity"].(map[string]any); id["email"] != nil {
		t.Errorf("an address the provider hasn't verified was passed on: %v", id)
	}
	p.extra = map[string]any{"allowedDomains": []string{"example.com"}}
	if _, out := p.signIn("user-1"); out["refused"] != true {
		t.Errorf("an unverified address at an allowed domain: %v", out)
	}
	p.extra = map[string]any{"acceptUnverifiedEmail": true}
	if _, out := p.signIn("user-1"); out["identity"].(map[string]any)["email"] != "admin@example.com" {
		t.Errorf("with unverified addresses accepted: %v", out)
	}
}

func TestUserInfo(t *testing.T) {
	idp := oidctest.New(t)
	idp.UserInfo = map[string]any{"preferred_username": "grace", "given_name": "Grace", "family_name": "Hopper", "email": "grace@example.com", "email_verified": "true"}
	_, out := newPanel(t, idp, nil).signIn("user-2")
	want := map[string]any{"subject": "user-2", "username": "grace", "name": "Grace Hopper", "email": "grace@example.com"}
	if !reflect.DeepEqual(out["identity"], want) {
		t.Errorf("identity %v, want %v", out["identity"], want)
	}
	idp.UserInfo["sub"] = "someone-else"
	if code, out := newPanel(t, idp, nil).signIn("user-2"); code != http.StatusBadGateway || !strings.Contains(out["detail"].(string), "someone else") {
		t.Errorf("UserInfo about someone else: %d %v", code, out)
	}
}

func TestBadTokens(t *testing.T) {
	for name, c := range map[string]struct {
		mangle func(header, claims map[string]any)
		detail string
	}{
		"another audience":   {func(h, c map[string]any) { c["aud"] = "other" }, "another client"},
		"another nonce":      {func(h, c map[string]any) { c["nonce"] = "replayed" }, "nonce"},
		"another issuer":     {func(h, c map[string]any) { c["iss"] = "https://id.example.org" }, "issued by"},
		"expired":            {func(h, c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, "expired"},
		"unsigned":           {func(h, c map[string]any) { h["alg"] = "none" }, "isn't accepted"},
		"the client secret":  {func(h, c map[string]any) { h["alg"] = "HS256" }, "isn't accepted"},
		"a key nobody knows": {func(h, c map[string]any) { h["kid"] = "forged" }, "key set doesn't have"},
	} {
		idp := oidctest.New(t)
		idp.Mangle = c.mangle
		code, out := newPanel(t, idp, nil).signIn("user-1")
		detail, _ := out["detail"].(string)
		if code != http.StatusBadGateway || !strings.Contains(detail, c.detail) {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}

func TestKeyRotation(t *testing.T) {
	idp := oidctest.New(t)
	p := newPanel(t, idp, nil)
	if code, out := p.signIn("user-1"); code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	for _, alg := range []string{"ES256", "EdDSA", "PS256", "ES384"} {
		idp.Rotate(t, alg)
		if code, out := p.signIn("user-1"); code != http.StatusOK || out["identity"] == nil {
			t.Errorf("after rotating to %s: %d %v", alg, code, out)
		}
	}
}

func TestIssuerParameter(t *testing.T) {
	idp := oidctest.New(t)
	idp.IssParameter = true
	p := newPanel(t, idp, nil)
	if code, out := p.signIn("user-1"); code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	redirect, state := p.begin()
	q := idp.SignIn(t, redirect, "user-1")
	q.Set("iss", "https://evil.example.com")
	if code, out := p.complete(state, q); code != http.StatusBadGateway || !strings.Contains(out["detail"].(string), "isn't from") {
		t.Errorf("another issuer's response: %d %v", code, out)
	}
	q.Del("iss")
	if code, out := p.complete(state, q); code != http.StatusBadGateway {
		t.Errorf("no issuer from a provider that always names it: %d %v", code, out)
	}
	if idp.Tokens() != 1 {
		t.Errorf("responses that were refused were traded for tokens: %d", idp.Tokens())
	}
}

func TestReturnChecks(t *testing.T) {
	idp := oidctest.New(t)
	p := newPanel(t, idp, nil)
	redirect, state := p.begin()
	q := idp.SignIn(t, redirect, "user-1")
	if code, out := p.call("complete", map[string]any{"state": state, "callback": "https://elsewhere.example.com/cb", "query": q}); code != http.StatusBadGateway {
		t.Errorf("another callback: %d %v", code, out)
	}
	if code, out := p.call("complete", map[string]any{"state": "bm90IGpzb24=", "callback": callback, "query": q}); code != http.StatusUnprocessableEntity {
		t.Errorf("a state that can't be read: %d %v", code, out)
	}
	q.Del("code")
	if code, out := p.complete(state, q); code != http.StatusBadGateway || !strings.Contains(out["detail"].(string), "without a code") {
		t.Errorf("no code: %d %v", code, out)
	}
	if code, out := p.call("begin", map[string]string{"callback": "javascript:alert(1)"}); code != http.StatusUnprocessableEntity {
		t.Errorf("a callback that isn't an address: %d %v", code, out)
	}
}

func TestSlowSignIns(t *testing.T) {
	idp := oidctest.New(t)
	p := newPanel(t, idp, nil)
	prov := newProvider()
	a := addon(slog.New(slog.DiscardHandler))
	a.Capabilities["auth-provider"]["begin"], a.Capabilities["auth-provider"]["complete"] = prov.begin, prov.complete
	p.handler = a.Handler()
	redirect, state := p.begin()
	q := idp.SignIn(t, redirect, "user-1")
	prov.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if code, out := p.complete(state, q); code != http.StatusOK || out["refused"] != true || !strings.Contains(out["reason"].(string), "too long") {
		t.Errorf("a sign-in that took eleven minutes: %d %v", code, out)
	}
}

func TestSettingsAreChecked(t *testing.T) {
	idp := oidctest.New(t)
	for want, settings := range map[string]map[string]any{
		"issuer is the provider's": {"issuer": nil},
		"isn't https":              {"issuer": "http://id.example.com"},
		"without a query":          {"issuer": idp.Issuer + "?x=1"},
		"clientId is":              {"clientId": " "},
		"prompt is":                {"prompt": "always"},
		"timeoutSeconds":           {"timeoutSeconds": 120},
		"has characters":           {"scopes": []string{"openid email"}},
		"isn't a claim's name":     {"usernameClaim": "user name"},
		"allowedDomains holds":     {"allowedDomains": []string{"ada@example.com"}},
		"no PEM certificate":       {"caCertificate": "nope"},
		"settings can't be read":   {"clientSecret": "x"},
	} {
		code, out := newPanel(t, idp, settings).call("begin", map[string]string{"callback": callback})
		detail, _ := out["detail"].(string)
		if code != http.StatusUnprocessableEntity || !strings.Contains(detail, want) {
			t.Errorf("%s: %d %v", want, code, out)
		}
	}
	if len(idp.Requests()) != 0 {
		t.Error("settings that can't work reached the provider")
	}
}
