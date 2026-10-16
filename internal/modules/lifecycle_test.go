package modules_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func init() {
	sim.RegisterPackage("webd", sim.PackageInfo{Version: "2.4.1-1", DownloadBytes: 1 << 20, InstalledBytes: 4 << 20, Units: []string{"webd.service"}})
	agent.AllowUnit("webd.service")
}

func manifest(id, extra string) []byte {
	hardware := "hardware: {arch: [amd64, arm64]}\n"
	if strings.Contains(extra, "hardware:") {
		hardware = ""
	}
	return []byte(hardware + `
id: ` + id + `
name: ` + strings.ToUpper(id[:1]) + id[1:] + `
description: A module for tests.
version: 0.1.0
license: AGPL-3.0-or-later
category: operations
profiles: [all-in-one]
cost: {idleMemoryMB: 4, activeMemoryMB: 40, diskMB: 10}
` + extra)
}

// netModule is required, as the network module is.
type netModule struct{ modules.Base }

func newNet() *netModule {
	return &netModule{modules.NewBase(manifest("net", "required: true\ndependencies: {}\nupstream: []\n"))}
}

type pageSpec struct {
	Path    string `json:"path" minLength:"1"`
	Content string `json:"content"`
}

var pageKind = store.Define[pageSpec, store.Status](store.KindInfo{
	Name: "web.page", Prefix: "page", Scope: store.ScopeServer, Title: "Page", Plural: "pages", Description: "A page the web server serves.",
})

const pagesDir = "/srv/racksail/pages"

// webModule serves pages with webd, which it installs when the first page
// needs it. Turned off, it stops webd and keeps the pages.
type webModule struct {
	modules.Base
}

func newWeb() *webModule {
	return &webModule{modules.NewBase(manifest("web", `dependencies: {requires: [net]}
upstream:
  - {id: webd, name: Webd, license: MIT, source: {type: debian, packages: [webd]}, install: When the first page is created.}
ports: [{port: 443, protocol: tcp, description: HTTPS}]
services: [{unit: webd.service, description: Serves the pages.}]
`))}
}

func (w *webModule) Kinds() []store.Kind { return []store.Kind{pageKind} }

func (w *webModule) Reconcilers() []reconcile.Reconciler {
	return []reconcile.Reconciler{reconcile.Typed(pageKind, pageReconciler{man: w.Manifest()})}
}

func (w *webModule) PlanTransition(ctx context.Context, req modules.TransitionRequest) (reconcile.Plan, error) {
	unit := agent.UnitInput{Name: "webd.service"}
	switch {
	case req.Remove:
		return reconcile.Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: pagesDir, Recursive: true})}}, nil
	case req.To == modules.StateOff:
		// Another module serving with webd keeps it running.
		if _, shared := req.Shared.Unit(unit.Name); shared {
			return reconcile.Plan{}, nil
		}
		return reconcile.Plan{Steps: []agent.Step{agent.StopUnit.Step(unit)}}, nil
	}
	return reconcile.Plan{Steps: []agent.Step{agent.StartUnit.Step(unit)}}, nil
}

// blogModule serves a blog with webd too, as two real modules share Valkey.
type blogModule struct{ modules.Base }

func newBlog() *blogModule {
	return &blogModule{modules.NewBase(manifest("blog", `dependencies: {requires: [net]}
upstream:
  - {id: webd, name: Webd, license: MIT, source: {type: debian, packages: [webd]}, install: With the blog.}
ports: [{port: 443, protocol: tcp, description: HTTPS}]
services: [{unit: webd.service, description: Serves the blog.}]
`))}
}

type pageReconciler struct{ man *modules.Manifest }

func (p pageReconciler) Plan(ctx context.Context, obj *store.Object[pageSpec, store.Status], req reconcile.Request) (reconcile.Plan, error) {
	file := pagesDir + "/" + obj.Spec.Path
	if obj.Deleted() {
		return reconcile.Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: file})}}, nil
	}
	steps, err := modules.InstallSteps(ctx, req.Read, p.man, "webd", modules.Install{})
	if err != nil {
		return reconcile.Plan{}, err
	}
	steps = append(steps,
		agent.StartUnit.Step(agent.UnitInput{Name: "webd.service"}),
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: pagesDir, Mode: 0o755}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: file, Content: []byte(obj.Spec.Content), Mode: 0o644}),
	)
	return reconcile.Plan{Steps: steps, Checks: []reconcile.HealthCheck{{Name: "webd", Type: "unit", Target: "webd.service", Timeout: time.Second}}}, nil
}

func (pageReconciler) Apply(ctx context.Context, obj *store.Object[pageSpec, store.Status], plan reconcile.Plan, ex reconcile.Executor) (store.Status, error) {
	st := obj.Status
	return st, reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st, time.Now())
}

func page(t testing.TB, path, content string) changeset.Change {
	return changeset.Change{Op: changeset.OpCreate, Kind: "web.page", Scope: store.ServerScope(), Spec: modulestest.JSON(t, pageSpec{Path: path, Content: content})}
}

// notesModule keeps notes but can't keep them while it is off.
type notesModule struct{ modules.Base }

type noteSpec struct {
	Text string `json:"text"`
}

var noteKind = store.Define[noteSpec, store.Status](store.KindInfo{
	Name: "notes.note", Prefix: "note", Scope: store.ScopeServer, Title: "Note", Plural: "notes", Description: "A note.",
})

func newNotes() *notesModule {
	return &notesModule{modules.NewBase(manifest("notes", "dependencies: {}\nupstream: []\n"))}
}

func (n *notesModule) Kinds() []store.Kind { return []store.Kind{noteKind} }

// shopModule needs one of web or notes, and vmModule needs hardware
// virtualization.
func newShop() modules.Module {
	return &netModule{modules.NewBase(manifest("shop", "dependencies: {requiresAny: [web, notes]}\nupstream: []\n"))}
}

func newVM() modules.Module {
	return &netModule{modules.NewBase(manifest("vm", "dependencies: {requires: [net]}\nupstream: []\nhardware: {arch: [amd64, arm64], features: [virtualization]}\n"))}
}

// starter records when it starts and stops, and fails to start when told.
type starter struct {
	modules.Base
	mu     sync.Mutex
	log    []string
	broken bool
}

func (s *starter) Start(ctx context.Context, svc *modules.Services) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return errors.New("its socket is taken")
	}
	s.log = append(s.log, "start")
	return nil
}

func (s *starter) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, "stop")
	return nil
}

func TestLifecycle(t *testing.T) {
	e := modulestest.Lifecycle(t, newWeb(), modulestest.Use{
		Changes: func(e *modulestest.Env) []changeset.Change {
			return []changeset.Change{page(t, "index.html", "Hello"), page(t, "about.html", "About")}
		},
		Check: func(t *testing.T, e *modulestest.Env) {
			e.RequireUnit(t, "webd.service", "active")
			data, err := e.Machine.Backend().Files.ReadFile(context.Background(), pagesDir+"/index.html", 0)
			if err != nil || string(data) != "Hello" {
				t.Errorf("page = %q, %v", data, err)
			}
		},
	}, newNet())
	info, err := e.Manager.Info(context.Background(), "web")
	if err != nil || info.State != modules.StateOff || info.Enabled || len(info.Installed) != 0 {
		t.Errorf("after removal = %+v, %v", info, err)
	}
}

func TestBootAddsModules(t *testing.T) {
	e := modulestest.New(t, modulestest.Options{}, newNet(), newWeb(), newNotes())
	if e.Manager.State("net") != modules.StateOn || e.Manager.State("web") != modules.StateOff {
		t.Errorf("states = %v", e.Manager.States())
	}
	objs, _, err := modules.Kind.List(context.Background(), e.Store, store.Query{})
	if err != nil || len(objs) != 3 {
		t.Fatalf("core.module resources = %d, %v", len(objs), err)
	}
	// Starting again finds them and adds nothing.
	again := modules.NewManager(e.Registry, modules.ManagerOptions{Services: e.Services})
	if err := again.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again.State("net") != modules.StateOn {
		t.Errorf("the state kept in the store = %v", again.States())
	}
	if objs, _, _ := modules.Kind.List(context.Background(), e.Store, store.Query{}); len(objs) != 3 {
		t.Errorf("a second boot added modules: %d", len(objs))
	}
}

func TestTurningOnAndOff(t *testing.T) {
	ctx := context.Background()
	e := modulestest.New(t, modulestest.Options{}, newNet(), newWeb(), newNotes(), newShop())
	var transitions []modules.Transition
	var mu sync.Mutex
	for _, p := range []hooks.Point{hooks.ModuleOn("web"), hooks.ModuleOff("web")} {
		if err := e.Hooks.Registry.Register(hooks.Registration{Point: p, Kind: hooks.Observe, Owner: "module:monitoring",
			OnObserve: func(ctx context.Context, c *hooks.Context) {
				mu.Lock()
				defer mu.Unlock()
				transitions = append(transitions, c.Data.(modules.Transition))
			}}); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing of a module that is off can be made.
	if _, err := e.Submit(t, changeset.Request{Changes: []changeset.Change{page(t, "a.html", "a")}}); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "Web is off") {
		t.Fatalf("a page while web is off: %v", err)
	}

	// The preview of turning a module on lists what it costs and what it
	// does once something uses it: the software it downloads then, from
	// where and how large, its packages, services and ports, and its
	// memory and disk.
	req, err := e.Manager.TurnOnRequest(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	req.DryRun = true
	preview, err := e.Submit(t, req)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Title != "Turn on Web" || preview.Estimate.MemoryBytes != 40<<20 || preview.Estimate.DownloadBytes != 1<<20 || preview.Estimate.DiskBytes != 10<<20 {
		t.Errorf("preview = %+v", preview)
	}
	effects := map[string]system.Effect{}
	for _, eff := range preview.Effects {
		effects[string(eff.Type)+" "+eff.Action+" "+eff.Target] = eff
	}
	for _, want := range []string{
		"process start module web", "download download_on_first_use Webd", "package install_on_first_use webd",
		"service start_when_used webd.service", "port open_when_used tcp/443", "process run_when_used Web",
	} {
		if _, ok := effects[want]; !ok {
			t.Errorf("the preview doesn't list %s: %+v", want, preview.Effects)
		}
	}
	if d := effects["download download_on_first_use Webd"]; d.Source != "Debian's archive" || d.DownloadBytes != 1<<20 || d.DiskBytes != 4<<20 {
		t.Errorf("the download says where from and how large: %+v", d)
	}
	e.TurnOn(t, "web")
	if e.Manager.State("web") != modules.StateOn {
		t.Fatalf("web = %s", e.Manager.State("web"))
	}

	// The first page makes it active, and installs its software.
	e.MustApply(t, changeset.Request{Changes: []changeset.Change{page(t, "index.html", "Hi")}})
	e.RequireState(t, "web", modules.StateActive)
	e.RequireUnit(t, "webd.service", "active")
	info, err := e.Manager.Info(ctx, "web")
	if err != nil || info.Usage.Resources != 1 || info.Usage.Kinds["web.page"] != 1 {
		t.Errorf("info = %+v, %v", info, err)
	}
	if sw, err := e.Manager.FirstUse(ctx, "web"); err != nil || len(sw) != 1 || !sw[0].Installed {
		t.Errorf("software = %+v, %v", sw, err)
	}

	// Turning it off stops the server and keeps the pages; the resync
	// doesn't start it again.
	e.TurnOff(t, "web")
	e.RequireState(t, "web", modules.StateOff)
	e.RequireUnit(t, "webd.service", "inactive")
	objs, _, _ := pageKind.List(ctx, e.Store, store.Query{})
	if len(objs) != 1 {
		t.Fatal("turning a module off keeps its resources")
	}
	if err := e.Runner.Reconcile(ctx, objs[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	e.RequireUnit(t, "webd.service", "inactive")

	// Turning it on again starts it.
	e.TurnOn(t, "web")
	e.RequireState(t, "web", modules.StateActive)
	e.Settle(t)
	e.RequireUnit(t, "webd.service", "active")

	// Observe hooks run in the background.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := slices.Clone(transitions)
		mu.Unlock()
		if len(got) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("transitions = %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDependenciesAndRules(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{NoVirtualization: true})
	e := modulestest.New(t, modulestest.Options{Machine: m}, newNet(), newWeb(), newNotes(), newShop(), newVM())

	// shop needs web or notes, and the person chooses which.
	if _, err := e.Manager.TurnOnRequest(context.Background(), "shop"); !errors.Is(err, modules.ErrDependency) {
		t.Errorf("shop without web or notes: %v", err)
	}
	e.TurnOn(t, "notes")
	e.TurnOn(t, "shop")
	if _, err := e.Submit(t, mustRequest(t)(e.Manager.TurnOffRequest("notes"))); !errors.Is(err, modules.ErrInUse) {
		t.Errorf("notes is needed by shop: %v", err)
	}
	e.TurnOn(t, "web")
	e.TurnOff(t, "notes")

	// Required modules stay on and nobody deletes a module.
	net, _ := e.Manager.Resource("net")
	if _, err := e.Submit(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpAction, Kind: "core.module", ID: net, Action: "turn_off"}}}); !errors.Is(err, modules.ErrRequired) {
		t.Errorf("turning net off: %v", err)
	}
	if _, err := e.Submit(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpDelete, Kind: "core.module", ID: net}}}); !errors.Is(err, hooks.ErrDenied) {
		t.Errorf("deleting a module: %v", err)
	}
	if _, err := e.Submit(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: "core.module", Scope: store.ServerScope(), Spec: modulestest.JSON(t, modules.ModuleSpec{Module: "ghost"})}}}); !errors.Is(err, hooks.ErrDenied) {
		t.Errorf("a person adding a module: %v", err)
	}

	// The VM module can't run without hardware virtualization.
	if _, err := e.Submit(t, mustRequest(t)(e.Manager.TurnOnRequest(context.Background(), "vm"))); !errors.Is(err, modules.ErrHardware) {
		t.Errorf("vm without virtualization: %v", err)
	}
	info, _ := e.Manager.Info(ctx, "vm")
	if len(info.HardwareProblems) == 0 {
		t.Error("the modules page says why")
	}

	// A module that can't keep its resources while off says so.
	e.TurnOn(t, "notes")
	e.MustApply(t, changeset.Request{Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: "notes.note", Scope: store.ServerScope(), Spec: modulestest.JSON(t, noteSpec{Text: "hi"})}}})
	e.RequireState(t, "notes", modules.StateActive)
	e.TurnOff(t, "shop")
	e.TurnOff(t, "web")
	if _, err := e.Submit(t, mustRequest(t)(e.Manager.TurnOffRequest("notes"))); !errors.Is(err, modules.ErrHasResources) {
		t.Errorf("turning notes off with a note: %v", err)
	}
}

// mustRequest fails the test when building a request failed.
func mustRequest(t testing.TB) func(changeset.Request, error) changeset.Request {
	return func(req changeset.Request, err error) changeset.Request {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
}

func TestRemoving(t *testing.T) {
	ctx := context.Background()
	e := modulestest.New(t, modulestest.Options{}, newNet(), newWeb())
	e.TurnOn(t, "web")
	e.MustApply(t, changeset.Request{Changes: []changeset.Change{page(t, "index.html", "Hi")}})
	e.RequireState(t, "web", modules.StateActive)

	if _, err := e.Manager.RemoveRequest(ctx, "web", "wbe", false); !errors.Is(err, modules.ErrConfirm) {
		t.Errorf("a typo in the confirmation: %v", err)
	}
	// The removal action on its own refuses to delete data it wasn't given.
	web, _ := e.Manager.Resource("web")
	if _, err := e.Submit(t, changeset.Request{Changes: []changeset.Change{{
		Op: changeset.OpAction, Kind: "core.module", ID: web, Action: "remove", Input: modulestest.JSON(t, modules.RemoveInput{Confirm: "web"}),
	}}}); !errors.Is(err, modules.ErrRemoveResources) {
		t.Errorf("removing without the deletes: %v", err)
	}
	req, err := e.Manager.RemoveRequest(ctx, "web", "Web", false)
	if err != nil {
		t.Fatal(err)
	}
	req.DryRun = true
	preview, err := e.Submit(t, req)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Snapshot && preview.Risk != store.RiskHigh {
		t.Errorf("removing is risky: %+v", preview)
	}
	removes := false
	for _, eff := range preview.Effects {
		removes = removes || (eff.Type == system.EffectPackage && eff.Action == "remove" && eff.Target == "webd")
	}
	if !removes || len(preview.Changes) != 2 {
		t.Errorf("the preview lists what is uninstalled: %+v", preview)
	}
	e.Remove(t, "web", false)
	if pkgs, _ := e.Machine.Backend().Packages.Installed(ctx, "webd"); len(pkgs) != 0 {
		t.Errorf("webd is still installed: %+v", pkgs)
	}
	obj, _ := modules.Kind.Get(ctx, e.Store, web)
	if !obj.Spec.Removed || !obj.Status.Uninstalled || obj.Status.State != modules.StateOff {
		t.Errorf("after removing = %+v %+v", obj.Spec, obj.Status)
	}
	// The resync uninstalls nothing again.
	before := len(e.Machine.Calls())
	if err := e.Runner.Reconcile(ctx, web, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.Machine.Calls()[before:] {
		if c.Area == "packages" && c.Op == "remove" {
			t.Errorf("the resync uninstalled again: %+v", c)
		}
	}
	// Turned on again, it is no longer removed, and installs its software
	// the first time something needs it.
	e.TurnOn(t, "web")
	if obj, _ := modules.Kind.Get(ctx, e.Store, web); obj.Spec.Removed || obj.Status.Uninstalled {
		t.Errorf("turned on again = %+v %+v", obj.Spec, obj.Status)
	}
	e.MustApply(t, changeset.Request{Changes: []changeset.Change{page(t, "index.html", "Back")}})
	e.RequireUnit(t, "webd.service", "active")
}

// Two modules naming the same software, services and ports: removing one
// leaves alone what the other still uses, and says so in the preview, and
// removing the last one uninstalls it.
func TestRemovingKeepsWhatOtherModulesUse(t *testing.T) {
	ctx := context.Background()
	e := modulestest.New(t, modulestest.Options{}, newNet(), newWeb(), newBlog())
	e.TurnOn(t, "web")
	e.TurnOn(t, "blog")
	e.MustApply(t, changeset.Request{Changes: []changeset.Change{page(t, "index.html", "Hi")}})
	e.RequireState(t, "web", modules.StateActive)
	installed := func() bool {
		pkgs, err := e.Machine.Backend().Packages.Installed(ctx, "webd")
		if err != nil {
			t.Fatal(err)
		}
		return len(pkgs) > 0
	}
	effects := func(id, confirm string) map[string]string {
		t.Helper()
		req, err := e.Manager.RemoveRequest(ctx, id, confirm, false)
		if err != nil {
			t.Fatal(err)
		}
		req.DryRun = true
		preview, err := e.Submit(t, req)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, eff := range preview.Effects {
			out[string(eff.Type)+" "+eff.Action+" "+eff.Target] = eff.Detail
		}
		return out
	}

	// Web still serves with webd, so removing the blog keeps it.
	got := effects("blog", "Blog")
	if got["package keep webd"] != "Web still uses it" || got["package remove webd"] != "" {
		t.Errorf("removing the blog = %v", got)
	}
	e.Remove(t, "blog", false)
	if !installed() {
		t.Fatal("removing the blog uninstalled what web uses")
	}
	e.RequireUnit(t, "webd.service", "active")

	// Turned off, web keeps its page and the software it needs for it.
	e.TurnOn(t, "blog")
	e.TurnOff(t, "web")
	if got := effects("blog", "Blog"); got["package keep webd"] != "Web still uses it" {
		t.Errorf("removing the blog while web keeps its page = %v", got)
	}
	e.TurnOn(t, "web")
	e.Settle(t)

	// The blog still serves with webd, so removing web keeps it running,
	// with its port open, and installed.
	got = effects("web", "Web")
	for _, want := range []string{"package keep webd", "service keep webd.service", "port keep tcp/443"} {
		if got[want] != "Blog still uses it" {
			t.Errorf("removing web doesn't keep %s: %v", want, got)
		}
	}
	if _, ok := got["service stop webd.service"]; ok {
		t.Errorf("removing web stops what the blog runs: %v", got)
	}
	e.Remove(t, "web", false)
	if !installed() {
		t.Fatal("removing web uninstalled what the blog uses")
	}
	e.RequireUnit(t, "webd.service", "active")

	// The last module that uses it takes it with it.
	if got := effects("blog", "Blog"); got["package keep webd"] != "" {
		t.Errorf("removing the last module that uses webd keeps it: %v", got)
	}
	e.Remove(t, "blog", false)
	if installed() {
		t.Error("webd is left behind once nothing uses it")
	}
}

// executor runs steps through the agent, and tells first what the module
// manager says of a module.
type executor struct {
	agent agent.Client
	seen  func()
}

func (x executor) Run(ctx context.Context, steps []agent.Step) ([]agent.StepResult, error) {
	x.seen()
	return agent.Execute(ctx, x.agent, steps, agent.CallOptions{}, nil)
}

// While a module's software is uninstalled the module is being removed,
// and while its services stop it is stopping.
func TestStatesOnTheWay(t *testing.T) {
	ctx := context.Background()
	e := modulestest.New(t, modulestest.Options{}, newNet(), newWeb())
	e.TurnOn(t, "web")
	e.MustApply(t, changeset.Request{Changes: []changeset.Change{page(t, "index.html", "Hi")}})
	e.RequireState(t, "web", modules.StateActive)
	id, _ := e.Manager.Resource("web")
	rec := e.Manager.Reconciler()
	facts, err := agent.GetFacts.Call(ctx, e.Agent, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		removed bool
		want    modules.State
	}{
		{"turning off", false, modules.StateStopping},
		{"removing", true, modules.StateRemoving},
	} {
		res, err := e.Store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		obj, err := modules.Kind.Decode(res)
		if err != nil {
			t.Fatal(err)
		}
		obj.Spec.Enabled, obj.Spec.Removed = false, c.removed
		planned, err := modules.Kind.Encode(obj)
		if err != nil {
			t.Fatal(err)
		}
		req := reconcile.Request{Resource: planned, Read: e.Store, Facts: facts}
		plan, err := rec.Plan(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var seen []modules.State
		if _, err := rec.Apply(ctx, req, plan, executor{agent: e.Agent, seen: func() { seen = append(seen, e.Manager.State("web")) }}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(seen) == 0 || seen[0] != c.want {
			t.Errorf("%s shows %v while it runs, not %s", c.name, seen, c.want)
		}
		stored, _ := modules.Kind.Get(ctx, e.Store, id)
		if e.Manager.State("web") != modules.StateOff {
			t.Errorf("after %s the module is %s (%s)", c.name, e.Manager.State("web"), stored.Status.State)
		}
		if c.removed && !strings.Contains(string(mustJSON(t, plan.Steps)), "RemovePackages") {
			t.Errorf("removing uninstalls: %s", mustJSON(t, plan.Steps))
		}
	}
}

// What the server said a module would install is kept in the module's
// status, which its plans read: a preview of turning it on and the apply
// that follows it plan the same however long apart, a restart of the
// panel between them included.
func TestTurnOnPreviewsPlanFromTheModule(t *testing.T) {
	ctx := context.Background()
	e := modulestest.New(t, modulestest.Options{}, newNet(), newWeb())
	req, err := e.Manager.TurnOnRequest(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	req.DryRun = true
	preview, err := e.Submit(t, req)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := e.Manager.Resource("web")
	obj, _ := modules.Kind.Get(ctx, e.Store, id)
	if len(obj.Status.Software) != 1 || obj.Status.Software[0].ID != "webd" || obj.Status.Software[0].DownloadBytes != 1<<20 || obj.Status.Software[0].Installed {
		t.Fatalf("software = %+v", obj.Status.Software)
	}

	// A manager that knows nothing of what the server said, as after a
	// restart, a day later, plans what the preview showed.
	later := modules.NewManager(e.Registry, modules.ManagerOptions{Services: e.Services, Now: func() time.Time { return time.Now().Add(24 * time.Hour) }})
	if err := later.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	facts, err := agent.GetFacts.Call(ctx, e.Agent, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obj.Spec.Enabled = true
	planned, _ := modules.Kind.Encode(obj)
	plan, err := later.Reconciler().Plan(ctx, reconcile.Request{Resource: planned, Read: e.Store, Facts: facts})
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(t, plan.Effects)) != string(mustJSON(t, preview.Effects)) {
		t.Errorf("planned later = %s\npreviewed = %s", mustJSON(t, plan.Effects), mustJSON(t, preview.Effects))
	}

	// Applying the preview finds it as it was.
	cs, err := e.Pipeline.Apply(ctx, changeset.ApplyRequest{Actor: e.Admin, ChangeSet: preview.ID})
	if err != nil {
		t.Fatalf("applying the preview: %v", err)
	}
	if cs.ID != preview.ID {
		t.Errorf("the preview became %s", cs.ID)
	}
	e.Settle(t)
	e.RequireState(t, "web", modules.StateOn)
}

// Turning a module on through the manager turns on what it needs with it,
// dependencies first.
func TestTurningOnWithWhatItNeeds(t *testing.T) {
	e := modulestest.New(t, modulestest.Options{}, newNet(), newWeb(), newNotes(), newShop())
	req, err := e.Manager.TurnOnRequest(context.Background(), "shop")
	if err == nil {
		t.Fatalf("shop needs web or notes, which the person chooses: %s", req.Title)
	}
	e.TurnOn(t, "notes")
	cs := e.TurnOn(t, "shop")
	if len(cs.Changes) != 1 {
		t.Errorf("only shop is turned on: %+v", cs.Changes)
	}
	e.TurnOff(t, "shop")
	e.TurnOff(t, "notes")
	req, err = e.Manager.TurnOnRequest(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Changes) != 1 || req.Title != "Turn on Web" {
		t.Errorf("web needs only net, which is on: %+v", req)
	}
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestStartFailures(t *testing.T) {
	broken := &starter{Base: modules.NewBase(manifest("flaky", "dependencies: {}\nupstream: []\n")), broken: true}
	e := modulestest.New(t, modulestest.Options{}, newNet(), broken)
	cs, err := e.Submit(t, mustRequest(t)(e.Manager.TurnOnRequest(context.Background(), "flaky")))
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != changeset.StateFailed || e.Manager.State("flaky") != modules.StateOff {
		t.Errorf("a module that can't start stays off: %s, %s", cs.State, e.Manager.State("flaky"))
	}
	if failed := e.Manager.Failed(); failed["flaky"] == nil {
		t.Errorf("failed = %v", failed)
	}
	broken.mu.Lock()
	broken.broken = false
	broken.mu.Unlock()
	if _, err := e.Pipeline.Undo(context.Background(), changeset.UndoRequest{Actor: e.Admin, ChangeSet: cs.ID}); err != nil {
		t.Fatal(err)
	}
	e.Settle(t)
	e.TurnOn(t, "flaky")
	e.TurnOff(t, "flaky")
	broken.mu.Lock()
	defer broken.mu.Unlock()
	if strings.Join(broken.log, ",") != "start,stop" {
		t.Errorf("lifecycle = %v", broken.log)
	}
}

func TestProfiles(t *testing.T) {
	m := sim.New(sim.Options{NoVirtualization: true})
	e := modulestest.New(t, modulestest.Options{Machine: m}, newNet(), newWeb(), newNotes(), newVM())
	req, skipped, err := e.Manager.ProfileRequest(context.Background(), modules.AllInOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].Module != "vm" {
		t.Errorf("skipped = %+v", skipped)
	}
	cs := e.MustApply(t, req)
	if !strings.HasPrefix(cs.Title, "Turn on the All-in-one profile") {
		t.Errorf("title = %s", cs.Title)
	}
	for _, id := range []string{"web", "notes"} {
		if !e.Manager.State(id).IsOn() {
			t.Errorf("%s = %s", id, e.Manager.State(id))
		}
	}
	if _, _, err := e.Manager.ProfileRequest(context.Background(), modules.AllInOne); !errors.Is(err, modules.ErrNothingToTurnOn) {
		t.Errorf("applying it twice: %v", err)
	}
	if _, _, err := e.Manager.ProfileRequest(context.Background(), "everything"); !errors.Is(err, modules.ErrUnknownProfile) {
		t.Errorf("an unknown profile: %v", err)
	}
}

// Turning a module on or off is a change set like any other: it is
// authorized for whoever asks, a preview changes nothing, and the audit
// log records it, and every refusal, with the change set.
func TestTransitionsAreAuditedChangeSets(t *testing.T) {
	ctx := context.Background()
	e := modulestest.New(t, modulestest.Options{}, newNet(), newWeb())
	owner := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "owner", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(store.NewID("acct"))}}}
	req, err := e.Manager.TurnOnRequest(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	req.Actor = owner
	if _, err := e.Submit(t, req); !i18n.ErrForbidden.Is(err) && !store.ErrNotFound.Is(err) {
		t.Fatalf("an account owner turning a module on: %v", err)
	}
	if e.Manager.Enabled("web") || e.Manager.State("web") != modules.StateOff {
		t.Fatal("a refused transition changed the module")
	}
	records := func(outcome events.Outcome) []events.AuditRecord {
		t.Helper()
		list, _, err := e.Events.QueryAudit(ctx, events.Query{Types: []string{"core.module.*"}, Outcome: outcome, Limit: store.MaxLimit})
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	if denied := records(events.OutcomeDenied); len(denied) != 1 || denied[0].Actor.ID != owner.ID || denied[0].Action != "core.module.turn_on" {
		t.Errorf("the refusal is recorded: %+v", denied)
	}

	req.Actor, req.DryRun = nil, true
	preview, err := e.Submit(t, req)
	if err != nil || !preview.DryRun || preview.State != changeset.StatePlanned || e.Manager.Enabled("web") {
		t.Fatalf("a preview of turning web on = %+v, %v, enabled %v", preview, err, e.Manager.Enabled("web"))
	}
	on := e.TurnOn(t, "web")
	off := e.TurnOff(t, "web")
	done := records(events.OutcomeSuccess)
	byChangeSet := map[store.ID]string{}
	for _, r := range done {
		byChangeSet[r.ChangeSet] = r.Action
	}
	if byChangeSet[on.ID] != "core.module.turn_on" || byChangeSet[off.ID] != "core.module.turn_off" {
		t.Errorf("turning web on and off is recorded with its change sets: %+v", done)
	}
}

func TestUsersCantAddModules(t *testing.T) {
	e := modulestest.New(t, modulestest.Options{}, newNet())
	owner := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(store.NewID("acct"))}}}
	net, _ := e.Manager.Resource("net")
	if _, err := e.Submit(t, changeset.Request{Actor: owner, Changes: []changeset.Change{{Op: changeset.OpAction, Kind: "core.module", ID: net, Action: "turn_off"}}}); err == nil {
		t.Error("an account owner can't turn modules on or off")
	}
}
