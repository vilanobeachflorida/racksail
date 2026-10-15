package webui

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFallback(t *testing.T) {
	h := Handler(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sites", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "make build") || rec.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("fallback = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/app.js", nil))
	if rec.Code != 404 {
		t.Errorf("missing assets answer 404, got %d", rec.Code)
	}
}

func TestSinglePageApp(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>Racksail</title>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
		"favicon.png":        {Data: []byte("png")},
	}
	h := Handler(assets)
	get := func(path string) (*httptest.ResponseRecorder, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		b, _ := io.ReadAll(rec.Body)
		return rec, string(b)
	}
	if rec, b := get("/web/sites/site_123"); rec.Code != 200 || !strings.Contains(b, "<title>Racksail") || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("client routes get index.html: %d %q", rec.Code, b)
	}
	if rec, b := get("/assets/app-1a2b.js"); rec.Code != 200 || b != "console.log(1)" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("hashed assets: %d %q %s", rec.Code, b, rec.Header().Get("Cache-Control"))
	}
	if rec, _ := get("/assets/missing.js"); rec.Code != 404 {
		t.Errorf("missing assets answer 404, got %d", rec.Code)
	}
	if rec, b := get("/../../etc/passwd"); rec.Code != 400 || strings.Contains(b, "root:") {
		t.Errorf("paths that climb out are refused: %d", rec.Code)
	}
}

func TestEmbeddedAssets(t *testing.T) {
	// The repository only commits dist/.gitkeep; a UI build adds dist/app.
	if a, ok := Assets(); ok {
		if _, err := a.Open("index.html"); err != nil {
			t.Error(err)
		}
	}
}
