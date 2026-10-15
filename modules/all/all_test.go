package all

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/spec"
)

func TestEveryModuleIsRegistered(t *testing.T) {
	reg := modules.Default()
	if err := reg.Err(); err != nil {
		t.Fatal(err)
	}
	got := reg.IDs()
	want := slices.Clone(IDs)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("registered modules = %v, want %v", got, want)
	}
	if len(IDs) != 22 {
		t.Errorf("there are %d modules, modules.md lists 22", len(IDs))
	}
}

func TestManifestsMatchTheSchema(t *testing.T) {
	for _, id := range IDs {
		raw, err := os.ReadFile(filepath.Join("..", id, "manifest.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if err := spec.ValidateYAML(spec.Module, raw); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		m, ok := modules.Default().Get(id)
		if !ok {
			t.Errorf("%s isn't registered", id)
			continue
		}
		man := m.Manifest()
		if man.ID != id {
			t.Errorf("modules/%s declares id %q", id, man.ID)
		}
		if man.Cost.ActiveMemoryMB < man.Cost.IdleMemoryMB {
			t.Errorf("%s: active memory is below idle memory", id)
		}
		for _, u := range man.Upstream {
			if u.OptIn && u.Source.Type == "embedded" {
				t.Errorf("%s: embedded software can't be opt-in", id)
			}
		}
	}
}

// The profiles and dependencies tables of docs/architecture/modules.md.
func TestProfilesAndDependencies(t *testing.T) {
	profiles := map[modules.Profile][]string{
		modules.AllInOne:           {"network", "web", "hosting", "databases", "dns", "mail", "containers", "vms", "storage", "shares", "marketplace", "backups", "monitoring", "files", "performance", "automations"},
		modules.WebHosting:         {"network", "web", "hosting", "databases", "dns", "mail", "marketplace", "backups", "monitoring", "files", "performance", "automations"},
		modules.HomeServer:         {"network", "storage", "shares", "containers", "vms", "home", "marketplace", "backups", "monitoring", "files", "automations"},
		modules.VirtualizationHost: {"network", "vms", "containers", "storage", "backups", "monitoring", "files"},
		modules.MailServer:         {"network", "mail", "dns", "backups", "monitoring"},
		modules.DNSServer:          {"network", "dns", "backups", "monitoring"},
		modules.Minimal:            {"network"},
	}
	mgr := modules.NewManager(modules.Default(), modules.ManagerOptions{})
	for p, want := range profiles {
		got := mgr.ProfileModules(p)
		slices.Sort(got)
		w := slices.Clone(want)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("profile %s = %v, want %v", p, got, w)
		}
	}
	requires := map[string][]string{
		"web": {"network"}, "hosting": {"web"}, "databases": {"network"}, "dns": {"network"}, "mail": {"network"},
		"containers": {"network"}, "vms": {"network"}, "shares": {"storage"}, "home": {"network"}, "builder": {"web"},
	}
	for _, id := range IDs {
		m, _ := modules.Default().Get(id)
		man := m.Manifest()
		if !slices.Equal(man.Dependencies.Requires, requires[id]) {
			t.Errorf("%s requires %v, want %v", id, man.Dependencies.Requires, requires[id])
		}
		if man.Required != (id == "network") {
			t.Errorf("%s: only network is required", id)
		}
	}
	perf, _ := modules.Default().Get("performance")
	if !slices.Equal(perf.Manifest().Dependencies.RequiresAny, []string{"web", "databases"}) {
		t.Error("performance needs web or databases")
	}
	mail, _ := modules.Default().Get("mail")
	if !slices.Equal(mail.Manifest().Dependencies.Uses, []string{"dns", "web"}) {
		t.Error("mail uses dns and web when they are on")
	}
}

func TestVMsNeedVirtualization(t *testing.T) {
	vms, _ := modules.Default().Get("vms")
	if problems := modules.HardwareProblems(vms.Manifest(), system.MachineFacts{Arch: "amd64", CPU: system.CPUInfo{Virtualization: false}}); len(problems) == 0 {
		t.Error("the VMs module must refuse machines without hardware virtualization")
	}
	if problems := modules.HardwareProblems(vms.Manifest(), system.MachineFacts{Arch: "amd64", MemoryBytes: 8 << 30, CPU: system.CPUInfo{Virtualization: true}, KVM: true}); len(problems) != 0 {
		t.Errorf("a capable machine was refused: %v", problems)
	}
}

// A module's recipes are the files its Recipes returns, and its manifest
// lists exactly their ids.
func TestShippedRecipesMatchTheManifests(t *testing.T) {
	for _, id := range IDs {
		m, _ := modules.Default().Get(id)
		declared := slices.Clone(m.Manifest().Recipes)
		slices.Sort(declared)
		var shipped []string
		if rp, ok := m.(modules.RecipeProvider); ok {
			err := fs.WalkDir(rp.Recipes(), ".", func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
					return err
				}
				data, err := fs.ReadFile(rp.Recipes(), path)
				if err != nil {
					return err
				}
				r, err := recipes.Parse(data)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				shipped = append(shipped, r.ID)
				return nil
			})
			if err != nil {
				t.Errorf("%s: %v", id, err)
				continue
			}
		}
		slices.Sort(shipped)
		if !slices.Equal(shipped, declared) {
			t.Errorf("%s ships recipes %v, its manifest lists %v", id, shipped, declared)
		}
	}
}
