package api

import "testing"

// The access log keeps the paths of routes signed in by a token in their
// path, without the token.
func TestLoggedPaths(t *testing.T) {
	s := &Server{credentials: [][]string{
		{"api", "v1", "addons", "{addon}", "frames", "{token}", "{path...}"},
		{"api", "v1", "shares", "{token}"},
	}}
	for path, want := range map[string]string{
		"/api/v1/addons/site-guard/frames/abc/ui/index.html": "/api/v1/addons/site-guard/frames/…/ui/index.html",
		"/api/v1/addons/site-guard/frames/abc":               "/api/v1/addons/site-guard/frames/…",
		"/api/v1/addons/site-guard/scans":                    "/api/v1/addons/site-guard/scans",
		"/api/v1/addons/site-guard/frames":                   "/api/v1/addons/site-guard/frames",
		"/api/v1/shares/abc":                                 "/api/v1/shares/…",
		"/api/v1/shares/abc/more":                            "/api/v1/shares/abc/more",
		"/api/v1/web/sites":                                  "/api/v1/web/sites",
	} {
		if got := s.loggedPath(path); got != want {
			t.Errorf("%s is logged as %s, want %s", path, got, want)
		}
	}
}
