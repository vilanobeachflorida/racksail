package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/agent"
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
	upstream := "upstream: []\n"
	if strings.Contains(extra, "upstream:") {
		upstream = ""
	}
	return []byte(hardware + upstream + `
id: ` + id + `
name: ` + strings.ToUpper(id[:1]) + id[1:] + `
description: A test module.
version: 0.1.0
license: AGPL-3.0-or-later
category: operations
profiles: [all-in-one]
cost: {idleMemoryMB: 1, activeMemoryMB: 2, diskMB: 3}
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
	m := NewManager(reg, ManagerOptions{})
	if id, ok := m.ModuleOf("web.site"); !ok || id != "web" || len(m.KindsOf("web")) != 1 {
		t.Errorf("kinds of web = %v", m.KindsOf("web"))
	}
}

func TestHardwareProblems(t *testing.T) {
	man := &Manifest{Hardware: Hardware{Arch: []string{"amd64"}, MinMemoryMB: 2048, MinDiskGB: 100, Features: []string{"kvm", "iommu", "tpm", "uefi"}}}
	problems := HardwareProblems(man, system.MachineFacts{Arch: "arm64", MemoryBytes: 1 << 30, Disks: []system.DiskFact{{SizeBytes: 20 << 30}}})
	if len(problems) != 7 {
		t.Errorf("problems = %v", problems)
	}
}

func TestProfilesAndOrder(t *testing.T) {
	m := NewManager(newRegistry(t), ManagerOptions{})
	order := m.ProfileModules(AllInOne)
	if order[0] != "net" || len(order) != 5 {
		t.Errorf("dependencies come first: %v", order)
	}
	if len(Profiles()) != 7 || !ValidProfile(Minimal) || ValidProfile("everything") {
		t.Error("there are seven profiles")
	}
	if got := dependencyOrder(m.reg, []string{"perf", "web", "net"}); strings.Join(got, ",") != "net,web,perf" {
		t.Errorf("order = %v", got)
	}
	if m.State("net") != StateOff || !m.Enabled("net") || m.Enabled("web") {
		t.Error("before boot, only required modules are meant to be on")
	}
}

func TestSettings(t *testing.T) {
	man, err := ParseManifest(manifestYAML("mail", `dependencies: {}
settings:
  type: object
  additionalProperties: false
  properties:
    relayHost: {type: string, format: hostname}
    greylisting: {type: boolean}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := man.ValidateSettings(map[string]any{"relayHost": "smtp.example.com", "greylisting": true}); err != nil {
		t.Errorf("valid settings: %v", err)
	}
	if err := man.ValidateSettings(map[string]any{"greylisting": "yes"}); !errors.Is(err, ErrSettings) {
		t.Errorf("a wrong type: %v", err)
	}
	if err := man.ValidateSettings(map[string]any{"colour": "blue"}); !errors.Is(err, ErrSettings) {
		t.Errorf("an unknown setting: %v", err)
	}
	plain, _ := ParseManifest(manifestYAML("dns", "dependencies: {}\n"))
	if err := plain.ValidateSettings(map[string]any{"x": 1}); !errors.Is(err, ErrSettings) {
		t.Errorf("a module without settings takes none: %v", err)
	}
}

func TestInstallAndUninstallSteps(t *testing.T) {
	ctx := context.Background()
	man, err := ParseManifest(manifestYAML("web", `dependencies: {}
upstream:
  - {id: php, name: PHP, license: PHP-3.01, source: {type: apt-repository, packages: ["php*-fpm", "php*-cli"], repository: "https://packages.sury.org/php/"}, install: When a site uses it.}
  - {id: apache2, name: Apache, license: Apache-2.0, source: {type: debian, packages: [apache2]}, install: For .htaccess sites.}
  - {id: nodejs, name: Node.js, license: MIT, source: {type: vendor-archive, url: "https://nodejs.org/dist/"}, install: When a Node site needs it., downloadMB: 30}
  - {id: zfs, name: ZFS, license: CDDL-1.0, source: {type: debian, packages: [zfs-dkms]}, install: When asked for., optIn: true}
  - {id: caddy, name: Caddy, license: Apache-2.0, source: {type: embedded}, install: Built in.}
`))
	if err != nil {
		t.Fatal(err)
	}
	steps, err := InstallSteps(ctx, nil, man, "php", Install{Packages: []string{"php8.4-fpm"}})
	if err != nil || len(steps) != 2 || steps[0].Op != "EnsureAptSource" || steps[1].Op != "InstallPackages" {
		t.Errorf("php = %+v, %v", steps, err)
	}
	if _, err := InstallSteps(ctx, nil, man, "php", Install{Packages: []string{"nginx"}}); !errors.Is(err, ErrNotUpstream) {
		t.Errorf("a package the manifest doesn't name: %v", err)
	}
	if steps, err := InstallSteps(ctx, nil, man, "apache2", Install{}); err != nil || len(steps) != 1 {
		t.Errorf("apache = %+v, %v", steps, err)
	}
	archive := &system.Archive{URL: "https://nodejs.org/dist/v22.11.0/node-v22.11.0-linux-x64.tar.xz", SHA256: strings.Repeat("a", 64), Size: 30 << 20, Format: "tar.xz", StripComponents: 1}
	steps, err = InstallSteps(ctx, nil, man, "nodejs", Install{Version: "22.11.0", Archive: archive})
	if err != nil || len(steps) != 1 || steps[0].Input.(agent.InstallArchiveInput).Archive.Dest != "/opt/racksail/runtimes/nodejs/22.11.0" {
		t.Errorf("node = %+v, %v", steps, err)
	}
	if _, err := InstallSteps(ctx, nil, man, "nodejs", Install{Version: "../../etc", Archive: archive}); !errors.Is(err, ErrNotUpstream) {
		t.Errorf("a version that isn't one: %v", err)
	}
	if _, err := InstallSteps(ctx, nil, man, "zfs", Install{}); !errors.Is(err, ErrOptIn) {
		t.Errorf("opt-in software needs the admin's go-ahead: %v", err)
	}
	if steps, err := InstallSteps(ctx, nil, man, "caddy", Install{}); err != nil || len(steps) != 0 {
		t.Errorf("embedded software installs nothing: %+v, %v", steps, err)
	}
	plan := UninstallPlan(man, Shared{})
	var ops []string
	for _, s := range plan.Steps {
		ops = append(ops, s.Op)
	}
	if strings.Join(ops, ",") != "RemoveAptSource,RemovePackages,RemovePath,RemovePackages" {
		t.Errorf("uninstall = %v", ops)
	}
}

func TestDeclareHookPoints(t *testing.T) {
	reg := newRegistry(t)
	reg.Add(testModule{NewBase(manifestYAML("edge", "dependencies: {}\nhooks: [{point: system.caddy.config, description: Caddy's configuration.}]\n"))}, nil)
	kinds := store.NewKindRegistry()
	if err := kinds.Register(Kind); err != nil {
		t.Fatal(err)
	}
	hr := hooks.NewRegistry()
	if err := DeclareHookPoints(hr, reg, kinds); err != nil {
		t.Fatal(err)
	}
	for _, p := range []hooks.Point{"modules.web.on", "modules.vm.off", "system.caddy.config", "core.module.turn_on.validate", "core.module.remove.applied"} {
		if !hr.Known(p) {
			t.Errorf("%s isn't declared", p)
		}
	}
}
