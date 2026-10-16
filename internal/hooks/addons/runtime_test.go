package addons

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

type siteSpec struct {
	Domains []string `json:"domains"`
	CDN     string   `json:"cdn,omitempty"`
	Notes   string   `json:"notes,omitempty"`
}

var siteKind = store.Define[siteSpec, store.Status](store.KindInfo{
	Name: "web.site", Prefix: "site", Scope: store.ScopeAccount, Title: "Site", Plural: "sites", Description: "A site.",
	Actions: []store.Action{{Name: "purge", Title: "Purge", Summary: "Empty the site's cache", Risk: store.RiskLow, Past: "purged"}},
	Indexes: []store.Index{{Name: "domain", Path: "domains"}, {Name: "note", Path: "notes"}},
})

const guardManifest = `
format: 1
id: site-guard
name: Site guard
version: 0.3.0
license: Apache-2.0
source: https://example.com/site-guard
author: Example
description: Refuses sites on evil domains and puts the others behind a CDN.
apiVersions: [v1]
hooks:
  - {point: web.site.create.validate, kind: validate, security: true, deadline: 2s}
  - {point: web.site.*.plan, kind: modify}
  - {point: web.site.create.applied, kind: observe}
provides: [notifier]
permissions:
  hooks: [validate, modify, observe]
  read: [{kind: web.site, fields: [domains]}]
  write: [{kind: web.site, fields: [cdn]}]
  network: [api.example.com]
  resources: {cpu: 5%, memoryMB: 32}
ui:
  - {type: tab, label: Security, kind: web.site, path: /tab}
routes:
  - {method: GET, path: /scans, summary: List scans}
`

// fakeAddon plays an add-on on a Unix socket.
type fakeAddon struct {
	mu     sync.Mutex
	calls  []hooks.HookCall
	sent   []json.RawMessage
	notes  bool
	health string
	// kindCalls are the plans and applies of its kinds it was asked for;
	// with stray set, its plans write outside its directory, and with
	// failDeletes, it can't undo what it did for a resource.
	kindCalls   []kindCall
	stray       bool
	failDeletes bool
	// adds are changes its modify hook's answers add, as JSON objects, so
	// they say exactly what an add-on's process could.
	adds []map[string]any
}

// kindCall is one plan or apply the fake add-on was asked for.
type kindCall struct {
	what string
	call hooks.KindCall
}

func (f *fakeAddon) serve(t *testing.T, socket string) {
	t.Helper()
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		status := f.health
		f.mu.Unlock()
		if status == "" {
			status = "ok"
		}
		json.NewEncoder(w).Encode(hooks.AddonHealth{Status: status, Version: "0.3.0", APIVersion: "v1"})
	})
	mux.HandleFunc("POST /hooks/{point}", func(w http.ResponseWriter, r *http.Request) {
		var call hooks.HookCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		notes, adds := f.notes, slices.Clone(f.adds)
		f.mu.Unlock()
		var reply hooks.HookReply
		switch call.Kind {
		case hooks.Validate:
			var spec siteSpec
			json.Unmarshal(call.ChangeSet.Changes[*call.Change].After, &spec)
			for _, d := range spec.Domains {
				if strings.HasSuffix(d, ".evil") {
					reply.Deny, reply.Reason = true, d+" is on the list of evil domains."
				}
			}
		case hooks.Modify:
			cs := call.ChangeSet
			for _, i := range call.Changes {
				var spec map[string]any
				json.Unmarshal(cs.Changes[i].After, &spec)
				spec["cdn"] = "on"
				if notes {
					spec["notes"] = "the add-on was here"
				}
				cs.Changes[i].After, _ = json.Marshal(spec)
			}
			reply.ChangeSet = cs
			if len(adds) > 0 {
				raw, _ := json.Marshal(cs)
				var answer map[string]any
				json.Unmarshal(raw, &answer)
				changes, _ := answer["changes"].([]any)
				for _, a := range adds {
					changes = append(changes, a)
				}
				answer["changes"] = changes
				json.NewEncoder(w).Encode(map[string]any{"changeSet": answer})
				return
			}
		}
		json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("POST /provide/{capability}/{method}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.sent = append(f.sent, body)
		f.mu.Unlock()
		w.Write([]byte("{}"))
	})
	// Its guard.rule resources are files of their pattern in its directory,
	// and a purge reports what it purged.
	mux.HandleFunc("POST /kinds/{kind}/{what}", func(w http.ResponseWriter, r *http.Request) {
		var call hooks.KindCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.kindCalls = append(f.kindCalls, kindCall{what: r.PathValue("what"), call: call})
		stray, failDeletes := f.stray, f.failDeletes
		f.mu.Unlock()
		var spec struct {
			Pattern string `json:"pattern"`
		}
		json.Unmarshal(call.Resource.Spec, &spec)
		file := call.Dir + "/" + string(call.Resource.ID) + ".conf"
		if stray {
			file = "/etc/racksail/stolen.conf"
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.PathValue("what") == "plan" && call.Action != nil:
			json.NewEncoder(w).Encode(hooks.KindPlan{Effects: []system.Effect{{Type: system.EffectProcess, Action: "purge", Target: spec.Pattern}}})
		case r.PathValue("what") == "plan" && call.Resource.Deleted:
			json.NewEncoder(w).Encode(hooks.KindPlan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: file})}})
		case r.PathValue("what") == "plan":
			json.NewEncoder(w).Encode(hooks.KindPlan{
				Steps: []agent.Step{
					agent.EnsureDir.Step(agent.EnsureDirInput{Path: call.Dir, Mode: 0o755, Parents: true}),
					agent.WriteFile.Step(agent.WriteFileInput{Path: file, Content: []byte(spec.Pattern + "\n"), Mode: 0o644}),
				},
				Effects: []system.Effect{{Type: system.EffectProcess, Action: "watch", Target: spec.Pattern}},
			})
		case call.Action != nil:
			json.NewEncoder(w).Encode(hooks.KindApplied{Output: json.RawMessage(`{"purged":3}`)})
		case call.Resource.Deleted && !failDeletes:
			json.NewEncoder(w).Encode(hooks.KindApplied{})
		case spec.Pattern == "fail" || call.Resource.Deleted:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]any{"status": 502, "detail": "the outside service refused the rule"})
		default:
			json.NewEncoder(w).Encode(hooks.KindApplied{Status: map[string]any{"watching": spec.Pattern, "phase": "forged"}})
		}
	})
	mux.HandleFunc("/routes/{path...}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"method": r.Method, "path": r.PathValue("path"), "actor": r.Header.Get("Racksail-Actor"), "cookie": r.Header.Get("Cookie"),
			"body": string(body),
		})
	})
	mux.HandleFunc("GET /ui/{path...}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// What it answers can't loosen the panel's policy for its frame.
		w.Header().Set("Content-Security-Policy", "script-src *")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		json.NewEncoder(w).Encode(map[string]string{
			"path": r.PathValue("path"), "query": r.URL.RawQuery, "actor": r.Header.Get("Racksail-Actor"), "cookie": r.Header.Get("Cookie"),
			"request": r.Header.Get("Racksail-Request-Id"), "nonce": r.Header.Get("Racksail-Nonce"), "base": r.Header.Get("Racksail-Frame-Base"),
		})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

// restarting reports an add-on's service restarting as often as told.
type restarting struct {
	agent.Client
	restarts atomic.Int32
}

func (r *restarting) Call(ctx context.Context, op string, input json.RawMessage, opts agent.CallOptions) (json.RawMessage, error) {
	if op == "UnitStatus" && strings.Contains(string(input), "racksail-addon@") && strings.Contains(string(input), ".service") {
		return json.Marshal(system.UnitState{Name: "x", LoadState: "loaded", ActiveState: "active", Restarts: int(r.restarts.Load())})
	}
	return r.Client.Call(ctx, op, input, opts)
}

// accounts places accounts' add-ons, as the module that keeps accounts
// does.
type accounts map[store.ID]Placement

func (a accounts) AddonPlacement(ctx context.Context, rd store.Reader, account store.ID) (Placement, error) {
	p, ok := a[account]
	if !ok {
		return Placement{}, store.ErrNotFound.New("what", string(account))
	}
	return p, nil
}

type env struct {
	t        *testing.T
	store    *store.Memory
	kinds    *store.KindRegistry
	engine   *changeset.Engine
	queue    *jobs.Manager
	registry *hooks.Registry
	runtime  *Runtime
	runner   *reconcile.Runner
	machine  *sim.Machine
	agent    *restarting
	bus      *events.Log
	catalogs *recipes.Catalogs
	// places are where accounts' add-ons run; with none, nothing keeps
	// accounts.
	places accounts
	dir    string
	admin  *auth.Actor
	fake   *fakeAddon
}

// addonRecipe is the recipe of an add-on item: what every catalog item
// says, and the program it pins.
func addonRecipe(t *testing.T, spec AddonSpec) string {
	t.Helper()
	man, err := hooks.ParseAddonManifest([]byte(spec.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	return "format: 1\nkind: addon\nid: " + man.ID + "\nname: " + man.Name + "\nversion: \"" + man.Version + "\"\nsummary: An add-on.\n" +
		"license: " + man.License + "\nlicenseClass: open-source\nmaintainers: [{name: Example}]\n" +
		"downloads:\n  - {url: \"" + spec.Package.URL + "\", sha256: " + spec.Package.SHA256 + ", size: " + fmt.Sprint(spec.Package.Size) + "}\n" +
		"health:\n  - {type: unit, target: \"racksail-addon@" + man.ID + ".service\"}\n"
}

// publish serves a signed catalog holding the add-ons, adds it to the
// panel and fetches its index.
func (e *env) publish(t *testing.T, name string, specs ...AddonSpec) store.ID {
	t.Helper()
	ctx := context.Background()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	files := map[string][]byte{}
	var items []recipes.IndexItem
	for _, spec := range specs {
		man, _ := hooks.ParseAddonManifest([]byte(spec.Manifest))
		recipe, manifest := []byte(addonRecipe(t, spec)), []byte(spec.Manifest)
		dir := "addons/" + man.ID + "/"
		files[dir+"recipe.yaml"], files[dir+recipes.AddonManifestFile] = recipe, manifest
		items = append(items, recipes.IndexItem{
			ID: man.ID, Kind: recipes.KindAddon, Name: man.Name, Version: man.Version, Summary: "An add-on.", LicenseClass: recipes.OpenSource,
			Path: dir + "recipe.yaml", SHA256: sum(recipe), Files: []recipes.IndexFile{
				{Path: dir + "recipe.yaml", SHA256: sum(recipe), Size: int64(len(recipe))},
				{Path: dir + recipes.AddonManifestFile, SHA256: sum(manifest), Size: int64(len(manifest))},
			},
		})
	}
	idx, _ := json.Marshal(recipes.Index{Format: 1, Catalog: name, Built: time.Now().UTC().Truncate(time.Second), Items: items})
	files[recipes.IndexName], files[recipes.SignatureName] = idx, ed25519.Sign(key, idx)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := files[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	id := store.NewID(recipes.CatalogKind.Info().Prefix)
	raw, _ := json.Marshal(recipes.CatalogSpec{Name: name, Title: name, URL: srv.URL, Keys: []string{base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}})
	if err := e.store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: id, Kind: recipes.CatalogKind.Name(), Scope: store.ServerScope()}, Spec: raw})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.catalogs.Sync(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Unix socket paths are short, so not under t.TempDir.
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{siteKind, Addon, AccountAddon, recipes.CatalogKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	st := store.NewMemory(kinds)
	reg := hooks.NewRegistry()
	for _, k := range kinds.All() {
		reg.DeclareKind(k)
	}
	bus := events.NewMemory(1000)
	m := sim.New(sim.Options{})
	ag := &restarting{Client: agent.NewLocal(m.Backend(), log)}
	recs := reconcile.NewRegistry()
	runner := &reconcile.Runner{Store: st, Agent: ag, Registry: recs, Events: bus, Log: log}
	e := &env{t: t, store: st, kinds: kinds, places: accounts{}}
	e.catalogs = recipes.NewCatalogs(recipes.CatalogOptions{Store: st, Cache: filepath.Join(dir, "catalogs"), Logger: log})
	rt := NewRuntime(Options{
		Registry: reg, Store: st, Kinds: kinds, Reconcilers: recs, Agent: ag, Events: bus, Audit: bus, Tokens: NewMemoryTokens(),
		SafeMode: OpenSafeMode(filepath.Join(dir, "state"), nil), TokenDir: filepath.Join(dir, "tokens"), SocketDir: dir,
		Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			if host == "api.example.com" {
				return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
			}
			return nil, errors.New("no such host")
		},
		Enqueue: runner.Enqueue, Catalogs: e.catalogs, Log: log,
		Accounts: func() Accounts {
			if len(e.places) == 0 {
				return nil
			}
			return e.places
		},
	})
	for _, r := range rt.Reconcilers() {
		if err := recs.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	queue := jobs.NewMemory(ctx, jobs.MemoryOptions{Log: log})
	t.Cleanup(func() { queue.Stop(context.Background()) })
	engine, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: queue, Events: bus, Audit: bus,
		Hooks: &hooks.Runner{Registry: reg, Events: bus, Audit: bus, Log: log}, Authorizer: auth.NewRoleAuthorizer(nil), Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range rt.Hooks() {
		if err := reg.Register(h); err != nil {
			t.Fatal(err)
		}
	}
	go runner.Run(ctx, time.Hour)
	fake := &fakeAddon{}
	fake.serve(t, filepath.Join(dir, "site-guard.sock"))
	e.engine, e.queue, e.registry, e.runtime, e.runner, e.machine, e.agent, e.bus, e.dir, e.fake = engine, queue, reg, rt, runner, m, ag, bus, dir, fake
	e.admin = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "admin", Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	// The site guard comes from the official catalog.
	e.publish(t, "official", guard(true))
	return e
}

func (e *env) submit(actor *auth.Actor, changes ...changeset.Change) (*changeset.ChangeSet, error) {
	e.t.Helper()
	cs, err := e.engine.Submit(context.Background(), changeset.Request{Actor: actor, Changes: changes})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := e.queue.Wait(ctx, cs.Job); err != nil {
		e.t.Fatal(err)
	}
	return e.engine.Get(ctx, cs.ID)
}

func spec(t *testing.T, v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func guard(enabled bool) AddonSpec {
	man, _ := hooks.ParseAddonManifest([]byte(guardManifest))
	return AddonSpec{
		ID: "site-guard", Manifest: guardManifest, Enabled: enabled, Catalog: "official", Item: "site-guard",
		Package:  Package{URL: "https://example.com/site-guard-0.3.0.tar.gz", SHA256: strings.Repeat("ab", 32), Size: 1 << 20, Format: "tar.gz"},
		Approved: man.Permissions,
	}
}

// accountManifest is an add-on an account owner may install: it only takes
// part in changes.
const accountManifest = `
format: 1
id: site-guard
name: Site guard
version: 0.3.0
license: Apache-2.0
source: https://example.com/site-guard
author: Example
description: Refuses sites on evil domains.
apiVersions: [v1]
hooks:
  - {point: web.site.create.validate, kind: validate, security: true, deadline: 2s}
  - {point: web.site.*.applied, kind: observe}
permissions:
  hooks: [validate, observe]
  read: [{kind: web.site, fields: [domains]}]
`

func account(t *testing.T, manifest string) AddonSpec {
	t.Helper()
	man, err := hooks.ParseAddonManifest([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	return AddonSpec{
		ID: man.ID, Manifest: manifest, Enabled: true, Catalog: "acme", Item: man.ID,
		Package:  Package{URL: "https://example.com/site-guard-0.3.0.tar.gz", SHA256: strings.Repeat("ab", 32), Size: 1 << 20, Format: "tar.gz"},
		Approved: man.Permissions,
	}
}

func (e *env) install(t *testing.T) store.ID {
	t.Helper()
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "core.addon", Scope: store.ServerScope(), Spec: spec(t, guard(true))})
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != changeset.StateApplied {
		t.Fatalf("installing: %s %s", cs.State, cs.Error)
	}
	return cs.Changes[0].ID
}

func TestChecks(t *testing.T) {
	ok := guard(true)
	if _, err := Check(ok, false); err != nil {
		t.Fatal(err)
	}
	less := ok
	less.Approved.Write = nil
	if _, err := Check(less, false); !errors.Is(err, ErrNotApproved) || !strings.Contains(i18n.From(err).Message, "changing web.site") {
		t.Errorf("unapproved permission: %v", err)
	}
	other := ok
	other.ID = "something-else"
	if _, err := Check(other, false); !errors.Is(err, ErrManifest) {
		t.Errorf("another id: %v", err)
	}
	plain := ok
	plain.Package.URL = "http://example.com/a.tar.gz"
	if _, err := Check(plain, false); !errors.Is(err, ErrManifest) {
		t.Errorf("a download that isn't https: %v", err)
	}
	system := ok
	system.Manifest = strings.Replace(guardManifest, "hooks: [validate, modify, observe]", "hooks: [validate, modify, observe, system]", 1)
	system.Approved.Hooks = append(system.Approved.Hooks, hooks.System)
	if _, err := Check(system, false); err != nil {
		t.Errorf("an admin may approve system hooks: %v", err)
	}
	if _, err := Check(system, true); !errors.Is(err, ErrTenancy) {
		t.Errorf("an account can't: %v", err)
	}
	// What add-ons provide serves the whole server, and so do lifecycle,
	// identity and provider points; an account's add-ons only take part in
	// changes.
	if _, err := Check(ok, true); !errors.Is(err, ErrTenancy) || !strings.Contains(i18n.From(err).Message, "provides notifier") {
		t.Errorf("an account's add-on that provides a notifier: %v", err)
	}
	if _, err := Check(account(t, accountManifest), true); err != nil {
		t.Errorf("an account's add-on at the points of changes: %v", err)
	}
	for _, point := range []string{"auth.login.validate", "core.os_upgrade.check", "core.boot", "modules.web.on", "backups.run.*", "web.site.create.*", "core.update.before"} {
		kind := "validate"
		if !strings.HasSuffix(point, ".validate") && !strings.HasSuffix(point, ".check") {
			kind = "observe"
		}
		man := strings.Replace(accountManifest, "{point: web.site.create.validate, kind: validate", "{point: \""+point+"\", kind: "+kind, 1)
		if _, err := Check(account(t, man), true); !errors.Is(err, ErrTenancy) {
			t.Errorf("an account's add-on at %s: %v", point, err)
		}
		if _, err := Check(account(t, man), false); err != nil {
			t.Errorf("a server admin's add-on at %s: %v", point, err)
		}
	}
	if Instance("core.account_addon", "site-guard", store.AccountScope("acct_01j9x3k4m5n6p7q8r9s0t1u2v3")) != "site-guard-r9s0t1u2v3" {
		t.Errorf("instance = %s", Instance("core.account_addon", "site-guard", store.AccountScope("acct_01j9x3k4m5n6p7q8r9s0t1u2v3")))
	}
}

func TestAddonsTakePartInChanges(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.install(t)
	obj, err := Addon.Get(ctx, e.store, id)
	if err != nil {
		t.Fatal(err)
	}
	if obj.Status.State != StateRunning || obj.Status.Health != "ok" || obj.Status.Hooks != 6 || obj.Status.Version != "0.3.0" {
		t.Fatalf("status = %+v", obj.Status)
	}
	if len(obj.Status.Hosts) != 1 || obj.Status.Hosts[0].Addresses[0] != "203.0.113.7/32" {
		t.Errorf("hosts = %+v", obj.Status.Hosts)
	}
	hostsFile, _ := e.machine.Backend().Files.ReadFile(ctx, HostsFile("site-guard"), 0)
	if !strings.Contains(string(hostsFile), "203.0.113.7 api.example.com") {
		t.Errorf("hosts file = %s", hostsFile)
	}
	token, err := os.ReadFile(filepath.Join(e.dir, "tokens", "site-guard.token"))
	if err != nil || !IsToken(string(token)) {
		t.Fatalf("token = %q, %v", token, err)
	}

	// Its validate hook refuses evil domains, with its reason.
	acct := store.AccountScope(store.NewID("acct"))
	_, err = e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Spec: spec(t, siteSpec{Domains: []string{"shop.evil"}})})
	if !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "list of evil domains") || !strings.Contains(i18n.From(err).Message, "Site guard 0.3.0") {
		t.Fatalf("an evil domain: %v", err)
	}

	// Its modify hook puts the site behind a CDN, labelled as its own, and
	// it only ever saw the domains.
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Spec: spec(t, siteSpec{Domains: []string{"shop.example"}, Notes: "private"})})
	if err != nil {
		t.Fatal(err)
	}
	site, _ := siteKind.Get(ctx, e.store, cs.Changes[0].ID)
	if site.Spec.CDN != "on" || site.Spec.Notes != "private" {
		t.Errorf("site = %+v", site.Spec)
	}
	if len(cs.Contributions) != 1 || cs.Contributions[0].Source.Name != "site-guard" {
		t.Errorf("contributions = %+v", cs.Contributions)
	}
	e.fake.mu.Lock()
	for _, call := range e.fake.calls {
		for _, ch := range call.ChangeSet.Changes {
			if strings.Contains(string(ch.After), "private") || strings.Contains(string(ch.Before), "private") {
				t.Errorf("the add-on saw a field it may not read: %s", ch.After)
			}
		}
		if call.Actor != nil && call.Actor.IP != "" {
			t.Error("the add-on learned where the person came from")
		}
	}
	e.fake.notes = true
	e.fake.mu.Unlock()

	// A change to a field it may not write is thrown away, as the work of a
	// hook that failed.
	cs, err = e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Spec: spec(t, siteSpec{Domains: []string{"blog.example"}, Notes: "mine"})})
	if err != nil {
		t.Fatal(err)
	}
	site, _ = siteKind.Get(ctx, e.store, cs.Changes[0].ID)
	if site.Spec.Notes != "mine" || site.Spec.CDN != "" {
		t.Errorf("a hook that oversteps leaves nothing: %+v", site.Spec)
	}

	// It provides a notification channel.
	ch, err := hooks.Open[notify.Channel](ctx, e.registry, hooks.ProvideNotifier, "site-guard", json.RawMessage(`{"room":"ops"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(ctx, notify.Message{Title: "Disk almost full", Severity: events.SeverityWarning}); err != nil {
		t.Fatal(err)
	}
	e.fake.mu.Lock()
	if len(e.fake.sent) != 1 || !strings.Contains(string(e.fake.sent[0]), "Disk almost full") || !strings.Contains(string(e.fake.sent[0]), `"room":"ops"`) {
		t.Errorf("sent = %s", e.fake.sent)
	}
	e.fake.mu.Unlock()
	if tabs := e.registry.Extensions(hooks.ExtendTab); len(tabs) != 1 || tabs[0].Extension.UI.Label != "Security" {
		t.Errorf("tabs = %+v", tabs)
	}

	// Its routes are served by it, with who asks but none of their
	// credentials.
	routes := e.registry.Extensions(hooks.ExtendRoutes)
	if len(routes) != 1 {
		t.Fatalf("routes = %+v", routes)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/addons/site-guard/{path...}", routes[0].Extension.Handler)
	req, _ := http.NewRequest("GET", "/api/v1/addons/site-guard/scans", nil)
	req.Header.Set("Cookie", "racksail_session=secret")
	req = req.WithContext(auth.WithActor(req.Context(), e.admin))
	rec := newRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.body.String(), `"path":"scans"`) || strings.Contains(rec.body.String(), "secret") || !strings.Contains(rec.body.String(), "admin") {
		t.Errorf("route = %d %s", rec.status, rec.body.String())
	}
	// Its screens are told who is looking, with the query they were asked
	// with and the panel's ID for the request, and nothing of their
	// credentials either.
	_, _, _, client, ok := e.runtime.Running("site-guard")
	if !ok {
		t.Fatal("the add-on doesn't run")
	}
	mux = http.NewServeMux()
	mux.Handle("GET /api/v1/addons/site-guard/ui/{path...}", client.Screens())
	req, _ = http.NewRequest("GET", "/api/v1/addons/site-guard/ui/tab?resource=site_1", nil)
	req.Header.Set("Cookie", "racksail_session=secret")
	req = req.WithContext(auth.WithActor(req.Context(), e.admin))
	rec = newRecorder()
	// The API's middleware answers with the request's ID before handlers run.
	rec.Header().Set("X-Request-Id", "req_01jq0000000000000000000001")
	mux.ServeHTTP(rec, req)
	var screen map[string]string
	if err := json.Unmarshal(rec.body.Bytes(), &screen); err != nil {
		t.Fatalf("screen = %d %s", rec.status, rec.body.String())
	}
	var looking auth.Actor
	if err := json.Unmarshal([]byte(screen["actor"]), &looking); err != nil || looking.ID != e.admin.ID {
		t.Errorf("the screen was told %q is looking", screen["actor"])
	}
	if screen["path"] != "tab" || screen["query"] != "resource=site_1" || screen["cookie"] != "" || screen["request"] != "req_01jq0000000000000000000001" {
		t.Errorf("screen = %+v", screen)
	}

	// Turned off, it is out of the pipeline and its token is gone.
	off := guard(false)
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "core.addon", ID: id, Spec: spec(t, off)}); err != nil {
		t.Fatal(err)
	}
	if owners := e.registry.Owners(); len(owners) != 1 || owners[0] != "core:addons" {
		t.Errorf("owners = %v", owners)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "tokens", "site-guard.token")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the token is still there: %v", err)
	}
	if st, _ := e.machine.Backend().Units.Status(ctx, SocketUnit("site-guard")); st.Active() {
		t.Error("its socket still listens")
	}
}

func newRecorder() *recorder { return &recorder{header: http.Header{}} }

func TestGateway(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.install(t)
	token, _ := os.ReadFile(filepath.Join(e.dir, "tokens", "site-guard.token"))
	var seen *auth.Actor
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = GatewayAuthenticator{}.Authenticate(r.Context(), r)
		if r.Header.Get("Authorization") != "" {
			t.Error("the add-on's token reached the API")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[{"id":"site_1","kind":"web.site","spec":{"domains":["a.example"],"cdn":"on","notes":"private"}},{"id":"mod_1","kind":"core.addon","spec":{"id":"x"}}]}`))
	})
	g := &Gateway{Runtime: e.runtime, API: api, Store: e.store}
	serve := func(path, token string) *recorder {
		req, _ := http.NewRequest("GET", path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := newRecorder()
		g.ServeHTTP(rec, req)
		return rec
	}
	if rec := serve("/api/v1/web/sites", ""); rec.status != http.StatusUnauthorized {
		t.Errorf("without a token = %d", rec.status)
	}
	if rec := serve("/api/v1/web/sites", tokenPrefix+"forged"); rec.status != http.StatusUnauthorized {
		t.Errorf("with a forged token = %d", rec.status)
	}
	rec := serve("/api/v1/web/sites", string(token))
	if rec.status != http.StatusOK {
		t.Fatalf("with its token = %d %s", rec.status, rec.body.String())
	}
	body := rec.body.String()
	if strings.Contains(body, "private") || !strings.Contains(body, "a.example") || !strings.Contains(body, `"cdn":"on"`) || strings.Contains(body, `"spec":{"id"`) {
		t.Errorf("the answer keeps only what the add-on may read: %s", body)
	}
	if seen == nil || seen.Type != auth.ActorAddon || seen.ID != id || len(seen.Grants) != 1 {
		t.Fatalf("actor = %+v", seen)
	}
	authz := auth.NewRoleAuthorizer(nil)
	acct := store.AccountScope(store.NewID("acct"))
	if err := authz.Authorize(ctx, seen, "web.site.update", auth.Target{Scope: acct}); err != nil {
		t.Errorf("it may change sites: %v", err)
	}
	if err := authz.Authorize(ctx, seen, "core.addon.read", auth.Target{Scope: store.ServerScope()}); err == nil {
		t.Error("it may not read add-ons")
	}
	if rec := serve("/secrets/site_01j9x3k4m5n6p7q8r9s0t1u2v3/password", string(token)); rec.status != http.StatusForbidden {
		t.Errorf("reading secrets without the permission = %d", rec.status)
	}

	// Through the API, it may only change the fields it was allowed.
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Spec: spec(t, siteSpec{Domains: []string{"c.example"}})})
	if err != nil {
		t.Fatal(err)
	}
	site, _ := siteKind.Get(ctx, e.store, cs.Changes[0].ID)
	changed := site.Spec
	changed.Notes = "the add-on's own"
	if _, err := e.submit(seen, changeset.Change{Op: changeset.OpUpdate, Kind: "web.site", ID: site.ID, Spec: spec(t, changed)}); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "notes") {
		t.Errorf("changing a field it may not: %v", err)
	}
	changed = site.Spec
	changed.CDN = "off"
	if _, err := e.submit(seen, changeset.Change{Op: changeset.OpUpdate, Kind: "web.site", ID: site.ID, Spec: spec(t, changed)}); err != nil {
		t.Errorf("changing a field it may: %v", err)
	}
}

func TestSafeMode(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.install(t)
	sub := e.bus.Subscribe(events.Filter{Types: []string{"core.safe_mode.*"}}, 4)
	defer sub.Close()

	// An add-on that keeps crashing as it starts turns safe mode on.
	e.agent.restarts.Store(3)
	e.runtime.Watch(ctx)
	st := e.runtime.SafeMode().Status()
	if !st.Active || st.Sources[0].Kind != SafeModeAutomatic || !strings.Contains(st.Sources[0].Reason, "crashed 3 times") {
		t.Fatalf("safe mode = %+v", st)
	}
	select {
	case ev := <-sub.C:
		if ev.Type != SafeModeEntered.Name || ev.Severity != events.SeverityCritical {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Error("no event says safe mode is on")
	}
	if len(e.runtime.Instances()) != 0 || len(e.registry.Extensions(hooks.ExtendTab)) != 0 {
		t.Error("safe mode takes every add-on out of the pipeline")
	}
	obj, _ := Addon.Get(ctx, e.store, id)
	if obj.Status.State != StateSafeMode {
		t.Errorf("status = %+v", obj.Status)
	}
	// A reconcile in safe mode keeps it off.
	if err := e.runner.Reconcile(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.runtime.Instances()) != 0 {
		t.Error("safe mode lasts through reconciles")
	}

	// Turning it off starts the add-ons again.
	e.agent.restarts.Store(0)
	if st, err := e.runtime.LeaveSafeMode(ctx, e.admin); err != nil || st.Active {
		t.Fatalf("leaving = %+v, %v", st, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.runtime.Instances()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(e.runtime.Instances()) != 1 {
		t.Error("the add-on runs again")
	}

	// The console's flag works the same way.
	if err := WriteFlag(ConsoleFlag(filepath.Join(e.dir, "state")), "Looking at a problem", time.Now()); err != nil {
		t.Fatal(err)
	}
	e.runtime.Watch(ctx)
	if len(e.runtime.Instances()) != 0 {
		t.Error("the console's flag turns safe mode on")
	}
	boot := OpenSafeMode(t.TempDir(), []byte("BOOT_IMAGE=/vmlinuz root=UUID=x ro racksail.safe_mode quiet"))
	if st := boot.Status(); !st.Active || st.Sources[0].Kind != SafeModeBoot {
		t.Errorf("boot option = %+v", st)
	}
	if err := boot.Leave(); err != nil || !boot.Active() {
		t.Error("the boot option lasts until the server boots without it")
	}
}

// An add-on an account owner installed takes part in that account's
// changes and in nothing else: not in signing in, not in what happens to
// the server, and not in other accounts' changes, of which it learns
// nothing even when they share a change set with its own.
func TestAccountAddonsOnlyTakePartInTheirChanges(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.publish(t, "acme", account(t, accountManifest))
	mine := store.AccountScope(store.NewID("acct"))
	theirs := store.AccountScope(store.NewID("acct"))
	e.places[mine.ID] = Placement{Slug: "mine", Limits: system.ResourceLimits{MemoryMaxBytes: 48 << 20, CPUQuotaPercent: 10}}
	inst := Instance(AccountAddon.Name(), "site-guard", mine)
	fake := &fakeAddon{}
	fake.serve(t, filepath.Join(e.dir, inst+".sock"))
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: AccountAddon.Name(), Scope: mine, Spec: spec(t, account(t, accountManifest))})
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != changeset.StateApplied {
		t.Fatalf("installing: %s %s", cs.State, cs.Error)
	}
	_, man, g, client, ok := e.runtime.Running(inst)
	if !ok || !g.Account() {
		t.Fatalf("the account's add-on runs as %s: %v", inst, e.runtime.Instances())
	}
	// It runs in its account's slice, within what the account's plan
	// allows the whole account.
	dropIn, err := e.machine.Backend().Files.ReadFile(ctx, system.UnitDir+"/"+Service(inst)+".d/"+DropIn+".conf", 0)
	if err != nil || !strings.Contains(string(dropIn), "Slice=racksail-accounts-mine.slice") || !strings.Contains(string(dropIn), "MemoryMax=48M") || !strings.Contains(string(dropIn), "CPUQuota=10%") {
		t.Errorf("drop-in = %s, %v", dropIn, err)
	}
	calls := func() []hooks.HookCall {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return slices.Clone(fake.calls)
	}

	// Signing in never reaches it, nor does anything else outside changes,
	// even through a registration made by hand.
	runner := &hooks.Runner{Registry: e.registry}
	if err := runner.Check(ctx, hooks.AuthLoginValidate, e.admin, map[string]string{"user": "server-admin", "ip": "198.51.100.9"}); err != nil {
		t.Fatalf("signing in: %v", err)
	}
	in := &installed{instance: inst, manifest: man, grants: g, client: client}
	for _, kind := range []hooks.Kind{hooks.Validate, hooks.Observe, hooks.Modify} {
		h := hooks.Registration{Kind: kind}
		e.runtime.handlers(in, &h)
		c := &hooks.Context{Point: hooks.AuthLoginValidate, Actor: e.admin, Data: map[string]string{"user": "server-admin"}}
		switch {
		case h.OnValidate != nil:
			if err := h.OnValidate(ctx, c); err != nil {
				t.Errorf("a validate hook outside changes: %v", err)
			}
		case h.OnObserve != nil:
			h.OnObserve(ctx, c)
		case h.OnModify != nil:
			if err := h.OnModify(ctx, c); err != nil {
				t.Errorf("a modify hook outside changes: %v", err)
			}
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("the add-on heard about something outside its account's changes: %+v", got)
	}

	// Another account's changes don't reach it.
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: theirs, Spec: spec(t, siteSpec{Domains: []string{"theirs.evil"}})}); err != nil {
		t.Fatalf("another account's site: %v", err)
	}

	// Its own account's do.
	_, err = e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: mine, Spec: spec(t, siteSpec{Domains: []string{"mine.evil"}})})
	if !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "list of evil domains") {
		t.Fatalf("its own account's site: %v", err)
	}

	// A change set that also changes another account tells it only about
	// its own account.
	cs, err = e.submit(e.admin,
		changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: mine, Spec: spec(t, siteSpec{Domains: []string{"mine.example"}})},
		changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: theirs, Spec: spec(t, siteSpec{Domains: []string{"secret.example"}, Notes: "theirs"})},
	)
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("a change set of two accounts: %v %+v", err, cs)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !slices.ContainsFunc(calls(), func(c hooks.HookCall) bool { return c.Kind == hooks.Observe }) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	observed := false
	for _, call := range calls() {
		raw, _ := json.Marshal(call)
		if strings.Contains(string(raw), "theirs") || strings.Contains(string(raw), "secret.example") || strings.Contains(string(raw), string(theirs.ID)) {
			t.Errorf("the add-on learned about another account: %s", raw)
		}
		if call.Data != nil {
			t.Errorf("the add-on got the data of a point outside changes: %s", raw)
		}
		if call.Kind == hooks.Observe {
			observed = true
			if call.ChangeSet == nil || call.ChangeSet.Title != "" || len(call.Changes) != 1 || call.Changes[0] != 0 {
				t.Errorf("observed = %s", raw)
			}
		}
	}
	if !observed {
		t.Errorf("the add-on never heard of its own account's site: %+v", calls())
	}
}

func TestAccountAddonsStayInTheirAccount(t *testing.T) {
	acct := store.AccountScope(store.NewID("acct"))
	man, _ := hooks.ParseAddonManifest([]byte(guardManifest))
	g := NewGrants(man.Permissions, acct)
	other := store.AccountScope(store.NewID("acct"))
	mineID, theirsID := store.NewID("site"), store.NewID("site")
	cs := &changeset.ChangeSet{
		Title: "Move theirs.example", Estimate: changeset.Estimate{DiskBytes: 1 << 30},
		Changes: []changeset.ResourceChange{
			{Op: changeset.OpCreate, Kind: "web.site", ID: mineID, Scope: acct, After: json.RawMessage(`{"domains":["mine.example"],"notes":"n"}`)},
			{Op: changeset.OpCreate, Kind: "web.site", ID: theirsID, Scope: other, After: json.RawMessage(`{"domains":["theirs.example"]}`)},
			{Op: changeset.OpCreate, Kind: "dns.zone", Scope: acct, After: json.RawMessage(`{"name":"mine.example"}`)},
		},
		Effects:       []system.Effect{{Type: system.EffectFile, Action: "write", Target: "/srv/racksail/accounts/theirs/site.conf"}},
		Conflicts:     []changeset.Conflict{{Resource: theirsID, Path: "domains"}, {Resource: mineID, Path: "cdn"}},
		Contributions: []changeset.Contribution{{Summary: "theirs", Changes: []int{1}}, {Summary: "mine", Changes: []int{0}, Effects: []int{0}}},
	}
	f := g.FilterChangeSet(cs)
	if f.Title != "" || f.Estimate != (changeset.Estimate{}) || len(f.Effects) != 1 || f.Effects[0] != (system.Effect{}) {
		t.Errorf("what the change set says of another account = %q %+v %+v", f.Title, f.Estimate, f.Effects)
	}
	if len(f.Conflicts) != 1 || f.Conflicts[0].Resource != mineID || len(f.Contributions) != 1 || f.Contributions[0].Summary != "mine" || f.Contributions[0].Effects != nil {
		t.Errorf("conflicts and contributions = %+v %+v", f.Conflicts, f.Contributions)
	}
	if own := g.FilterChangeSet(&changeset.ChangeSet{Title: "Add mine.example", Changes: cs.Changes[:1], Effects: cs.Effects}); own.Title == "" || own.Effects[0].Target == "" {
		t.Errorf("a change set of its own account keeps its title and effects: %+v", own)
	}
	if string(f.Changes[0].After) != `{"domains":["mine.example"]}` {
		t.Errorf("its own site = %s", f.Changes[0].After)
	}
	if f.Changes[1].After != nil || f.Changes[1].Kind != "" || f.Changes[1].ID != "" {
		t.Errorf("another account's site = %+v", f.Changes[1])
	}
	if f.Changes[2].After != nil || f.Changes[2].Kind != "dns.zone" {
		t.Errorf("a kind it may not read = %+v", f.Changes[2])
	}
	if a := g.Actor(store.NewID("acaddon"), "site-guard"); a.Grants[0].Scope != acct {
		t.Errorf("its API grants = %+v", a.Grants)
	}
}

// overrideManifest is an add-on that asks for what only a server admin may
// approve.
var overrideManifest = strings.Replace(guardManifest, "  resources: {cpu: 5%, memoryMB: 32}", "  resources: {cpu: 5%, memoryMB: 32}\n  override: true\n  secrets: true", 1)

// Only a server admin approves core.override, secrets.read and system
// hooks, whoever else may write add-ons: a narrow grant on add-ons, or an
// add-on that may change add-ons. Turning such an add-on off needs no
// admin; giving it a new program does.
func TestOnlyServerAdminsApproveWhatOnlyTheyMayGrant(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	collaborator := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "collaborator", Source: auth.SourceAPI, Grants: []auth.Grant{{
		Role: auth.RoleCollaborator, Scope: store.ServerScope(),
		Permissions: []auth.Permission{"core.addon.read", "core.addon.create", "core.addon.update"},
	}}}
	own := func(manifest string) AddonSpec {
		s := guard(false)
		man, err := hooks.ParseAddonManifest([]byte(manifest))
		if err != nil {
			t.Fatal(err)
		}
		// A server admin's own add-on, from no catalog.
		s.Manifest, s.Approved, s.Catalog, s.Item = manifest, man.Permissions, "", ""
		return s
	}
	privileged := own(overrideManifest)
	system := own(strings.Replace(guardManifest, "hooks: [validate, modify, observe]", "hooks: [validate, modify, observe, system]", 1))
	for name, s := range map[string]AddonSpec{"core.override": privileged, "system hooks": system} {
		_, err := e.submit(collaborator, changeset.Change{Op: changeset.OpCreate, Kind: Addon.Name(), Scope: store.ServerScope(), Spec: spec(t, s)})
		if !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "Only a server admin can approve "+name) {
			t.Errorf("a collaborator approving %s: %v", name, err)
		}
	}
	// An add-on whose grants let it change add-ons can't approve them
	// either.
	addonActor := NewGrants(hooks.Permissions{Write: []hooks.DataScope{{Kind: Addon.Name()}}}, store.ServerScope()).Actor(store.NewID("addon"), "helper")
	if _, err := e.submit(addonActor, changeset.Change{Op: changeset.OpCreate, Kind: Addon.Name(), Scope: store.ServerScope(), Spec: spec(t, privileged)}); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "core.override") {
		t.Errorf("an add-on approving core.override: %v", err)
	}
	// A server admin can.
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: Addon.Name(), Scope: store.ServerScope(), Spec: spec(t, privileged)})
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("an admin approving it: %v %+v", err, cs)
	}
	id := cs.Changes[0].ID
	obj, _ := Addon.Get(ctx, e.store, id)
	// What it holds stays: turning it on needs nothing an admin hasn't
	// approved already.
	on := privileged
	on.Enabled = true
	if _, err := e.submit(collaborator, changeset.Change{Op: changeset.OpUpdate, Kind: Addon.Name(), ID: id, Revision: obj.Revision, Spec: spec(t, on)}); err != nil {
		t.Errorf("turning it on: %v", err)
	}
	// Another program would get what was approved for this one.
	swapped := privileged
	swapped.Package.SHA256 = strings.Repeat("cd", 32)
	obj, _ = Addon.Get(ctx, e.store, id)
	if _, err := e.submit(collaborator, changeset.Change{Op: changeset.OpUpdate, Kind: Addon.Name(), ID: id, Revision: obj.Revision, Spec: spec(t, swapped)}); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "core.override") {
		t.Errorf("a collaborator swapping its program: %v", err)
	}
	// Nor can a collaborator add one to an add-on that had none.
	cs, err = e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: Addon.Name(), Scope: store.ServerScope(), Spec: spec(t, func() AddonSpec {
		s := own(guardManifest)
		s.ID = "plain"
		s.Manifest = strings.Replace(guardManifest, "id: site-guard", "id: plain", 1)
		return s
	}())})
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("an add-on with nothing only admins grant: %v %+v", err, cs)
	}
	plain, _ := Addon.Get(ctx, e.store, cs.Changes[0].ID)
	widened := plain.Spec
	widened.Approved.Secrets = true
	if _, err := e.submit(collaborator, changeset.Change{Op: changeset.OpUpdate, Kind: Addon.Name(), ID: plain.ID, Revision: plain.Revision, Spec: spec(t, widened)}); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "secrets.read") {
		t.Errorf("a collaborator approving secrets.read: %v", err)
	}
}

// Every account's add-on comes from a catalog and is what that catalog's
// signed index offers: its manifest, and a program its recipe pins. So is
// a server's add-on that names a catalog. What it is checked against
// can't be changed by writing its spec.
func TestAddonsAreWhatTheirCatalogOffers(t *testing.T) {
	e := newEnv(t)
	e.publish(t, "acme", account(t, accountManifest))
	acct := store.AccountScope(store.NewID("acct"))
	e.places[acct.ID] = Placement{Slug: "acme"}
	install := func(kind string, scope store.Scope, s AddonSpec) error {
		t.Helper()
		_, err := e.engine.Submit(context.Background(), changeset.Request{Actor: e.admin, DryRun: true, Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: kind, Scope: scope, Spec: spec(t, s)}}})
		return err
	}
	if err := install(AccountAddon.Name(), acct, account(t, accountManifest)); err != nil {
		t.Fatalf("what the catalog offers: %v", err)
	}
	for name, c := range map[string]struct {
		change func(*AddonSpec)
		want   string
	}{
		"another program":  {func(s *AddonSpec) { s.Package.URL = "https://attacker.example/site-guard.tar.gz" }, "isn't one the catalog's recipe pins"},
		"another checksum": {func(s *AddonSpec) { s.Package.SHA256 = strings.Repeat("ef", 32) }, "isn't one the catalog's recipe pins"},
		"another manifest": {func(s *AddonSpec) {
			s.Manifest = strings.Replace(accountManifest, "Refuses sites on evil domains.", "Reads every site.", 1)
		}, "isn't the one the catalog signed"},
		"another catalog": {func(s *AddonSpec) { s.Catalog = "elsewhere" }, "no catalog called elsewhere"},
		"another item":    {func(s *AddonSpec) { s.Item = "site-watch" }, "no item site-watch"},
	} {
		s := account(t, accountManifest)
		c.change(&s)
		if err := install(AccountAddon.Name(), acct, s); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An account's add-on from no catalog isn't one.
	s := account(t, accountManifest)
	s.Catalog, s.Item = "", ""
	if err := install(AccountAddon.Name(), acct, s); !errors.Is(err, i18n.ErrInvalid) || !strings.Contains(fmt.Sprint(i18n.From(err).Fields), "from a catalog") {
		t.Errorf("from no catalog: %v", err)
	}
	// A server's add-on that names a catalog is held to it too.
	server := guard(true)
	server.Package.URL = "https://attacker.example/site-guard.tar.gz"
	if err := install(Addon.Name(), store.ServerScope(), server); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "official catalog") {
		t.Errorf("a server's add-on not what its catalog offers: %v", err)
	}
}

// Without anything that keeps accounts, an account's add-on has no
// account to run in, so it doesn't run.
func TestAccountAddonsNeedTheirAccount(t *testing.T) {
	e := newEnv(t)
	e.publish(t, "acme", account(t, accountManifest))
	acct := store.AccountScope(store.NewID("acct"))
	_, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: AccountAddon.Name(), Scope: acct, Spec: spec(t, account(t, accountManifest))})
	if !errors.Is(err, ErrNoAccount) || !strings.Contains(i18n.From(err).Message, "nothing on this server keeps accounts") {
		t.Errorf("no account to run in: %v", err)
	}
	// An account the module that keeps accounts doesn't know doesn't run
	// either.
	e.places[store.NewID("acct")] = Placement{Slug: "other"}
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: AccountAddon.Name(), Scope: acct, Spec: spec(t, account(t, accountManifest))}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown account: %v", err)
	}
}
