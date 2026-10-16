package recipesapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type pageSpec struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

var pageKind = store.Define[pageSpec, store.Status](store.KindInfo{
	Name: "web.page", Prefix: "page", Scope: store.ScopeServer, Title: "Page", Plural: "pages", Description: "A page.",
})

type web struct{ modules.Base }

func (web) Kinds() []store.Kind { return []store.Kind{pageKind} }

type pages struct{}

func (pages) Plan(ctx context.Context, obj *store.Object[pageSpec, store.Status], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (pages) Apply(ctx context.Context, obj *store.Object[pageSpec, store.Status], plan reconcile.Plan, ex reconcile.Executor) (store.Status, error) {
	st := obj.Status
	return st, reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st, time.Now())
}

func (web) Reconcilers() []reconcile.Reconciler {
	return []reconcile.Reconciler{reconcile.Typed(pageKind, pages{})}
}

const recipe = `format: 1
kind: app
id: hello
name: Hello
version: "1.0.0"
summary: A page that says hello.
license: MIT
licenseClass: open-source
maintainers: [{name: Tests, github: tests}]
requires: {modules: [web]}
inputs:
  - {name: greeting, type: string, label: Greeting, default: Hello}
resources:
  - name: page
    kind: web.page
    spec: {path: /hello, content: "${inputs.greeting}"}
health:
  - {type: resource, target: page}
`

func catalog(t *testing.T) (*httptest.Server, string) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	icon := []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	idx, _ := json.Marshal(recipes.Index{Format: 1, Catalog: "test", Built: time.Now().UTC().Truncate(time.Second), Items: []recipes.IndexItem{{
		ID: "hello", Kind: recipes.KindApp, Name: "Hello", Version: "1.0.0", Summary: "A page that says hello.", LicenseClass: recipes.OpenSource,
		Path: "apps/hello/recipe.yaml", SHA256: sum([]byte(recipe)), Icon: "apps/hello/icon.svg",
		Files: []recipes.IndexFile{{Path: "apps/hello/icon.svg", SHA256: sum(icon), Size: int64(len(icon))}, {Path: "apps/hello/recipe.yaml", SHA256: sum([]byte(recipe)), Size: int64(len(recipe))}},
	}}})
	files := map[string][]byte{"/index.json": idx, "/index.json.sig": ed25519.Sign(key, idx), "/apps/hello/recipe.yaml": []byte(recipe), "/apps/hello/icon.svg": icon}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := files[r.URL.Path]; ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

func do(t *testing.T, h http.Handler, method, path string, body any) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	return rec, out
}

func TestCatalogAPI(t *testing.T) {
	ctx := context.Background()
	srv, key := catalog(t)
	mod := &web{modules.NewBase([]byte(`hardware: {arch: [amd64, arm64]}
id: web
name: Web
description: A module for tests.
version: 0.1.0
license: AGPL-3.0-or-later
category: operations
profiles: [all-in-one]
required: true
dependencies: {}
cost: {idleMemoryMB: 4, activeMemoryMB: 40, diskMB: 10}
upstream: []
`))}
	e := modulestest.New(t, modulestest.Options{
		Kinds: []store.Kind{recipes.CatalogKind, recipes.InstallationKind, recipes.ServerInstallationKind},
		Reconcilers: func(svc *modules.Services) []reconcile.Reconciler {
			svc.Catalogs = recipes.NewCatalogs(recipes.CatalogOptions{Store: svc.Store, Cache: t.TempDir()})
			svc.Recipes = recipes.NewEngine(recipes.EngineOptions{Store: svc.Store, Kinds: svc.Kinds, Catalogs: svc.Catalogs,
				ModuleOn: func(id string) bool { return svc.Modules.State(id).IsOn() }})
			return append(svc.Recipes.Reconcilers(), svc.Catalogs.Reconciler())
		},
	}, mod)
	spec, _ := json.Marshal(recipes.CatalogSpec{Name: "test", Title: "Test", URL: srv.URL, Keys: []string{key}})
	cs, err := e.Submit(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: recipes.CatalogKind.Name(), Scope: store.ServerScope(), Spec: spec}}})
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("catalog: %v", err)
	}
	cat := cs.Changes[0].ID
	if _, err := e.Services.Catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	h := api.New(api.Options{Dev: true, Modules: e.Registry, Services: e.Services, Log: slog.New(slog.DiscardHandler)}).Handler()

	rec, body := do(t, h, http.MethodGet, "/api/v1/catalog/items?q=hel", nil)
	if rec.Code != http.StatusOK || !strings.Contains(string(body), `"id":"hello"`) {
		t.Fatalf("items = %d %s", rec.Code, body)
	}
	rec, body = do(t, h, http.MethodGet, "/api/v1/catalog/items/"+string(cat)+"/hello", nil)
	if rec.Code != http.StatusOK || !strings.Contains(string(body), `"greeting"`) || strings.Contains(string(body), `"fits"`) {
		t.Fatalf("item = %d %s", rec.Code, body)
	}
	rec, body = do(t, h, http.MethodGet, "/api/v1/catalog/items/"+string(cat)+"/hello/icon", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("icon = %d %v %s", rec.Code, rec.Header(), body)
	}

	in := recipes.InstallInput{Source: recipes.Source{Catalog: cat, Item: "hello"}, Scope: store.ServerScope(), Inputs: map[string]any{"greeting": "Hi"}}
	rec, body = do(t, h, http.MethodPost, "/api/v1/catalog/install?dryRun=true", in)
	if rec.Code != http.StatusOK || !strings.Contains(string(body), `"dryRun":true`) {
		t.Fatalf("preview = %d %s", rec.Code, body)
	}
	rec, body = do(t, h, http.MethodPost, "/api/v1/catalog/install", in)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install = %d %s", rec.Code, body)
	}
	e.Settle(t)
	list, _, _ := recipes.ServerInstallationKind.List(ctx, e.Store, store.Query{})
	if len(list) != 1 || list[0].Spec.Inputs["greeting"] != "Hi" || list[0].Status.Phase != store.PhaseReady {
		t.Fatalf("installations = %+v", list)
	}
	rec, body = do(t, h, http.MethodPost, "/api/v1/catalog/installations/"+string(list[0].ID)+"/upgrade", recipes.UpgradeInput{})
	if rec.Code != http.StatusConflict || !strings.Contains(string(body), "recipes.no_upgrade") {
		t.Errorf("upgrade without a new version = %d %s", rec.Code, body)
	}
	rec, body = do(t, h, http.MethodPost, "/api/v1/catalog/installations/"+string(list[0].ID)+"/uninstall", recipes.UninstallInput{})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("uninstall = %d %s", rec.Code, body)
	}
	e.Settle(t)
	if list, _, _ := recipes.ServerInstallationKind.List(ctx, e.Store, store.Query{}); len(list) != 0 {
		t.Errorf("after uninstalling = %+v", list)
	}
}
