// Package oidctest runs an OpenID Connect provider for tests. It publishes
// its metadata and keys, signs people in at its authorization endpoint
// without asking anything, and checks token requests as strictly as a real
// provider: the code, the redirect URI, the client's credentials and PKCE.
package oidctest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Server is a running provider.
type Server struct {
	// Issuer is its issuer, the address it runs at.
	Issuer string
	// CA is the PEM certificate of a provider started with NewTLS.
	CA []byte

	ClientID     string
	ClientSecret string
	// AuthMethods are the client authentication methods it publishes and
	// takes; client_secret_basic when empty.
	AuthMethods []string
	// IssParameter publishes and sends RFC 9207's iss parameter.
	IssParameter bool

	// Claims go into every ID token, after the ones it makes itself, and
	// UserInfo is what its UserInfo endpoint answers besides sub.
	Claims   map[string]any
	UserInfo map[string]any
	// Mangle changes an ID token's claims or header before it is signed,
	// for tests of tokens that must be refused.
	Mangle func(header, claims map[string]any)

	srv *httptest.Server

	mu       sync.Mutex
	keys     []signingKey
	codes    map[string]grant
	requests []url.Values
	tokens   int
}

type grant struct {
	query url.Values
	sub   string
}

type signingKey struct {
	kid string
	alg string
	key crypto.Signer
}

// New starts a provider on http://127.0.0.1, which relying parties accept
// because it is this machine, with an RSA key.
func New(t testing.TB) *Server {
	return start(t, false)
}

// NewTLS starts a provider on https://.
func NewTLS(t testing.TB) *Server {
	return start(t, true)
}

func start(t testing.TB, secure bool) *Server {
	t.Helper()
	s := &Server{ClientID: "racksail", ClientSecret: "client-secret", codes: map[string]grant{}}
	s.Rotate(t, "RS256")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.metadata)
	mux.HandleFunc("GET /keys", s.jwks)
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("GET /userinfo", s.userinfo)
	if secure {
		s.srv = httptest.NewTLSServer(mux)
		s.CA = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.srv.Certificate().Raw})
	} else {
		s.srv = httptest.NewServer(mux)
	}
	s.Issuer = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// Rotate makes a new key for alg the one tokens are signed with, keeping
// the old ones published.
func (s *Server) Rotate(t testing.TB, alg string) {
	t.Helper()
	var key crypto.Signer
	var err error
	switch {
	case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	case alg == "ES256":
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case alg == "ES384":
		key, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case alg == "ES512":
		key, err = ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	case alg == "EdDSA":
		_, key, err = ed25519.GenerateKey(rand.Reader)
	default:
		t.Fatalf("oidctest: no keys for %s", alg)
	}
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append([]signingKey{{kid: fmt.Sprintf("key-%d", len(s.keys)+1), alg: alg, key: key}}, s.keys...)
}

// Requests are the authorization requests it was sent, in order.
func (s *Server) Requests() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.requests...)
}

// Tokens counts the codes it traded for tokens.
func (s *Server) Tokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens
}

// SignIn plays the browser: it follows the redirect to the authorization
// endpoint, where the person with subject sub signs in, and returns the
// query the provider sends the browser back to the redirect URI with.
func (s *Server) SignIn(t testing.TB, redirect, sub string) url.Values {
	t.Helper()
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("sub", sub)
	u.RawQuery = q.Encode()
	client := s.srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("the authorization endpoint answered %s", resp.Status)
	}
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return back.Query()
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	methods := s.AuthMethods
	if len(methods) == 0 {
		methods = []string{"client_secret_basic"}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.Issuer,
		"authorization_endpoint":                         s.Issuer + "/authorize?tenant=example",
		"token_endpoint":                                 s.Issuer + "/token",
		"userinfo_endpoint":                              s.Issuer + "/userinfo",
		"jwks_uri":                                       s.Issuer + "/keys",
		"response_types_supported":                       []string{"code"},
		"id_token_signing_alg_values_supported":          []string{"RS256", "PS256", "ES256", "ES384", "ES512", "EdDSA"},
		"token_endpoint_auth_methods_supported":          methods,
		"code_challenge_methods_supported":               []string{"S256"},
		"authorization_response_iss_parameter_supported": s.IssParameter,
	})
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *Server) jwks(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []map[string]any
	for _, k := range s.keys {
		keys = append(keys, PublicJWK(k.key, k.kid, k.alg))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// PublicJWK writes the public half of key as a JWK for signatures.
func PublicJWK(key crypto.Signer, kid, alg string) map[string]any {
	jwk := map[string]any{"kid": kid, "use": "sig", "alg": alg}
	switch pub := key.Public().(type) {
	case *rsa.PublicKey:
		jwk["kty"], jwk["n"], jwk["e"] = "RSA", b64(pub.N.Bytes()), b64(big.NewInt(int64(pub.E)).Bytes())
	case *ecdsa.PublicKey:
		raw, _ := pub.Bytes()
		size := (len(raw) - 1) / 2
		jwk["kty"], jwk["crv"], jwk["x"], jwk["y"] = "EC", pub.Curve.Params().Name, b64(raw[1:1+size]), b64(raw[1+size:])
	case ed25519.PublicKey:
		jwk["kty"], jwk["crv"], jwk["x"] = "OKP", "Ed25519", b64(pub)
	}
	return jwk
}

// authorize signs in whoever the sub parameter names and sends the browser
// back with a code.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	s.requests = append(s.requests, q)
	s.mu.Unlock()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("client_id") != s.ClientID || q.Get("response_type") != "code" || q.Get("tenant") != "example" {
		http.Error(w, "a bad authorization request", http.StatusBadRequest)
		return
	}
	answer := back.Query()
	answer.Set("state", q.Get("state"))
	if s.IssParameter {
		answer.Set("iss", s.Issuer)
	}
	if q.Get("sub") == "" {
		answer.Set("error", "access_denied")
		answer.Set("error_description", "The person turned the sign-in down.")
	} else {
		code := b64(randomBytes())
		s.mu.Lock()
		s.codes[code] = grant{query: q, sub: q.Get("sub")}
		s.mu.Unlock()
		answer.Set("code", code)
	}
	back.RawQuery = answer.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func randomBytes() []byte {
	b := make([]byte, 16)
	rand.Read(b)
	return b
}

func tokenError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	f := r.PostForm
	methods := s.AuthMethods
	if len(methods) == 0 {
		methods = []string{"client_secret_basic"}
	}
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = f.Get("client_id"), f.Get("client_secret")
	}
	switch {
	case basic && !slices.Contains(methods, "client_secret_basic"),
		!basic && secret != "" && !slices.Contains(methods, "client_secret_post"),
		!basic && secret == "" && !slices.Contains(methods, "none"):
		tokenError(w, http.StatusUnauthorized, "invalid_client", "that way of authenticating isn't allowed")
		return
	case id != s.ClientID || (secret != s.ClientSecret && !(secret == "" && slices.Contains(methods, "none"))):
		tokenError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	s.mu.Lock()
	g, ok := s.codes[f.Get("code")]
	delete(s.codes, f.Get("code"))
	s.mu.Unlock()
	switch {
	case f.Get("grant_type") != "authorization_code" || !ok:
		tokenError(w, http.StatusBadRequest, "invalid_grant", "unknown code")
		return
	case f.Get("redirect_uri") != g.query.Get("redirect_uri"):
		tokenError(w, http.StatusBadRequest, "invalid_grant", "another redirect URI")
		return
	case g.query.Get("code_challenge_method") != "S256" || g.query.Get("code_challenge") != challenge(f.Get("code_verifier")):
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the PKCE verifier doesn't match")
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": s.Issuer, "sub": g.sub, "aud": s.ClientID, "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"nonce": g.query.Get("nonce"),
	}
	for k, v := range s.Claims {
		claims[k] = v
	}
	s.mu.Lock()
	key := s.keys[0]
	s.tokens++
	s.mu.Unlock()
	header := map[string]any{"alg": key.alg, "kid": key.kid, "typ": "JWT"}
	if s.Mangle != nil {
		s.Mangle(header, claims)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "access-" + g.sub, "token_type": "Bearer", "expires_in": 300, "id_token": Sign(key.key, header, claims),
	})
}

func (s *Server) userinfo(w http.ResponseWriter, r *http.Request) {
	sub, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer access-")
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	out := map[string]any{"sub": sub}
	for k, v := range s.UserInfo {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return b64(sum[:])
}

// Sign makes a compact JWS of claims with key and the header's algorithm,
// or a token that isn't signed for alg none, or one with a made-up
// signature for HS256.
func Sign(key crypto.Signer, header, claims map[string]any) string {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	alg, _ := header["alg"].(string)
	if alg == "none" {
		return input + "."
	}
	if alg == "HS256" {
		return input + "." + b64([]byte("not checked"))
	}
	var sig []byte
	var err error
	switch key := key.(type) {
	case ed25519.PrivateKey:
		sig = ed25519.Sign(key, []byte(input))
	case *rsa.PrivateKey:
		hash, digest := digest(alg, input)
		if strings.HasPrefix(alg, "PS") {
			sig, err = rsa.SignPSS(rand.Reader, key, hash, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		} else {
			sig, err = rsa.SignPKCS1v15(rand.Reader, key, hash, digest)
		}
	case *ecdsa.PrivateKey:
		_, digest := digest(alg, input)
		var r, s *big.Int
		r, s, err = ecdsa.Sign(rand.Reader, key, digest)
		size := (key.Curve.Params().BitSize + 7) / 8
		sig = make([]byte, 2*size)
		if err == nil {
			r.FillBytes(sig[:size])
			s.FillBytes(sig[size:])
		}
	}
	if err != nil {
		panic(err)
	}
	return input + "." + b64(sig)
}

func digest(alg, input string) (crypto.Hash, []byte) {
	switch alg[2:] {
	case "384":
		sum := sha512.Sum384([]byte(input))
		return crypto.SHA384, sum[:]
	case "512":
		sum := sha512.Sum512([]byte(input))
		return crypto.SHA512, sum[:]
	}
	sum := sha256.Sum256([]byte(input))
	return crypto.SHA256, sum[:]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
