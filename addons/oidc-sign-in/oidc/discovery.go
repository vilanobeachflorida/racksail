// Package oidc is the part of OpenID Connect a relying party needs to sign
// people in with the authorization code flow: discovery, PKCE, the token
// request, ID token validation with the provider's published keys, and the
// UserInfo endpoint.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// maxBody bounds what any endpoint of the provider may answer with.
const maxBody = 1 << 20

// Metadata is what a provider publishes about itself at
// /.well-known/openid-configuration (OpenID Connect Discovery 1.0).
type Metadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	UserinfoEndpoint                  string   `json:"userinfo_endpoint,omitempty"`
	JWKSURI                           string   `json:"jwks_uri"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	IDTokenSigningAlgValuesSupported  []string `json:"id_token_signing_alg_values_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	// IssParameterSupported says the provider names itself in every
	// authorization response (RFC 9207), so a response that doesn't is
	// refused.
	IssParameterSupported bool `json:"authorization_response_iss_parameter_supported,omitempty"`
}

// CheckURL makes sure a provider's address is https, or http to this
// machine, where nothing on the network sees it.
func CheckURL(what, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%s %q isn't an absolute address", what, raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if Loopback(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("%s %q isn't https", what, raw)
}

// Loopback reports whether host is this machine.
func Loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Discover fetches a provider's metadata and checks it: the issuer must be
// the one asked for, exactly, and every endpoint must be safe to use.
func Discover(ctx context.Context, hc *http.Client, issuer string) (*Metadata, error) {
	if err := CheckURL("the issuer", issuer); err != nil {
		return nil, err
	}
	var md Metadata
	if err := getJSON(ctx, hc, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", "", &md); err != nil {
		return nil, fmt.Errorf("oidc: discovering %s: %w", issuer, err)
	}
	if md.Issuer != issuer {
		return nil, fmt.Errorf("oidc: %s says its issuer is %q; the provider's issuer must be written exactly so", issuer, md.Issuer)
	}
	for what, u := range map[string]string{"authorization endpoint": md.AuthorizationEndpoint, "token endpoint": md.TokenEndpoint, "key set": md.JWKSURI} {
		if err := CheckURL("the provider's "+what, u); err != nil {
			return nil, fmt.Errorf("oidc: %w", err)
		}
	}
	if md.UserinfoEndpoint != "" {
		if err := CheckURL("the provider's UserInfo endpoint", md.UserinfoEndpoint); err != nil {
			return nil, fmt.Errorf("oidc: %w", err)
		}
	}
	if len(md.ResponseTypesSupported) > 0 && !slices.Contains(md.ResponseTypesSupported, "code") {
		return nil, errors.New("oidc: the provider doesn't support the authorization code flow")
	}
	return &md, nil
}

// getJSON fetches a JSON document, with a bearer token when one is given.
func getJSON(ctx context.Context, hc *http.Client, u, bearer string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return fmt.Errorf("%s answered with more than %d bytes", u, maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", u, resp.Status)
	}
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); ct != "" && ct != "application/json" && !strings.HasSuffix(ct, "+json") {
		return fmt.Errorf("%s answered with %s, not JSON", u, ct)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s answered with JSON that can't be read: %w", u, err)
	}
	return nil
}
