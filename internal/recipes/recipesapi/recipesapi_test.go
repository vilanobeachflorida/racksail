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
	"sync"
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
	srv, key, _ := versionedCatalog(t)
	return srv, key
}

// versionedCatalog serves a signed catalog with the hello recipe at 1.0.0,
// and publish replaces it with another version, as a catalog's maintainers
// release one.
func versionedCatalog(t *testing.T) (*httptest.Server, string, func(version string)) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	icon := []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	var mu sync.Mutex
	files := map[string][]byte{}
	publish := func(version string) {
		r := []byte(strings.Replace(recipe, `version: "1.0.0"`, `version: "`+version+`"`, 1))
		idx, _ := json.Marshal(recipes.Index{Format: 1, Catalog: "test", Built: time.Now().UTC().Truncate(time.Second), Items: []recipes.IndexItem{{
			ID: "hello", Kind: recipes.KindApp, Name: "Hello", Version: version, Summary: "A page that says hello.", LicenseClass: recipes.OpenSource,
			Path: "apps/hello/recipe.yaml", SHA256: sum(r), Icon: "apps/hello/icon.svg",
			Files: []recipes.IndexFile{{Path: "apps/hello/icon.svg", SHA256: sum(icon), Size: int64(len(icon))}, {Path: "apps/hello/recipe.yaml", SHA256: sum(r), Size: int64(len(r))}},
		}}})
		mu.Lock()
		defer mu.Unlock()
		files = map[string][]byte{"/index.json": idx, "/index.json.sig": ed25519.Sign(key, idx), "/apps/hello/recipe.yaml": r, "/apps/hello/icon.svg": icon}
	}
	publish("1.0.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		data, ok := files[r.URL.Path]
		mu.Unlock()
		if ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)), publish
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

// catalogPanel is a panel with the web module and the catalog srv serves,
// signed with key, synced; it returns the panel, its API and the catalog's
// ID.
func catalogPanel(t *testing.T, srv *httptest.Server, key string) (*modulestest.Env, http.Handler, store.ID) {
	t.Helper()
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
	// Installs through the API are held to their catalog, as the panel holds them.
	for _, h := range e.Services.Recipes.Hooks() {
		if err := e.Hooks.Registry.Register(h); err != nil {
			t.Fatal(err)
		}
	}
	spec, _ := json.Marshal(recipes.CatalogSpec{Name: "test", Title: "Test", URL: srv.URL, Keys: []string{key}})
	cs, err := e.Submit(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: recipes.CatalogKind.Name(), Scope: store.ServerScope(), Spec: spec}}})
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("catalog: %v", err)
	}
	cat := cs.Changes[0].ID
	if _, err := e.Services.Catalogs.Sync(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	return e, api.New(api.Options{Dev: true, Modules: e.Registry, Services: e.Services, Log: slog.New(slog.DiscardHandler)}).Handler(), cat
}

func TestCatalogAPI(t *testing.T) {
	ctx := context.Background()
	srv, key := catalog(t)
	e, h, cat := catalogPanel(t, srv, key)

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

// keyed sends a request with an Idempotency-Key and returns the ID of the
// change set it answers with, if any.
func keyed(t *testing.T, h http.Handler, path, key string, body any) (int, store.ID, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	var cs changeset.ChangeSet
	json.Unmarshal(out, &cs)
	return rec.Code, cs.ID, out
}

// Retries of installing, upgrading and uninstalling with the first
// request's Idempotency-Key answer with the first change set, even once the
// installation is at the version the upgrade asked for or the uninstall
// removed it.
func TestRetriesGetTheFirstChangeSet(t *testing.T) {
	ctx := context.Background()
	srv, key, publish := versionedCatalog(t)
	e, h, cat := catalogPanel(t, srv, key)
	in := recipes.InstallInput{Source: recipes.Source{Catalog: cat, Item: "hello"}, Scope: store.ServerScope(), Inputs: map[string]any{"greeting": "Hi"}}
	installation := func() *store.Object[recipes.InstallationSpec, recipes.InstallationStatus] {
		t.Helper()
		list, _, err := recipes.ServerInstallationKind.List(ctx, e.Store, store.Query{})
		if err != nil || len(list) > 1 {
			t.Fatalf("installations = %+v, %v", list, err)
		}
		if len(list) == 0 {
			return nil
		}
		return list[0]
	}

	code, first, body := keyed(t, h, "/api/v1/catalog/install", "install-1", in)
	if code != http.StatusAccepted || first == "" {
		t.Fatalf("install = %d %s", code, body)
	}
	e.Settle(t)
	if code, again, body := keyed(t, h, "/api/v1/catalog/install", "install-1", in); code != http.StatusAccepted || again != first {
		t.Errorf("installing again with the same key = %d %s, want %s", code, body, first)
	}
	inst := installation()
	if inst == nil || inst.Spec.Version != "1.0.0" {
		t.Fatalf("installation = %+v", inst)
	}

	publish("1.1.0")
	if _, err := e.Services.Catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	upgrade := "/api/v1/catalog/installations/" + string(inst.ID) + "/upgrade"
	code, first, body = keyed(t, h, upgrade, "upgrade-1", recipes.UpgradeInput{})
	if code != http.StatusAccepted || first == "" {
		t.Fatalf("upgrade = %d %s", code, body)
	}
	e.Settle(t)
	if inst := installation(); inst == nil || inst.Spec.Version != "1.1.0" {
		t.Fatalf("after the upgrade = %+v", inst)
	}
	if code, again, body := keyed(t, h, upgrade, "upgrade-1", recipes.UpgradeInput{}); code != http.StatusAccepted || again != first {
		t.Errorf("upgrading again with the same key = %d %s, want %s", code, body, first)
	}
	if code, _, body := keyed(t, h, upgrade, "upgrade-2", recipes.UpgradeInput{}); code != http.StatusConflict || !strings.Contains(string(body), "recipes.no_upgrade") {
		t.Errorf("another upgrade = %d %s", code, body)
	}

	uninstall := "/api/v1/catalog/installations/" + string(inst.ID) + "/uninstall"
	code, first, body = keyed(t, h, uninstall, "uninstall-1", recipes.UninstallInput{})
	if code != http.StatusAccepted || first == "" {
		t.Fatalf("uninstall = %d %s", code, body)
	}
	e.Settle(t)
	if inst := installation(); inst != nil {
		t.Fatalf("after uninstalling = %+v", inst)
	}
	if code, again, body := keyed(t, h, uninstall, "uninstall-1", recipes.UninstallInput{}); code != http.StatusAccepted || again != first {
		t.Errorf("uninstalling again with the same key = %d %s, want %s", code, body, first)
	}
	if code, _, body := keyed(t, h, uninstall, "uninstall-2", recipes.UninstallInput{}); code != http.StatusNotFound {
		t.Errorf("another uninstall of what is gone = %d %s", code, body)
	}
}
