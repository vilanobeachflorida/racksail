package api

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

func do(t *testing.T, h http.Handler, method, path string, header map[string]string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Result(), string(b)
}

func newServer(health ...HealthCheck) *Server {
	return New(Options{Health: health, Log: slog.New(slog.DiscardHandler), UI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ui") })})
}

func TestHealthAndVersion(t *testing.T) {
	s := newServer(HealthCheck{Name: "agent", Check: func(context.Context) error { return nil }})
	resp, body := do(t, s.Handler(), "GET", "/api/v1/health", nil)
	var report HealthReport
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || report.Status != "ok" || len(report.Checks) != 1 || resp.Header.Get("X-Request-Id") == "" {
		t.Errorf("health = %d %+v", resp.StatusCode, report)
	}
	failing := newServer(HealthCheck{Name: "agent", Check: func(context.Context) error { return errors.New("connection refused") }})
	resp, body = do(t, failing.Handler(), "GET", "/api/v1/health", nil)
	if resp.StatusCode != 503 || !strings.Contains(body, `"degraded"`) || strings.Contains(body, "connection refused") {
		t.Errorf("failing health = %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, s.Handler(), "GET", "/api/v1/version", map[string]string{"X-Request-Id": "req-from-client-1"})
	if resp.StatusCode != 200 || !strings.Contains(body, `"version"`) || resp.Header.Get("X-Request-Id") != "req-from-client-1" {
		t.Errorf("version = %d %s", resp.StatusCode, body)
	}
}

func TestProblems(t *testing.T) {
	s := newServer()
	resp, body := do(t, s.Handler(), "GET", "/api/v1/nope", nil)
	var p httpapi.Problem
	json.Unmarshal([]byte(body), &p)
	if resp.StatusCode != 404 || resp.Header.Get("Content-Type") != "application/problem+json" || p.Code != i18n.ErrNotFound.Code {
		t.Errorf("unknown path = %d %s", resp.StatusCode, body)
	}
	if _, body := do(t, s.Handler(), "GET", "/sites", nil); body != "ui" {
		t.Errorf("paths outside the API go to the UI: %q", body)
	}

	type out struct{ Body struct{ OK bool } }
	huma.Register(s.API(), huma.Operation{OperationID: "secret", Method: "GET", Path: "/api/v1/secret", Summary: "Secret", Security: Protected},
		func(ctx context.Context, _ *struct{}) (*out, error) { return &out{}, nil })
	huma.Register(s.API(), huma.Operation{OperationID: "boom", Method: "GET", Path: "/api/v1/boom", Summary: "Boom"},
		func(ctx context.Context, _ *struct{}) (*out, error) { panic("boom") })
	huma.Register(s.API(), huma.Operation{OperationID: "taken", Method: "GET", Path: "/api/v1/taken", Summary: "Taken"},
		func(ctx context.Context, _ *struct{}) (*out, error) {
			return nil, i18n.ErrAlreadyExists.New("what", "Site example.com")
		})
	resp, body = do(t, s.Handler(), "GET", "/api/v1/secret", nil)
	if resp.StatusCode != 401 || !strings.Contains(body, "core.unauthenticated") {
		t.Errorf("protected without sign-in = %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, s.Handler(), "GET", "/api/v1/boom", nil)
	if resp.StatusCode != 500 || !strings.Contains(body, "core.internal") || strings.Contains(body, "boom") {
		t.Errorf("panic = %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, s.Handler(), "GET", "/api/v1/taken", nil)
	if resp.StatusCode != 409 || !strings.Contains(body, `"core.already_exists"`) || !strings.Contains(body, "Site example.com already exists.") {
		t.Errorf("coded error = %d %s", resp.StatusCode, body)
	}

	dev := New(Options{Auth: auth.DevAuthenticator{}, Log: slog.New(slog.DiscardHandler)})
	huma.Register(dev.API(), huma.Operation{OperationID: "secret", Method: "GET", Path: "/api/v1/secret", Summary: "Secret", Security: Protected},
		func(ctx context.Context, _ *struct{}) (*out, error) { return &out{}, nil })
	if resp, _ := do(t, dev.Handler(), "GET", "/api/v1/secret", nil); resp.StatusCode != 200 {
		t.Errorf("a signed-in actor gets through: %d", resp.StatusCode)
	}
}

func TestOpenAPIDocument(t *testing.T) {
	s := newServer()
	doc, err := s.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		OpenAPI    string         `json:"openapi"`
		Paths      map[string]any `json:"paths"`
		Components struct {
			Schemas         map[string]any `json:"schemas"`
			SecuritySchemes map[string]any `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OpenAPI != "3.1.0" || parsed.Paths["/api/v1/health"] == nil || parsed.Paths["/api/v1/version"] == nil {
		t.Errorf("document = %s", doc)
	}
	if parsed.Components.Schemas["Problem"] == nil || parsed.Components.SecuritySchemes["token"] == nil {
		t.Errorf("components = %+v", parsed.Components)
	}
	resp, body := do(t, s.Handler(), "GET", "/api/v1/openapi.json", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"openapi": "3.1.0"`) {
		t.Errorf("served document = %d", resp.StatusCode)
	}
}

func TestPanelCertificate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	cert, err := PanelCertificate(dir, []string{"panel.example.test", "203.0.113.10"}, now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.DNSNames[0] != "panel.example.test" || len(leaf.IPAddresses) != 1 {
		t.Errorf("names = %v %v", leaf.DNSNames, leaf.IPAddresses)
	}
	again, err := PanelCertificate(dir, []string{"panel.example.test"}, now)
	if err != nil || !bytes.Equal(again.Certificate[0], cert.Certificate[0]) {
		t.Error("an existing certificate should be reused")
	}
	renewed, err := PanelCertificate(dir, []string{"panel.example.test"}, now.Add(364*24*time.Hour))
	if err != nil || bytes.Equal(renewed.Certificate[0], cert.Certificate[0]) {
		t.Error("a certificate close to expiry should be replaced")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "panel.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v", fi.Mode().Perm())
	}
}
