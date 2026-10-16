package addons

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// A screen opens in a frame of its own: its address signs in what the
// page loads and asks for under it, with no session, as the person who
// opened it, for that add-on alone and only while it is used. The page's
// own scripts run, inline ones with the answer's nonce, and they call the
// routes the add-on declares, from the frame's origin, which is none.
func TestScreenFrames(t *testing.T) {
	e := newEnv(t)
	manifest := strings.Replace(guardManifest, "  - {method: GET, path: /scans, summary: List scans}",
		"  - {method: GET, path: /scans, summary: List scans}\n  - {method: POST, path: '/scans/{id}/run', summary: Scan a site now}", 1)
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "core.addon", Scope: store.ServerScope(), Spec: spec(t, withKinds(t, manifest))})
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("installing: %v %+v", err, cs)
	}
	addonID := cs.Changes[0].ID
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	e.runtime.frames.now = func() time.Time { return now }
	mux := http.NewServeMux()
	mux.Handle("/api/v1/addons/{addon}/frames/{token}/{path...}", e.runtime.Frames(nil))
	serve := func(method, path string, header http.Header, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		for k, v := range header {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	answer := func(rec *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		var m map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("answer = %d %s", rec.Code, rec.Body)
		}
		return m
	}
	if _, err := e.runtime.OpenFrame("site-guard", nil); !i18n.ErrUnauthenticated.Is(err) {
		t.Errorf("a frame for nobody: %v", err)
	}
	if _, err := e.runtime.OpenFrame("nothing-here", e.admin); !ErrUnknown.Is(err) {
		t.Errorf("a frame of an add-on that doesn't run: %v", err)
	}
	base, err := e.runtime.OpenFrame("site-guard", e.admin)
	if err != nil || !regexp.MustCompile(`^/api/v1/addons/site-guard/frames/[A-Za-z0-9_-]{43}/$`).MatchString(base) {
		t.Fatalf("frame = %q, %v", base, err)
	}

	// A screen, told who looks, where its frame is and its nonce, with
	// nothing of the session the request carries.
	rec := serve(http.MethodGet, base+"ui/tab?resource=site_1", http.Header{"Cookie": {"racksail_session=secret"}}, "")
	screen := answer(rec)
	var looking auth.Actor
	if err := json.Unmarshal([]byte(screen["actor"]), &looking); err != nil || looking.ID != e.admin.ID {
		t.Errorf("the screen was told %q is looking", screen["actor"])
	}
	if screen["path"] != "tab" || screen["query"] != "resource=site_1" || screen["cookie"] != "" || screen["base"] != base || len(screen["nonce"]) < 20 {
		t.Errorf("screen = %+v", screen)
	}
	h := rec.Header()
	policy := h.Get("Content-Security-Policy")
	for _, want := range []string{"sandbox allow-scripts allow-forms", "script-src 'self' 'nonce-" + screen["nonce"] + "'", "frame-ancestors 'self'", "connect-src 'self'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("the screen's policy lacks %q: %s", want, policy)
		}
	}
	if strings.Contains(policy, "script-src *") || h.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("the add-on's own headers loosened its frame: %v", h)
	}
	if h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Cross-Origin-Resource-Policy") != "cross-origin" || h.Get("X-Frame-Options") != "" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("the screen's headers = %v", h)
	}
	if again := answer(serve(http.MethodGet, base+"ui/tab", nil, "")); again["nonce"] == screen["nonce"] {
		t.Error("two answers share a nonce")
	}
	if rec := serve(http.MethodPost, base+"ui/tab", nil, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("posting to a screen = %d", rec.Code)
	}

	// The routes the add-on declares, as the person, from a page of no
	// origin, after the browser asks.
	rec = serve(http.MethodOptions, base+"routes/scans/site_1/run", http.Header{"Origin": {"null"}, "Access-Control-Request-Method": {"POST"}}, "")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Methods") != "POST" || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("asking to post = %d %v", rec.Code, rec.Header())
	}
	run := answer(serve(http.MethodPost, base+"routes/scans/site_1/run", http.Header{"Origin": {"null"}, "Content-Type": {"application/json"}}, `{"deep":true}`))
	if run["method"] != "POST" || run["path"] != "scans/site_1/run" || run["body"] != `{"deep":true}` || !strings.Contains(run["actor"], string(e.admin.ID)) {
		t.Errorf("a route = %+v", run)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodDelete, base + "routes/scans/site_1/run"},
		{http.MethodGet, base + "routes/reports"},
		{http.MethodGet, base + "secrets"},
	} {
		if rec := serve(c.method, c.path, nil, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d", c.method, c.path, rec.Code)
		}
	}
	if rec := serve(http.MethodOptions, base+"routes/scans/site_1/run", http.Header{"Access-Control-Request-Method": {"DELETE"}}, ""); rec.Code != http.StatusNotFound {
		t.Errorf("asking to delete = %d", rec.Code)
	}

	// The token is for this add-on, while it is used, and for half a day
	// at most.
	gone := func(path, what string) {
		t.Helper()
		if rec := serve(http.MethodGet, path, nil, ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "addons.frame_gone") {
			t.Errorf("%s = %d %s", what, rec.Code, rec.Body)
		}
	}
	gone(strings.Replace(base, "/site-guard/", "/other-addon/", 1)+"ui/tab", "the frame under another add-on")
	gone("/api/v1/addons/site-guard/frames/"+strings.Repeat("a", 43)+"/ui/tab", "a token nobody was given")
	now = now.Add(FrameIdle - time.Minute)
	answer(serve(http.MethodGet, base+"ui/tab", nil, ""))
	now = now.Add(FrameIdle - time.Minute)
	answer(serve(http.MethodGet, base+"ui/tab", nil, ""))
	now = now.Add(FrameIdle + time.Second)
	gone(base+"ui/tab", "a frame left alone too long")

	long, _ := e.runtime.OpenFrame("site-guard", e.admin)
	for range int(FrameLifetime / (FrameIdle / 2)) {
		now = now.Add(FrameIdle / 2)
		serve(http.MethodGet, long+"ui/tab", nil, "")
	}
	now = now.Add(time.Minute)
	gone(long+"ui/tab", "a frame open longer than its lifetime")

	// Turned off, the add-on's frames close.
	open, _ := e.runtime.OpenFrame("site-guard", e.admin)
	off := withKinds(t, manifest)
	off.Enabled = false
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "core.addon", ID: addonID, Spec: spec(t, off)}); err != nil {
		t.Fatal(err)
	}
	gone(open+"ui/tab", "a frame of an add-on turned off")
}
