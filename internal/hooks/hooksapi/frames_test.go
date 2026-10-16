package hooksapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

const screensManifest = `
format: 1
id: site-guard
name: Site guard
version: 0.3.0
license: Apache-2.0
source: https://example.com/site-guard
author: Example
description: Shows what it found on sites.
apiVersions: [v1]
permissions:
  hooks: []
ui:
  - {type: nav, label: Site guard, path: overview.html}
routes:
  - {method: POST, path: '/sites/{id}/scan', summary: Scan a site now}
`

// syncBuffer keeps what the panel logs, which requests write as they end.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// people is the directory of users of a panel, which says who is turned
// off, and which sessions and tokens ended.
type people struct {
	mu    sync.Mutex
	off   map[store.ID]bool
	ended map[string]bool
}

func (p *people) SignedIn(ctx context.Context, actor *auth.Actor) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if actor.Type == auth.ActorToken {
		return !p.ended[string(actor.ID)]
	}
	return !p.ended[actor.SessionID]
}

func (p *people) end(what string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ended[what] = true
}

func (p *people) User(ctx context.Context, id store.ID) (auth.DirectoryUser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return auth.DirectoryUser{ID: id, Username: "ada", Disabled: p.off[id], Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.ServerScope()}}}, nil
}

func (p *people) Members(ctx context.Context, account store.ID) ([]auth.DirectoryUser, error) {
	return nil, nil
}

func (p *people) SSHKeys(ctx context.Context, user store.ID) ([]auth.SSHKey, error) { return nil, nil }

// signedIn signs in requests with the session cookie of one person, and
// with tokens, when it has some, as they are.
type signedIn struct {
	actor  *auth.Actor
	tokens map[string]*auth.Actor
}

const adaSession = "racksail_session=ada"

func (s signedIn) Authenticate(ctx context.Context, r *http.Request) (*auth.Actor, error) {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if a, ok := s.tokens[bearer]; ok {
			return a.Clone(), nil
		}
		return nil, auth.ErrTokenInvalid.New()
	}
	if r.Header.Get("Cookie") != adaSession {
		return nil, nil
	}
	return s.actor.Clone(), nil
}

// withAddon is a panel running an add-on with a screen and a route, served
// by a stand-in for its process, and what its API logs. Without who, the
// panel signs everyone in as its developer; users is its directory of them.
func withAddon(t *testing.T, who auth.Authenticator, users auth.Directory) (http.Handler, *syncBuffer) {
	t.Helper()
	// Unix socket paths are short, so not under t.TempDir.
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "site-guard.sock"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(hooks.AddonHealth{Status: "ok", Version: "0.3.0", APIVersion: "v1"})
	})
	mux.HandleFunc("GET /ui/{path...}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><script nonce="`+r.Header.Get("Racksail-Nonce")+`">scan("`+r.Header.Get("Racksail-Frame-Base")+`routes/sites/site_1/scan")</script>`+r.Header.Get("Racksail-Actor"))
	})
	mux.HandleFunc("POST /routes/{path...}", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"path": r.PathValue("path"), "actor": r.Header.Get("Racksail-Actor")})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	var rt *addons.Runtime
	e := modulestest.New(t, modulestest.Options{
		Kinds: []store.Kind{addons.Addon, addons.AccountAddon},
		Reconcilers: func(svc *modules.Services) []reconcile.Reconciler {
			rt = addons.NewRuntime(addons.Options{
				Registry: svc.Hooks.Registry, Store: svc.Store, Kinds: svc.Kinds, Agent: svc.Agent, Events: svc.Events, Audit: svc.Audit,
				Tokens: addons.NewMemoryTokens(), SafeMode: addons.OpenSafeMode(filepath.Join(dir, "state"), nil),
				TokenDir: filepath.Join(dir, "tokens"), SocketDir: dir, Log: slog.New(slog.DiscardHandler),
			})
			svc.Addons = rt
			return rt.Reconcilers()
		},
	}, &module{modules.NewBase([]byte(`hardware: {arch: [amd64, arm64]}
id: net
name: Net
description: A module for tests.
version: 0.1.0
license: AGPL-3.0-or-later
category: operations
profiles: [all-in-one]
required: true
dependencies: {}
cost: {idleMemoryMB: 4, activeMemoryMB: 40, diskMB: 10}
upstream: []
`))})
	man, err := hooks.ParseAddonManifest([]byte(screensManifest))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(addons.AddonSpec{
		ID: "site-guard", Manifest: screensManifest, Enabled: true, Approved: man.Permissions,
		Package: addons.Package{URL: "https://example.com/site-guard-0.3.0.tar.gz", SHA256: strings.Repeat("ab", 32), Size: 1 << 20, Format: "tar.gz"},
	})
	e.MustApply(t, changeset.Request{Actor: e.Admin, Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: addons.Addon.Name(), Scope: store.ServerScope(), Spec: raw}}})
	e.Settle(t)
	if _, _, _, _, ok := rt.Running("site-guard"); !ok {
		t.Fatalf("the add-on isn't running: %v", rt.Instances())
	}
	e.Services.Identity = users
	logs := &syncBuffer{}
	srvAPI := api.New(api.Options{Dev: who == nil, Auth: who, Modules: e.Registry, Services: e.Services, Log: slog.New(slog.NewTextHandler(logs, nil))})
	return srvAPI.Handler(), logs
}

// openScreen opens a screen, with a session cookie, or a token as
// "Bearer <token>", when given one, and returns the frame the panel sends
// it to.
func openScreen(t *testing.T, h http.Handler, path, cookie string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer, ok := strings.CutPrefix(cookie, "Bearer "); ok {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	frame := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(frame, "/api/v1/addons/site-guard/frames/") {
		t.Fatalf("opening %s = %d %q %s", path, rec.Code, frame, rec.Body)
	}
	return frame
}

// A screen opens in a frame of its own: the panel sends a person who may
// use the add-on there, and what the frame's page asks for there is signed
// in by its address, not the session, so the page, of no origin, can call
// the add-on's routes, which no other site's page can with the session.
// The log leaves the frame's token out.
func TestAddonScreensOpenInFrames(t *testing.T) {
	h, logs := withAddon(t, nil, nil)
	frame := openScreen(t, h, "/api/v1/addons/site-guard/ui/overview.html?tab=sites", "")
	if !strings.HasSuffix(frame, "/ui/overview.html?tab=sites") {
		t.Fatalf("the frame of the screen = %q", frame)
	}
	base := strings.TrimSuffix(frame, "ui/overview.html?tab=sites")
	token := strings.TrimSuffix(strings.TrimPrefix(base, "/api/v1/addons/site-guard/frames/"), "/")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, frame, nil))
	page := rec.Body.String()
	policy := rec.Header().Get("Content-Security-Policy")
	if rec.Code != http.StatusOK || !strings.Contains(page, `scan("`+base+`routes/sites/site_1/scan")`) || !strings.Contains(page, "developer") {
		t.Fatalf("the frame = %d %s", rec.Code, page)
	}
	if !strings.Contains(policy, "sandbox allow-scripts") || !strings.Contains(page, `nonce="`) || rec.Header().Get("X-Frame-Options") != "" ||
		rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Errorf("the frame's headers = %v", rec.Header())
	}

	// Its page posts to the add-on's route from no origin.
	scan := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Origin", "null")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := scan(base + "routes/sites/site_1/scan"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"path":"sites/site_1/scan"`) {
		t.Errorf("a frame's page calling its route = %d %s", rec.Code, rec.Body)
	}
	// The same route with the session still turns other sites' pages away.
	if rec := scan("/api/v1/addons/site-guard/sites/site_1/scan"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross_site") {
		t.Errorf("another site's page calling the route with the session = %d %s", rec.Code, rec.Body)
	}
	if rec := scan("/api/v1/addons/site-guard/frames/" + strings.Repeat("b", 43) + "/routes/sites/site_1/scan"); rec.Code != http.StatusNotFound {
		t.Errorf("a frame nobody opened = %d %s", rec.Code, rec.Body)
	}

	out := logs.String()
	if strings.Contains(out, token) || !strings.Contains(out, "/api/v1/addons/site-guard/frames/…/ui/overview.html") {
		t.Errorf("the log keeps the frame's token: %s", out)
	}
}

// A frame's requests, which carry no session, are the person's it was
// opened for: their own rate limit, and their name in the log. It stops
// working once they're turned off, and stays closed when they're turned on
// again.
func TestFramesCloseForPeopleTurnedOff(t *testing.T) {
	ada := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "ada", Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.ServerScope()}}}
	dir := &people{off: map[store.ID]bool{}, ended: map[string]bool{}}
	h, logs := withAddon(t, signedIn{actor: ada}, dir)
	frame := openScreen(t, h, "/api/v1/addons/site-guard/ui/overview.html", adaSession)
	load := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, frame, nil))
		return rec
	}
	if rec := load(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), string(ada.ID)) {
		t.Fatalf("the frame = %d %s", rec.Code, rec.Body)
	}
	var framed bool
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "/frames/…/ui/overview.html") {
			framed = strings.Contains(line, `actor="user:`+string(ada.ID))
		}
	}
	if !framed {
		t.Errorf("the log doesn't say whose the frame's request was: %s", logs)
	}
	dir.mu.Lock()
	dir.off[ada.ID] = true
	dir.mu.Unlock()
	if rec := load(); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "addons.frame_gone") {
		t.Errorf("the frame of someone turned off = %d %s", rec.Code, rec.Body)
	}
	dir.mu.Lock()
	dir.off[ada.ID] = false
	dir.mu.Unlock()
	if rec := load(); rec.Code != http.StatusNotFound {
		t.Errorf("the frame once they're back = %d %s", rec.Code, rec.Body)
	}
}

// A frame works only as long as what its person opened it with does: it
// closes once the session ends, when they sign out or their sessions are
// ended, or once the API token it was opened with is revoked, and it stays
// closed.
func TestFramesCloseWithTheirSignIn(t *testing.T) {
	ada := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "ada", Source: auth.SourceUI, SessionID: "sess_ada",
		Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.ServerScope()}}}
	token := &auth.Actor{Type: auth.ActorToken, ID: store.NewID("tok"), Name: "scripts", User: ada.ID, Source: auth.SourceAPI,
		Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.ServerScope()}}}
	dir := &people{off: map[store.ID]bool{}, ended: map[string]bool{}}
	h, _ := withAddon(t, signedIn{actor: ada, tokens: map[string]*auth.Actor{"rst_scripts": token}}, dir)
	load := func(frame string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, frame, nil))
		return rec.Code
	}
	bySession := openScreen(t, h, "/api/v1/addons/site-guard/ui/overview.html", adaSession)
	byToken := openScreen(t, h, "/api/v1/addons/site-guard/ui/overview.html", "Bearer rst_scripts")
	if load(bySession) != http.StatusOK || load(byToken) != http.StatusOK {
		t.Fatal("the frames don't work")
	}
	dir.end("sess_ada")
	if code := load(bySession); code != http.StatusNotFound {
		t.Errorf("the frame of a session that ended = %d", code)
	}
	if code := load(byToken); code != http.StatusOK {
		t.Errorf("the frame of the token, which still works = %d", code)
	}
	dir.end(string(token.ID))
	if code := load(byToken); code != http.StatusNotFound {
		t.Errorf("the frame of a token revoked = %d", code)
	}
	dir.mu.Lock()
	dir.ended = map[string]bool{}
	dir.mu.Unlock()
	if load(bySession) != http.StatusNotFound || load(byToken) != http.StatusNotFound {
		t.Error("a frame closed opens again")
	}
}
