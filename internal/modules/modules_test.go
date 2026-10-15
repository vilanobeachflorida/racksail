package modules

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

func manifestYAML(id string, extra string) []byte {
	hardware := "hardware: {arch: [amd64, arm64]}\n"
	if strings.Contains(extra, "hardware:") {
		hardware = ""
	}
	return []byte(hardware + `
id: ` + id + `
name: ` + strings.ToUpper(id[:1]) + id[1:] + `
description: A test module.
version: 0.1.0
license: AGPL-3.0-or-later
category: operations
profiles: [all-in-one]
cost: {idleMemoryMB: 1, activeMemoryMB: 2, diskMB: 3}
upstream: []
` + extra)
}

type testModule struct{ Base }

type siteSpec struct{}

type withKinds struct {
	Base
	kinds []store.Kind
}

func (w withKinds) Kinds() []store.Kind { return w.kinds }

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry()
	kinds := store.NewKindRegistry()
	for _, m := range []Module{
		testModule{NewBase(manifestYAML("net", "required: true\ndependencies: {}\n"))},
		testModule{NewBase(manifestYAML("web", "dependencies: {requires: [net]}\n"))},
		testModule{NewBase(manifestYAML("db", "dependencies: {requires: [net]}\n"))},
		testModule{NewBase(manifestYAML("perf", "dependencies: {requiresAny: [web, db]}\n"))},
		testModule{NewBase(manifestYAML("vm", "dependencies: {requires: [net]}\nhardware: {arch: [amd64, arm64], features: [virtualization]}\n"))},
	} {
		if err := reg.Add(m, kinds); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// resellers stands in for the hosting module, which keeps resellers.
type resellers struct {
	Base
	owners map[store.ID]store.ID
}

func (r resellers) Ownership(svc *Services) auth.Ownership { return r }

func (r resellers) ResellerOf(ctx context.Context, account store.ID) (store.ID, error) {
	return r.owners[account], nil
}

func TestOwnership(t *testing.T) {
	reg := newRegistry(t)
	if o, err := reg.Ownership(&Services{}); o != nil || err != nil {
		t.Errorf("without a module that keeps resellers = %v, %v", o, err)
	}
	reseller, customer := store.NewID("rsl"), store.NewID("acct")
	hosting := resellers{Base: NewBase(manifestYAML("hosting", "dependencies: {}\n")), owners: map[store.ID]store.ID{customer: reseller}}
	if err := reg.Add(hosting, nil); err != nil {
		t.Fatal(err)
	}
	o, err := reg.Ownership(&Services{})
	if err != nil || o == nil {
		t.Fatalf("ownership = %v, %v", o, err)
	}
	if got, _ := o.ResellerOf(context.Background(), customer); got != reseller {
		t.Errorf("reseller of the customer = %s", got)
	}
	if err := reg.Add(resellers{Base: NewBase(manifestYAML("billing", "dependencies: {}\n"))}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Ownership(&Services{}); err == nil || !strings.Contains(err.Error(), "billing and hosting") {
		t.Errorf("two modules saying it: %v", err)
	}
}

func TestRegistry(t *testing.T) {
	reg := newRegistry(t)
	if err := reg.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(reg.IDs(), ","); got != "db,net,perf,vm,web" {
		t.Errorf("ids = %s", got)
	}
	if err := reg.Add(testModule{NewBase(manifestYAML("web", "dependencies: {}\n"))}, nil); err == nil {
		t.Error("a duplicate module must be refused")
	}
	if err := reg.Add(testModule{NewBase([]byte("id: Broken"))}, nil); err == nil {
		t.Error("a broken manifest must be refused")
	}
	reg.Add(testModule{NewBase(manifestYAML("lonely", "dependencies: {requires: [ghost]}\n"))}, nil)
	if err := reg.Err(); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown dependencies should be reported: %v", err)
	}
}

func TestKindsBelongToTheirModule(t *testing.T) {
	kinds := store.NewKindRegistry()
	good := store.Define[siteSpec, store.Status](store.KindInfo{Name: "web.site", Prefix: "site", Scope: store.ScopeAccount, Title: "Site", Plural: "sites", Description: "A site."})
	foreign := store.Define[siteSpec, store.Status](store.KindInfo{Name: "mail.box", Prefix: "mbox", Scope: store.ScopeAccount, Title: "Box", Plural: "boxes", Description: "A box."})
	reg := NewRegistry()
	if err := reg.Add(withKinds{NewBase(manifestYAML("web", "dependencies: {}\n")), []store.Kind{good}}, kinds); err != nil {
		t.Fatal(err)
	}
	if _, ok := kinds.Get("web.site"); !ok {
		t.Error("the module's kind should be registered")
	}
	if err := reg.Add(withKinds{NewBase(manifestYAML("other", "dependencies: {}\n")), []store.Kind{foreign}}, kinds); err == nil {
		t.Error("a module can't declare another module's kinds")
	}
}

func facts(virt bool) func(context.Context) (system.MachineFacts, error) {
	return func(context.Context) (system.MachineFacts, error) {
		return system.MachineFacts{Arch: "amd64", MemoryBytes: 4 << 30, CPU: system.CPUInfo{Virtualization: virt}, KVM: virt}, nil
	}
}

func TestManagerRules(t *testing.T) {
	ctx := context.Background()
	m := NewManager(newRegistry(t), ManagerOptions{Facts: facts(false)})
	if m.State("net") != StateOn || m.State("web") != StateOff {
		t.Errorf("states = %v", m.States())
	}
	if err := m.TurnOn(ctx, "perf"); !errors.Is(err, ErrDependency) {
		t.Errorf("perf without web or db: %v", err)
	}
	if err := m.TurnOn(ctx, "db"); err != nil {
		t.Fatal(err)
	}
	if err := m.TurnOn(ctx, "perf"); err != nil {
		t.Fatalf("perf with db on: %v", err)
	}
	if err := m.TurnOff(ctx, "db"); !errors.Is(err, ErrInUse) {
		t.Errorf("db is needed by perf: %v", err)
	}
	if err := m.TurnOn(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if err := m.TurnOff(ctx, "db"); err != nil {
		t.Errorf("perf still has web: %v", err)
	}
	if err := m.TurnOff(ctx, "net"); !errors.Is(err, ErrRequired) {
		t.Errorf("net is required: %v", err)
	}
	if err := m.TurnOn(ctx, "vm"); !errors.Is(err, ErrHardware) {
		t.Errorf("vm without virtualization: %v", err)
	}
	if err := m.TurnOn(ctx, "nope"); !errors.Is(err, ErrUnknownModule) {
		t.Errorf("unknown module: %v", err)
	}
}

func TestApplyProfileSkipsUnsupportedHardware(t *testing.T) {
	ctx := context.Background()
	m := NewManager(newRegistry(t), ManagerOptions{Facts: facts(false)})
	order := m.ProfileModules(AllInOne)
	if order[0] != "net" {
		t.Errorf("dependencies come first: %v", order)
	}
	skipped, err := m.ApplyProfile(ctx, AllInOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0] != "vm" {
		t.Errorf("skipped = %v", skipped)
	}
	if m.State("perf") != StateOn || m.State("web") != StateOn {
		t.Errorf("states = %v", m.States())
	}
	capable := NewManager(newRegistry(t), ManagerOptions{Facts: facts(true)})
	if skipped, _ := capable.ApplyProfile(ctx, AllInOne); len(skipped) != 0 {
		t.Errorf("a capable machine skips nothing: %v", skipped)
	}
	if len(Profiles()) != 7 {
		t.Error("there are seven profiles")
	}
}

func TestHardwareProblems(t *testing.T) {
	man := &Manifest{Hardware: Hardware{Arch: []string{"amd64"}, MinMemoryMB: 2048, Features: []string{"kvm", "iommu", "tpm", "uefi"}}}
	problems := HardwareProblems(man, system.MachineFacts{Arch: "arm64", MemoryBytes: 1 << 30})
	if len(problems) != 6 {
		t.Errorf("problems = %v", problems)
	}
}

// lifecycleModule records when it is started and stopped.
type lifecycleModule struct {
	Base
	log     *[]string
	failing bool
}

func (l lifecycleModule) Start(ctx context.Context, svc *Services) error {
	if l.failing {
		return errors.New("the socket is taken")
	}
	*l.log = append(*l.log, "start "+l.Manifest().ID)
	return nil
}

func (l lifecycleModule) Stop(ctx context.Context) error {
	*l.log = append(*l.log, "stop "+l.Manifest().ID)
	return nil
}

func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	var log []string
	reg := NewRegistry()
	for _, m := range []Module{
		lifecycleModule{Base: NewBase(manifestYAML("net", "required: true\ndependencies: {}\n")), log: &log},
		lifecycleModule{Base: NewBase(manifestYAML("web", "dependencies: {requires: [net]}\n")), log: &log},
		lifecycleModule{Base: NewBase(manifestYAML("broken", "dependencies: {}\n")), log: &log, failing: true},
	} {
		if err := reg.Add(m, nil); err != nil {
			t.Fatal(err)
		}
	}
	hookReg := hooks.NewRegistry()
	for _, id := range reg.IDs() {
		for _, p := range hooks.ModulePoints(id) {
			hookReg.Declare(p, "lifecycle")
		}
	}
	transitions := make(chan Transition, 4)
	for _, p := range []hooks.Point{hooks.ModuleOn("web"), hooks.ModuleOff("web")} {
		if err := hookReg.Register(hooks.Registration{Point: p, Kind: hooks.Observe, Owner: "module:monitoring",
			OnObserve: func(ctx context.Context, c *hooks.Context) { transitions <- c.Data.(Transition) }}); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(reg, ManagerOptions{Services: &Services{Hooks: &hooks.Runner{Registry: hookReg}}})
	next := func() Transition {
		select {
		case tr := <-transitions:
			return tr
		case <-time.After(time.Second):
			t.Fatal("no lifecycle notification")
			return Transition{}
		}
	}

	if err := m.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.TurnOn(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if tr := next(); tr != (Transition{Module: "web", From: StateOff, To: StateOn}) {
		t.Errorf("on = %+v", tr)
	}
	if err := m.TurnOn(ctx, "web"); err != nil {
		t.Errorf("turning on twice is harmless: %v", err)
	}
	if err := m.TurnOn(ctx, "broken"); !errors.Is(err, ErrStartFailed) {
		t.Errorf("a module that can't start: %v", err)
	}
	if m.State("broken") != StateOff {
		t.Error("a module that didn't start stays off")
	}
	if err := m.TurnOff(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if tr := next(); tr.To != StateOff || tr.From != StateOn {
		t.Errorf("off = %+v", tr)
	}
	if err := m.TurnOn(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	next()
	m.Shutdown(ctx)
	want := "start net,start web,stop web,start web,stop web,stop net"
	if got := strings.Join(log, ","); got != want {
		t.Errorf("lifecycle = %s, want %s", got, want)
	}
	if m.State("web") != StateOn {
		t.Error("stopping the panel doesn't turn modules off")
	}

	m.mu.Lock()
	m.states["broken"] = StateActive
	m.mu.Unlock()
	if err := m.Boot(ctx); !errors.Is(err, ErrStartFailed) {
		t.Errorf("boot reports modules that didn't start: %v", err)
	}
	if failed := m.Failed(); failed["broken"] == nil || len(failed) != 1 {
		t.Errorf("failed = %v", failed)
	}
}
