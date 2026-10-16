package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/addons/internal/serve"
	"github.com/vilanobeachflorida/racksail/addons/oidc-sign-in/oidc"
)

// Settings are what the admin who connects the provider gives: the
// provider's issuer and the client registered with it, and which claims say
// who people are. The panel fills Secret in from the provider's secret.
type Settings struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientId"`
	// Secret is the client secret; without one, the panel is a public
	// client, which PKCE protects.
	Secret string   `json:"secret,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
	// Prompt asks the provider to sign people in again (login), to ask for
	// consent (consent) or to let them choose an account (select_account).
	Prompt string `json:"prompt,omitempty"`

	UsernameClaim string `json:"usernameClaim,omitempty"`
	NameClaim     string `json:"nameClaim,omitempty"`
	GroupsClaim   string `json:"groupsClaim,omitempty"`
	// AllowedGroups and AllowedDomains, when given, are the groups people
	// must be in, and the domains their verified email address must be
	// at, to sign in.
	AllowedGroups  []string `json:"allowedGroups,omitempty"`
	AllowedDomains []string `json:"allowedDomains,omitempty"`
	// AcceptUnverifiedEmail passes on email addresses the provider hasn't
	// verified. Off, they are left out, since the panel can match people by
	// email and anyone can type any address at some providers.
	AcceptUnverifiedEmail bool `json:"acceptUnverifiedEmail,omitempty"`

	CACertificate  string `json:"caCertificate,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

const (
	defaultTimeout = 10
	maxTimeout     = 60
	// signInAge is how long someone has to sign in at the provider.
	signInAge = 10 * time.Minute
	// cacheAge is how long metadata and keys are kept.
	cacheAge = 10 * time.Minute
)

var (
	scopePattern = regexp.MustCompile(`^[\x21\x23-\x5b\x5d-\x7e]+$`)
	claimPattern = regexp.MustCompile(`^[A-Za-z0-9_:./-]{1,128}$`)
	prompts      = []string{"", "none", "login", "consent", "select_account"}
)

// load reads the settings, fills in defaults and checks that they can
// work.
func load(raw json.RawMessage) (*Settings, error) {
	var s Settings
	if err := serve.Decode(raw, &s, "OpenID Connect settings"); err != nil {
		return nil, err
	}
	def := func(v *string, d string) {
		if *v = strings.TrimSpace(*v); *v == "" {
			*v = d
		}
	}
	def(&s.UsernameClaim, "preferred_username")
	def(&s.NameClaim, "name")
	def(&s.GroupsClaim, "groups")
	if len(s.Scopes) == 0 {
		s.Scopes = []string{"openid", "profile", "email"}
	}
	if !slices.Contains(s.Scopes, "openid") {
		s.Scopes = append([]string{"openid"}, s.Scopes...)
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = defaultTimeout
	}
	u, err := url.Parse(s.Issuer)
	switch {
	case s.Issuer == "":
		return nil, serve.Invalid("issuer is the provider's address, such as https://accounts.example.com")
	case err != nil || u.RawQuery != "":
		return nil, serve.Invalid("issuer %q is an address without a query", s.Issuer)
	case strings.TrimSpace(s.ClientID) == "":
		return nil, serve.Invalid("clientId is the client the provider registered for the panel")
	case !slices.Contains(prompts, s.Prompt):
		return nil, serve.Invalid("prompt is none, login, consent or select_account")
	case s.TimeoutSeconds < 1 || s.TimeoutSeconds > maxTimeout:
		return nil, serve.Invalid("timeoutSeconds is from 1 to %d", maxTimeout)
	}
	if err := oidc.CheckURL("issuer", s.Issuer); err != nil {
		return nil, serve.Invalid("%v; only a provider on this server may use http", err)
	}
	for _, sc := range s.Scopes {
		if !scopePattern.MatchString(sc) {
			return nil, serve.Invalid("scope %q has characters scopes can't", sc)
		}
	}
	for name, claim := range map[string]string{"usernameClaim": s.UsernameClaim, "nameClaim": s.NameClaim, "groupsClaim": s.GroupsClaim} {
		if !claimPattern.MatchString(claim) {
			return nil, serve.Invalid("%s %q isn't a claim's name", name, claim)
		}
	}
	for i, d := range s.AllowedDomains {
		s.AllowedDomains[i] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if s.AllowedDomains[i] == "" || strings.ContainsAny(s.AllowedDomains[i], "@ /") {
			return nil, serve.Invalid("allowedDomains holds domains, such as example.com")
		}
	}
	if s.CACertificate != "" {
		if _, err := pool(s.CACertificate); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

func pool(caPEM string) (*x509.CertPool, error) {
	p, err := x509.SystemCertPool()
	if err != nil {
		p = x509.NewCertPool()
	}
	if !p.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, serve.Invalid("caCertificate holds no PEM certificate")
	}
	return p, nil
}

// provider answers the auth-provider capability. It keeps providers'
// metadata and keys for a while; everything about a sign-in in progress
// is kept by the panel, which hands it back.
type provider struct {
	now func() time.Time

	mu   sync.Mutex
	meta map[string]cached[*oidc.Metadata]
	keys map[string]cached[*oidc.KeySet]
}

type cached[T any] struct {
	value   T
	fetched time.Time
}

func newProvider() *provider {
	return &provider{now: time.Now, meta: map[string]cached[*oidc.Metadata]{}, keys: map[string]cached[*oidc.KeySet]{}}
}

func (s *Settings) client() (*http.Client, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if s.CACertificate != "" {
		p, err := pool(s.CACertificate)
		if err != nil {
			return nil, err
		}
		t.TLSClientConfig.RootCAs = p
	}
	return &http.Client{
		Transport: t,
		Timeout:   time.Duration(s.TimeoutSeconds) * time.Second,
		// A provider's endpoints answer directly; following a redirect
		// could leave https.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func (p *provider) metadata(ctx context.Context, hc *http.Client, issuer string) (*oidc.Metadata, error) {
	p.mu.Lock()
	c, ok := p.meta[issuer]
	p.mu.Unlock()
	if ok && p.now().Sub(c.fetched) < cacheAge {
		return c.value, nil
	}
	md, err := oidc.Discover(ctx, hc, issuer)
	if err != nil {
		return nil, serve.Failed("%v", err)
	}
	p.mu.Lock()
	p.meta[issuer] = cached[*oidc.Metadata]{value: md, fetched: p.now()}
	p.mu.Unlock()
	return md, nil
}

// verify checks an ID token's signature with the provider's keys, fetching
// them again once when the token names a key they don't have.
func (p *provider) verify(ctx context.Context, hc *http.Client, md *oidc.Metadata, token string) ([]byte, error) {
	algs := oidc.Algorithms
	if len(md.IDTokenSigningAlgValuesSupported) > 0 {
		algs = md.IDTokenSigningAlgValuesSupported
	}
	for fresh := false; ; fresh = true {
		p.mu.Lock()
		c, ok := p.keys[md.JWKSURI]
		p.mu.Unlock()
		if fresh || !ok || p.now().Sub(c.fetched) >= cacheAge {
			ks, err := oidc.FetchKeys(ctx, hc, md.JWKSURI)
			if err != nil {
				return nil, serve.Failed("%v", err)
			}
			c = cached[*oidc.KeySet]{value: ks, fetched: p.now()}
			p.mu.Lock()
			p.keys[md.JWKSURI] = c
			p.mu.Unlock()
			fresh = true
		}
		payload, err := c.value.Verify(token, algs)
		if errors.Is(err, oidc.ErrUnknownKey) && !fresh {
			continue
		}
		if err != nil {
			return nil, serve.Failed("%v", err)
		}
		return payload, nil
	}
}

// describe tells the panel that people sign in on the provider's own page.
func (p *provider) describe(ctx context.Context, settings, input json.RawMessage) (any, error) {
	if _, err := load(settings); err != nil {
		return nil, err
	}
	return map[string]string{"method": "redirect"}, nil
}

// pending is what the panel keeps of a sign-in while the person is at the
// provider.
type pending struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Callback string `json:"callback"`
	Issuer   string `json:"issuer"`
	Started  int64  `json:"started"`
}

type redirectStart struct {
	Callback string `json:"callback"`
}

type redirectBegun struct {
	Redirect string `json:"redirect"`
	State    []byte `json:"state"`
}

// begin sends the browser to the provider's authorization endpoint with a
// new state, nonce and PKCE challenge.
func (p *provider) begin(ctx context.Context, settings, input json.RawMessage) (any, error) {
	s, err := load(settings)
	if err != nil {
		return nil, err
	}
	var in redirectStart
	if err := serve.Decode(input, &in, "start of the sign-in"); err != nil {
		return nil, err
	}
	cb, err := url.Parse(in.Callback)
	if err != nil || (cb.Scheme != "https" && cb.Scheme != "http") || cb.Host == "" || cb.Fragment != "" {
		return nil, serve.Invalid("the callback %q isn't an address the provider can send the browser back to", in.Callback)
	}
	hc, err := s.client()
	if err != nil {
		return nil, err
	}
	md, err := p.metadata(ctx, hc, s.Issuer)
	if err != nil {
		return nil, err
	}
	pd := pending{State: oidc.Random(), Nonce: oidc.Random(), Verifier: oidc.Random(), Callback: in.Callback, Issuer: s.Issuer, Started: p.now().Unix()}
	to, err := url.Parse(md.AuthorizationEndpoint)
	if err != nil {
		return nil, serve.Failed("the provider's authorization endpoint can't be read")
	}
	q := to.Query()
	q.Set("response_type", "code")
	q.Set("client_id", s.ClientID)
	q.Set("redirect_uri", in.Callback)
	q.Set("scope", strings.Join(s.Scopes, " "))
	q.Set("state", pd.State)
	q.Set("nonce", pd.Nonce)
	q.Set("code_challenge", oidc.Challenge(pd.Verifier))
	q.Set("code_challenge_method", "S256")
	if s.Prompt != "" {
		q.Set("prompt", s.Prompt)
	}
	to.RawQuery = q.Encode()
	state, err := json.Marshal(pd)
	if err != nil {
		return nil, err
	}
	return redirectBegun{Redirect: to.String(), State: state}, nil
}

type redirectReturn struct {
	State    []byte     `json:"state"`
	Callback string     `json:"callback"`
	Query    url.Values `json:"query"`
}

type identity struct {
	Subject  string   `json:"subject"`
	Username string   `json:"username,omitempty"`
	Name     string   `json:"name,omitempty"`
	Email    string   `json:"email,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

type answer struct {
	Identity *identity `json:"identity,omitempty"`
	Refused  bool      `json:"refused,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

// reasons say, for people, why a provider turned a sign-in down.
var reasons = map[string]string{
	"access_denied":              "the sign-in was turned down at the provider",
	"login_required":             "the provider needs you to sign in there first",
	"interaction_required":       "the provider needs you to finish something there first",
	"consent_required":           "the provider needs your consent first",
	"account_selection_required": "the provider needs you to choose an account first",
}

// complete finishes the sign-in with what the browser came back with: it
// checks the response is the one this sign-in waits for, trades the code
// for tokens and validates the ID token.
func (p *provider) complete(ctx context.Context, settings, input json.RawMessage) (any, error) {
	s, err := load(settings)
	if err != nil {
		return nil, err
	}
	var in redirectReturn
	if err := serve.Decode(input, &in, "return from the provider"); err != nil {
		return nil, err
	}
	var pd pending
	if err := json.Unmarshal(in.State, &pd); err != nil || pd.State == "" || pd.Nonce == "" || pd.Verifier == "" {
		return nil, serve.Invalid("the sign-in's state can't be read")
	}
	switch {
	case pd.Issuer != s.Issuer:
		return nil, serve.Failed("the sign-in was started with another provider")
	case p.now().Sub(time.Unix(pd.Started, 0)) > signInAge:
		return answer{Refused: true, Reason: "the sign-in took too long; start it again"}, nil
	case in.Callback != pd.Callback:
		return nil, serve.Failed("the browser came back to another address than the sign-in started from")
	}
	q := in.Query
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(pd.State)) != 1 {
		return nil, serve.Failed("the provider came back for another sign-in")
	}
	hc, err := s.client()
	if err != nil {
		return nil, err
	}
	md, err := p.metadata(ctx, hc, s.Issuer)
	if err != nil {
		return nil, err
	}
	// RFC 9207: a response that names its issuer must name this one, and
	// one from a provider that always names it must do so.
	if iss, ok := q["iss"]; (ok && (len(iss) != 1 || iss[0] != s.Issuer)) || (!ok && md.IssParameterSupported) {
		return nil, serve.Failed("the response isn't from %s", s.Issuer)
	}
	if code := q.Get("error"); code != "" {
		reason := q.Get("error_description")
		if reason == "" {
			reason = reasons[code]
		}
		if reason == "" {
			reason = "the provider answered " + code
		}
		return answer{Refused: true, Reason: reason}, nil
	}
	if q.Get("code") == "" {
		return nil, serve.Failed("the provider came back without a code")
	}
	token, err := oidc.Exchange(ctx, hc, md, s.ClientID, s.Secret, q.Get("code"), pd.Callback, pd.Verifier)
	if err != nil {
		return nil, serve.Failed("the provider didn't take the code: %v", err)
	}
	payload, err := p.verify(ctx, hc, md, token.IDToken)
	if err != nil {
		return nil, err
	}
	claims, err := oidc.ParseClaims(payload)
	if err != nil {
		return nil, serve.Failed("%v", err)
	}
	if err := claims.Check(s.Issuer, s.ClientID, pd.Nonce, p.now()); err != nil {
		return nil, serve.Failed("%v", err)
	}
	if missing(claims, s) && md.UserinfoEndpoint != "" && token.AccessToken != "" {
		info, err := oidc.UserInfo(ctx, hc, md, token.AccessToken)
		if err != nil {
			return nil, serve.Failed("%v", err)
		}
		// UserInfo about someone else is a substitution attack (OpenID
		// Connect Core 1.0, section 5.3.2).
		if info.String("sub") != claims.String("sub") {
			return nil, serve.Failed("the provider's UserInfo is about someone else")
		}
		for k, v := range info {
			if _, ok := claims[k]; !ok {
				claims[k] = v
			}
		}
	}
	return s.answer(claims), nil
}

// missing reports whether the ID token lacks claims UserInfo may have.
func missing(c oidc.Claims, s *Settings) bool {
	for _, name := range []string{s.UsernameClaim, s.NameClaim, "email"} {
		if _, ok := c[name]; !ok {
			return true
		}
	}
	_, ok := c[s.GroupsClaim]
	return !ok && len(s.AllowedGroups) > 0
}

func (s *Settings) answer(c oidc.Claims) answer {
	email := c.String("email")
	if !s.AcceptUnverifiedEmail && !c.True("email_verified") {
		email = ""
	}
	if len(s.AllowedDomains) > 0 {
		_, domain, _ := strings.Cut(strings.ToLower(email), "@")
		if email == "" || !slices.Contains(s.AllowedDomains, domain) {
			return answer{Refused: true, Reason: "your email address isn't at a domain that may sign in here"}
		}
	}
	groups := c.Strings(s.GroupsClaim)
	if len(s.AllowedGroups) > 0 && !slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(s.AllowedGroups, g) }) {
		return answer{Refused: true, Reason: "you aren't in a group that may sign in here"}
	}
	name := c.String(s.NameClaim)
	if name == "" {
		name = strings.TrimSpace(c.String("given_name") + " " + c.String("family_name"))
	}
	return answer{Identity: &identity{
		Subject:  c.String("sub"),
		Username: c.String(s.UsernameClaim),
		Name:     name,
		Email:    email,
		Groups:   groups,
	}}
}
