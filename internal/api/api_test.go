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
	"net/netip"
	"os"
	"path/filepath"
	"slices"
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
	off := newServer(
		HealthCheck{Name: "agent", Check: func(context.Context) error { return nil }},
		HealthCheck{Name: "dns.knot", Active: func() bool { return false }, Check: func(context.Context) error { return errors.New("not running") }},
	)
	resp, body = do(t, off.Handler(), "GET", "/api/v1/health", nil)
	if resp.StatusCode != 200 || strings.Contains(body, "dns.knot") {
		t.Errorf("checks of modules that are off aren't run: %d %s", resp.StatusCode, body)
	}
	// Checks run side by side, so slow ones don't add up.
	slow := func(context.Context) error { time.Sleep(300 * time.Millisecond); return nil }
	parallel := newServer(HealthCheck{Name: "a", Check: slow}, HealthCheck{Name: "b", Check: slow}, HealthCheck{Name: "c", Check: slow})
	start := time.Now()
	resp, body = do(t, parallel.Handler(), "GET", "/api/v1/health", nil)
	if resp.StatusCode != 200 || time.Since(start) > 800*time.Millisecond || strings.Index(body, `"a"`) > strings.Index(body, `"c"`) {
		t.Errorf("parallel checks = %d %s in %s", resp.StatusCode, body, time.Since(start))
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

// sessionAuth signs in requests whose session cookie is "good" and refuses
// any other session, as an expired or revoked one would be.
type sessionAuth struct{}

var errSessionExpired = i18n.Define("apitest.session_expired", http.StatusUnauthorized, "Your session has expired. Sign in again.")

func (sessionAuth) Authenticate(ctx context.Context, r *http.Request) (*auth.Actor, error) {
	c, err := r.Cookie(SessionCookie)
	switch {
	case err != nil:
		return nil, nil
	case c.Value == "good":
		return &auth.Actor{Type: auth.ActorUser, Source: auth.SourceUI}, nil
	}
	return nil, errSessionExpired.New()
}

func TestInvalidCredentials(t *testing.T) {
	s := New(Options{Auth: sessionAuth{}, Log: slog.New(slog.DiscardHandler), UI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ui") })})
	type out struct{ Body struct{ OK bool } }
	huma.Register(s.API(), huma.Operation{OperationID: "secret", Method: "GET", Path: "/api/v1/secret", Summary: "Secret", Security: Protected},
		func(ctx context.Context, _ *struct{}) (*out, error) { return &out{}, nil })
	expired := map[string]string{"Cookie": SessionCookie + "=stale"}
	if resp, body := do(t, s.Handler(), "GET", "/", expired); resp.StatusCode != 200 || body != "ui" {
		t.Errorf("the UI, with its sign-in screen, loads with an expired session: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, s.Handler(), "GET", "/api/v1/health", expired); resp.StatusCode != 200 {
		t.Errorf("public operations answer: %d", resp.StatusCode)
	}
	resp, body := do(t, s.Handler(), "GET", "/api/v1/secret", expired)
	if resp.StatusCode != 401 || !strings.Contains(body, "apitest.session_expired") {
		t.Errorf("protected operations explain why: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, s.Handler(), "GET", "/api/v1/secret", map[string]string{"Cookie": SessionCookie + "=good"}); resp.StatusCode != 200 {
		t.Errorf("a valid session gets through: %d", resp.StatusCode)
	}
}

func TestAllowFrom(t *testing.T) {
	s := New(Options{
		AllowFrom: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8::/32")},
		Log:       slog.New(slog.DiscardHandler), UI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ui") }),
	})
	for remote, allowed := range map[string]bool{
		"198.51.100.7:52000":          true,
		"[::ffff:198.51.100.7]:52000": true,
		"[2001:db8::5]:52000":         true,
		"203.0.113.9:52000":           false,
		"127.0.0.1:52000":             false,
		"not an address":              false,
	} {
		for _, path := range []string{"/", "/api/v1/health"} {
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = remote
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if got := rec.Code != http.StatusForbidden; got != allowed {
				t.Errorf("%s from %s = %d %s", path, remote, rec.Code, rec.Body)
			}
			if !allowed && !strings.Contains(rec.Body.String(), "api.address_not_allowed") {
				t.Errorf("refusals have a code: %s", rec.Body)
			}
		}
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

type noteSpec struct {
	Text string `json:"text"`
}

var noteKind = store.Define[noteSpec, store.Status](store.KindInfo{
	Name: "test.note", Prefix: "note", Scope: store.ScopeAccount, Title: "Note", Plural: "notes",
	Description: "A note, for list tests.",
})

func TestListing(t *testing.T) {
	ctx := context.Background()
	kinds := store.NewKindRegistry()
	if err := kinds.Register(noteKind); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(kinds)
	acctA, acctB := store.NewID("acct"), store.NewID("acct")
	create := func(acct store.ID) store.ID {
		r := &store.Resource{Meta: store.Meta{Kind: "test.note", Scope: store.AccountScope(acct)}, Spec: json.RawMessage(`{"text":"x"}`)}
		if err := st.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, r) }); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	a1, a2, a3, b1 := create(acctA), create(acctA), create(acctA), create(acctB)
	authz := auth.NewRoleAuthorizer(nil)
	as := func(grants ...auth.Grant) context.Context {
		return auth.WithActor(ctx, &auth.Actor{Type: auth.ActorUser, Grants: grants})
	}
	scopeA := store.AccountScope(acctA)
	list := func(ctx context.Context, q store.Query) ([]store.ID, string, error) {
		t.Helper()
		q.Kind = "test.note"
		page, err := List(ctx, authz, st, q)
		if err != nil {
			return nil, "", err
		}
		var ids []store.ID
		for _, r := range page.Items {
			ids = append(ids, r.ID)
		}
		return ids, page.Next, nil
	}
	same := func(got []store.ID, want ...store.ID) bool { return slices.Equal(got, want) }

	owner := as(auth.Grant{Role: auth.RoleOwner, Scope: scopeA})
	if got, _, err := list(owner, store.Query{Scope: &scopeA}); err != nil || !same(got, a1, a2, a3) {
		t.Errorf("the owner's list = %v, %v", got, err)
	}
	first, next, err := list(owner, store.Query{Scope: &scopeA, Limit: 2})
	if err != nil || !same(first, a1, a2) || next == "" {
		t.Errorf("first page = %v, %q, %v", first, next, err)
	}
	if rest, _, _ := list(owner, store.Query{Scope: &scopeA, Limit: 2, Cursor: next}); !same(rest, a3) {
		t.Errorf("second page = %v", rest)
	}

	collab := as(auth.Grant{Role: auth.RoleCollaborator, Scope: scopeA, Object: a2, Permissions: []auth.Permission{"test.note.read"}})
	if got, _, err := list(collab, store.Query{Scope: &scopeA}); err != nil || !same(got, a2) {
		t.Errorf("a collaborator granted one note sees that note: %v, %v", got, err)
	}
	if got, _, err := list(collab, store.Query{}); err != nil || !same(got, a2) {
		t.Errorf("listing every scope falls back the same way: %v, %v", got, err)
	}
	if got, _, err := list(collab, store.Query{Scope: &scopeA, IDs: []store.ID{a1}}); err != nil || len(got) != 0 {
		t.Errorf("asking for notes not granted finds nothing: %v, %v", got, err)
	}
	stranger := as(auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope(acctB)})
	if _, _, err := list(stranger, store.Query{Scope: &scopeA}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("another account's owner: %v", err)
	}
	admin := as(auth.Grant{Role: auth.RoleAdmin, Scope: store.ServerScope()})
	if got, _, err := list(admin, store.Query{}); err != nil || !same(got, a1, a2, a3, b1) {
		t.Errorf("the admin lists everything: %v, %v", got, err)
	}
	if _, _, err := list(ctx, store.Query{Scope: &scopeA}); !errors.Is(err, i18n.ErrUnauthenticated) {
		t.Errorf("without signing in: %v", err)
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
