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
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
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

func TestUnmatchedPaths(t *testing.T) {
	s := newServer()
	for _, tc := range []struct {
		method, path string
		status       int
		code         i18n.Code
		allow        string
	}{
		{"POST", "/api/v1/health", 405, i18n.ErrMethodNotAllowed.Code, "GET, HEAD"},
		{"DELETE", "/api/v1/openapi.json", 405, i18n.ErrMethodNotAllowed.Code, "GET, HEAD"},
		{"GET", "/api/v1/nope", 404, i18n.ErrNotFound.Code, ""},
		{"GET", "/api/v2/health", 404, i18n.ErrNotFound.Code, ""},
		{"GET", "/api", 404, i18n.ErrNotFound.Code, ""},
		{"POST", "/api/", 404, i18n.ErrNotFound.Code, ""},
	} {
		resp, body := do(t, s.Handler(), tc.method, tc.path, nil)
		var p httpapi.Problem
		json.Unmarshal([]byte(body), &p)
		if resp.StatusCode != tc.status || p.Code != tc.code || resp.Header.Get("Allow") != tc.allow || resp.Header.Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s %s = %d %s (Allow %q)", tc.method, tc.path, resp.StatusCode, body, resp.Header.Get("Allow"))
		}
	}
	if _, body := do(t, s.Handler(), "POST", "/api/v1/health", nil); !strings.Contains(body, "/api/v1/health doesn't accept POST; use GET or HEAD.") {
		t.Errorf("405 detail = %s", body)
	}
	if resp, body := do(t, s.Handler(), "HEAD", "/api/v1/health", nil); resp.StatusCode != 200 {
		t.Errorf("HEAD on a GET operation = %d %s", resp.StatusCode, body)
	}
}

func TestAuthorizingReads(t *testing.T) {
	acct := store.NewID("acct")
	mine := &store.Resource{Meta: store.Meta{ID: store.NewID("site"), Kind: "web.site", Scope: store.AccountScope(acct)}}
	theirs := &store.Resource{Meta: store.Meta{ID: store.NewID("site"), Kind: "web.site", Scope: store.AccountScope(store.NewID("acct"))}}
	owner := auth.WithActor(context.Background(), &auth.Actor{Type: auth.ActorUser, Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(acct)}}})
	authz := auth.NewRoleAuthorizer(nil)

	if err := AuthorizeRead(owner, authz, mine.Meta); err != nil {
		t.Errorf("reading your own site: %v", err)
	}
	if err := AuthorizeRead(owner, authz, theirs.Meta); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another account's site doesn't exist for you: %v", err)
	}
	if err := AuthorizeRead(context.Background(), authz, mine.Meta); !errors.Is(err, i18n.ErrUnauthenticated) {
		t.Errorf("reading without signing in: %v", err)
	}
	if err := Authorize(owner, nil, ReadPermission("web.site"), auth.Target{Scope: mine.Scope}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("without an authorizer nothing is allowed: %v", err)
	}
	items, err := Readable(owner, authz, []*store.Resource{theirs, mine})
	if err != nil || len(items) != 1 || items[0] != mine {
		t.Errorf("readable = %v, %v", items, err)
	}
}

// pipelineFunc is a changeset.Pipeline whose Submit is a function.
type pipelineFunc func(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error)

func (f pipelineFunc) Submit(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error) {
	return f(ctx, req)
}

func (f pipelineFunc) Get(ctx context.Context, id store.ID) (*changeset.ChangeSet, error) {
	return nil, i18n.ErrNotFound.New("what", string(id))
}

func (f pipelineFunc) Undo(ctx context.Context, req changeset.UndoRequest) (*changeset.ChangeSet, error) {
	return nil, changeset.ErrNotApplied.New()
}

func TestChangeOperations(t *testing.T) {
	var got changeset.Request
	pipeline := pipelineFunc(func(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error) {
		got = req
		return &changeset.ChangeSet{ID: store.NewID(store.PrefixChangeSet), Title: "Rename site", DryRun: req.DryRun, State: changeset.StatePlanned}, nil
	})
	s := New(Options{Auth: auth.DevAuthenticator{}, Log: slog.New(slog.DiscardHandler)})
	type renameInput struct {
		ChangeParams
		ID   string `path:"id"`
		Body struct {
			Name string `json:"name" maxLength:"64"`
		}
	}
	huma.Register(s.API(), huma.Operation{
		OperationID: "renameSite", Method: http.MethodPost, Path: Prefix + "/web/sites/{id}/actions/rename",
		Summary: "Rename a site", Security: Protected,
	}, func(ctx context.Context, in *renameInput) (*ChangeSetOutput, error) {
		input, _ := json.Marshal(in.Body)
		return Submit(ctx, pipeline, in.ChangeParams, changeset.Request{Changes: []changeset.Change{{
			Op: changeset.OpAction, Kind: "web.site", ID: store.ID(in.ID), Action: "rename", Input: input,
		}}})
	})

	doc, err := s.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), `"name": "dryRun"`) || !strings.Contains(string(doc), `"name": "Idempotency-Key"`) || !strings.Contains(string(doc), `"ChangeSet"`) {
		t.Errorf("the change parameters and the change set are documented: %s", doc)
	}

	call := func(query string, header map[string]string) *http.Response {
		req := httptest.NewRequest("POST", "/api/v1/web/sites/site_01/actions/rename"+query, strings.NewReader(`{"name":"blog"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Result()
	}
	if resp := call("?dryRun=true", nil); resp.StatusCode != 200 || !got.DryRun || got.Actor == nil {
		t.Errorf("a dry run answers 200 with the preview: %d, %+v", resp.StatusCode, got)
	}
	if resp := call("", map[string]string{"Idempotency-Key": "k-1"}); resp.StatusCode != 202 || got.DryRun || got.IdempotencyKey != "k-1" {
		t.Errorf("an applied change answers 202: %d, %+v", resp.StatusCode, got)
	}
}
