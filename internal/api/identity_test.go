package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/events"
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
	if resp := b.do("PUT", "/api/v1/me", map[string]any{"name": "Ada L."}, nil); resp.StatusCode != http.StatusOK {
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
	if resp := call("POST", "/api/v1/me/tokens", map[string]any{"name": "more"}); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpUnavailable.Code {
		t.Errorf("a token making tokens = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := call("POST", "/api/v1/core/accounts/open?dryRun=true", map[string]any{"slug": "acme", "name": "Acme"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read-only token opening an account = %d", resp.StatusCode)
	}
	if resp := call("GET", "/api/v1/users", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("a read-only token listing users = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := b.do("DELETE", "/api/v1/me/tokens/"+string(created.ID), nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := call("GET", "/api/v1/session", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a revoked token = %d", resp.StatusCode)
	}
}

func TestPendingStepsOverTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	s.identity.SetPolicy(identity.PolicySpec{RequireTwoFactor: []auth.Role{auth.RoleAdmin}})
	fresh := s.browser()
	fresh.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	if resp := fresh.do("GET", "/api/v1/users", nil, nil); resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrTwoFactorRequired.Code {
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

	var users struct {
		Users []identity.UserView `json:"users"`
	}
	admin.do("GET", "/api/v1/users", nil, &users)
	if len(users.Users) != 2 {
		t.Errorf("users = %+v", users.Users)
	}
	owner.do("GET", "/api/v1/users", nil, &users)
	if len(users.Users) != 1 || users.Users[0].Username != "owen" {
		t.Errorf("the owner sees = %+v", users.Users)
	}

	// Viewing the panel as the owner shows the banner's details, and goes
	// back.
	if resp := admin.do("POST", "/api/v1/users/"+string(opened.Owner.ID)+"/impersonate", nil, nil); resp.StatusCode != http.StatusNoContent {
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
		if e.Permission == "test.site.deploy" {
			found = e.Roles[auth.RoleOwner] == auth.AccessOwnScope && e.Roles[auth.RoleReadOnly] == auth.AccessNone
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
