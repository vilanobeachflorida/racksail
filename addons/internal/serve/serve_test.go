package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAddon() *Addon {
	echo := func(ctx context.Context, settings, input json.RawMessage) (any, error) {
		return map[string]json.RawMessage{"settings": settings, "input": input}, nil
	}
	return &Addon{Name: "Example", Version: "1.2.3", Log: slog.New(slog.DiscardHandler), Capabilities: map[string]map[string]Method{
		"auth-provider": {
			"echo": echo,
			"invalid": func(context.Context, json.RawMessage, json.RawMessage) (any, error) {
				return nil, Invalid("no %s", "url")
			},
			"failed": func(context.Context, json.RawMessage, json.RawMessage) (any, error) {
				return nil, Failed("the directory is down")
			},
			"broken": func(context.Context, json.RawMessage, json.RawMessage) (any, error) {
				return nil, errors.New("something broke")
			},
		},
	}}
}

func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s answered %q", method, path, rec.Body.String())
	}
	return rec, out
}

func TestHealth(t *testing.T) {
	rec, out := do(t, testAddon().Handler(), http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK || out["status"] != "ok" || out["version"] != "1.2.3" || out["apiVersion"] != APIVersion {
		t.Errorf("health: %d %v", rec.Code, out)
	}
}

func TestProvide(t *testing.T) {
	h := testAddon().Handler()
	rec, out := do(t, h, http.MethodPost, "/provide/auth-provider/echo", `{"input":{"settings":{"url":"ldaps://x"},"input":{"username":"ada"}}}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("echo: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if s, _ := json.Marshal(out); string(s) != `{"input":{"username":"ada"},"settings":{"url":"ldaps://x"}}` {
		t.Errorf("the method got %s", s)
	}
	for _, c := range []struct {
		path, body string
		status     int
		detail     string
	}{
		{"/provide/dns/echo", `{}`, http.StatusNotFound, "doesn't provide dns"},
		{"/provide/auth-provider/nothing", `{}`, http.StatusNotFound, "no method nothing"},
		{"/provide/auth-provider/echo", `{"input":`, http.StatusBadRequest, "isn't what the add-on protocol says"},
		{"/provide/auth-provider/echo", `{"input":"` + strings.Repeat("x", maxCall) + `"}`, http.StatusBadRequest, "isn't what"},
		{"/provide/auth-provider/invalid", `{}`, http.StatusUnprocessableEntity, "no url"},
		{"/provide/auth-provider/failed", `{}`, http.StatusBadGateway, "the directory is down"},
		{"/provide/auth-provider/broken", `{}`, http.StatusInternalServerError, "something broke"},
	} {
		rec, out := do(t, h, http.MethodPost, c.path, c.body)
		if rec.Code != c.status || rec.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(out["detail"].(string), c.detail) {
			t.Errorf("%s: %d %v", c.path, rec.Code, out)
		}
	}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/provide/auth-provider/echo", nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET of a method: %d", get.Code)
	}
}

func TestDecode(t *testing.T) {
	var v struct {
		URL string `json:"url"`
	}
	if err := Decode(json.RawMessage(`{"url":"x"}`), &v, "settings"); err != nil || v.URL != "x" {
		t.Errorf("decoding: %v %v", v, err)
	}
	if err := Decode(nil, &v, "settings"); err != nil {
		t.Errorf("nothing is no settings: %v", err)
	}
	var f *Failure
	if err := Decode(json.RawMessage(`{"urls":"x"}`), &v, "settings"); !errors.As(err, &f) || f.Status != http.StatusUnprocessableEntity || !strings.Contains(f.Detail, "settings can't be read") {
		t.Errorf("a setting it doesn't have: %v", err)
	}
}

func TestListen(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	if _, err := Listen(""); err == nil {
		t.Error("no socket to listen on")
	}
	// Socket paths are short on some systems, so not under t.TempDir.
	dir, err := os.MkdirTemp("", "addon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "addon.sock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Listen(path)
	if err != nil {
		t.Fatalf("a socket left behind by an earlier run: %v", err)
	}
	srv := &http.Server{Handler: testAddon().Handler()}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}}
	resp, err := client.Get("http://addon/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("health over the socket: %d %s", resp.StatusCode, body)
	}
}
