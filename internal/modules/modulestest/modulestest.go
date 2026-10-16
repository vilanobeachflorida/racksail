// Package modulestest runs modules in a whole panel kept in memory, against
// a simulated server, for their tests: the store, the change pipeline, the
// hook pipeline, the reconcilers and the module manager, wired as the panel
// wires them.
//
// Lifecycle is the test every module passes: it is turned on, used,
// turned off and removed, and afterwards nothing it installed, wrote or
// started is left on the server, except the data the person chose to keep.
//
//	func TestLifecycle(t *testing.T) {
//		modulestest.Lifecycle(t, &Module{Base: modules.NewBase(manifest)}, modulestest.Use{
//			Changes: func(e *modulestest.Env) []changeset.Change { return []changeset.Change{newZone(e, "example.com")} },
//			Check:   func(t *testing.T, e *modulestest.Env) { e.RequireUnit(t, "knot.service", "active") },
//		})
//	}
package modulestest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// Env is a panel and the simulated server it manages.
type Env struct {
	Machine  *sim.Machine
	Agent    agent.Client
	Store    *store.Memory
	Kinds    *store.KindRegistry
	Events   *events.Log
	Queue    *jobs.Manager
	Runner   *reconcile.Runner
	Pipeline *changeset.Engine
	Hooks    *hooks.Runner
	Registry *modules.Registry
	Manager  *modules.Manager
	Services *modules.Services
	// Admin is a server admin, who makes the changes.
	Admin *auth.Actor
}

// Options adjust the panel and the server.
type Options struct {
	// Machine is the simulated server; a new one when nil.
	Machine *sim.Machine
	// Kinds are core kinds the panel has besides the modules', such as
	// catalogs or notification channels.
	Kinds []store.Kind
	// Reconcilers builds the reconcilers of those kinds from the services,
	// once the store and the agent are there; it may fill in services too.
	Reconcilers func(svc *modules.Services) []reconcile.Reconciler
}

// New builds a panel with the given modules registered, and boots the
// module manager, which adds the modules' core.module resources and turns
// on the required ones.
func New(t testing.TB, opts Options, mods ...modules.Module) *Env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := modules.NewRegistry()
	kinds := store.NewKindRegistry()
	for _, k := range append([]store.Kind{modules.Kind}, opts.Kinds...) {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range mods {
		if err := reg.Add(m, kinds); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Err(); err != nil {
		t.Fatal(err)
	}
	machine := opts.Machine
	if machine == nil {
		machine = sim.New(sim.Options{})
	}
	client := agent.NewLocal(machine.Backend(), log)
	st := store.NewMemory(kinds)
	bus := events.NewMemory(10_000)
	queue := jobs.NewMemory(ctx, jobs.MemoryOptions{Workers: 2, Log: log})
	t.Cleanup(func() { queue.Stop(context.Background()) })
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	vault := secrets.NewMemory(box)

	hookReg := hooks.NewRegistry()
	if err := modules.DeclareHookPoints(hookReg, reg, kinds); err != nil {
		t.Fatal(err)
	}
	hookRunner := &hooks.Runner{Registry: hookReg, Events: bus, Audit: bus, Log: log}
	svc := &modules.Services{
		Store: st, Kinds: kinds, Jobs: queue, Events: bus, Audit: bus, Agent: client, Secrets: vault,
		Hooks: hookRunner, Authorizer: auth.NewRoleAuthorizer(nil), Log: log,
	}
	facts := func(ctx context.Context) (system.MachineFacts, error) {
		return agent.GetFacts.Call(ctx, client, agent.Empty{}, agent.CallOptions{})
	}
	runner := &reconcile.Runner{
		Store: st, Agent: client, Registry: reconcile.NewRegistry(), Secrets: vault, Events: bus, Log: log,
		Facts: func(ctx context.Context, server store.ID) (system.MachineFacts, error) { return facts(ctx) },
	}
	manager := modules.NewManager(reg, modules.ManagerOptions{Facts: facts, Services: svc, Enqueue: runner.Enqueue, Log: log})
	svc.Modules = manager
	if err := runner.Registry.Register(manager.Reconciler()); err != nil {
		t.Fatal(err)
	}
	for _, m := range reg.All() {
		if rp, ok := m.(modules.ReconcilerProvider); ok {
			for _, r := range rp.Reconcilers() {
				if err := runner.Registry.Register(manager.Gate(r)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if opts.Reconcilers != nil {
		for _, r := range opts.Reconcilers(svc) {
			if err := runner.Registry.Register(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	pipeline, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: queue, Events: bus, Audit: bus, Hooks: hookRunner,
		Authorizer: svc.Authorizer, Secrets: vault, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Pipeline = pipeline
	for _, h := range manager.Hooks() {
		if err := hookReg.Register(h); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range reg.All() {
		if hp, ok := m.(modules.HookProvider); ok {
			for _, h := range hp.Hooks(svc) {
				if err := hookReg.Register(h); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	e := &Env{
		Machine: machine, Agent: client, Store: st, Kinds: kinds, Events: bus, Queue: queue, Runner: runner, Pipeline: pipeline,
		Hooks: hookRunner, Registry: reg, Manager: manager, Services: svc,
		Admin: &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "admin", Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}},
	}
	if err := manager.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	queue.Start()
	go manager.Run(ctx)
	e.Settle(t)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	return e
}

// Settle waits until no job is queued or running, so what the last change
// set set off is done.
func (e *Env) Settle(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		busy := false
		for _, state := range []jobs.State{jobs.StateQueued, jobs.StateRunning} {
			list, _, err := e.Queue.List(ctx, jobs.Filter{State: state, Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			busy = busy || len(list) > 0
		}
		if !busy {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("jobs are still running")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Submit sends a request through the change pipeline as the admin and
// waits for its job. It returns the change set as it ended, or the error
// that stopped it from being planned.
func (e *Env) Submit(t testing.TB, req changeset.Request) (*changeset.ChangeSet, error) {
	t.Helper()
	if req.Actor == nil {
		req.Actor = e.Admin
	}
	cs, err := e.Pipeline.Submit(context.Background(), req)
	if err != nil || req.DryRun {
		return cs, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Queue.Wait(ctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	final, err := e.Pipeline.Get(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return final, nil
}

// MustApply submits a request and fails the test unless its change set
// was applied.
func (e *Env) MustApply(t testing.TB, req changeset.Request) *changeset.ChangeSet {
	t.Helper()
	cs, err := e.Submit(t, req)
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != changeset.StateApplied {
		t.Fatalf("%s: the change set ended %s: %s", cs.Title, cs.State, cs.Error)
	}
	e.Settle(t)
	return cs
}

// TurnOn turns a module on, with the modules it needs.
func (e *Env) TurnOn(t testing.TB, id string) *changeset.ChangeSet {
	t.Helper()
	req, err := e.Manager.TurnOnRequest(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return e.MustApply(t, req)
}

// TurnOff turns a module off.
func (e *Env) TurnOff(t testing.TB, id string) *changeset.ChangeSet {
	t.Helper()
	req, err := e.Manager.TurnOffRequest(id)
	if err != nil {
		t.Fatal(err)
	}
	return e.MustApply(t, req)
}

// Remove removes a module, deleting its resources unless keepData is set.
func (e *Env) Remove(t testing.TB, id string, keepData bool) *changeset.ChangeSet {
	t.Helper()
	req, err := e.Manager.RemoveRequest(context.Background(), id, id, keepData)
	if err != nil {
		t.Fatal(err)
	}
	return e.MustApply(t, req)
}

// RequireState fails the test unless a module is in a state.
func (e *Env) RequireState(t testing.TB, id string, want modules.State) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if got := e.Manager.State(id); got == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s is %s, not %s", id, e.Manager.State(id), want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// RequireUnit fails the test unless a unit is in an active state, such as
// active or inactive.
func (e *Env) RequireUnit(t testing.TB, name, activeState string) {
	t.Helper()
	st, err := e.Machine.Backend().Units.Status(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != activeState {
		t.Fatalf("%s is %s, not %s", name, st.ActiveState, activeState)
	}
}

// Footprint is what a module can leave on a server: installed packages,
// apt sources, units, users and groups, and files under Racksail's roots.
type Footprint struct {
	Packages []string
	Sources  []string
	Units    []string
	Users    []string
	Groups   []string
	Files    []string
}

// managedRoots are where modules write.
var managedRoots = []string{system.ConfigDir, system.StateDir, system.RuntimesDir, system.ModulesDir, "/srv/racksail", "/etc/systemd/system"}

// Footprint records what is on the server now. packages are the Debian
// packages to look for, such as everything the module's manifest names.
func (e *Env) Footprint(t testing.TB, packages ...string) Footprint {
	t.Helper()
	ctx := context.Background()
	b := e.Machine.Backend()
	var fp Footprint
	if len(packages) > 0 {
		installed, err := b.Packages.Installed(ctx, packages...)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range installed {
			fp.Packages = append(fp.Packages, p.Name)
		}
	}
	sources, err := b.Packages.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fp.Sources = sources
	units, err := b.Units.List(ctx, "*")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if u.LoadState != "not-found" {
			fp.Units = append(fp.Units, u.Name+" "+u.ActiveState)
		}
	}
	users, err := b.Users.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		fp.Users = append(fp.Users, u.Name)
	}
	groups, err := b.Users.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		fp.Groups = append(fp.Groups, g.Name)
	}
	for _, root := range managedRoots {
		fp.Files = append(fp.Files, walk(ctx, b.Files, root)...)
	}
	for _, list := range [][]string{fp.Packages, fp.Sources, fp.Units, fp.Users, fp.Groups, fp.Files} {
		slices.Sort(list)
	}
	return fp
}

func walk(ctx context.Context, files system.Files, dir string) []string {
	entries, err := files.ReadDir(ctx, dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Path)
		if e.Mode.IsDir() {
			out = append(out, walk(ctx, files, e.Path)...)
		}
	}
	return out
}

// Leftovers lists what is in after but not in before, leaving out paths
// under kept.
func Leftovers(before, after Footprint, kept ...string) []string {
	var out []string
	diff := func(what string, a, b []string, skip func(string) bool) {
		for _, x := range b {
			if !slices.Contains(a, x) && !skip(x) {
				out = append(out, what+" "+x)
			}
		}
	}
	never := func(string) bool { return false }
	diff("package", before.Packages, after.Packages, never)
	diff("apt source", before.Sources, after.Sources, never)
	diff("unit", before.Units, after.Units, never)
	diff("user", before.Users, after.Users, never)
	diff("group", before.Groups, after.Groups, never)
	diff("file", before.Files, after.Files, func(p string) bool {
		for _, k := range kept {
			if p == k || strings.HasPrefix(p, k+"/") || strings.HasPrefix(k, p+"/") {
				return true
			}
		}
		return false
	})
	return out
}

// Use says how a module is used in its lifecycle test.
type Use struct {
	// Changes create what uses the module, such as a site for the web
	// module. They are applied as one change set once the module is on.
	Changes func(e *Env) []changeset.Change
	// Check verifies the module works while it is used, such as a service
	// answering. It may be nil.
	Check func(t *testing.T, e *Env)
	// Packages are the Debian packages the module may install, which are
	// checked for after removal; the ones its manifest names exactly are
	// checked anyway.
	Packages []string
	// KeepData removes the module keeping its data, and Kept lists the
	// paths that may remain then.
	KeepData bool
	Kept     []string
}

// Lifecycle is the on → use → off → remove test every module passes. The
// module is turned on and installs nothing; it is used, which makes it
// active and installs what the use needs; it is turned off, which must
// work if the module keeps resources while off, and is skipped otherwise;
// and it is removed. Then nothing it installed, wrote or started may be
// left on the server, except the data the test keeps. Modules the module
// needs are registered with deps.
func Lifecycle(t *testing.T, mod modules.Module, use Use, deps ...modules.Module) *Env {
	t.Helper()
	e := New(t, Options{}, append(deps, mod)...)
	id := mod.Manifest().ID
	var packages []string
	for _, u := range mod.Manifest().Upstream {
		for _, p := range u.Source.Packages {
			if !strings.ContainsAny(p, "*?[") {
				packages = append(packages, p)
			}
		}
	}
	packages = append(packages, use.Packages...)
	before := e.Footprint(t, packages...)

	on := e.TurnOn(t, id)
	e.RequireState(t, id, modules.StateOn)
	for _, eff := range on.Effects {
		if (eff.Type == system.EffectPackage || eff.Type == system.EffectDownload) && !modules.Later(eff) {
			t.Errorf("turning %s on installed %s %s; software is installed the first time something needs it", id, eff.Action, eff.Target)
		}
	}
	if left := Leftovers(before, e.Footprint(t, packages...)); len(left) > 0 {
		t.Errorf("turning %s on changed the server: %v", id, left)
	}

	if use.Changes != nil {
		e.MustApply(t, changeset.Request{Title: "Use " + id, Changes: use.Changes(e)})
		e.RequireState(t, id, modules.StateActive)
		if use.Check != nil {
			use.Check(t, e)
		}
	}

	info, err := e.Manager.Info(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Usage.Resources == 0 || info.Suspends {
		e.TurnOff(t, id)
		e.RequireState(t, id, modules.StateOff)
		e.TurnOn(t, id)
		if use.Check != nil && use.Changes != nil {
			e.RequireState(t, id, modules.StateActive)
			e.Settle(t)
			use.Check(t, e)
		}
	}

	e.Remove(t, id, use.KeepData)
	e.RequireState(t, id, modules.StateOff)
	if left := Leftovers(before, e.Footprint(t, packages...), use.Kept...); len(left) > 0 {
		t.Errorf("removing %s left things behind: %v", id, left)
	}
	if !use.KeepData {
		for _, kind := range e.Manager.KindsOf(id) {
			page, err := e.Store.List(context.Background(), store.Query{Kind: kind, IncludeDeleted: true, Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > 0 {
				t.Errorf("removing %s left %s resources", id, kind)
			}
		}
	}
	return e
}

// JSON encodes v for a change's spec, failing the test when it can't.
func JSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
