package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	file := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/hooks/addons -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from what is rendered now; run go test ./internal/hooks/addons -update after an intended change:\n%s", file, got)
	}
}

func TestRenderUnits(t *testing.T) {
	templates, err := RenderTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	for _, name := range []string{ServiceTemplate, SocketTemplate} {
		all.WriteString("# " + name + "\n")
		all.Write(templates[name])
		all.WriteString("\n")
	}
	for _, spec := range []ServiceSpec{
		{ID: "site-guard", Version: "0.3.0", MemoryMB: 64, CPUPercent: 20},
		{ID: "cloudflare-dns", Version: "1.2.0-beta.1", MemoryMB: 128, CPUPercent: 50, Hosts: []Host{{Addresses: []string{"2606:4700::/32", "104.16.0.0/13"}}, {Name: "api.cloudflare.com", Addresses: []string{"104.16.132.229"}}}},
		// An account's add-on runs in the account's slice.
		{ID: "site-guard-r9s0t1u2v3", Version: "0.3.0", MemoryMB: 48, CPUPercent: 10, Slice: "racksail-accounts-blog.slice"},
	} {
		dropIn, err := spec.RenderDropIn()
		if err != nil {
			t.Fatal(err)
		}
		all.WriteString("# " + Service(spec.ID) + ".d/" + DropIn + ".conf\n")
		all.Write(dropIn)
		all.WriteString("\n# " + HostsFile(spec.ID) + "\n")
		all.Write(spec.RenderHosts())
		all.WriteString("\n")
	}
	golden(t, "units.golden", all.Bytes())

	for _, bad := range []ServiceSpec{
		{ID: "Bad", Version: "1.0.0", MemoryMB: 64, CPUPercent: 10},
		{ID: "ok", Version: "1.0", MemoryMB: 64, CPUPercent: 10},
		{ID: "ok", Version: "1.0.0", MemoryMB: 8, CPUPercent: 10},
		{ID: "ok", Version: "1.0.0", MemoryMB: 64, CPUPercent: 0},
		{ID: "ok", Version: "1.0.0", MemoryMB: 64, CPUPercent: 10, Hosts: []Host{{Name: "example.com", Addresses: []string{"example.com"}}}},
		{ID: "ok", Version: "1.0.0", MemoryMB: 64, CPUPercent: 10, Hosts: []Host{{Addresses: []string{"10.0.0.1/8"}}}},
		{ID: "ok", Version: "1.0.0", MemoryMB: 64, CPUPercent: 10, Hosts: []Host{{Name: "example.com", Addresses: []string{"10.0.0.0/8"}}}},
		{ID: "ok", Version: "1.0.0", MemoryMB: 64, CPUPercent: 10, Hosts: []Host{{Name: "bad name", Addresses: []string{"10.0.0.1"}}}},
		{ID: "../etc", Version: "1.0.0", MemoryMB: 64, CPUPercent: 10},
		{ID: "ok", Version: "1.0.0", MemoryMB: 64, CPUPercent: 10, Slice: "racksail.slice"},
		{ID: "ok", Version: "1.0.0", MemoryMB: 64, CPUPercent: 10, Slice: "racksail-accounts-../x.slice"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
	if !strings.Contains(string(templates[ServiceTemplate]), "DynamicUser=yes") || !strings.Contains(string(templates[SocketTemplate]), "SocketUser=racksail") {
		t.Error("add-ons run as users of their own, and only the panel reaches their socket")
	}
}

func call(t *testing.T, c agent.Client, op string, in any, check bool) agent.Result {
	t.Helper()
	raw, _ := json.Marshal(in)
	out, err := c.Call(context.Background(), op, raw, agent.CallOptions{Check: check})
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	var res agent.Result
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestServiceOperations(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{})
	c := agent.NewLocal(m.Backend(), nil)
	spec := ServiceSpec{ID: "site-guard", Version: "0.3.0", MemoryMB: 64, CPUPercent: 20}
	if res := call(t, c, "EnsureAddonService", spec, true); !res.Changed {
		t.Error("check mode reports what would be written")
	}
	if _, err := m.Backend().Units.ReadUnit(ctx, ServiceTemplate); err == nil {
		t.Error("check mode wrote the template")
	}
	if res := call(t, c, "EnsureAddonService", spec, false); !res.Changed || len(res.Effects) != 4 {
		t.Errorf("first write = %+v", res)
	}
	if res := call(t, c, "EnsureAddonService", spec, false); res.Changed {
		t.Errorf("writing it again changes nothing: %+v", res)
	}
	dropIn, err := m.Backend().Files.ReadFile(ctx, system.UnitDir+"/"+Service("site-guard")+".d/racksail.conf", 0)
	if err != nil || !strings.Contains(string(dropIn), "ExecStart="+Dir+"/site-guard/0.3.0/addon") || !strings.Contains(string(dropIn), "PrivateNetwork=yes") {
		t.Errorf("drop-in = %s, %v", dropIn, err)
	}
	call(t, c, "StartUnit", agent.UnitInput{Name: SocketUnit("site-guard")}, false)
	if st, _ := m.Backend().Units.Status(ctx, SocketUnit("site-guard")); !st.Active() {
		t.Errorf("socket = %+v", st)
	}
	if res := call(t, c, "RemoveAddonService", AddonInput{ID: "site-guard"}, false); !res.Changed {
		t.Error("removing stops the socket and deletes the drop-in")
	}
	if st, _ := m.Backend().Units.Status(ctx, SocketUnit("site-guard")); st.Active() {
		t.Error("the socket still listens")
	}
	if res := call(t, c, "RemoveAddonService", AddonInput{ID: "site-guard"}, false); res.Changed {
		t.Error("removing twice changes nothing")
	}
}
