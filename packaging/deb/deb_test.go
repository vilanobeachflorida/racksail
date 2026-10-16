package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/installer"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestPackageMatchesTheGoldenFile(t *testing.T) {
	files, err := Files([]byte("License text.\n\nSecond paragraph.\n"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, name := range names {
		b.WriteString("### " + name + " " + files[name].mode.String() + "\n")
		b.Write(files[name].data)
		b.WriteString("\n")
	}
	golden := filepath.Join("testdata", "package.golden")
	if *update {
		if err := os.WriteFile(golden, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Bytes(), want) {
		t.Errorf("the package's files differ from %s; run go test ./packaging/deb -update after an intended change\n%s", golden, b.String())
	}
}

// The package installs exactly the units a converted server gets, running
// the binary from where the package puts it.
func TestPackageHasTheConvertedServersUnits(t *testing.T) {
	p, err := Render(Binary)
	if err != nil {
		t.Fatal(err)
	}
	units, err := installer.Units(Binary)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != len(p.Units) {
		t.Errorf("the package has %d units, a converted server %d", len(p.Units), len(units))
	}
	for name, data := range units {
		if !bytes.Equal(p.Units[name], data) {
			t.Errorf("%s differs from the converted server's", name)
		}
		if !bytes.Contains(data, []byte("ExecStart="+Binary)) && !strings.HasSuffix(name, ".slice") && !strings.HasSuffix(name, ".socket") {
			t.Errorf("%s doesn't run %s", name, Binary)
		}
	}
	if !slices.Equal(p.UnitOrder, installer.UnitNames()) {
		t.Errorf("unit order = %v, want %v", p.UnitOrder, installer.UnitNames())
	}
	for _, want := range []string{installer.AgentUnit, installer.PanelUnit, installer.EdgeSocket} {
		if !slices.Contains(p.Enable, want) || !slices.Contains(p.Start, want) {
			t.Errorf("a first install must enable and start %s: %v %v", want, p.Enable, p.Start)
		}
	}
	if !bytes.Contains(p.Sysusers, []byte(`u racksail - "Racksail panel" /var/lib/racksail -`)) {
		t.Errorf("sysusers:\n%s", p.Sysusers)
	}
	if !bytes.Contains(p.Tmpfiles, []byte("d /var/lib/racksail 0750 racksail racksail -\n")) {
		t.Errorf("tmpfiles:\n%s", p.Tmpfiles)
	}
}

func TestScriptsAreValidShell(t *testing.T) {
	p, err := Render(Binary)
	if err != nil {
		t.Fatal(err)
	}
	scripts, err := p.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range scripts {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = bytes.NewReader(data)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
	stop := string(scripts["prerm"])
	if strings.Index(stop, "racksail-agent.service") < strings.Index(stop, "racksail-panel.service") {
		t.Error("removing the package must stop the panel before the agent")
	}
}

func TestRunWritesTheFiles(t *testing.T) {
	dir := t.TempDir()
	license := filepath.Join(dir, "LICENSE")
	os.WriteFile(license, []byte("License text.\n"), 0o644)
	out := filepath.Join(dir, "out")
	if err := run([]string{"-out", out, "-license", license}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-out", out, "-license", license}); err == nil {
		t.Error("writing into a directory that isn't empty must fail")
	}
	info, err := os.Stat(filepath.Join(out, "scripts", "postinst"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("postinst: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(out, "units", installer.PanelUnit)); err != nil {
		t.Error(err)
	}
	if err := run(nil); err == nil {
		t.Error("-out is required")
	}
}
