package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestParseAnswers(t *testing.T) {
	a, err := ParseAnswers([]byte(`
format: 1
mode: install
hostname: web1.example.test
network: {mode: static, address: 203.0.113.10/24, gateway: 203.0.113.1, dns: [9.9.9.9]}
disks: {layout: mirror, devices: [/dev/sda, /dev/sdb], confirmWipe: true}
account: {username: admin, sshKeys: ["ssh-ed25519 AAAAC3Nza admin@example"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode != ModeInstall || a.Network.Gateway != "203.0.113.1" || len(a.Disks.Devices) != 2 {
		t.Errorf("answers = %+v", a)
	}
	bad := map[string]string{
		"gateway off network":  "format: 1\nmode: install\nnetwork: {mode: static, address: 203.0.113.10/24, gateway: 198.51.100.1}\ndisks: {layout: single, devices: [/dev/sda], confirmWipe: true}\naccount: {username: admin, githubKeys: someone}\n",
		"mirror with one disk": "format: 1\nmode: install\nnetwork: {mode: dhcp}\ndisks: {layout: mirror, devices: [/dev/sda], confirmWipe: true}\naccount: {username: admin, githubKeys: someone}\n",
		"no wipe confirmation": "format: 1\nmode: install\nnetwork: {mode: dhcp}\ndisks: {layout: single, devices: [/dev/sda]}\naccount: {username: admin, githubKeys: someone}\n",
		"no admin credentials": "format: 1\nmode: install\nnetwork: {mode: dhcp}\ndisks: {layout: single, devices: [/dev/sda], confirmWipe: true}\naccount: {username: admin}\n",
		"plain password":       "format: 1\nmode: install\nnetwork: {mode: dhcp}\ndisks: {layout: single, devices: [/dev/sda], confirmWipe: true}\naccount: {username: admin, passwordHash: hunter2}\n",
		"restore without repo": "format: 1\nmode: restore\nnetwork: {mode: dhcp}\ndisks: {layout: single, devices: [/dev/sda]}\naccount: {username: admin, githubKeys: someone}\n",
		"unknown key":          "format: 1\nmode: convert\nhostnmae: x\n",
	}
	for name, doc := range bad {
		if _, err := ParseAnswers([]byte(doc)); err == nil {
			t.Errorf("%s: should fail", name)
		}
	}
	if _, err := ParseAnswers([]byte("format: 1\nmode: convert\n")); err != nil {
		t.Errorf("converting only needs the mode: %v", err)
	}
}

func TestUnitsGolden(t *testing.T) {
	units, err := Units("/usr/local/bin/racksail")
	if err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	for _, name := range UnitNames() {
		all.WriteString("### " + name + "\n")
		all.Write(units[name])
		all.WriteString("\n")
	}
	golden := filepath.Join("testdata", "units.golden")
	if *update {
		if err := os.WriteFile(golden, all.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(all.Bytes(), want) {
		t.Errorf("units differ from %s:\n%s", golden, all.String())
	}
	if _, err := Units("racksail"); err == nil {
		t.Error("a relative binary path must be refused")
	}
}

func TestConvertOnTheSimulatedServer(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "old"})
	client := agent.NewLocal(m.Backend(), nil)
	steps, err := ConvertSteps(&Answers{Format: 1, Mode: ModeConvert, Hostname: "web1.example.test"}, "/usr/bin/racksail")
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	if err := Convert(ctx, client, steps, func(i int, s Step) { titles = append(titles, s.Title) }); err != nil {
		t.Fatal(err)
	}
	if len(titles) != 5 {
		t.Errorf("steps = %v", titles)
	}
	b := m.Backend()
	if h, _ := b.Network.Hostname(ctx); h != "web1" {
		t.Errorf("hostname = %q", h)
	}
	unit, err := b.Units.ReadUnit(ctx, PanelUnit)
	if err != nil || !strings.Contains(string(unit), "User=racksail") {
		t.Errorf("panel unit = %q, %v", unit, err)
	}
	st, _ := b.Units.Status(ctx, PanelUnit)
	if !st.Active() || st.UnitFileState != "enabled" {
		t.Errorf("panel = %+v", st)
	}
	if st, _ := b.Units.Status(ctx, EdgeSocket); !st.Active() || st.SubState != "listening" || st.UnitFileState != "enabled" {
		t.Errorf("the edge's socket waits for the panel: %+v", st)
	}
	if st, _ := b.Units.Status(ctx, EdgeUnit); st.Active() {
		t.Errorf("the edge itself only starts when it gets routes: %+v", st)
	}
	if _, err := b.Files.Stat(ctx, "/opt/racksail/runtimes"); err != nil {
		t.Errorf("directories: %v", err)
	}
	// Converting twice changes nothing.
	m.ResetCalls()
	if err := Convert(ctx, client, steps, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Calls() {
		if c.Op == "write" || c.Op == "mkdir" || c.Op == "create_user" {
			t.Errorf("a second conversion changed something: %v", c)
		}
	}
	if _, err := ConvertSteps(&Answers{Mode: ModeInstall}, "/usr/bin/racksail"); err == nil {
		t.Error("ConvertSteps only plans conversions")
	}
}

// ubuntu answers GetFacts as an Ubuntu machine would.
type ubuntu struct{ agent.Client }

func (u ubuntu) Call(ctx context.Context, op string, input json.RawMessage, opts agent.CallOptions) (json.RawMessage, error) {
	if op == agent.GetFacts.Name() {
		return json.Marshal(system.MachineFacts{OS: system.OSRelease{ID: "ubuntu", VersionID: "24.04"}})
	}
	return u.Client.Call(ctx, op, input, opts)
}

func TestConvertRefusesOtherSystems(t *testing.T) {
	client := ubuntu{agent.NewLocal(sim.New(sim.Options{}).Backend(), nil)}
	if err := Convert(context.Background(), client, nil, nil); !errors.Is(err, ErrNotDebian13) {
		t.Errorf("err = %v", err)
	}
}

const cryptHash = "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"

func TestAdminHandover(t *testing.T) {
	for name, acct := range map[string]Account{
		"nobody":         {},
		"no credentials": {Username: "admin"},
	} {
		if _, ok, err := AdminHandover(&Answers{Account: acct}); ok || err != nil {
			t.Errorf("%s: handed over = %v, %v", name, ok, err)
		}
	}
	step, ok, err := AdminHandover(&Answers{Account: Account{Username: "admin", PasswordHash: cryptHash, SSHKeys: []string{"ssh-ed25519 AAAA admin@laptop"}}})
	if err != nil || !ok || step.Op != agent.WriteFile.Name() {
		t.Fatalf("handover = %+v, %v, %v", step, ok, err)
	}
	in, ok := step.Input.(agent.WriteFileInput)
	if !ok {
		t.Fatalf("the step writes %T", step.Input)
	}
	// Only the panel's user may read the account it's handed.
	if in.Path != system.StateDir+"/"+BootstrapFile || in.Mode != 0o600 || in.Owner.User != "racksail" {
		t.Errorf("written as %s %o %+v", in.Path, in.Mode, in.Owner)
	}
	var got handover
	if err := json.Unmarshal(in.Content, &got); err != nil {
		t.Fatal(err)
	}
	if got.Username != "admin" || got.PasswordHash != cryptHash || len(got.SSHKeys) != 1 {
		t.Errorf("handed over %+v", got)
	}
}

func TestConvertHandsTheAdminToThePanel(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "old"})
	answers := &Answers{Format: 1, Mode: ModeConvert, Account: Account{Username: "admin", PasswordHash: cryptHash}}
	steps, err := ConvertSteps(answers, "/usr/bin/racksail")
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, s := range steps {
		titles = append(titles, s.Title)
	}
	if !strings.Contains(strings.Join(titles, "|"), "Hand the admin's account to the panel|Install Racksail's services") {
		t.Errorf("steps = %v", titles)
	}
	if err := Convert(ctx, agent.NewLocal(m.Backend(), nil), steps, nil); err != nil {
		t.Fatal(err)
	}
	files := m.Backend().Files
	path := system.StateDir + "/" + BootstrapFile
	data, err := files.ReadFile(ctx, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got handover
	if err := json.Unmarshal(data, &got); err != nil || got.Username != "admin" || got.PasswordHash != cryptHash {
		t.Errorf("the panel was handed %s, %v", data, err)
	}
	if fi, err := files.Stat(ctx, path); err != nil || fi.Mode.Perm() != 0o600 || fi.Owner != "racksail" {
		t.Errorf("the handover file = %+v, %v", fi, err)
	}
}
