package edge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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
	site, wild := store.NewID("site"), store.NewID("site")
	if err := table.SetRoutes(ctx, site, []Route{
		{ID: "main", Hosts: []string{"example.com", "www.example.com"}, Handler: Handler{Type: HandleRespond, Body: "site"}},
		{ID: "api", Hosts: []string{"example.com"}, Path: "/api", Handler: Handler{Type: HandleRespond, Body: "api"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := table.SetRoutes(ctx, wild, []Route{{ID: "wild", Hosts: []string{"*.example.com"}, Handler: Handler{Type: HandleRespond, Body: "wild"}}}); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ host, path, want string }{
		{"example.com", "/", "main"},
		{"EXAMPLE.com:443", "/about", "main"},
		{"example.com", "/api/users", "api"},
		{"example.com", "/apiary", "main"},
		{"www.example.com", "/", "main"},
		{"shop.example.com", "/", "wild"},
		{"a.b.example.com", "/", ""},
		{"other.test", "/", ""},
	}
	for _, tt := range tests {
		r, ok := table.Match(tt.host, tt.path)
		if got := map[bool]string{true: r.ID, false: ""}[ok]; got != tt.want {
			t.Errorf("Match(%s, %s) = %q, want %q", tt.host, tt.path, got, tt.want)
		}
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
	if table.Len() != 2 {
		t.Errorf("routes = %d", table.Len())
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
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "udp", Address: "x"}}}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleRedirect, To: "https://b.test", Code: 200}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, Headers: []Header{{Name: "X-A", Value: "a\r\nInjected: 1"}}, Handler: Handler{Type: HandleRespond}},
		{ID: "x", Owner: owner, Hosts: []string{"a.test"}, TLS: TLS{Mode: TLSCertificate}, Handler: Handler{Type: HandleRespond}},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("Validate(%+v) should fail", r)
		}
	}
	other := store.NewID("site")
	if err := NewTable().SetRoutes(context.Background(), owner, []Route{{ID: "x", Owner: other, Hosts: []string{"a.test"}, Handler: Handler{Type: HandleRespond}}}); err == nil {
		t.Error("an owner can't set another owner's routes")
	}
}

func get(t *testing.T, h http.Handler, host, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", "http://"+host+path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestServerHandlers(t *testing.T) {
	ctx := context.Background()
	dir := shortTemp(t)
	root := filepath.Join(dir, "www")
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	os.MkdirAll(filepath.Join(root, "empty"), 0o755)
	os.WriteFile(filepath.Join(root, "index.html"), []byte("home page"), 0o644)
	os.WriteFile(filepath.Join(root, "docs", "index.html"), []byte("docs"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0o644)
	os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(root, "escape.txt"))

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "backend saw %s %s %s", r.Host, r.URL.Path, r.Header.Get("X-Forwarded-Host"))
	}))
	defer backend.Close()
	sock := filepath.Join(dir, "app.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	unixSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from the socket") })}
	go unixSrv.Serve(ln)
	defer unixSrv.Close()

	table := NewTable()
	owner := store.NewID("site")
	err = table.SetRoutes(ctx, owner, []Route{
		{ID: "static", Hosts: []string{"static.test"}, Handler: Handler{Type: HandleStatic, Root: root}, Headers: []Header{{Name: "X-Frame-Options", Value: "DENY"}}},
		{ID: "proxy", Hosts: []string{"proxy.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "tcp", Address: strings.TrimPrefix(backend.URL, "http://")}}}},
		{ID: "unix", Hosts: []string{"unix.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "unix", Address: sock}}}},
		{ID: "down", Hosts: []string{"down.test"}, Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "unix", Address: filepath.Join(dir, "missing.sock")}}}},
		{ID: "www", Hosts: []string{"www.redirect.test"}, Handler: Handler{Type: HandleRedirect, To: "https://redirect.test{uri}", Code: 301}},
		{ID: "maintenance", Hosts: []string{"busy.test"}, Handler: Handler{Type: HandleRespond, Code: 503, Body: "Back soon"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(table, nil, slog.New(slog.DiscardHandler))

	if r := get(t, s, "static.test", "/"); body(t, r) != "home page" || r.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("static home page")
	}
	if r := get(t, s, "static.test", "/docs/"); body(t, r) != "docs" {
		t.Error("static subdirectory index")
	}
	if r := get(t, s, "static.test", "/empty/"); r.StatusCode != http.StatusNotFound {
		t.Errorf("directories without an index must not be listed: %d", r.StatusCode)
	}
	if r := get(t, s, "static.test", "/escape.txt"); r.StatusCode == http.StatusOK {
		t.Error("a symbolic link must not reach outside the root")
	}
	if r := get(t, s, "proxy.test", "/hello"); body(t, r) != "backend saw proxy.test /hello proxy.test" {
		t.Error("tcp proxy")
	}
	if r := get(t, s, "unix.test", "/"); body(t, r) != "from the socket" {
		t.Error("unix socket proxy")
	}
	if r := get(t, s, "down.test", "/"); r.StatusCode != http.StatusBadGateway {
		t.Errorf("a missing upstream answers 502, got %d", r.StatusCode)
	}
	if r := get(t, s, "www.redirect.test", "/a?b=c"); r.StatusCode != 301 || r.Header.Get("Location") != "https://redirect.test/a?b=c" {
		t.Errorf("redirect = %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := get(t, s, "busy.test", "/"); r.StatusCode != 503 || body(t, r) != "Back soon" {
		t.Error("fixed response")
	}
	if r := get(t, s, "nowhere.test", "/"); r.StatusCode != http.StatusNotFound {
		t.Error("unknown hosts answer 404")
	}
}

func TestFastCGI(t *testing.T) {
	ctx := context.Background()
	dir := shortTemp(t)
	root := filepath.Join(dir, "app")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "index.php"), []byte("<?php"), 0o644)
	os.WriteFile(filepath.Join(root, "info.php"), []byte("<?php"), 0o644)
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
		if r.URL.Path == "/missing-page" {
			w.WriteHeader(http.StatusNotFound)
		}
		// SCRIPT_NAME goes into the request URL; SCRIPT_FILENAME stays in the
		// environment.
		w.Header().Set("X-Script", strings.TrimPrefix(env["SCRIPT_FILENAME"], root))
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s %s", r.Method, env["SCRIPT_FILENAME"], b)
	}))

	table := NewTable()
	owner := store.NewID("site")
	if err := table.SetRoutes(ctx, owner, []Route{{ID: "php", Hosts: []string{"php.test"}, Handler: Handler{Type: HandleFastCGI, Root: root, Upstreams: []Upstream{{Network: "unix", Address: sock}}}}}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(table, nil, slog.New(slog.DiscardHandler))
	r := get(t, s, "php.test", "/info.php")
	if b := body(t, r); r.Header.Get("X-Script") != "/info.php" || !strings.Contains(b, filepath.Join(root, "info.php")) {
		t.Errorf("script = %s %q", r.Header.Get("X-Script"), b)
	}
	if r := get(t, s, "php.test", "/blog/hello-world"); r.Header.Get("X-Script") != "/index.php" {
		t.Errorf("unknown paths go to the front controller: %s", r.Header.Get("X-Script"))
	}
	if r := get(t, s, "php.test", "/missing-page"); r.StatusCode != http.StatusNotFound {
		t.Errorf("status from the application = %d", r.StatusCode)
	}
	if r := get(t, s, "php.test", "/style.css"); body(t, r) != "body{}" {
		t.Error("static files next to scripts are served directly")
	}
	req := httptest.NewRequest("POST", "http://php.test/index.php", strings.NewReader("name=value"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if !strings.HasSuffix(rec.Body.String(), "name=value") {
		t.Errorf("request body = %q", rec.Body.String())
	}
}

func TestAdminAndLazyStart(t *testing.T) {
	dir := shortTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := filepath.Join(dir, "edge.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpAddr := httpLn.Addr().String()
	httpLn.Close()
	table := NewTable()
	s := NewServer(table, nil, slog.New(slog.DiscardHandler))
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln, httpAddr, "127.0.0.1:0") }()

	client := DialAdmin(sock)
	if _, err := http.Get("http://" + httpAddr); err == nil {
		t.Error("the edge must not listen before it has routes")
	}
	owner := store.NewID("site")
	if err := client.SetRoutes(ctx, owner, []Route{{ID: "main", Hosts: []string{"127.0.0.1"}, Handler: Handler{Type: HandleRespond, Body: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if err := client.SetRoutes(ctx, owner, []Route{{ID: "BAD"}}); err == nil {
		t.Error("invalid routes must be refused over the socket")
	}
	routes, err := client.Routes(ctx)
	if err != nil || len(routes) != 1 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	var resp *http.Response
	for i := 0; i < 50; i++ {
		if resp, err = http.Get("http://" + httpAddr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the edge didn't start: %v", err)
	}
	if b := body(t, resp); b != "hello" {
		t.Errorf("body = %q", b)
	}
	if err := client.DeleteRoutes(ctx, owner); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("run = %v", err)
	}
}
