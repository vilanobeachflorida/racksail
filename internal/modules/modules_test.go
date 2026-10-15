package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	m := NewManager(newRegistry(t), facts(false))
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
	m := NewManager(newRegistry(t), facts(false))
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
	capable := NewManager(newRegistry(t), facts(true))
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
