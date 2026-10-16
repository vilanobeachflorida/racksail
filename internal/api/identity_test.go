package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// browser is a person in a browser: it keeps the panel's cookies and says
// it's on the panel's own page, as browsers do.
type browser struct {
	t      *testing.T
	s      *stack
	client *http.Client
	header http.Header
}

func (s *stack) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: s.t, s: s, client: &http.Client{Jar: jar}, header: http.Header{"Sec-Fetch-Site": {"same-origin"}}}
}

func (b *browser) do(method, path string, body, out any) *http.Response {
	b.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, b.s.http.URL+path, r)
	if err != nil {
		b.t.Fatal(err)
	}
	for k, v := range b.header {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(strings.NewReader(string(raw)))
	if out != nil && len(raw) > 0 && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			b.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	return resp
}

func (b *browser) cookie(name string) string {
	req, _ := http.NewRequest(http.MethodGet, b.s.http.URL+"/", nil)
	for _, c := range b.client.Jar.Cookies(req.URL) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

const testPassword = "tangerine kettle orbit"

// setUp creates the first admin through the API and returns their browser.
func (s *stack) setUp() *browser {
	s.t.Helper()
	code, err := s.identity.Bootstrap(context.Background(), "")
	if err != nil {
		s.t.Fatal(err)
	}
	b := s.browser()
	var answer SignInAnswer
	resp := b.do("POST", "/api/v1/auth/setup", map[string]any{"code": code, "username": "ada", "name": "Ada", "password": testPassword}, &answer)
	if resp.StatusCode != http.StatusOK || !answer.SignedIn {
		s.t.Fatalf("setup = %d %s", resp.StatusCode, bodyOf(resp))
	}
	return b
}

func TestSecurityHeaders(t *testing.T) {
	s := newStack(t, RateLimit{})
	resp := s.call("", "GET", "/api/v1/health", nil, nil)
	for header, want := range map[string]string{
		"Content-Security-Policy": apiPolicy, "X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff",
		"Referrer-Policy": "no-referrer", "Cross-Origin-Opener-Policy": "same-origin", "Cache-Control": "no-store",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("plain HTTP gets no HSTS")
	}
}

func TestCrossSiteRequests(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	b.header.Set("Sec-Fetch-Site", "cross-site")
	resp := b.do("POST", "/api/v1/auth/sign-out", nil, nil)
	if resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrCrossSite.Code {
		t.Errorf("a request from another site = %d %s", resp.StatusCode, bodyOf(resp))
	}
	b.header.Del("Sec-Fetch-Site")
	b.header.Set("Origin", "https://evil.example")
	if resp := b.do("PUT", "/api/v1/me", map[string]any{"name": "Mallory"}, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another origin = %d", resp.StatusCode)
	}
	// Reads, and programs that send neither header, go through.
	b.header.Del("Origin")
	if resp := b.do("PUT", "/api/v1/me", map[string]any{"name": "Ada L."}, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("a program = %d %s", resp.StatusCode, bodyOf(resp))
	}
}

func TestSigningInOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	var options SignInOptions
	s.browser().do("GET", "/api/v1/auth/options", nil, &options)
	if !options.SetupRequired || options.Passkeys {
		t.Errorf("options before setup = %+v", options)
	}
	b := s.setUp()
	secret := b.cookie(auth.SessionCookie)
	if !strings.HasPrefix(secret, auth.SessionPrefix) {
		t.Fatalf("session cookie = %q", secret)
	}
	var session Session
	if resp := b.do("GET", "/api/v1/session", nil, &session); resp.StatusCode != http.StatusOK {
		t.Fatalf("session = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if session.User.Username != "ada" || len(session.Permissions) == 0 || session.Permissions[0] != "*" || session.ExpiresAt == nil ||
		len(session.StepUpMethods) != 1 {
		t.Errorf("session = %+v", session)
	}

	// The cookie is what the security rules ask for.
	other := s.browser()
	resp := other.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword, "remember": true}, nil)
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge <= 0 {
		t.Fatalf("cookie = %+v", cookie)
	}
	if resp := other.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": "wrong"}, nil); resp.StatusCode != http.StatusUnauthorized || s.problem(resp) != auth.ErrBadCredentials.Code {
		t.Errorf("a wrong password = %d %s", resp.StatusCode, bodyOf(resp))
	}

	// Signing out ends the session and clears the cookie.
	if resp := b.do("POST", "/api/v1/auth/sign-out", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sign-out = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if b.cookie(auth.SessionCookie) != "" {
		t.Error("the cookie is cleared")
	}
	b.client.Jar.SetCookies(mustURL(t, s.http.URL), []*http.Cookie{{Name: auth.SessionCookie, Value: secret}})
	if resp := b.do("GET", "/api/v1/session", nil, nil); resp.StatusCode != http.StatusUnauthorized || s.problem(resp) != auth.ErrSessionExpired.Code {
		t.Errorf("a signed-out session = %d %s", resp.StatusCode, bodyOf(resp))
	}

	// The edge says who the browser is; the panel believes it from
	// loopback.
	edge := s.browser()
	edge.header.Set("X-Forwarded-For", "203.0.113.50")
	edge.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	records, _, _ := s.log.QueryAudit(context.Background(), events.Query{Types: []string{"auth.sign_in"}, Limit: 1})
	if len(records) != 1 || records[0].Actor.IP != "203.0.113.50" {
		t.Errorf("audit = %+v", records)
	}
}

// Too many sign-ins from one address answer as the API's rate limit does,
// with Retry-After.
func TestSignInLimitsSayWhenToTryAgain(t *testing.T) {
	s := newStack(t, RateLimit{})
	s.setUp()
	b := s.browser()
	var resp *http.Response
	for range 30 {
		resp = b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "nobody", "password": "not a password at all"}, nil)
		if resp.StatusCode == http.StatusTooManyRequests {
			break
		}
	}
	if resp.StatusCode != http.StatusTooManyRequests || s.problem(resp) != i18n.ErrRateLimited.Code {
		t.Fatalf("after many tries = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var p struct {
		Detail string         `json:"detail"`
		Params map[string]any `json:"params"`
	}
	json.Unmarshal([]byte(bodyOf(resp)), &p)
	if after := resp.Header.Get("Retry-After"); after == "" || after != fmt.Sprint(p.Params["retryAfter"]) {
		t.Errorf("Retry-After = %q, params = %v", after, p.Params)
	}
	want := fmt.Sprintf("Too many requests. Wait %v seconds and try again.", p.Params["retryAfter"])
	if p.Params["retryAfter"] == "1" {
		want = "Too many requests. Wait 1 second and try again."
	}
	if p.Detail != want {
		t.Errorf("detail = %q, want %q", p.Detail, want)
	}
}

// A lockout says how long it lasts in Retry-After as well, as the API's own
// rate limit and the limits on signing in from one address do.
func TestLockoutsSayWhenToTryAgain(t *testing.T) {
	s := newStack(t, RateLimit{})
	s.setUp()
	b := s.browser()
	for range identity.DefaultPolicy().LockoutAttempts {
		b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": "not the password at all"}, nil)
	}
	resp := b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	if resp.StatusCode != http.StatusTooManyRequests || s.problem(resp) != auth.ErrLocked.Code {
		t.Fatalf("a locked-out user = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var p struct {
		Params map[string]any `json:"params"`
	}
	json.Unmarshal([]byte(bodyOf(resp)), &p)
	after := resp.Header.Get("Retry-After")
	if seconds, err := strconv.Atoi(after); err != nil || after != fmt.Sprint(p.Params["retryAfter"]) || seconds < 14*60 || seconds > 15*60 {
		t.Errorf("Retry-After = %q, params = %v", after, p.Params)
	}
}

// A sign-in provider names what an add-on or a module provides, or adding
// it is refused, and the problem points at the name.
func TestSignInProvidersNameWhatIsProvided(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	connect := func(name string) *http.Response {
		return admin.do("POST", "/api/v1/core/signin-providers", map[string]any{"spec": map[string]any{"capability": "identity", "provider": name, "title": "Directory"}}, nil)
	}
	resp := connect("no-such-provider")
	var p struct {
		Detail string `json:"detail"`
		Errors []struct {
			Location string    `json:"location"`
			Code     i18n.Code `json:"code"`
		} `json:"errors"`
	}
	json.Unmarshal([]byte(bodyOf(resp)), &p)
	if resp.StatusCode != http.StatusUnprocessableEntity || s.problem(resp) != auth.ErrProviderMissing.Code || !strings.Contains(p.Detail, "no-such-provider") ||
		len(p.Errors) != 1 || p.Errors[0].Location != "spec.provider" || p.Errors[0].Code != auth.ErrProviderMissing.Code {
		t.Errorf("a provider nothing provides = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if err := s.hooks.Register(hooks.Registration{
		Point: hooks.AuthProvider, Kind: hooks.Provide, Owner: "addon:ldap-sign-in",
		Provider: &hooks.Provider{Name: "ldap-sign-in", Title: "LDAP sign-in", Open: func(ctx context.Context, settings json.RawMessage) (any, error) { return nil, nil }},
	}); err != nil {
		t.Fatal(err)
	}
	resp = connect("ldap-sign-in")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a provider an add-on provides = %d %s", resp.StatusCode, bodyOf(resp))
	}
	cs := s.finish("admin", changeSetOf(t, resp))
	var provider struct {
		Status identity.ProviderStatus `json:"status"`
	}
	admin.do("GET", "/api/v1/core/signin-providers/"+string(cs.Changes[0].ID), nil, &provider)
	if provider.Status.Phase != store.PhaseReady {
		t.Errorf("its status = %+v", provider.Status)
	}
}

func TestStepUpAndTokensOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	resp := b.do("POST", "/api/v1/me/tokens", map[string]any{"name": "ci"}, nil)
	if resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
		t.Fatalf("a token without confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}
	before := b.cookie(auth.SessionCookie)
	var stepped struct {
		SteppedUpUntil string `json:"steppedUpUntil"`
	}
	if resp := b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, &stepped); resp.StatusCode != http.StatusOK || stepped.SteppedUpUntil == "" {
		t.Fatalf("step-up = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if b.cookie(auth.SessionCookie) == before {
		t.Error("stepping up rotates the session")
	}
	var created identity.CreatedToken
	if resp := b.do("POST", "/api/v1/me/tokens", map[string]any{"name": "ci", "permissions": []string{"*.read"}}, &created); resp.StatusCode != http.StatusCreated {
		t.Fatalf("token = %d %s", resp.StatusCode, bodyOf(resp))
	}

	// A token acts as its user, narrowed, and can't confirm anything.
	call := func(method, path string, body any) *http.Response {
		var r io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			r = strings.NewReader(string(raw))
		}
		req, _ := http.NewRequest(method, s.http.URL+path, r)
		req.Header.Set("Authorization", "Bearer "+created.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader(string(raw)))
		return resp
	}
	var session Session
	resp = call("GET", "/api/v1/session", nil)
	json.Unmarshal([]byte(bodyOf(resp)), &session)
	if session.Token == nil || session.Token.Name != "ci" || session.User.Username != "ada" || strings.Join(session.Permissions, ",") != "*.read" {
		t.Errorf("token session = %+v", session)
	}
	if resp := call("POST", "/api/v1/me/tokens", map[string]any{"name": "more"}); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrPanelOnly.Code {
		t.Errorf("a token making tokens = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := call("POST", "/api/v1/core/accounts/open?dryRun=true", map[string]any{"slug": "acme", "name": "Acme"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read-only token opening an account = %d", resp.StatusCode)
	}
	if resp := call("GET", "/api/v1/core/users", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("a read-only token listing users = %d %s", resp.StatusCode, bodyOf(resp))
	}
	// What only the person in the panel changes stays theirs.
	if resp := call("PUT", "/api/v1/session/preferences", map[string]any{"mode": "dark"}); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrPanelOnly.Code {
		t.Errorf("a token changing preferences for the web UI = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := call("PUT", "/api/v1/me", map[string]any{"name": "Mallory", "email": "mallory@example.net"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read-only token changing the profile = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var other identity.CreatedToken
	b.do("POST", "/api/v1/me/tokens", map[string]any{"name": "deploys"}, &other)
	if resp := call("DELETE", "/api/v1/me/tokens/"+string(other.ID), nil); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrPanelOnly.Code {
		t.Errorf("a token revoking another = %d %s", resp.StatusCode, bodyOf(resp))
	}
	// A token that leaked can always revoke itself.
	if resp := call("DELETE", "/api/v1/me/tokens/"+string(created.ID), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a token revoking itself = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := call("GET", "/api/v1/session", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a revoked token = %d", resp.StatusCode)
	}
	if resp := b.do("DELETE", "/api/v1/me/tokens/"+string(other.ID), nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", resp.StatusCode, bodyOf(resp))
	}
}

// A token narrowed to some permissions sees no more of its own user than
// they allow, while one kept to reading sees it all.
func TestNarrowedTokensKeepToTheirNarrowing(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	var me Session
	b.do("GET", "/api/v1/session", nil, &me)
	token := func(perms ...string) string {
		var created identity.CreatedToken
		if resp := b.do("POST", "/api/v1/me/tokens", map[string]any{"name": strings.Join(perms, " "), "permissions": perms}, &created); resp.StatusCode != http.StatusCreated {
			t.Fatalf("token = %d %s", resp.StatusCode, bodyOf(resp))
		}
		return created.Token
	}
	get := func(secret, path string) int {
		req, _ := http.NewRequest(http.MethodGet, s.http.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	sites, reads := token("web.*"), token("*.read")
	own := []string{"/api/v1/me", "/api/v1/me/ssh-keys", "/api/v1/me/tokens", "/api/v1/me/passkeys", "/api/v1/core/users/" + string(me.User.ID)}
	for _, path := range own {
		if code := get(sites, path); code == http.StatusOK {
			t.Errorf("a token kept to sites reads %s", path)
		}
		if code := get(reads, path); code != http.StatusOK {
			t.Errorf("a token kept to reading reads %s = %d", path, code)
		}
	}
	// Who a token is stays its own to see.
	if code := get(sites, "/api/v1/session"); code != http.StatusOK {
		t.Errorf("a token kept to sites asking who it is = %d", code)
	}
}

func TestPendingStepsOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	s.identity.SetPolicy(identity.SignInPolicySpec{RequireTwoFactor: []auth.Role{auth.RoleAdmin}})
	fresh := s.browser()
	fresh.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	if resp := fresh.do("GET", "/api/v1/core/users", nil, nil); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrTwoFactorRequired.Code {
		t.Errorf("before setting up a second step = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var session Session
	fresh.do("GET", "/api/v1/session", nil, &session)
	if session.Pending != identity.PendingTwoFactor {
		t.Errorf("pending = %q", session.Pending)
	}
	_ = b
}

func TestManagingUsersOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)

	// Opening an account adds its owner, with a sign-in link.
	var opened OpenedAccount
	resp := admin.do("POST", "/api/v1/core/accounts/open", map[string]any{
		"slug": "acme", "name": "Acme", "owner": map[string]any{"username": "owen", "email": "owen@acme.example"},
	}, &opened)
	if resp.StatusCode != http.StatusAccepted || opened.Owner == nil || opened.Link == nil || opened.ChangeSet == nil {
		t.Fatalf("open = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", opened.ChangeSet)
	acct := opened.ChangeSet.Changes[0].ID

	owner := s.browser()
	var answer SignInAnswer
	if resp := owner.do("POST", "/api/v1/auth/links/use", map[string]any{"code": opened.Link.Code, "password": "an owner's own passphrase"}, &answer); !answer.SignedIn {
		t.Fatalf("using the link = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var session Session
	owner.do("GET", "/api/v1/session", nil, &session)
	if len(session.Accounts) != 1 || session.Accounts[0].Slug != "acme" {
		t.Errorf("the owner's accounts = %+v", session.Accounts)
	}

	if names := usernames(t, admin); strings.Join(names, ",") != "ada,owen" {
		t.Errorf("users = %v", names)
	}
	if names := usernames(t, owner); strings.Join(names, ",") != "owen" {
		t.Errorf("the owner sees = %v", names)
	}

	// Viewing the panel as the owner shows the banner's details, and goes
	// back.
	if resp := admin.do("POST", "/api/v1/core/users/"+string(opened.Owner.ID)+"/impersonate", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("impersonate = %d %s", resp.StatusCode, bodyOf(resp))
	}
	admin.do("GET", "/api/v1/session", nil, &session)
	if session.Impersonation == nil || session.Impersonation.Name != "Ada" || session.User.Username != "owen" {
		t.Errorf("impersonating = %+v", session)
	}
	if resp := admin.do("POST", "/api/v1/me/tokens", map[string]any{"name": "x"}, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("making the owner a token = %d", resp.StatusCode)
	}
	if resp := admin.do("DELETE", "/api/v1/session/impersonation", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var back Session
	admin.do("GET", "/api/v1/session", nil, &back)
	if back.Impersonation != nil || back.User.Username != "ada" {
		t.Errorf("back = %+v", back)
	}

	// Who did what shows in the audit log.
	records, _, _ := s.log.QueryAudit(context.Background(), events.Query{Subject: opened.Owner.ID, Limit: 100})
	var actions []string
	for _, r := range records {
		actions = append(actions, r.Action)
	}
	for _, want := range []string{"core.user.create", "core.impersonate", "core.impersonate.stop"} {
		if !strings.Contains(strings.Join(actions, " "), want) {
			t.Errorf("audit lacks %s: %v", want, actions)
		}
	}
	_ = acct
}

// An account's owner is a new user, so opening an account with one asks
// for the confirmation adding a user does; one without an owner doesn't.
func TestOpeningAnAccountWithAnOwnerNeedsStepUp(t *testing.T) {
	s := newStack(t, RateLimit{})
	s.setUp()
	b := s.browser()
	b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	withOwner := map[string]any{"slug": "acme", "name": "Acme", "owner": map[string]any{"username": "owen"}}
	if resp := b.do("POST", "/api/v1/core/accounts/open", withOwner, nil); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
		t.Errorf("with an owner = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var preview OpenedAccount
	if resp := b.do("POST", "/api/v1/core/accounts/open?dryRun=true", withOwner, &preview); resp.StatusCode != http.StatusOK || preview.ChangeSet == nil || preview.Link != nil {
		t.Errorf("its preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var opened OpenedAccount
	if resp := b.do("POST", "/api/v1/core/accounts/open", map[string]any{"slug": "bare", "name": "Bare"}, &opened); resp.StatusCode != http.StatusAccepted {
		t.Errorf("without an owner = %d %s", resp.StatusCode, bodyOf(resp))
	}
	b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	opened = OpenedAccount{}
	if resp := b.do("POST", "/api/v1/core/accounts/open", withOwner, &opened); resp.StatusCode != http.StatusAccepted || opened.Link == nil {
		t.Errorf("after confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}
}

func TestResellersOpenTheirOwnAccounts(t *testing.T) {
	s := newStack(t, RateLimit{})
	rival := store.NewID("rsl")
	open := func(reseller store.ID) *http.Response {
		return s.call("reseller", "POST", "/api/v1/core/accounts/open", map[string]any{"slug": "shop" + strings.ToLower(string(reseller[len(reseller)-4:])), "name": "Shop", "reseller": reseller}, nil)
	}
	if resp := open(s.reseller); resp.StatusCode != http.StatusAccepted {
		t.Errorf("for themselves = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := open(rival); resp.StatusCode != http.StatusForbidden {
		t.Errorf("for someone else = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := s.call("reseller", "POST", "/api/v1/core/accounts/open", map[string]any{"slug": "direct", "name": "Direct"}, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("for the server = %d %s", resp.StatusCode, bodyOf(resp))
	}
}

func TestPermissionMatrix(t *testing.T) {
	s := newStack(t, RateLimit{})
	if problems := CheckMatrix(s.server.Catalog(), s.kinds.All()); len(problems) > 0 {
		t.Errorf("operations the matrix doesn't cover:\n%s", strings.Join(problems, "\n"))
	}
	bad := []Operation{
		{ID: "sneaky", Public: true},
		{ID: "loose"},
		{ID: "made-up", Permission: "web.site.dance"},
		{ID: "getSession", Permission: "core.user.read"},
	}
	if problems := CheckMatrix(bad, s.kinds.All()); len(problems) != 4 {
		t.Errorf("problems = %v", problems)
	}
	var roles struct {
		Roles  []auth.RoleInfo `json:"roles"`
		Matrix []MatrixEntry   `json:"matrix"`
	}
	s.call("nobody", "GET", "/api/v1/roles", nil, &roles)
	found := false
	for _, e := range roles.Matrix {
		switch e.Permission {
		case "test.site.deploy":
			found = e.Roles[auth.RoleOwner] == auth.AccessOwnScope && e.Roles[auth.RoleReadOnly] == auth.AccessNone
		case PermMetricsRead, "core.webhook.create", "core.signin_policy.update":
			// Only checked against the whole server, which grants on a
			// reseller or an account never reach.
			if e.Roles[auth.RoleReseller] != auth.AccessNone || e.Roles[auth.RoleOwner] != auth.AccessNone || e.Roles[auth.RoleAdmin] != auth.AccessEverywhere {
				t.Errorf("%s = %v", e.Permission, e.Roles)
			}
		}
	}
	if len(roles.Roles) != 5 || !found {
		t.Errorf("roles = %+v", roles)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Critical changes from the panel need the person to confirm it's them,
// however they're sent; previews don't.
func TestCriticalChangesNeedStepUp(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	var opened OpenedAccount
	admin.do("POST", "/api/v1/core/accounts/open", map[string]any{"slug": "acme", "name": "Acme"}, &opened)
	s.finish("admin", opened.ChangeSet)
	acct := string(opened.ChangeSet.Changes[0].ID)

	// A fresh session hasn't confirmed anything.
	b := s.browser()
	b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	if resp := b.do("DELETE", "/api/v1/core/accounts/"+acct, nil, nil); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
		t.Errorf("deleting an account = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var preview struct {
		ID   string `json:"id"`
		Risk string `json:"risk"`
	}
	if resp := b.do("DELETE", "/api/v1/core/accounts/"+acct+"?dryRun=true", nil, &preview); resp.StatusCode != http.StatusOK || preview.Risk != "critical" {
		t.Fatalf("a preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := b.do("POST", "/api/v1/changesets/"+preview.ID+"/apply", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("applying the preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	change := map[string]any{"changes": []map[string]any{{"op": "delete", "kind": "core.account", "id": acct}}}
	if resp := b.do("POST", "/api/v1/changesets", change, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("submitting it = %d %s", resp.StatusCode, bodyOf(resp))
	}
	b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	if resp := b.do("POST", "/api/v1/changesets/"+preview.ID+"/apply", nil, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("after confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}
}

// Asking for a dry run spares the confirmation only where the operation
// previews the change: the others ignore dryRun and go ahead.
func TestDryRunsOfOperationsWithoutPreviewsNeedStepUp(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	var opened OpenedAccount
	admin.do("POST", "/api/v1/core/accounts/open", map[string]any{"slug": "acme", "name": "Acme", "owner": map[string]any{"username": "owen"}}, &opened)
	s.finish("admin", opened.ChangeSet)
	acct, owner := string(opened.ChangeSet.Changes[0].ID), string(opened.Owner.ID)
	newUser := map[string]any{"scope": map[string]any{"level": "account", "id": acct}, "spec": map[string]any{
		"username": "bea", "grants": []map[string]any{{"role": "readonly", "scope": map[string]any{"level": "account", "id": acct}}},
	}}

	// A fresh session hasn't confirmed anything.
	b := s.browser()
	b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/me/tokens?dryRun=true", map[string]any{"name": "ci"}},
		{"POST", "/api/v1/me/tokens?dryRun=1", map[string]any{"name": "ci"}},
		{"PUT", "/api/v1/core/users/" + owner + "/password?dryRun=true", map[string]any{"password": "a passphrase of mine now"}},
		{"POST", "/api/v1/core/users/" + owner + "/impersonate?dryRun=true", nil},
		{"POST", "/api/v1/core/users/" + owner + "/link?dryRun=true", nil},
		{"POST", "/api/v1/core/users/" + owner + "/reset?dryRun=true", map[string]any{"revokeTokens": true}},
		{"POST", "/api/v1/me/recovery-codes?dryRun=true", nil},
		// Huma reads a bare dryRun as true and url.Values as empty, so it
		// isn't a dry run for the confirmation either.
		{"POST", "/api/v1/core/users?dryRun", newUser},
		{"POST", "/api/v1/core/users?dry%52un=false&dryRun=true", newUser},
	} {
		resp := b.do(c.method, c.path, c.body, nil)
		if resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
			t.Errorf("%s %s = %d %s", c.method, c.path, resp.StatusCode, bodyOf(resp))
		}
	}
	ctx := context.Background()
	var session Session
	b.do("GET", "/api/v1/session", nil, &session)
	if session.Impersonation != nil || session.User.Username != "ada" {
		t.Errorf("session = %+v", session)
	}
	if tokens, _ := s.identity.Store().Tokens(ctx, session.User.ID); len(tokens) != 0 {
		t.Errorf("tokens = %+v", tokens)
	}
	if u, err := s.identity.User(ctx, opened.Owner.ID); err != nil || u.PasswordHash != "" {
		t.Errorf("the owner's password was set: %v", err)
	}
	if names := usernames(t, admin); strings.Join(names, ",") != "ada,owen" {
		t.Errorf("users = %v", names)
	}

	// Previews of operations that take dryRun need nothing.
	resp := b.do("POST", "/api/v1/core/users?dryRun=true", newUser, nil)
	if resp.StatusCode != http.StatusOK || !changeSetOf(t, resp).DryRun {
		t.Errorf("a preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
}

func TestPreviewOnly(t *testing.T) {
	previews := &huma.Operation{Extensions: map[string]any{ExtDryRun: true}}
	acts := &huma.Operation{Extensions: map[string]any{}}
	for query, want := range map[string]bool{
		"dryRun=true": true, "dryRun=1": true, "dryRun=TRUE": true, "dryRun=false": false, "": false,
		"dryRun": false, "dryRun=": false, "dryRun=yes": false, "dryRun=true&dryRun=false": true,
		"dryRun=false&dryRun=true": false, "dry%52un=false&dryRun=true": false, "dryRun&dryRun=true": false,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/things?"+query, nil)
		ctx := humatest.NewContext(previews, req, httptest.NewRecorder())
		if got := previewOnly(ctx, previews); got != want {
			t.Errorf("%q previews = %v, want %v", query, got, want)
		}
		if previewOnly(humatest.NewContext(acts, req, httptest.NewRecorder()), acts) {
			t.Errorf("%q counts as a preview of an operation without dryRun", query)
		}
	}
}

// userItem is a user as the API lists them.
type userItem struct {
	ID       store.ID            `json:"id"`
	Revision int64               `json:"revision"`
	Spec     identity.UserSpec   `json:"spec"`
	Status   identity.UserStatus `json:"status"`
}

// usernames lists the users a browser sees, by username.
func usernames(t *testing.T, b *browser) []string {
	t.Helper()
	var page struct {
		Items []userItem `json:"items"`
	}
	if resp := b.do("GET", "/api/v1/core/users?sort=username", nil, &page); resp.StatusCode != http.StatusOK {
		t.Fatalf("users = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var out []string
	for _, u := range page.Items {
		out = append(out, u.Spec.Username)
	}
	return out
}

func (b *browser) user(id store.ID) userItem {
	b.t.Helper()
	var u userItem
	if resp := b.do("GET", "/api/v1/core/users/"+string(id), nil, &u); resp.StatusCode != http.StatusOK {
		b.t.Fatalf("user = %d %s", resp.StatusCode, bodyOf(resp))
	}
	return u
}

// changeSet decodes the change set an answer carries.
func changeSetOf(t *testing.T, resp *http.Response) *changeset.ChangeSet {
	t.Helper()
	var cs changeset.ChangeSet
	if err := json.Unmarshal([]byte(bodyOf(resp)), &cs); err != nil || cs.ID == "" {
		t.Fatalf("not a change set: %d %s", resp.StatusCode, bodyOf(resp))
	}
	return &cs
}

func TestUsersAreChangesOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	acct := s.openAccount(admin, "acme")
	newUser := map[string]any{"scope": map[string]any{"level": "account", "id": acct}, "spec": map[string]any{
		"username": "bea", "name": "Bea", "grants": []map[string]any{{"role": "readonly", "scope": map[string]any{"level": "account", "id": acct}}},
	}}

	// Users are core.user resources: changing them needs the person to
	// confirm it's them, previews them, and goes through change sets.
	fresh := s.browser()
	fresh.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	if resp := fresh.do("POST", "/api/v1/core/users", newUser, nil); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
		t.Errorf("adding a user without confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	resp := admin.do("POST", "/api/v1/core/users?dryRun=true", newUser, nil)
	preview := changeSetOf(t, resp)
	if resp.StatusCode != http.StatusOK || !preview.DryRun || preview.Changes[0].Kind != identity.KindUser {
		t.Fatalf("a preview = %d %+v", resp.StatusCode, preview)
	}
	if names := usernames(t, admin); strings.Join(names, ",") != "ada" {
		t.Errorf("a preview added a user: %v", names)
	}
	resp = admin.do("POST", "/api/v1/changesets/"+string(preview.ID)+"/apply", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("applying = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", changeSetOf(t, resp))
	bea := preview.Changes[0].ID
	if u := admin.user(bea); u.Spec.Username != "bea" || u.Status.PasswordSet {
		t.Errorf("bea = %+v", u)
	}

	// A change from what someone saw before the user changed is refused.
	before := admin.user(bea)
	update := func(rev int64, name string) *http.Response {
		spec := before.Spec
		spec.Name = name
		return admin.do("PUT", "/api/v1/core/users/"+string(bea), map[string]any{"spec": spec, "revision": rev}, nil)
	}
	resp = update(before.Revision, "Beatrice")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("renaming = %d %s", resp.StatusCode, bodyOf(resp))
	}
	renamed := s.finish("admin", changeSetOf(t, resp))
	if resp := update(before.Revision, "Bee"); resp.StatusCode != http.StatusConflict {
		t.Errorf("a stale change = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := admin.do("POST", "/api/v1/changesets/"+string(renamed.ID)+"/undo", nil, nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("undo = %d %s", resp.StatusCode, bodyOf(resp))
	} else {
		s.finish("admin", changeSetOf(t, resp))
	}
	if u := admin.user(bea); u.Spec.Name != "Bea" {
		t.Errorf("after the undo = %+v", u.Spec)
	}
	if resp := admin.do("POST", "/api/v1/core/users", newUser, nil); resp.StatusCode != http.StatusConflict || s.problem(resp) != auth.ErrUsernameTaken.Code {
		t.Errorf("a taken username = %d %s", resp.StatusCode, bodyOf(resp))
	}
	resp = admin.do("DELETE", "/api/v1/core/users/"+string(bea), nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("removing = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", changeSetOf(t, resp))
	if names := usernames(t, admin); strings.Join(names, ",") != "ada" {
		t.Errorf("after removing bea = %v", names)
	}
}

// openAccount opens an account through the API and returns it.
func (s *stack) openAccount(b *browser, slug string) store.ID {
	s.t.Helper()
	var opened OpenedAccount
	if resp := b.do("POST", "/api/v1/core/accounts/open", map[string]any{"slug": slug, "name": slug}, &opened); resp.StatusCode != http.StatusAccepted {
		s.t.Fatalf("opening %s = %d %s", slug, resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", opened.ChangeSet)
	return opened.ChangeSet.Changes[0].ID
}

func TestOwnChangesOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	resp := b.do("PUT", "/api/v1/me?dryRun=true", map[string]any{"name": "Ada Lovelace", "email": "ada@example.com"}, nil)
	if resp.StatusCode != http.StatusOK || !changeSetOf(t, resp).DryRun {
		t.Fatalf("a preview of a profile change = %d %s", resp.StatusCode, bodyOf(resp))
	}
	// A new name goes ahead as it is; a new email address, which outside
	// sign-in may match people by, needs the person to confirm it's them.
	resp = b.do("PUT", "/api/v1/me", map[string]any{"name": "Ada Lovelace"}, nil)
	cs := changeSetOf(t, resp)
	if cs.Changes[0].Action != "update_profile" || cs.Changes[0].Kind != identity.KindUser {
		t.Fatalf("a profile change = %+v", cs.Changes)
	}
	s.finish("admin", cs)
	if resp := b.do("PUT", "/api/v1/me", map[string]any{"name": "Ada Lovelace", "email": "ada@example.com"}, nil); s.problem(resp) != auth.ErrStepUpRequired.Code {
		t.Errorf("a new email address without confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}

	// Keys need the person to confirm it's them, and keys added at the same
	// moment all land.
	if resp := b.do("POST", "/api/v1/me/ssh-keys", map[string]any{"publicKey": testKeys[0]}, nil); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
		t.Errorf("a key without confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}
	b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	resp = b.do("PUT", "/api/v1/me", map[string]any{"name": "Ada Lovelace", "email": "ada@example.com"}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a new email address once confirmed = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", changeSetOf(t, resp))
	var me identity.Me
	b.do("GET", "/api/v1/me", nil, &me)
	if me.Name != "Ada Lovelace" || me.Email != "ada@example.com" {
		t.Errorf("me = %+v", me.UserView)
	}
	var wg sync.WaitGroup
	for _, key := range testKeys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := b.do("POST", "/api/v1/me/ssh-keys", map[string]any{"publicKey": key}, nil)
			if resp.StatusCode != http.StatusAccepted {
				t.Errorf("adding %s = %d %s", key, resp.StatusCode, bodyOf(resp))
				return
			}
			s.finish("admin", changeSetOf(t, resp))
		}()
	}
	wg.Wait()
	var keys struct {
		Keys []identity.SSHKey `json:"keys"`
	}
	b.do("GET", "/api/v1/me/ssh-keys", nil, &keys)
	if len(keys.Keys) != len(testKeys) {
		t.Fatalf("keys = %+v", keys.Keys)
	}
	resp = b.do("DELETE", "/api/v1/me/ssh-keys/"+string(keys.Keys[0].ID), nil, nil)
	if resp.StatusCode != http.StatusAccepted || changeSetOf(t, resp).Changes[0].Action != "remove_ssh_key" {
		t.Fatalf("removing a key = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", changeSetOf(t, resp))
	b.do("GET", "/api/v1/me/ssh-keys", nil, &keys)
	if len(keys.Keys) != len(testKeys)-1 {
		t.Errorf("after removing one = %+v", keys.Keys)
	}
}

// An email address can become a way in, since outside sign-in may match
// people by it the first time they sign in, so a person in the panel
// confirms it's them before giving anyone a new one, however the change is
// sent: a profile change of their own or someone else's, several changes
// at once, a preview applied later or an undo.
func TestNewEmailAddressesNeedConfirming(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	acct := s.openAccount(admin, "acme")
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	resp := admin.do("POST", "/api/v1/core/users", map[string]any{"scope": map[string]any{"level": "account", "id": acct}, "spec": map[string]any{
		"username": "bea", "name": "Bea", "email": "bea@example.com", "grants": []map[string]any{{"role": "readonly", "scope": map[string]any{"level": "account", "id": acct}}},
	}}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("adding bea = %d %s", resp.StatusCode, bodyOf(resp))
	}
	bea := s.finish("admin", changeSetOf(t, resp)).Changes[0].ID
	profile := func(name, email string) map[string]any { return map[string]any{"name": name, "email": email} }
	action := "/api/v1/core/users/" + string(bea) + "/actions/update_profile"

	// Ada again, in a browser where she hasn't confirmed it's her.
	b := s.browser()
	b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	asks := func(what string, resp *http.Response) {
		t.Helper()
		if resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
			t.Errorf("%s = %d %s", what, resp.StatusCode, bodyOf(resp))
		}
	}
	asks("her own new address", b.do("PUT", "/api/v1/me", profile("Ada", "ada@example.org"), nil))
	asks("someone else's", b.do("POST", action, profile("Bea", "mallory@example.net"), nil))
	asks("taking it away", b.do("POST", action, profile("Bea", ""), nil))
	asks("several changes at once", b.do("POST", "/api/v1/changesets", map[string]any{"changes": []map[string]any{{
		"op": "action", "kind": identity.KindUser, "id": bea, "action": "update_profile", "input": profile("Bea", "mallory@example.net"),
	}}}, nil))
	if resp := b.do("GET", "/api/v1/core/users/"+string(bea), nil, nil); !strings.Contains(bodyOf(resp), `"email":"bea@example.com"`) {
		t.Errorf("bea's address changed: %s", bodyOf(resp))
	}

	// Another name, or the address in other letters, goes ahead.
	if resp := b.do("POST", action, profile("Beatrice", "BEA@example.com"), nil); resp.StatusCode >= 300 {
		t.Errorf("a new name = %d %s", resp.StatusCode, bodyOf(resp))
	}
	// A preview needs nothing, but applying it does.
	resp = b.do("POST", action+"?dryRun=true", profile("Bea", "mallory@example.net"), nil)
	preview := changeSetOf(t, resp)
	if resp.StatusCode != http.StatusOK || !preview.DryRun {
		t.Fatalf("a preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	asks("applying the preview", b.do("POST", "/api/v1/changesets/"+string(preview.ID)+"/apply", nil, nil))

	// Undoing a change of someone's address gives them another one too.
	resp = admin.do("POST", action, profile("Bea", "bea@example.org"), nil)
	if resp.StatusCode >= 300 {
		t.Fatalf("a new address once confirmed = %d %s", resp.StatusCode, bodyOf(resp))
	}
	changed := changeSetOf(t, resp)
	asks("undoing it", b.do("POST", "/api/v1/changesets/"+string(changed.ID)+"/undo", nil, nil))

	b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	resp = b.do("POST", "/api/v1/changesets/"+string(preview.ID)+"/apply", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("applying once confirmed = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := b.do("POST", "/api/v1/changesets/"+string(changed.ID)+"/undo", nil, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("undoing once confirmed = %d %s", resp.StatusCode, bodyOf(resp))
	}
}

// Someone may be allowed to change a user's profile without being allowed to
// read the user, as with a grant that names only update_profile on them.
// Giving that user a new address still takes confirming it's them, whether
// they send the change at once or apply a preview of it: what a change
// needs is judged by what it does, not by what its maker may read.
func TestNewAddressesNeedConfirmingWhateverYouMayRead(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	acct := s.openAccount(admin, "acme")
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	home := map[string]any{"level": "account", "id": acct}
	add := func(spec map[string]any) store.ID {
		t.Helper()
		resp := admin.do("POST", "/api/v1/core/users", map[string]any{"scope": home, "spec": spec}, nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("adding %s = %d %s", spec["username"], resp.StatusCode, bodyOf(resp))
		}
		return s.finish("admin", changeSetOf(t, resp)).Changes[0].ID
	}
	bea := add(map[string]any{"username": "bea", "email": "bea@example.com", "grants": []map[string]any{{"role": "readonly", "scope": home}}})
	cleo := add(map[string]any{"username": "cleo", "grants": []map[string]any{{
		"role": "collaborator", "scope": home, "object": bea, "permissions": []string{string(auth.PermUserUpdateProfile)},
	}}})
	const password = "a passphrase of her own"
	if resp := admin.do("PUT", "/api/v1/core/users/"+string(cleo)+"/password", map[string]any{"password": password}, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("giving cleo a password = %d %s", resp.StatusCode, bodyOf(resp))
	}
	b := s.browser()
	var answer SignInAnswer
	if resp := b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "cleo", "password": password}, &answer); !answer.SignedIn {
		t.Fatalf("cleo signing in = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := b.do("GET", "/api/v1/core/users/"+string(bea), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cleo reading bea = %d %s", resp.StatusCode, bodyOf(resp))
	}
	change := map[string]any{"changes": []map[string]any{{
		"op": "action", "kind": identity.KindUser, "id": bea, "action": identity.ActionUpdateProfile,
		"input": map[string]any{"name": "Bea", "email": "mallory@example.net"},
	}}}
	asks := func(what string, resp *http.Response) {
		t.Helper()
		if resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
			t.Errorf("%s = %d %s", what, resp.StatusCode, bodyOf(resp))
		}
		if u, err := identity.UserKind.Get(context.Background(), s.store, bea); err != nil || u.Spec.Email != "bea@example.com" {
			t.Fatalf("after %s, bea = %+v, %v", what, u, err)
		}
	}
	asks("sending the change", b.do("POST", "/api/v1/changesets", change, nil))
	resp := b.do("POST", "/api/v1/changesets?dryRun=true", change, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	apply := "/api/v1/changesets/" + string(changeSetOf(t, resp).ID) + "/apply"
	asks("applying the preview", b.do("POST", apply, nil, nil))

	b.do("POST", "/api/v1/session/step-up", map[string]any{"password": password}, nil)
	resp = b.do("POST", apply, nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("applying it once confirmed = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", changeSetOf(t, resp))
	if u, _ := identity.UserKind.Get(context.Background(), s.store, bea); u.Spec.Email != "mallory@example.net" {
		t.Errorf("once confirmed, bea's address is %s", u.Spec.Email)
	}
}

// Whether a change needs a confirmation is decided on the change set as
// planned, which is written only at the revisions it was planned against:
// a change landing after the decision doesn't turn a new name into putting
// back an old address. Sent at once, the change is planned and judged again
// from what landed; a preview being applied fails instead.
func TestConfirmationsHoldToWhatWasPlanned(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	var me identity.Me
	admin.do("GET", "/api/v1/me", nil, &me)
	ctx := context.Background()
	unconfirmed := &auth.Actor{Type: auth.ActorUser, ID: me.ID, User: me.ID, Name: "ada", Source: auth.SourceUI, SessionID: "a session",
		Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	address := ""
	// landing has someone who confirmed it's them give ada another address
	// once a plan was judged, the first time only.
	landing := func() changeset.Confirm {
		judge, landed := s.server.confirmPlan(unconfirmed), false
		return func(ctx context.Context, cs *changeset.ChangeSet) error {
			if err := judge(ctx, cs); err != nil || landed {
				return err
			}
			landed = true
			address = "ada" + strconv.Itoa(len(address)) + "@example.com"
			resp := admin.do("PUT", "/api/v1/me", map[string]any{"name": "Ada", "email": address}, nil)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("a new address meanwhile = %d %s", resp.StatusCode, bodyOf(resp))
			}
			s.finish("admin", changeSetOf(t, resp))
			return nil
		}
	}
	// A new name, keeping the address she has.
	rename := func() []changeset.Change {
		input, _ := json.Marshal(identity.ProfileInput{Name: "Ada L.", Email: address})
		return []changeset.Change{{Op: changeset.OpAction, Kind: identity.KindUser, ID: me.ID, Action: identity.ActionUpdateProfile, Input: input}}
	}
	held := func(what string, err error, code error) {
		t.Helper()
		if !errors.Is(err, code) {
			t.Errorf("%s = %v, want %v", what, err, code)
		}
		if u, _ := identity.UserKind.Get(ctx, s.store, me.ID); u.Spec.Email != address || u.Spec.Name != "Ada" {
			t.Errorf("after %s, ada = %+v", what, u.Spec)
		}
	}
	p := s.server.pipeline()
	_, err := p.Submit(ctx, changeset.Request{Actor: unconfirmed, Changes: rename(), Confirm: landing()})
	held("a new name sent as an address lands", err, auth.ErrStepUpRequired)

	preview, err := p.Submit(ctx, changeset.Request{Actor: unconfirmed, Changes: rename(), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Apply(ctx, changeset.ApplyRequest{Actor: unconfirmed, ChangeSet: preview.ID, Confirm: landing()})
	held("a preview applied as an address lands", err, store.ErrConflict)
	// The preview would now put back the address she had, which asks first.
	_, err = p.Apply(ctx, changeset.ApplyRequest{Actor: unconfirmed, ChangeSet: preview.ID, Confirm: s.server.confirmPlan(unconfirmed)})
	held("applying it again", err, auth.ErrStepUpRequired)
}

var testKeys = []string{
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFw7LvwMxHf0FpGF5oxM8D5Gd+wMx3sVQyBO9g0R4e7 laptop",
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHaSw601QLuAPAILOu5mzYiHEjI06gxucUPArdc/9DHt one",
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID/EzP50WHDiwNmfcfMP8GVsje3UHMHX09N2sNvmheLz two",
}

func TestSettingPasswordsOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	var opened OpenedAccount
	admin.do("POST", "/api/v1/core/accounts/open", map[string]any{"slug": "acme", "name": "Acme", "owner": map[string]any{"username": "owen"}}, &opened)
	s.finish("admin", opened.ChangeSet)
	owen := opened.Owner.ID

	resp := admin.do("PUT", "/api/v1/core/users/"+string(owen)+"/password", map[string]any{"password": "chosen by the admin", "mustChange": true}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("setting a password = %d %s", resp.StatusCode, bodyOf(resp))
	}
	owner := s.browser()
	owner.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "owen", "password": "chosen by the admin"}, nil)
	var session Session
	owner.do("GET", "/api/v1/session", nil, &session)
	if session.Pending != identity.PendingPassword {
		t.Errorf("pending = %q", session.Pending)
	}
	if resp := owner.do("GET", "/api/v1/core/users", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("before choosing a password = %d", resp.StatusCode)
	}
	if resp := owner.do("PUT", "/api/v1/me/password", map[string]any{"new": "owner's own passphrase"}, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("choosing a password = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var now Session
	owner.do("GET", "/api/v1/session", nil, &now)
	if now.Pending != "" || now.User.Username != "owen" {
		t.Errorf("after choosing a password = %+v", now)
	}
	if resp := admin.do("PUT", "/api/v1/core/users/"+string(owen)+"/password", map[string]any{"password": "short"}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a weak password = %d %s", resp.StatusCode, bodyOf(resp))
	}
	fresh := s.browser()
	fresh.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	if resp := fresh.do("PUT", "/api/v1/core/users/"+string(owen)+"/password", map[string]any{"mustChange": true}, nil); s.problem(resp) != auth.ErrStepUpRequired.Code {
		t.Errorf("without confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var me Session
	admin.do("GET", "/api/v1/session", nil, &me)
	if resp := admin.do("PUT", "/api/v1/core/users/"+string(me.User.ID)+"/password", map[string]any{"password": "my own new password"}, nil); s.problem(resp) != auth.ErrOwnPassword.Code {
		t.Errorf("setting your own = %d %s", resp.StatusCode, bodyOf(resp))
	}
}

func TestOpeningAnAccountIsOneChange(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	// The owner's username is taken, so the account isn't opened either.
	resp := admin.do("POST", "/api/v1/core/accounts/open", map[string]any{"slug": "acme", "name": "Acme", "owner": map[string]any{"username": "ADA"}}, nil)
	if resp.StatusCode != http.StatusConflict || s.problem(resp) != auth.ErrUsernameTaken.Code {
		t.Fatalf("an owner whose name is taken = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var accounts struct {
		Items []map[string]any `json:"items"`
	}
	admin.do("GET", "/api/v1/core/accounts", nil, &accounts)
	if len(accounts.Items) != 0 {
		t.Errorf("the account was opened without its owner: %+v", accounts.Items)
	}
	var opened OpenedAccount
	if resp := admin.do("POST", "/api/v1/core/accounts/open", map[string]any{"slug": "acme", "name": "Acme", "owner": map[string]any{"username": "owen"}}, &opened); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("opening = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if len(opened.ChangeSet.Changes) != 2 || opened.ChangeSet.Changes[1].Kind != identity.KindUser {
		t.Errorf("the account and its owner are one change set: %+v", opened.ChangeSet.Changes)
	}
}

func TestSessionsFollowGrantsOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	var opened OpenedAccount
	admin.do("POST", "/api/v1/core/accounts/open", map[string]any{"slug": "acme", "name": "Acme", "owner": map[string]any{"username": "owen"}}, &opened)
	s.finish("admin", opened.ChangeSet)
	owner := s.browser()
	owner.do("POST", "/api/v1/auth/links/use", map[string]any{"code": opened.Link.Code, "password": "an owner's own passphrase"}, nil)
	before := owner.cookie(auth.SessionCookie)

	u := admin.user(opened.Owner.ID)
	spec := u.Spec
	spec.Grants = []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(opened.ChangeSet.Changes[0].ID)}}
	resp := admin.do("PUT", "/api/v1/core/users/"+string(u.ID), map[string]any{"spec": spec, "revision": u.Revision}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("making owen read-only = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", changeSetOf(t, resp))

	// The owner's next request gets a new session cookie for what he may do
	// now.
	var session Session
	if resp := owner.do("GET", "/api/v1/session", nil, &session); resp.StatusCode != http.StatusOK {
		t.Fatalf("session = %d %s", resp.StatusCode, bodyOf(resp))
	}
	after := owner.cookie(auth.SessionCookie)
	if after == before || after == "" {
		t.Error("the session kept its cookie after its grants changed")
	}
	if strings.Join(session.Permissions, ",") != "*.read" {
		t.Errorf("permissions = %v", session.Permissions)
	}
}

// directory is an outside provider for the API tests: a password one, and
// a redirect one, as LDAP and OpenID Connect add-ons are.
type directory struct{ method auth.ProviderMethod }

func (d directory) Method() auth.ProviderMethod { return d.method }

func (directory) CheckPassword(ctx context.Context, username, password string) (*auth.ExternalIdentity, error) {
	if password != "directory pass" {
		return nil, auth.ErrBadCredentials.New()
	}
	return &auth.ExternalIdentity{Subject: "uid=" + username, Username: username}, nil
}

func (directory) Begin(ctx context.Context, callback string) (string, []byte, error) {
	return "https://idp.example/authorize?redirect_uri=" + url.QueryEscape(callback), []byte("nonce"), nil
}

func (directory) Complete(ctx context.Context, state []byte, callback string, q url.Values) (*auth.ExternalIdentity, error) {
	if string(state) != "nonce" || q.Get("code") != "good" {
		return nil, auth.ErrBadCredentials.New()
	}
	return &auth.ExternalIdentity{Subject: "sub-1", Username: "ada.sso"}, nil
}

func (s *stack) connectProvider(name string, method auth.ProviderMethod) store.ID {
	s.t.Helper()
	ctx := context.Background()
	if err := s.hooks.Register(hooks.Registration{
		Point: hooks.AuthProvider, Kind: hooks.Provide, Owner: "addon:" + name,
		Provider: &hooks.Provider{Name: name, Title: name, Open: func(context.Context, json.RawMessage) (any, error) { return directory{method}, nil }},
	}); err != nil {
		s.t.Fatal(err)
	}
	raw, _ := json.Marshal(identity.ProviderSpec{Capability: "identity", Provider: name, Title: "The " + name})
	r := &store.Resource{Meta: store.Meta{ID: store.NewID("signprov"), Kind: identity.ProviderKind.Name(), Scope: store.ServerScope()}, Spec: raw}
	if err := s.store.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, r) }); err != nil {
		s.t.Fatal(err)
	}
	return r.ID
}

func TestLinkingProvidersOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	dir := s.connectProvider("ldap", auth.MethodPassword)
	sso := s.connectProvider("oidc", auth.MethodRedirect)

	// Linking changes how someone signs in, so it needs them to confirm
	// it's them.
	link := map[string]any{"username": "ada.d", "password": "directory pass"}
	if resp := b.do("POST", "/api/v1/me/links/"+string(dir), link, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("linking without confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}
	b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	var linked identity.LinkView
	if resp := b.do("POST", "/api/v1/me/links/"+string(dir), link, &linked); resp.StatusCode != http.StatusOK || linked.Username != "ada.d" {
		t.Fatalf("linking = %d %s", resp.StatusCode, bodyOf(resp))
	}
	other := s.browser()
	var answer SignInAnswer
	if resp := other.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada.d", "password": "directory pass"}, &answer); !answer.SignedIn {
		t.Errorf("signing in through the directory = %d %s", resp.StatusCode, bodyOf(resp))
	}

	// A provider with its own page links through a trip there and back,
	// which ends in the security settings.
	var start LinkStart
	if resp := b.do("POST", "/api/v1/me/links/"+string(sso)+"/start", nil, &start); resp.StatusCode != http.StatusOK || !strings.HasPrefix(start.Location, "https://idp.example/authorize") {
		t.Fatalf("starting = %d %s", resp.StatusCode, bodyOf(resp))
	}
	b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp := b.do("GET", "/api/v1/auth/providers/"+string(sso)+"/callback?code=good", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/security#linked="+string(sso) {
		t.Fatalf("back from the provider = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var links struct {
		Links []identity.LinkView `json:"links"`
	}
	b.do("GET", "/api/v1/me/links", nil, &links)
	if len(links.Links) != 2 {
		t.Errorf("links = %+v", links.Links)
	}
	// What goes wrong on the way back shows there too.
	b.do("POST", "/api/v1/me/links/"+string(sso)+"/start", nil, &start)
	resp = b.do("GET", "/api/v1/auth/providers/"+string(sso)+"/callback?code=bad", nil, nil)
	if !strings.HasPrefix(resp.Header.Get("Location"), "/settings/security#error=") {
		t.Errorf("a link that failed = %s", resp.Header.Get("Location"))
	}
}

// Alerts can go to the notification channel types the core has, whose
// secret settings a person's alert settings keep with the rest.
func TestAlertChannelTypesOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	var alerts struct {
		Types []AlertChannelTypeView `json:"types"`
	}
	if resp := b.do("GET", "/api/v1/me/alerts", nil, &alerts); resp.StatusCode != http.StatusOK {
		t.Fatalf("alerts = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var ntfy *AlertChannelTypeView
	for i, ty := range alerts.Types {
		if ty.ID == "ntfy" {
			ntfy = &alerts.Types[i]
		}
	}
	if ntfy == nil || !slices.Equal(ntfy.Secrets, []string{"token", "password"}) {
		t.Fatalf("types = %+v", alerts.Types)
	}
	var settings struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(ntfy.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	for _, name := range ntfy.Secrets {
		if d := settings.Properties[name].Description; strings.Contains(d, "change's secrets") || !strings.Contains(d, "encrypted") {
			t.Errorf("%s is described as %q", name, d)
		}
	}
	if d := settings.Properties["topic"].Description; !strings.Contains(d, "topic") {
		t.Errorf("topic is described as %q", d)
	}
}
