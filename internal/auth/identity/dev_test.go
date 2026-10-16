package identity

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
)

// What racksail dev's developer does is recorded with where the request
// came from, without its port, and the surface it came through.
func TestDevAuthenticatorSaysWhereRequestsComeFrom(t *testing.T) {
	h := newHarness(t)
	dev, err := h.svc.Developer(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	authn := DevAuthenticator{Service: h.svc, User: dev}
	for _, tc := range []struct {
		name   string
		header http.Header
		want   auth.Source
	}{
		{"the panel's pages", http.Header{"Sec-Fetch-Site": {"same-origin"}, "User-Agent": {"Mozilla/5.0"}}, auth.SourceUI},
		{"the racksail command", http.Header{"User-Agent": {"racksail/1.4.0 (linux/amd64)"}}, auth.SourceCLI},
		{"another program", http.Header{"User-Agent": {"curl/8.14.1"}}, auth.SourceAPI},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
		r.RemoteAddr = "127.0.0.1:63049"
		r.Header = tc.header
		a, err := authn.Authenticate(h.ctx, r)
		if err != nil || a.ID != dev || a.Source != tc.want || a.IP != "127.0.0.1" {
			t.Errorf("%s = %+v, %v", tc.name, a, err)
		}
		// Behind the API, the address it worked out wins.
		a, err = authn.Authenticate(auth.WithClient(h.ctx, auth.Client{IP: "::1"}), r)
		if err != nil || a.IP != "::1" || a.Source != tc.want {
			t.Errorf("%s behind the API = %+v, %v", tc.name, a, err)
		}
	}
	// A session of one's own still signs in as it says.
	ada := h.admin("ada")
	_, secret := h.signIn("ada")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: secret})
	if a, err := authn.Authenticate(auth.WithClient(h.ctx, client), r); err != nil || a.ID != ada.ID || a.Source != auth.SourceUI {
		t.Errorf("a session = %+v, %v", a, err)
	}
}
