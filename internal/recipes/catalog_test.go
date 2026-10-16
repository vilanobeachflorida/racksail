package recipes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The keys built into the panel are the ones catalog/keys publishes.
func TestOfficialKeysMatchCatalog(t *testing.T) {
	read := func(dir string) map[string]string {
		out := map[string]string{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".pub") {
				data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
				out[e.Name()] = string(data)
			}
		}
		return out
	}
	built, published := read("keys"), read("../../catalog/keys")
	if len(built) != len(published) {
		t.Fatalf("internal/recipes/keys has %d keys and catalog/keys %d; copy catalog/keys/*.pub there", len(built), len(published))
	}
	for name, data := range published {
		if built[name] != data {
			t.Errorf("internal/recipes/keys/%s differs from catalog/keys/%s", name, name)
		}
	}
	if len(OfficialKeys()) != len(published) {
		t.Errorf("OfficialKeys found %d keys", len(OfficialKeys()))
	}
}

// catalogServer serves a signed catalog.
type catalogServer struct {
	t     *testing.T
	srv   *httptest.Server
	key   ed25519.PrivateKey
	mu    sync.Mutex
	files map[string][]byte
	built time.Time
	items []IndexItem
	down  bool
}

func newCatalogServer(t *testing.T) *catalogServer {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	c := &catalogServer{t: t, key: key, files: map[string][]byte{}, built: time.Now().UTC().Truncate(time.Second)}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		data, ok := c.files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *catalogServer) publicKey() string {
	return "# Racksail catalog key (Ed25519 public key, base64).\n" + base64.StdEncoding.EncodeToString(c.key.Public().(ed25519.PublicKey)) + "\n"
}

// add publishes an item's recipe and icon, and signs a new index.
func (c *catalogServer) add(recipe string, extra func(*IndexItem)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, err := Parse([]byte(recipe))
	if err != nil {
		c.t.Fatal(err)
	}
	dir := string(r.Kind) + "s/" + r.ID + "/"
	icon := []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")
	c.files[dir+"recipe.yaml"] = []byte(recipe)
	c.files[dir+"icon.svg"] = icon
	it := IndexItem{
		ID: r.ID, Kind: r.Kind, Name: r.Name, Version: r.Version, Summary: r.Summary, LicenseClass: r.LicenseClass, Categories: r.Categories,
		Path: dir + "recipe.yaml", SHA256: hashOf([]byte(recipe)), Icon: dir + "icon.svg", Requires: r.Requires,
		Files: []IndexFile{
			{Path: dir + "icon.svg", SHA256: hashOf(icon), Size: int64(len(icon))},
			{Path: dir + "recipe.yaml", SHA256: hashOf([]byte(recipe)), Size: int64(len(recipe))},
		},
	}
	if extra != nil {
		extra(&it)
	}
	c.items = slices.DeleteFunc(c.items, func(o IndexItem) bool { return o.ID == it.ID })
	c.items = append(c.items, it)
	sort.Slice(c.items, func(i, j int) bool { return c.items[i].ID < c.items[j].ID })
	c.built = c.built.Add(time.Minute)
	c.sign()
}

func (c *catalogServer) sign() {
	data, _ := json.Marshal(Index{Format: 1, Catalog: "test", Built: c.built, Items: c.items})
	c.files[IndexName] = data
	c.files[SignatureName] = ed25519.Sign(c.key, data)
}

// panel is a panel with catalogs and the recipe engine, and two kinds to
// install: sites and databases.
type panel struct {
	store    store.Store
	engine   *changeset.Engine
	jobs     *jobs.Manager
	catalogs *Catalogs
	recipes  *Engine
	admin    *auth.Actor
	cache    string
	on       map[string]bool
}

func newPanel(t *testing.T, configure func(*EngineOptions)) *panel {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.DiscardHandler)
	kinds := testKinds(t)
	for _, k := range []store.Kind{CatalogKind, InstallationKind, ServerInstallationKind, account.Kind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	st := store.NewMemory(kinds)
	bus := events.NewMemory(1000)
	q := jobs.NewMemory(ctx, jobs.MemoryOptions{Workers: 2, Log: log})
	t.Cleanup(func() { q.Stop(context.Background()) })
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	vault := secrets.NewMemory(box)
	p := &panel{store: st, jobs: q, cache: t.TempDir(), on: map[string]bool{"web": true, "databases": true},
		admin: &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}}
	p.catalogs = NewCatalogs(CatalogOptions{Store: st, Cache: p.cache, Logger: log})
	opts := EngineOptions{
		Store: st, Kinds: kinds, Catalogs: p.catalogs, Checker: reconcile.AgentChecker{Interval: 10 * time.Millisecond},
		ModuleOn:  func(id string) bool { return p.on[id] },
		DataKinds: func(kind string) bool { return kind == "databases.database" },
		Logger:    log,
	}
	if configure != nil {
		configure(&opts)
	}
	p.recipes = NewEngine(opts)
	recs := reconcile.NewRegistry()
	for _, r := range append(p.recipes.Reconcilers(), p.catalogs.Reconciler(),
		reconcile.Typed(kindOf[siteSpec](kinds, "web.site"), noop[siteSpec]{}),
		reconcile.Typed(kindOf[databaseSpec](kinds, "databases.database"), noop[databaseSpec]{})) {
		if err := recs.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	runner := &reconcile.Runner{Store: st, Registry: recs, Secrets: vault, Events: bus, Log: log, Checker: reconcile.AgentChecker{Interval: 10 * time.Millisecond}}
	// The engine's own hooks hold every installation to its source, the
	// engine's requests included.
	reg := hooks.NewRegistry()
	for _, k := range kinds.All() {
		reg.DeclareKind(k)
	}
	for _, h := range p.recipes.Hooks() {
		if err := reg.Register(h); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	p.engine, err = changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: q, Events: bus, Audit: bus, Secrets: vault, Authorizer: auth.NewRoleAuthorizer(nil),
		Hooks: &hooks.Runner{Registry: reg, Events: bus, Audit: bus, Log: log}, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	return p
}

func kindOf[S any](kinds *store.KindRegistry, name string) *store.KindOf[S, store.Status] {
	k, _ := kinds.Get(name)
	return k.(*store.KindOf[S, store.Status])
}

// noop applies a resource by marking it ready, as a module with nothing
// to do on the machine would.
type noop[S any] struct{}

func (noop[S]) Plan(ctx context.Context, obj *store.Object[S, store.Status], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (noop[S]) Apply(ctx context.Context, obj *store.Object[S, store.Status], plan reconcile.Plan, ex reconcile.Executor) (store.Status, error) {
	st := obj.Status
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st, time.Now())
	return st, err
}

// newAccount puts an account straight in the store, for what the tests
// install to belong to: resources only belong to accounts that exist.
func (p *panel) newAccount(t *testing.T) store.Scope {
	t.Helper()
	id := store.NewID("acct")
	raw, _ := json.Marshal(account.AccountSpec{Slug: "a" + strings.ToLower(string(id[len(id)-8:])), Name: "Test"})
	ctx := context.Background()
	if err := p.store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: id, Kind: account.Kind.Name(), Scope: store.AccountScope(id)}, Spec: raw})
	}); err != nil {
		t.Fatal(err)
	}
	return store.AccountScope(id)
}

func (p *panel) submit(t *testing.T, req changeset.Request) *changeset.ChangeSet {
	t.Helper()
	ctx := context.Background()
	req.Actor = p.admin
	cs, err := p.engine.Submit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := p.jobs.Wait(wctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	final, err := p.engine.Get(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func (p *panel) addCatalog(t *testing.T, c *catalogServer, url string) store.ID {
	t.Helper()
	spec, _ := json.Marshal(CatalogSpec{Name: "test", Title: "Test catalog", URL: url, Keys: []string{c.publicKey()}})
	cs := p.submit(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: CatalogKind.Name(), Scope: store.ServerScope(), Spec: spec}}})
	if cs.State != changeset.StateApplied {
		t.Fatalf("adding the catalog: %s %s", cs.State, mustJSON(cs.Error))
	}
	return cs.Changes[0].ID
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func TestCatalogSync(t *testing.T) {
	ctx := context.Background()
	c := newCatalogServer(t)
	c.add(wordpress, func(it *IndexItem) { it.Featured = true })
	c.add(strings.NewReplacer("id: wordpress", "id: ghost", "name: WordPress", "name: Ghost").Replace(wordpress), nil)
	p := newPanel(t, nil)
	// Adding a catalog fetches its index.
	id := p.addCatalog(t, c, c.srv.URL)
	if idx, err := p.catalogs.Index(ctx, id); err != nil || len(idx.Items) != 2 {
		t.Errorf("once added: %+v, %v", idx, err)
	}
	if obj, _ := CatalogKind.Get(ctx, p.store, id); obj.Status.SyncedAt == nil || obj.Status.Phase != store.PhaseReady {
		t.Errorf("status once added = %+v", obj.Status)
	}
	c.add(strings.NewReplacer("id: wordpress", "id: forgejo", "name: WordPress", "name: Forgejo").Replace(wordpress), nil)
	cs := p.submit(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpAction, Kind: CatalogKind.Name(), ID: id, Action: "sync"}}})
	var out SyncOutput
	if err := json.Unmarshal(cs.Changes[0].Output, &out); err != nil || out.Items != 3 {
		t.Fatalf("sync = %s, %v", cs.Changes[0].Output, err)
	}
	obj, _ := CatalogKind.Get(ctx, p.store, id)
	if obj.Status.Items != 3 || obj.Status.SyncedAt == nil || obj.Status.IndexSHA256 == "" || obj.Status.Official {
		t.Errorf("status = %+v", obj.Status)
	}
	items, err := p.catalogs.Items(ctx, ItemFilter{})
	if err != nil || len(items) != 3 || items[0].ID != "wordpress" || items[0].Official {
		t.Fatalf("items = %+v, %v: featured first, and labelled third-party", items, err)
	}
	if found, _ := p.catalogs.Items(ctx, ItemFilter{Query: "GHO"}); len(found) != 1 || found[0].ID != "ghost" {
		t.Errorf("search = %+v", found)
	}
	r, _, it, err := p.catalogs.Recipe(ctx, id, "wordpress")
	if err != nil || r.Name != "WordPress" || it.CatalogName != "test" {
		t.Fatalf("recipe = %+v, %v", r, err)
	}
	// A file that doesn't match the index is refused, however it is
	// served.
	c.mu.Lock()
	c.files["apps/ghost/icon.svg"] = []byte("<svg onload='alert(1)'/>")
	c.mu.Unlock()
	if _, err := p.catalogs.File(ctx, id, "ghost", "apps/ghost/icon.svg"); !errors.Is(err, ErrFileMismatch) {
		t.Errorf("tampered file: %v", err)
	}
	// What was fetched is kept: a panel that restarted finds it, checked
	// again, even with the catalog down.
	c.mu.Lock()
	c.down = true
	c.mu.Unlock()
	again := NewCatalogs(CatalogOptions{Store: p.store, Cache: p.cache})
	if _, _, _, err := again.Recipe(ctx, id, "wordpress"); err != nil {
		t.Errorf("after a restart: %v", err)
	}
	c.mu.Lock()
	c.down = false
	c.mu.Unlock()
	// An index signed by another key is refused, and the one kept stays.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	c.mu.Lock()
	c.files[SignatureName] = ed25519.Sign(other, c.files[IndexName])
	c.mu.Unlock()
	if _, err := p.catalogs.Sync(ctx, id); err == nil || !strings.Contains(err.Error(), "trusted key") {
		t.Errorf("forged index: %v", err)
	}
	if obj, _ := CatalogKind.Get(ctx, p.store, id); obj.Status.SyncError == "" {
		t.Error("the failure is recorded")
	}
	// An older index, signed but replayed, is refused.
	c.mu.Lock()
	c.built = c.built.Add(-time.Hour)
	c.sign()
	c.mu.Unlock()
	if _, err := p.catalogs.Sync(ctx, id); !errors.Is(err, ErrOlderIndex) {
		t.Errorf("replayed index: %v", err)
	}
	if items, _ := p.catalogs.Items(ctx, ItemFilter{}); len(items) != 3 {
		t.Error("the index kept before is still offered")
	}
}

func TestCatalogMirrorsAndMarketplace(t *testing.T) {
	ctx := context.Background()
	c := newCatalogServer(t)
	c.add(wordpress, nil)
	p := newPanel(t, nil)
	// The URL is down; the mirror serves the same files.
	spec, _ := json.Marshal(CatalogSpec{Name: "mirrored", Title: "Mirrored", URL: "http://127.0.0.1:1", Mirrors: []string{c.srv.URL}, Keys: []string{c.publicKey()}})
	cs := p.submit(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: CatalogKind.Name(), Scope: store.ServerScope(), Spec: spec}}})
	id := cs.Changes[0].ID
	if _, err := p.catalogs.Sync(ctx, id); err != nil {
		t.Fatalf("through the mirror: %v", err)
	}
	off := NewCatalogs(CatalogOptions{Store: p.store, Active: func() bool { return false }})
	if _, err := off.Sync(ctx, id); !errors.Is(err, ErrMarketplaceOn) {
		t.Errorf("with the marketplace off, nothing is fetched: %v", err)
	}
	bad, _ := json.Marshal(CatalogSpec{Name: "bad", Title: "Bad", URL: "ftp://example.com", Keys: []string{"not a key"}})
	if _, err := p.engine.Submit(ctx, changeset.Request{Actor: p.admin, Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: CatalogKind.Name(), Scope: store.ServerScope(), Spec: bad}}}); err == nil {
		t.Error("a catalog without a URL and key is refused")
	}
}

func TestOfficialCatalog(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	p := newPanel(t, nil)
	cats := NewCatalogs(CatalogOptions{Store: p.store, Official: []ed25519.PublicKey{pub}})
	submit := func(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error) {
		return p.submit(t, req), nil
	}
	if err := cats.Boot(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	if err := cats.Boot(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	list, _, _ := CatalogKind.List(context.Background(), p.store, store.Query{})
	if len(list) != 1 || list[0].Spec.URL != OfficialURL || !cats.official(list[0].Spec) {
		t.Fatalf("catalogs = %+v", list)
	}
	// No key, no official catalog.
	none := NewCatalogs(CatalogOptions{Store: store.NewMemory(testKinds(t)), Official: []ed25519.PublicKey{}})
	if err := none.Boot(context.Background(), submit); err != nil {
		t.Error(err)
	}
	if k, err := ParseKey(base64.StdEncoding.EncodeToString(pub)); err != nil || !k.Equal(pub) {
		t.Errorf("a bare key line parses: %v", err)
	}
	sum := sha256.Sum256(pub)
	if _, err := ParseKey(hex.EncodeToString(sum[:])); err == nil {
		t.Error("hex isn't a key")
	}
}
