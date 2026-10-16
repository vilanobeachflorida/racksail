package oidc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Leeway is how far apart the provider's clock and this one may be.
const Leeway = 2 * time.Minute

// Random returns 32 random bytes, base64url, for a state, a nonce or a
// PKCE verifier (RFC 7636, section 4.1).
func Random() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Challenge is the S256 code challenge of a PKCE verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Token is the token endpoint's answer.
type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	IDToken     string `json:"id_token"`
}

// TokenError is a token request the provider refused (RFC 6749, section
// 5.2).
type TokenError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *TokenError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

// Exchange trades an authorization code for tokens. With a secret the
// client authenticates with HTTP Basic, or in the form when the provider
// only takes that; without one it is a public client and PKCE is all that
// binds the code to it.
func Exchange(ctx context.Context, hc *http.Client, md *Metadata, clientID, secret, code, redirectURI, verifier string) (*Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	basic := secret != "" && (len(md.TokenEndpointAuthMethodsSupported) == 0 ||
		slices.Contains(md.TokenEndpointAuthMethodsSupported, "client_secret_basic") ||
		!slices.Contains(md.TokenEndpointAuthMethodsSupported, "client_secret_post"))
	if !basic {
		form.Set("client_id", clientID)
		if secret != "" {
			form.Set("client_secret", secret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		// The client's id and secret are form-encoded before they go into
		// the header (RFC 6749, section 2.3.1).
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: the token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("oidc: the token request: %w", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("oidc: the token endpoint answered with more than %d bytes", maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		var te TokenError
		if json.Unmarshal(body, &te) == nil && te.Code != "" {
			return nil, &te
		}
		return nil, fmt.Errorf("oidc: the token endpoint answered %s", resp.Status)
	}
	var t Token
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("oidc: the token endpoint's answer can't be read: %w", err)
	}
	if t.IDToken == "" {
		return nil, errors.New("oidc: the token endpoint gave no ID token; is openid among the scopes?")
	}
	return &t, nil
}

// UserInfo fetches the claims the UserInfo endpoint has about the person
// the access token is for.
func UserInfo(ctx context.Context, hc *http.Client, md *Metadata, accessToken string) (Claims, error) {
	var c Claims
	if err := getJSON(ctx, hc, md.UserinfoEndpoint, accessToken, &c); err != nil {
		return nil, fmt.Errorf("oidc: fetching UserInfo: %w", err)
	}
	return c, nil
}

// Claims are the claims of an ID token or UserInfo.
type Claims map[string]any

// ParseClaims reads an ID token's payload.
func ParseClaims(payload []byte) (Claims, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var c Claims
	if err := dec.Decode(&c); err != nil || c == nil {
		return nil, errors.New("oidc: the ID token's claims aren't a JSON object")
	}
	return c, nil
}

// String returns a claim that is a string.
func (c Claims) String(name string) string {
	s, _ := c[name].(string)
	return s
}

// Strings returns a claim that is a list of strings, or one string.
func (c Claims) Strings(name string) []string {
	switch v := c[name].(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// True reports whether a claim is true, as a boolean or, as some providers
// send it, the string "true".
func (c Claims) True(name string) bool {
	switch v := c[name].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// time reads a NumericDate claim.
func (c Claims) time(name string) (time.Time, bool) {
	var f float64
	switch v := c[name].(type) {
	case json.Number:
		var err error
		if f, err = v.Float64(); err != nil {
			return time.Time{}, false
		}
	case float64:
		f = v
	default:
		return time.Time{}, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1<<40 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// Check validates an ID token's claims (OpenID Connect Core 1.0, section
// 3.1.3.7): who issued it, that it is for this client, that it hasn't
// expired and that it answers this sign-in's nonce.
func (c Claims) Check(issuer, clientID, nonce string, now time.Time) error {
	if c.String("iss") != issuer {
		return fmt.Errorf("oidc: the ID token was issued by %q, not %s", c.String("iss"), issuer)
	}
	aud := c.Strings("aud")
	if !slices.Contains(aud, clientID) {
		return errors.New("oidc: the ID token is for another client")
	}
	if azp, ok := c["azp"]; (ok && azp != clientID) || (!ok && len(aud) > 1) {
		return errors.New("oidc: the ID token was issued to another party")
	}
	exp, ok := c.time("exp")
	if !ok {
		return errors.New("oidc: the ID token has no expiry")
	}
	if !now.Before(exp.Add(Leeway)) {
		return errors.New("oidc: the ID token has expired")
	}
	if iat, ok := c.time("iat"); !ok || iat.After(now.Add(Leeway)) {
		return errors.New("oidc: the ID token's issue time is missing or in the future")
	}
	if nonce == "" || c.String("nonce") != nonce {
		return errors.New("oidc: the ID token doesn't answer this sign-in's nonce")
	}
	if c.String("sub") == "" {
		return errors.New("oidc: the ID token doesn't say who signed in")
	}
	return nil
}
