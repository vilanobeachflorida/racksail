package edge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/fcgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

func shortTemp(t *testing.T) string {
	t.Helper()
	// Unix socket paths are limited to about 100 bytes, so avoid t.TempDir.
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestTableMatching(t *testing.T) {
	ctx := context.Background()
	table := NewTable()
	site, wild, any := store.NewID("site"), store.NewID("site"), store.NewID("site")
	if err := table.SetRoutes(ctx, site, []Route{
		{ID: "main", Hosts: []string{"example.com", "www.example.com", "2001:db8::1"}, Handler: Handler{Type: HandleRespond, Body: "site"}},
		{ID: "api", Hosts: []string{"example.com"}, Path: "/api", Handler: Handler{Type: HandleRespond, Body: "api"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := table.SetRoutes(ctx, wild, []Route{{ID: "wild", Hosts: []string{"*.example.com"}, Handler: Handler{Type: HandleRespond, Body: "wild"}}}); err != nil {
		t.Fatal(err)
	}
	if err := table.SetRoutes(ctx, any, []Route{{ID: "any", Hosts: []string{CatchAll}, TLS: TLS{Mode: TLSOnDemand, Ask: true}, Handler: Handler{Type: HandleRespond}}}); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ host, path, want string }{
		{"example.com", "/", "main"},
		{"EXAMPLE.com:443", "/about", "main"},
		{"example.com.", "/", "main"},
		{"[2001:db8::1]:443", "/", "main"},
		{"[2001:db8::1]", "/", "main"},
		{"example.com", "/api/users", "api"},
		{"example.com", "/apiary", "main"},
		{"www.example.com", "/", "main"},
		{"shop.example.com", "/", "wild"},
		{"a.b.example.com", "/", "any"},
		{"other.test", "/", "any"},
	}
	for _, tt := range tests {
		r, ok := table.Match(tt.host, tt.path)
		got := ""
		if ok {
			got = r.ID
		}
		if got != tt.want {
			t.Errorf("Match(%s, %s) = %q, want %q", tt.host, tt.path, got, tt.want)
		}
	}
	// Certificates are decided per host: a host whose only route is for a
	// path still has one, and the route for the whole site decides.
	api := store.NewID("site")
	if err := table.SetRoutes(ctx, api, []Route{{ID: "docs", Hosts: []string{"docs.test"}, Path: "/v1", TLS: TLS{Mode: TLSCertificate, Certificate: "docs"}, Handler: Handler{Type: HandleRespond}}}); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{"docs.test": "docs", "example.com": "main", "shop.example.com": "wild", "other.test": "any"} {
		if r, ok := table.MatchHost(host); !ok || r.ID != want {
			t.Errorf("MatchHost(%s) = %q, %v; want %q", host, r.ID, ok, want)
		}
	}
	if err := table.DeleteRoutes(ctx, api); err != nil {
		t.Fatal(err)
	}
	changed := table.Changed()
	if err := table.DeleteRoutes(ctx, wild); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Error("deleting routes should signal a change")
	}
	if table.Len() != 3 || table.Active() != 3 {
		t.Errorf("routes = %d, active %d", table.Len(), table.Active())
	}
	if err := table.SetRoutes(ctx, store.NewID("panel"), []Route{{ID: "panel", Hosts: []string{"panel.test"}, Passive: true, Handler: Handler{Type: HandleRespond}}}); err != nil {
		t.Fatal(err)
	}
	if table.Len() != 4 || table.Active() != 3 {
		t.Errorf("a passive route doesn't count as active: %d, %d", table.Len(), table.Active())
	}
}

func TestRouteValidation(t *testing.T) {
	owner := store.NewID("site")
	bad := []Route{
		{ID: "Main", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"bad host"}, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Path: "/a/../b", Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleStatic, Root: "relative"}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleStatic, Root: "/srv", Fallback: "index.html"}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "udp", Address: "x"}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "tcp", Address: "127.0.0.1:80", Weight: 500}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy, WakeTimeout: time.Hour, Upstreams: []Upstream{{Network: "tcp", Address: "127.0.0.1:80"}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleFastCGI, Root: "/srv/app", Env: map[string]string{"bad-name": "x"}, Upstreams: []Upstream{{Network: "tcp", Address: "127.0.0.1:9000"}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleFastCGI, Root: "/srv/app", Index: "../x.php", Upstreams: []Upstream{{Network: "tcp", Address: "127.0.0.1:9000"}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleRedirect, To: "https://b.test", Code: 200}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Headers: []Header{{Name: "X-A", Value: "a\r\nInjected: 1"}}, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, TLS: TLS{Mode: TLSCertificate}, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, TLS: TLS{Mode: TLSAuto, Ask: true}, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{CatchAll}, TLS: TLS{Mode: TLSAuto}, Handler: Handler{Type: HandleRespond}},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("Validate(%+v) should fail", r)
		}
	}
	good := []Route{
		{ID: "x", Owner: owner, Hosts: []string{CatchAll}, TLS: TLS{Mode: TLSOff}, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test", "*.a.test", "203.0.113.4"}, TLS: TLS{Mode: TLSOnDemand, Ask: true}, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleFastCGI, Root: "/srv/app", SplitPath: []string{".php", ".phar"}, Env: map[string]string{"APP_ENV": "prod"}, Upstreams: []Upstream{{Network: "unix", Address: "/run/php.sock"}}}},
	}
	for _, r := range good {
		if err := r.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", r, err)
		}
	}
	other := store.NewID("site")
	if err := NewTable().SetRoutes(context.Background(), owner, []Route{{ID: "x", Owner: other, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleRespond}}}); err == nil {
		t.Error("an owner can't set another owner's routes")
	}
}

// testEdge is an edge serving on loopback ports for a test.
type testEdge struct {
	*Edge
	admin *AdminClient
	dir   string
	stop  func()
}

func startEdge(t *testing.T, opts Options, tweak ...func(*Edge)) *testEdge {
	t.Helper()
	dir := shortTemp(t)
	// Caddy keeps an instance ID in the data directory; not the home of
	// whoever runs the tests.
	t.Setenv("XDG_DATA_HOME", dir)
	if opts.StateDir == "" {
		opts.StateDir = filepath.Join(dir, "state")
	}
	if opts.HTTP == "" {
		opts.HTTP, opts.HTTPS = "127.0.0.1:0", "127.0.0.1:0"
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	e, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	// A tweak runs before Run starts, so it never races the serve loop.
	for _, fn := range tweak {
		fn(e)
	}
	sock := filepath.Join(dir, "edge.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, ln) }()
	te := &testEdge{Edge: e, admin: DialAdmin(sock), dir: dir}
	stopped := false
	te.stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Errorf("edge run: %v", err)
		}
	}
	t.Cleanup(te.stop)
	return te
}

// waitRunning waits until the edge serves, or no longer does.
func (te *testEdge) waitRunning(t *testing.T, want bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for te.Running() != want {
		if time.Now().After(deadline) {
			t.Fatalf("the edge's running state never became %v (%+v)", want, te.Health())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// client returns an HTTP client that reaches the edge for any host name,
// trusting the local CA.
func (te *testEdge) client(t *testing.T) *http.Client {
	t.Helper()
	httpAddr, httpsAddr := te.Addrs()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		target := httpAddr
		if port == "443" {
			target = httpsAddr
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", target)
	}
	return &http.Client{
		Transport: &http.Transport{DialContext: dial, TLSClientConfig: &tls.Config{RootCAs: te.localRoots(t)}},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}
}

// localRoots is the local CA's root, once the edge made it.
func (te *testEdge) localRoots(t *testing.T) *x509.CertPool {
	pool := x509.NewCertPool()
	if data, err := os.ReadFile(filepath.Join(te.opts.StateDir, caddyDir, "pki", "authorities", "local", "root.crt")); err == nil {
		pool.AppendCertsFromPEM(data)
	}
	return pool
}

func get(t *testing.T, c *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func TestEdgeServesRoutesWithoutReloading(t *testing.T) {
	ctx := context.Background()
	dir := shortTemp(t)
	root := filepath.Join(dir, "www")
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	os.MkdirAll(filepath.Join(root, "empty"), 0o755)
	os.WriteFile(filepath.Join(root, "index.html"), []byte("home page"), 0o644)
	os.WriteFile(filepath.Join(root, "docs", "index.html"), []byte("docs"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0o644)
	os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(root, "escape.txt"))
	app := filepath.Join(dir, "app")
	os.MkdirAll(filepath.Join(app, "assets"), 0o755)
	os.WriteFile(filepath.Join(app, "index.html"), []byte("the application"), 0o644)
	os.WriteFile(filepath.Join(app, "assets", "app.js"), []byte(strings.Repeat("console.log(1);", 200)), 0o644)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "backend saw %s %s %s", r.Host, r.URL.Path, r.Header.Get("X-Forwarded-Proto"))
	}))
	defer backend.Close()
	sock := filepath.Join(dir, "app.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	unixSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from the socket "+r.URL.Path) })}
	go unixSrv.Serve(ln)
	defer unixSrv.Close()

	te := startEdge(t, Options{Dev: true})
	owner := store.NewID("site")
	backendAddr := strings.TrimPrefix(backend.URL, "http://")
	routes := []Route{
		{ID: "static", Hosts: []string{"static.test"}, Handler: Handler{Type: HandleStatic, Root: root}, Headers: []Header{{Name: "X-Frame-Options", Value: "DENY"}}},
		{ID: "spa", Hosts: []string{"spa.test"}, Handler: Handler{Type: HandleStatic, Root: app, Fallback: "/index.html"}},
		{ID: "proxy", Hosts: []string{"proxy.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "tcp", Address: backendAddr}}}},
		{ID: "unix", Hosts: []string{"unix.test"}, Path: "/app", Handler: Handler{Type: HandleProxy, StripPath: true, Upstreams: []Upstream{{Network: "unix", Address: sock}}}},
		{ID: "down", Hosts: []string{"down.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "unix", Address: filepath.Join(dir, "missing.sock")}}}},
		{ID: "www", Hosts: []string{"www.redirect.test"}, Handler: Handler{Type: HandleRedirect, To: "https://redirect.test{uri}", Code: 301}},
		{ID: "busy", Hosts: []string{"busy.test"}, Handler: Handler{Type: HandleRespond, Code: 503, Body: "Back {soon}"}},
		{ID: "plain", Hosts: []string{"plain.test"}, TLS: TLS{Mode: TLSOff}, Handler: Handler{Type: HandleRespond, Body: "no TLS here"}},
	}
	if err := te.admin.SetRoutes(ctx, owner, routes); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, true)
	c := te.client(t)

	if r, b := get(t, c, "https://static.test/"); b != "home page" || r.Header.Get("X-Frame-Options") != "DENY" || r.TLS == nil {
		t.Errorf("static home page = %d %q %v", r.StatusCode, b, r.Header)
	}
	if _, b := get(t, c, "https://static.test/docs/"); b != "docs" {
		t.Errorf("static subdirectory index = %q", b)
	}
	if r, _ := get(t, c, "https://static.test/empty/"); r.StatusCode != http.StatusNotFound {
		t.Errorf("directories without an index must not be listed: %d", r.StatusCode)
	}
	if r, b := get(t, c, "https://static.test/escape.txt"); r.StatusCode == http.StatusOK || strings.Contains(b, "secret") {
		t.Error("a symbolic link must not reach outside the root")
	}
	if r, b := get(t, c, "https://static.test/.env"); r.StatusCode == http.StatusOK || strings.Contains(b, "SECRET") {
		t.Error("hidden files must not be served")
	}
	if _, b := get(t, c, "https://spa.test/settings/profile"); b != "the application" {
		t.Errorf("unknown paths of an application get the fallback: %q", b)
	}
	req, _ := http.NewRequest("GET", "https://spa.test/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	if resp, err := c.Transport.RoundTrip(req); err != nil || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("text is compressed: %v %v", err, resp.Header)
	} else {
		resp.Body.Close()
	}
	if _, b := get(t, c, "https://proxy.test/hello"); b != "backend saw proxy.test /hello https" {
		t.Errorf("tcp proxy = %q", b)
	}
	if _, b := get(t, c, "https://unix.test/app/page"); b != "from the socket /page" {
		t.Errorf("unix socket proxy = %q", b)
	}
	if r, b := get(t, c, "https://down.test/"); r.StatusCode != http.StatusBadGateway || !strings.Contains(b, "answering") {
		t.Errorf("a missing upstream answers 502 with a page, got %d %q", r.StatusCode, b)
	}
	if r, _ := get(t, c, "https://www.redirect.test/a?b=c"); r.StatusCode != 301 || r.Header.Get("Location") != "https://redirect.test/a?b=c" {
		t.Errorf("redirect = %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r, b := get(t, c, "https://busy.test/"); r.StatusCode != 503 || b != "Back {soon}" {
		t.Errorf("fixed response = %d %q", r.StatusCode, b)
	}
	if r, _ := get(t, c, "http://static.test/docs/?x=1"); r.StatusCode != http.StatusPermanentRedirect || !strings.HasPrefix(r.Header.Get("Location"), "https://static.test:") || !strings.HasSuffix(r.Header.Get("Location"), "/docs/?x=1") {
		t.Errorf("plain HTTP goes to HTTPS: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r, b := get(t, c, "http://plain.test/"); r.StatusCode != 200 || b != "no TLS here" {
		t.Errorf("a route without TLS = %d %q", r.StatusCode, b)
	}
	if r, _ := get(t, c, "http://nowhere.test/"); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown hosts answer 404, got %d", r.StatusCode)
	}

	// Routes change while Caddy keeps running its configuration.
	loads := te.Loads()
	routes[6].Handler.Body = "Back now"
	routes = append(routes, Route{ID: "new", Hosts: []string{"new.test"}, Handler: Handler{Type: HandleRespond, Body: "brand new"}})
	if err := te.admin.SetRoutes(ctx, owner, routes); err != nil {
		t.Fatal(err)
	}
	if _, b := get(t, c, "https://busy.test/"); b != "Back now" {
		t.Errorf("a changed route = %q", b)
	}
	if _, b := get(t, c, "https://new.test/"); b != "brand new" {
		t.Errorf("an added route = %q", b)
	}
	if err := te.admin.SetRoutes(ctx, owner, routes[:1]); err != nil {
		t.Fatal(err)
	}
	if r, _ := get(t, c, "https://proxy.test/"); r.StatusCode != http.StatusNotFound {
		t.Errorf("a removed route answers 404, got %d", r.StatusCode)
	}
	if te.Loads() != loads {
		t.Errorf("changing routes reloaded Caddy %d times", te.Loads()-loads)
	}
	if act := te.Activity(); len(act) == 0 || act[0].Requests == 0 || act[0].Owner != owner {
		t.Errorf("activity = %+v", act)
	}
}

// Removing a site's automatic-certificate route leaves its certificate in
// Caddy's cache until the edge reloads to drop it, which it does a while
// later. This is the one reload a site change causes, so the docs and
// Edge.Loads account for it.
func TestEdgeReloadsToDropCertificatesNoLongerServed(t *testing.T) {
	ctx := context.Background()
	te := startEdge(t, Options{Dev: true}, func(e *Edge) { e.purgeDelay = 20 * time.Millisecond })
	owner := store.NewID("site")
	routes := []Route{
		{ID: "keep", Hosts: []string{"keep.test"}, Handler: Handler{Type: HandleRespond, Body: "kept"}},
		{ID: "plain", Hosts: []string{"plain.test"}, TLS: TLS{Mode: TLSOff}, Handler: Handler{Type: HandleRespond, Body: "plain"}},
	}
	if err := te.admin.SetRoutes(ctx, owner, routes); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, true)
	c := te.client(t)
	// Serving the automatic route makes the edge manage its certificate.
	if _, b := get(t, c, "https://keep.test/"); b != "kept" {
		t.Fatalf("keep.test = %q", b)
	}

	loads := te.Loads()
	if err := te.admin.SetRoutes(ctx, owner, routes[1:]); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for te.Loads() == loads {
		if time.Now().After(deadline) {
			t.Fatalf("the edge never reloaded to drop the certificate of keep.test (loads stayed %d)", loads)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The remaining route keeps serving across the reload.
	if _, b := get(t, c, "http://plain.test/"); b != "plain" {
		t.Errorf("plain.test after the reload = %q", b)
	}
}

func TestFastCGI(t *testing.T) {
	ctx := context.Background()
	dir := shortTemp(t)
	root := filepath.Join(dir, "app")
	os.MkdirAll(filepath.Join(root, "admin"), 0o755)
	os.WriteFile(filepath.Join(root, "index.php"), []byte("<?php"), 0o644)
	os.WriteFile(filepath.Join(root, "info.php"), []byte("<?php"), 0o644)
	os.WriteFile(filepath.Join(root, "admin", "index.php"), []byte("<?php"), 0o644)
	os.WriteFile(filepath.Join(root, "style.css"), []byte("body{}"), 0o644)
	sock := filepath.Join(dir, "php.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// net/http/fcgi is a FastCGI server, standing in for PHP-FPM.
	go fcgi.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env := fcgi.ProcessEnv(r)
		w.Header().Set("X-Script", strings.TrimPrefix(env["SCRIPT_FILENAME"], root))
		w.Header().Set("X-App-Env", env["APP_ENV"])
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s %s", r.Method, env["SCRIPT_FILENAME"], b)
	}))

	te := startEdge(t, Options{Dev: true})
	owner := store.NewID("site")
	route := Route{ID: "php", Hosts: []string{"php.test"}, Handler: Handler{Type: HandleFastCGI, Root: root, Env: map[string]string{"APP_ENV": "test"}, Upstreams: []Upstream{{Network: "unix", Address: sock}}}}
	if err := te.admin.SetRoutes(ctx, owner, []Route{route}); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, true)
	c := te.client(t)
	r, b := get(t, c, "https://php.test/info.php")
	if r.Header.Get("X-Script") != "/info.php" || r.Header.Get("X-App-Env") != "test" || !strings.Contains(b, filepath.Join(root, "info.php")) {
		t.Errorf("script = %s %q", r.Header.Get("X-Script"), b)
	}
	if r, _ := get(t, c, "https://php.test/blog/hello-world"); r.Header.Get("X-Script") != "/index.php" {
		t.Errorf("unknown paths go to the front controller: %s", r.Header.Get("X-Script"))
	}
	if r, _ := get(t, c, "https://php.test/admin"); r.StatusCode != http.StatusPermanentRedirect || r.Header.Get("Location") != "/admin/" {
		t.Errorf("a directory with an index redirects to its canonical path: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r, _ := get(t, c, "https://php.test/admin/"); r.Header.Get("X-Script") != "/admin/index.php" {
		t.Errorf("directory index = %s", r.Header.Get("X-Script"))
	}
	if _, b := get(t, c, "https://php.test/style.css"); b != "body{}" {
		t.Errorf("static files next to scripts are served directly: %q", b)
	}
	resp, err := c.Post("https://php.test/index.php", "application/x-www-form-urlencoded", strings.NewReader("name=value"))
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasSuffix(string(b2), "name=value") {
		t.Errorf("request body = %q", b2)
	}
}

// Routes come from the panel, built from what customers enter, such as
// custom headers and their applications' environment. Nothing in them may
// read the edge's files or the environment through Caddy's placeholders,
// and no root may serve the edge's own state, which holds every site's
// keys.
func TestRoutesCantReadTheEdgesFiles(t *testing.T) {
	ctx := context.Background()
	dir := shortTemp(t)
	secret := filepath.Join(dir, "secret.txt")
	os.WriteFile(secret, []byte("top secret"), 0o600)
	certs := filepath.Join(dir, "certs")
	os.MkdirAll(filepath.Join(certs, "uploaded"), 0o755)
	os.WriteFile(filepath.Join(certs, "uploaded", "privkey.pem"), []byte("uploaded key"), 0o600)
	www := filepath.Join(dir, "www")
	os.MkdirAll(www, 0o755)
	os.WriteFile(filepath.Join(www, "index.html"), []byte("home"), 0o644)
	app := filepath.Join(dir, "app")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "index.php"), []byte("<?php"), 0o644)
	sock := filepath.Join(dir, "php.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go fcgi.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, fcgi.ProcessEnv(r)["LEAK"])
	}))

	te := startEdge(t, Options{Dev: true, Certs: FileCerts{Dir: certs}})
	state := te.opts.StateDir
	os.MkdirAll(state, 0o750)
	os.WriteFile(filepath.Join(state, "account.key"), []byte("acme account key"), 0o600)
	// An account controls its own directories, so it can make a site's root
	// a link to wherever it likes.
	os.Symlink(state, filepath.Join(dir, "linked-state"))
	os.Symlink(certs, filepath.Join(dir, "linked-certs"))

	leak := "{file." + secret + "}"
	owner := store.NewID("site")
	routes := []Route{
		{ID: "headers", Hosts: []string{"headers.test"}, Handler: Handler{Type: HandleStatic, Root: www},
			Headers: []Header{{Name: "X-Leak", Value: leak}, {Name: "X-Env", Value: "{env.HOME}"}}},
		{ID: "body", Hosts: []string{"body.test"}, Handler: Handler{Type: HandleRespond, Body: leak}},
		{ID: "redirect", Hosts: []string{"redirect.test"}, Handler: Handler{Type: HandleRedirect, To: "https://elsewhere.test/" + leak + "{uri}"}},
		{ID: "php", Hosts: []string{"php.test"}, Handler: Handler{Type: HandleFastCGI, Root: app, Env: map[string]string{"LEAK": leak}, Upstreams: []Upstream{{Network: "unix", Address: sock}}}},
		{ID: "state", Hosts: []string{"state.test"}, Handler: Handler{Type: HandleStatic, Root: filepath.Join(dir, "linked-state")}},
		{ID: "certs", Hosts: []string{"certs.test"}, Handler: Handler{Type: HandleStatic, Root: filepath.Join(dir, "linked-certs")}},
	}
	if err := te.admin.SetRoutes(ctx, owner, routes); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, true)
	c := te.client(t)
	if r, b := get(t, c, "https://headers.test/"); b != "home" || r.Header.Get("X-Leak") != leak || r.Header.Get("X-Env") != "{env.HOME}" {
		t.Errorf("headers = %q, %q (%q)", r.Header.Get("X-Leak"), r.Header.Get("X-Env"), b)
	}
	if _, b := get(t, c, "https://body.test/"); b != leak {
		t.Errorf("body = %q", b)
	}
	if r, _ := get(t, c, "https://redirect.test/a"); r.Header.Get("Location") != "https://elsewhere.test/"+leak+"/a" {
		t.Errorf("redirect = %q", r.Header.Get("Location"))
	}
	if _, b := get(t, c, "https://php.test/index.php"); b != leak {
		t.Errorf("FastCGI parameter = %q", b)
	}
	for _, u := range []string{"https://state.test/account.key", "https://certs.test/uploaded/privkey.pem"} {
		if r, b := get(t, c, u); r.StatusCode == http.StatusOK || strings.Contains(b, "key") {
			t.Errorf("%s = %d %q", u, r.StatusCode, b)
		}
	}

	// Roots that are, hold or lie in the edge's files are refused outright.
	for _, root := range []string{state, filepath.Join(state, "caddy"), dir, "/", certs, filepath.Join(certs, "uploaded")} {
		bad := []Route{{ID: "bad", Hosts: []string{"bad.test"}, Handler: Handler{Type: HandleStatic, Root: root}}}
		if err := te.admin.SetRoutes(ctx, store.NewID("site"), bad); err == nil {
			t.Errorf("a root at %s was accepted", root)
		}
	}
	for _, bad := range []Route{
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Path: "/{file./etc/passwd}", Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleStatic, Root: "/srv/{env.HOME}"}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleStatic, Root: "/srv/www", Fallback: "/{file./etc/passwd}"}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleFastCGI, Root: "/srv/app", Index: "{env.X}.php", Upstreams: []Upstream{{Network: "unix", Address: "/run/php.sock"}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy, HealthPath: "/{env.X}", Upstreams: []Upstream{{Network: "tcp", Address: "127.0.0.1:80"}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "tcp", Address: "{env.HOST}:80"}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "unix", Address: "/run/{env.X}.sock"}}}},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("Validate(%+v) should refuse the placeholder", bad)
		}
	}
}

func TestEdgeStartsWithTheFirstRouteAndStopsWithTheLast(t *testing.T) {
	ctx := context.Background()
	te := startEdge(t, Options{Dev: true})
	panel := store.NewID("panel")
	if err := te.admin.SetRoutes(ctx, panel, []Route{{ID: "panel", Hosts: []string{"panel.test"}, Passive: true, Handler: Handler{Type: HandleRespond, Body: "panel"}}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if te.Running() {
		t.Fatal("a passive route alone must not start the edge")
	}
	if h, err := te.admin.Health(ctx); err != nil || h.Running || h.Routes != 1 {
		t.Errorf("health = %+v, %v", h, err)
	}
	site := store.NewID("site")
	if err := te.admin.SetRoutes(ctx, site, []Route{{ID: "main", Hosts: []string{"site.test"}, Handler: Handler{Type: HandleRespond, Body: "site"}}}); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, true)
	c := te.client(t)
	if _, b := get(t, c, "https://panel.test/"); b != "panel" {
		t.Errorf("the panel is served while the edge is on: %q", b)
	}
	httpAddr, _ := te.Addrs()
	if err := te.admin.DeleteRoutes(ctx, site); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, false)
	if conn, err := net.DialTimeout("tcp", httpAddr, time.Second); err == nil {
		conn.Close()
		t.Error("the edge must stop listening when no routes are left")
	}
}

func TestRoutesSurviveARestart(t *testing.T) {
	ctx := context.Background()
	te := startEdge(t, Options{Dev: true})
	owner := store.NewID("site")
	if err := te.admin.SetRoutes(ctx, owner, []Route{{ID: "main", Hosts: []string{"kept.test"}, Handler: Handler{Type: HandleRespond, Body: "still here"}}}); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, true)
	if err := te.admin.SetSettings(ctx, Settings{Email: "admin@kept.test"}); err != nil {
		t.Fatal(err)
	}
	te.stop()

	again := startEdge(t, Options{Dev: true, StateDir: te.opts.StateDir})
	again.waitRunning(t, true)
	if _, b := get(t, again.client(t), "https://kept.test/"); b != "still here" {
		t.Errorf("after a restart = %q", b)
	}
	if s, _ := again.admin.Settings(ctx); s.Email != "admin@kept.test" {
		t.Errorf("settings after a restart = %+v", s)
	}
	if err := again.admin.SetSettings(ctx, Settings{Email: "not an email"}); err == nil {
		t.Error("invalid settings must be refused")
	}
}

func selfSigned(t *testing.T, dir, name string, hosts ...string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "Uploaded " + name},
		Issuer: pkix.Name{CommonName: "Uploaded " + name}, DNSNames: hosts,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	os.MkdirAll(filepath.Join(dir, name), 0o755)
	os.WriteFile(filepath.Join(dir, name, "fullchain.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(filepath.Join(dir, name, "privkey.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
}

func TestUploadedCertificatesAndTheLocalCA(t *testing.T) {
	ctx := context.Background()
	certs := filepath.Join(shortTemp(t), "certs")
	selfSigned(t, certs, "custom", "custom.test")
	te := startEdge(t, Options{Dev: true, Certs: FileCerts{Dir: certs}})
	owner := store.NewID("site")
	if err := te.admin.SetRoutes(ctx, owner, []Route{
		{ID: "custom", Hosts: []string{"custom.test"}, TLS: TLS{Mode: TLSCertificate, Certificate: "custom"}, Handler: Handler{Type: HandleRespond, Body: "custom"}},
		{ID: "auto", Hosts: []string{"auto.test"}, Handler: Handler{Type: HandleRespond, Body: "auto"}},
	}); err != nil {
		t.Fatal(err)
	}
	te.waitRunning(t, true)
	_, httpsAddr := te.Addrs()
	handshake := func(name string) *x509.Certificate {
		conn, err := tls.Dial("tcp", httpsAddr, &tls.Config{ServerName: name, InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("handshake for %s: %v", name, err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0]
	}
	if leaf := handshake("custom.test"); leaf.Subject.CommonName != "Uploaded custom" {
		t.Errorf("the uploaded certificate isn't served: %s", leaf.Subject)
	}
	if leaf := handshake("auto.test"); leaf.Issuer.CommonName == "" || !strings.Contains(leaf.Issuer.CommonName, "Racksail Local CA") || leaf.DNSNames[0] != "auto.test" {
		t.Errorf("auto.test = issuer %q, names %v", leaf.Issuer.CommonName, leaf.DNSNames)
	}
	if _, err := tls.Dial("tcp", httpsAddr, &tls.Config{ServerName: "unknown.test", InsecureSkipVerify: true}); err == nil {
		t.Error("no certificate may be issued for names no route serves")
	}

	// The keeper obtained auto.test's certificate before anyone asked.
	var list []Certificate
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, _ = te.admin.Certificates(ctx)
		if len(list) == 2 && list[0].Status == CertValid && list[1].Status == CertValid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("certificates = %+v", list)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if list[0].Subject != "auto.test" || list[0].Source != "local" || list[0].NotAfter.IsZero() || list[0].Owners[0] != owner {
		t.Errorf("auto.test = %+v", list[0])
	}
	if list[1].Subject != "custom" || list[1].Source != "uploaded" || list[1].Names[0] != "custom.test" {
		t.Errorf("custom = %+v", list[1])
	}
	exported, err := te.admin.ExportCertificate(ctx, "auto.test")
	if err != nil || !strings.Contains(exported.Certificate, "BEGIN CERTIFICATE") || !strings.Contains(exported.Key, "PRIVATE KEY") {
		t.Errorf("export = %v", err)
	}
	if _, err := te.admin.ExportCertificate(ctx, "custom.test"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("exporting a certificate the edge didn't issue: %v", err)
	}
	if err := te.admin.RenewCertificate(ctx, "auto.test"); err != nil {
		t.Errorf("renew: %v", err)
	}
	if err := te.admin.RenewCertificate(ctx, "nothing.test"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("renewing a name no route serves: %v", err)
	}
}

// fakePanel answers the edge's questions.
type fakePanel struct {
	allowed map[string]bool
	records chan DNSRecord
}

func (f *fakePanel) AllowCertificate(ctx context.Context, name string) error {
	if f.allowed[name] {
		return nil
	}
	return fmt.Errorf("%w: %s isn't a customer domain", ErrDenied, name)
}

func (f *fakePanel) PresentRecord(ctx context.Context, zone string, rec DNSRecord) error {
	f.records <- rec
	return nil
}

func (f *fakePanel) CleanUpRecord(ctx context.Context, zone string, rec DNSRecord) error {
	return errors.New("the zone is read-only")
}

func TestOnDemandPermission(t *testing.T) {
	ctx := context.Background()
	e, err := New(Options{StateDir: t.TempDir(), HTTP: "127.0.0.1:0", HTTPS: "127.0.0.1:0", Responder: &fakePanel{allowed: map[string]bool{"customer.example": true}}})
	if err != nil {
		t.Fatal(err)
	}
	owner := store.NewID("site")
	e.table.SetRoutes(ctx, owner, []Route{
		{ID: "site", Hosts: []string{"site.test", "*.site.test"}, Handler: Handler{Type: HandleRespond}},
		{ID: "demand", Hosts: []string{"demand.test"}, TLS: TLS{Mode: TLSOnDemand}, Handler: Handler{Type: HandleRespond}},
		{ID: "saas", Hosts: []string{CatchAll}, TLS: TLS{Mode: TLSOnDemand, Ask: true}, Handler: Handler{Type: HandleRespond}},
		{ID: "custom", Hosts: []string{"custom.test"}, TLS: TLS{Mode: TLSCertificate, Certificate: "custom"}, Handler: Handler{Type: HandleRespond}},
		{ID: "api", Hosts: []string{"api.test"}, Path: "/v1", TLS: TLS{Mode: TLSOnDemand}, Handler: Handler{Type: HandleRespond}},
	})
	for name, allowed := range map[string]bool{
		"site.test": true, "shop.site.test": true, "demand.test": true, "api.test": true,
		"customer.example": true, "stranger.example": false, "custom.test": false,
	} {
		err := e.allowCertificate(ctx, name)
		if (err == nil) != allowed {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A refusal is remembered for a while, so a flood of handshakes for a
	// name doesn't reach the panel each time.
	e.opts.Responder.(*fakePanel).allowed["stranger.example"] = true
	if err := e.allowCertificate(ctx, "stranger.example"); err == nil {
		t.Error("a recent refusal should stand")
	}
	e.asked.now = func() time.Time { return time.Now().Add(refusalMemory + time.Second) }
	if err := e.allowCertificate(ctx, "stranger.example"); err != nil {
		t.Errorf("once the refusal is forgotten the panel is asked again: %v", err)
	}

	// Without a panel answering, questions fail at once rather than hang.
	alone, _ := New(Options{StateDir: t.TempDir(), HTTP: "127.0.0.1:0", HTTPS: "127.0.0.1:0"})
	alone.table.SetRoutes(ctx, owner, []Route{{ID: "saas", Hosts: []string{CatchAll}, TLS: TLS{Mode: TLSOnDemand, Ask: true}, Handler: Handler{Type: HandleRespond}}})
	if err := alone.allowCertificate(ctx, "x.example"); !errors.Is(err, ErrNoPanel) {
		t.Errorf("no panel: %v", err)
	}
}

func TestQuestionsOverTheAdminSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	te := startEdge(t, Options{})
	panel := &fakePanel{allowed: map[string]bool{"customer.example": true}, records: make(chan DNSRecord, 1)}
	answered := make(chan error, 1)
	go func() { answered <- AnswerQuestions(ctx, te.admin, panel, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for !te.Health().Answered {
		if time.Now().After(deadline) {
			t.Fatal("the panel never polled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if err := te.ask(qctx, Question{Kind: AskCertificate, Name: "customer.example"}); err != nil {
		t.Errorf("an approved name: %v", err)
	}
	if err := te.ask(qctx, Question{Kind: AskCertificate, Name: "stranger.example"}); !errors.Is(err, ErrDenied) {
		t.Errorf("a refused name: %v", err)
	}
	rec := DNSRecord{Name: "_acme-challenge", Type: "TXT", Value: "token"}
	if err := te.ask(qctx, Question{Kind: AskPresentDNS, Zone: "example.com", Record: &rec}); err != nil {
		t.Errorf("presenting a record: %v", err)
	}
	if got := <-panel.records; got != rec {
		t.Errorf("record = %+v", got)
	}
	if err := te.ask(qctx, Question{Kind: AskCleanUpDNS, Zone: "example.com", Record: &rec}); err == nil || errors.Is(err, ErrDenied) || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("a failure is passed on as one: %v", err)
	}
	cancel()
	if err := <-answered; err != nil {
		t.Errorf("answering: %v", err)
	}
}

func TestAnswersFromModules(t *testing.T) {
	ctx := context.Background()
	a := &Answers{}
	if err := a.AllowCertificate(ctx, "x.example"); !errors.Is(err, ErrDenied) {
		t.Errorf("nothing approves yet: %v", err)
	}
	remove := a.ApproveNames(func(ctx context.Context, name string) (bool, error) { return name == "x.example", nil })
	if err := a.AllowCertificate(ctx, "x.example"); err != nil {
		t.Errorf("an approved name: %v", err)
	}
	remove()
	if err := a.AllowCertificate(ctx, "x.example"); !errors.Is(err, ErrDenied) {
		t.Errorf("after removing the check: %v", err)
	}
	if err := a.PresentRecord(ctx, "example.com", DNSRecord{}); !errors.Is(err, ErrNoDNS) {
		t.Errorf("no DNS service: %v", err)
	}
	panel := &fakePanel{records: make(chan DNSRecord, 1)}
	stop := a.SetDNSChallenger(panel)
	if err := a.PresentRecord(ctx, "example.com", DNSRecord{Name: "_acme-challenge"}); err != nil {
		t.Errorf("present: %v", err)
	}
	stop()
	if err := a.CleanUpRecord(ctx, "example.com", DNSRecord{}); !errors.Is(err, ErrNoDNS) {
		t.Errorf("after the DNS service stopped: %v", err)
	}
}

func TestWakeProxy(t *testing.T) {
	dir := shortTemp(t)
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream := filepath.Join(dir, "app.sock")
	// The application starts a moment after the first connection, as a
	// service systemd starts would.
	go func() {
		time.Sleep(300 * time.Millisecond)
		ln, err := net.Listen("unix", upstream)
		if err != nil {
			return
		}
		http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "awake") }))
	}()
	p := &WakeProxy{Upstream: upstream, IdleTimeout: 400 * time.Millisecond, StartTimeout: 5 * time.Second, Log: slog.New(slog.DiscardHandler)}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- p.Run(context.Background(), []net.Listener{front}) }()
	resp, err := http.Get("http://" + front.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "awake" {
		t.Errorf("body = %q", b)
	}
	http.DefaultClient.CloseIdleConnections()
	select {
	case err := <-done:
		if err != nil || time.Since(start) < p.IdleTimeout {
			t.Errorf("the proxy should exit after being idle: %v after %s", err, time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy never went idle")
	}
	if err := (&WakeProxy{Upstream: "nowhere", IdleTimeout: time.Second, StartTimeout: time.Second}).Validate(); err == nil {
		t.Error("an upstream that is neither a path nor host:port must be refused")
	}
}
