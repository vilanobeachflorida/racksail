package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/installer"
)

// Stand-ins for the tools the maintainer scripts call. They keep what
// deb-systemd-helper and systemd would know under $SYSTEMD: whether
// deb-systemd-helper has a unit's state file, the unit's enablement links,
// masks deb-systemd-helper set and masks an administrator set, and which
// units run. As with the real tools, was-enabled holds for a unit
// deb-systemd-helper has never seen, unmask only lifts its own masks, and
// deb-systemd-invoke starts neither a disabled unit nor a masked one. An
// action they don't know, such as mask, fails the test.
var tools = map[string]string{
	"deb-systemd-helper": `[ "$1" != --quiet ] || shift
action=$1
shift
echo "deb-systemd-helper $action $*" >>"$SYSTEMD/log"
status=0
for unit; do
	case $action in
	unmask) rm -f "$SYSTEMD/masked/$unit" ;;
	was-enabled) [ ! -e "$SYSTEMD/state/$unit" ] || [ -e "$SYSTEMD/enabled/$unit" ] || status=1 ;;
	enable) touch "$SYSTEMD/state/$unit" "$SYSTEMD/enabled/$unit" ;;
	update-state) touch "$SYSTEMD/state/$unit" ;;
	purge) rm -f "$SYSTEMD/state/$unit" "$SYSTEMD/enabled/$unit" ;;
	*) echo "deb-systemd-helper $action" >>"$SYSTEMD/unexpected"; exit 2 ;;
	esac
done
exit $status`,
	"deb-systemd-invoke": `action=$1
shift
echo "deb-systemd-invoke $action $*" >>"$SYSTEMD/log"
for unit; do
	case $action in
	start)
		if [ -e "$SYSTEMD/enabled/$unit" ] && [ ! -e "$SYSTEMD/masked/$unit" ] && [ ! -e "$SYSTEMD/admin-masked/$unit" ]; then
			touch "$SYSTEMD/active/$unit"
		fi
		;;
	try-restart) [ ! -e "$SYSTEMD/active/$unit" ] || echo "$unit" >>"$SYSTEMD/restarted" ;;
	stop) rm -f "$SYSTEMD/active/$unit" ;;
	*) echo "deb-systemd-invoke $action" >>"$SYSTEMD/unexpected"; exit 2 ;;
	esac
done`,
	"systemctl":        `echo "systemctl $*" >>"$SYSTEMD/log"`,
	"systemd-sysusers": `echo "systemd-sysusers $*" >>"$SYSTEMD/log"`,
	"systemd-tmpfiles": `echo "systemd-tmpfiles $*" >>"$SYSTEMD/log"`,
}

// The scripts tell a running systemd by this directory; the tests point
// them at one of their own.
const runningSystemd = "/run/systemd/system"

var (
	enabledUnits = []string{installer.AgentUnit, installer.PanelUnit, installer.EdgeSocket}
	allUnits     = []string{installer.AgentUnit, installer.PanelUnit, installer.EdgeSocket, installer.EdgeUnit}
)

// machine is a server the maintainer scripts run on, as dpkg runs them.
type machine struct {
	t       *testing.T
	shell   string
	state   string
	scripts string
	env     []string
}

func newMachine(t *testing.T, shell string, systemdRuns bool) *machine {
	t.Helper()
	p, err := Render(Binary)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := p.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m := &machine{t: t, shell: shell, state: filepath.Join(dir, "systemd"), scripts: filepath.Join(dir, "scripts")}
	run := filepath.Join(dir, "run")
	if systemdRuns {
		mkdir(t, run)
	}
	for _, d := range []string{"state", "enabled", "masked", "admin-masked", "active"} {
		mkdir(t, filepath.Join(m.state, d))
	}
	mkdir(t, m.scripts)
	for name, data := range rendered {
		text := string(data)
		// preinst only looks at files, under DPKG_ROOT in these tests.
		if name != "preinst" && !strings.Contains(text, runningSystemd) {
			t.Fatalf("%s no longer checks %s, so these tests can't tell it whether systemd runs", name, runningSystemd)
		}
		write(t, filepath.Join(m.scripts, name), strings.ReplaceAll(text, runningSystemd, run), 0o755)
	}
	bin := filepath.Join(dir, "bin")
	mkdir(t, bin)
	for name, body := range tools {
		write(t, filepath.Join(bin, name), "#!/bin/sh\n"+body+"\n", 0o755)
	}
	m.env = []string{"PATH=" + bin + ":/usr/bin:/bin", "SYSTEMD=" + m.state}
	return m
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, file, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// dpkg runs a maintainer script with dpkg's arguments.
func (m *machine) dpkg(script string, args ...string) {
	m.t.Helper()
	cmd := exec.Command(m.shell, append([]string{filepath.Join(m.scripts, script)}, args...)...)
	cmd.Env = m.env
	if out, err := cmd.CombinedOutput(); err != nil {
		m.t.Fatalf("%s %s: %v\n%s", script, strings.Join(args, " "), err, out)
	}
	if data, err := os.ReadFile(filepath.Join(m.state, "unexpected")); err == nil {
		m.t.Fatalf("%s %s called what the package shouldn't:\n%s", script, strings.Join(args, " "), data)
	}
}

func (m *machine) is(what, unit string) bool {
	_, err := os.Stat(filepath.Join(m.state, what, unit))
	return err == nil
}

func (m *machine) set(what, unit string) { write(m.t, filepath.Join(m.state, what, unit), "", 0o644) }

func (m *machine) clear(what, unit string) {
	if err := os.Remove(filepath.Join(m.state, what, unit)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) log() string {
	data, _ := os.ReadFile(filepath.Join(m.state, "log"))
	return string(data)
}

func (m *machine) restarted() []string {
	data, _ := os.ReadFile(filepath.Join(m.state, "restarted"))
	return strings.Fields(string(data))
}

func (m *machine) resetLog() {
	os.Remove(filepath.Join(m.state, "log"))
	os.Remove(filepath.Join(m.state, "restarted"))
}

// mustRun checks that the package's units are enabled, unmasked and running.
func (m *machine) mustRun(when string) {
	m.t.Helper()
	for _, u := range enabledUnits {
		if !m.is("enabled", u) || m.is("masked", u) || !m.is("active", u) {
			m.t.Errorf("%s: %s is enabled %v, masked %v, running %v; want enabled, unmasked and running",
				when, u, m.is("enabled", u), m.is("masked", u), m.is("active", u))
		}
	}
}

func scriptShells() []string {
	out := []string{"sh"}
	if _, err := os.Stat("/bin/dash"); err == nil {
		out = append(out, "/bin/dash")
	}
	return out
}

func TestScriptsInstallAndUpgrade(t *testing.T) {
	for _, shell := range scriptShells() {
		t.Run(shell, func(t *testing.T) {
			m := newMachine(t, shell, true)
			m.dpkg("postinst", "configure")
			m.mustRun("after the first install")
			log := m.log()
			for _, want := range []string{"systemd-sysusers racksail.conf\n", "systemd-tmpfiles --create racksail.conf\n", "systemctl --system daemon-reload\n"} {
				if !strings.Contains(log, want) {
					t.Errorf("the first install didn't run %q:\n%s", want, log)
				}
			}
			if len(m.restarted()) > 0 {
				t.Errorf("the first install restarted %v", m.restarted())
			}

			// The edge runs once the panel has sent it routes.
			m.set("active", installer.EdgeUnit)
			m.resetLog()
			m.dpkg("prerm", "upgrade", "1.1.0")
			m.dpkg("postrm", "upgrade", "1.1.0")
			m.dpkg("postinst", "configure", "1.0.0")
			m.mustRun("after an upgrade")
			if got := m.restarted(); !slices.Equal(got, []string{installer.AgentUnit, installer.PanelUnit, installer.EdgeUnit}) {
				t.Errorf("an upgrade restarted %v; want the services that run, and not the edge's socket", got)
			}
		})
	}
}

// Removing the package and installing it again, without purging it in
// between, brings the services back enabled and running.
func TestScriptsRemoveAndInstallAgain(t *testing.T) {
	for _, shell := range scriptShells() {
		t.Run(shell, func(t *testing.T) {
			m := newMachine(t, shell, true)
			m.dpkg("postinst", "configure")
			m.dpkg("prerm", "remove")
			for _, u := range enabledUnits {
				if m.is("active", u) {
					t.Errorf("%s still runs after the removal", u)
				}
			}
			m.dpkg("postrm", "remove")
			for _, u := range allUnits {
				if m.is("masked", u) {
					t.Errorf("the removal masked %s", u)
				}
			}
			// dpkg kept the package's configuration, so it passes the version
			// it last configured.
			m.dpkg("postinst", "configure", "1.0.0")
			m.mustRun("after installing the package again")

			m.dpkg("prerm", "remove")
			m.dpkg("postrm", "remove")
			m.dpkg("postrm", "purge")
			for _, u := range enabledUnits {
				if m.is("state", u) || m.is("enabled", u) {
					t.Errorf("purging left %s enabled or remembered", u)
				}
			}
			m.dpkg("postinst", "configure")
			m.mustRun("after installing the package again after a purge")
		})
	}
}

// dpkg may purge the package after init-system-helpers is gone.
func TestScriptsPurgeWithoutHelpers(t *testing.T) {
	m := newMachine(t, "sh", true)
	m.dpkg("postinst", "configure")
	m.dpkg("prerm", "remove")
	m.dpkg("postrm", "remove")
	m.env[0] = "PATH=" + t.TempDir()
	m.dpkg("postrm", "purge")
}

// A unit an administrator disabled or masked stays as they left it through
// upgrades, while the others restart.
func TestScriptsKeepWhatAnAdministratorChose(t *testing.T) {
	m := newMachine(t, "sh", true)
	m.dpkg("postinst", "configure")
	m.clear("enabled", installer.PanelUnit)
	m.clear("active", installer.PanelUnit)
	m.set("admin-masked", installer.AgentUnit)
	m.clear("active", installer.AgentUnit)

	m.dpkg("postinst", "configure", "1.0.0")
	if m.is("enabled", installer.PanelUnit) || m.is("active", installer.PanelUnit) {
		t.Error("the upgrade enabled or started the panel an administrator disabled")
	}
	if !m.is("admin-masked", installer.AgentUnit) || m.is("active", installer.AgentUnit) {
		t.Error("the upgrade unmasked or started the agent an administrator masked")
	}
	if !m.is("active", installer.EdgeSocket) {
		t.Error("the edge's socket stopped")
	}
}

// Packages built before removal stopped masking the units left masks
// behind, which installing this one lifts.
func TestScriptsLiftMasksRemovalLeft(t *testing.T) {
	m := newMachine(t, "sh", true)
	for _, u := range enabledUnits {
		m.set("state", u)
		m.set("enabled", u)
	}
	for _, u := range allUnits {
		m.set("masked", u)
	}
	m.dpkg("postinst", "configure", "1.0.0")
	m.mustRun("after installing over masked units")
	if m.is("masked", installer.EdgeUnit) {
		t.Error("the edge's service is still masked")
	}
}

// When removing the package fails after prerm stopped the services, dpkg
// calls postinst abort-remove, which starts them again.
func TestScriptsAfterAFailedRemoval(t *testing.T) {
	m := newMachine(t, "sh", true)
	m.dpkg("postinst", "configure")
	m.dpkg("prerm", "remove")
	m.dpkg("postinst", "abort-remove")
	m.mustRun("after a failed removal")

	m.dpkg("postinst", "abort-upgrade", "1.1.0")
	m.mustRun("after a failed upgrade")
}

// In a chroot or an image being built, systemd doesn't run: the units are
// enabled for the first boot and nothing is started.
func TestScriptsWithoutSystemdRunning(t *testing.T) {
	m := newMachine(t, "sh", false)
	m.dpkg("postinst", "configure")
	for _, u := range enabledUnits {
		if !m.is("enabled", u) || m.is("active", u) {
			t.Errorf("%s: enabled %v, running %v; want enabled and not running", u, m.is("enabled", u), m.is("active", u))
		}
	}
	if log := m.log(); strings.Contains(log, "systemctl") || strings.Contains(log, "deb-systemd-invoke") {
		t.Errorf("the scripts acted on a systemd that doesn't run:\n%s", log)
	}
}

// With DPKG_ROOT, dpkg installs into another root: users and directories
// are made there, and nothing on the running system starts or stops.
func TestScriptsIntoAnotherRoot(t *testing.T) {
	m := newMachine(t, "sh", true)
	m.env = append(m.env, "DPKG_ROOT=/target")
	m.dpkg("postinst", "configure")
	m.dpkg("prerm", "remove")
	m.dpkg("postrm", "remove")
	log := m.log()
	for _, want := range []string{"systemd-sysusers --root=/target racksail.conf\n", "systemd-tmpfiles --root=/target --create racksail.conf\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("postinst didn't run %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "systemctl") || strings.Contains(log, "deb-systemd-invoke") {
		t.Errorf("the scripts acted on the running system:\n%s", log)
	}
}

// dpkg also calls postinst for triggers and the like, which the package
// doesn't use.
func TestScriptsIgnoreOtherActions(t *testing.T) {
	m := newMachine(t, "sh", true)
	m.dpkg("postinst", "triggered", "something")
	m.dpkg("prerm", "deconfigure", "in-favour", "other", "1.0")
	m.dpkg("postrm", "abort-install")
	if log := m.log(); log != "" {
		t.Errorf("the scripts did something:\n%s", log)
	}
}

// A server Racksail's install script set up keeps its own binary, run by
// units in /etc/systemd/system, so the package refuses to install there;
// anywhere else, and on upgrades, preinst lets dpkg go on.
func TestPreinstRefusesAServerTheScriptSetUp(t *testing.T) {
	p, err := Render(Binary)
	if err != nil {
		t.Fatal(err)
	}
	scripts, err := p.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "preinst")
	write(t, script, string(scripts["preinst"]), 0o755)
	scriptUnit := "[Service]\nExecStart=" + ScriptBinary + " panel\n"
	packageUnit := "[Service]\nExecStart=" + Binary + " panel --verbose\n"
	cases := []struct {
		name   string
		files  map[string]string
		args   []string
		refuse bool
	}{
		{"a fresh server", nil, []string{"install"}, false},
		{"the script's binary", map[string]string{ScriptBinary: "binary"}, []string{"install"}, true},
		{"the script's units without its binary", map[string]string{"/etc/systemd/system/" + installer.PanelUnit: scriptUnit}, []string{"install"}, true},
		{"installing again after a removal", map[string]string{ScriptBinary: "binary"}, []string{"install", "1.0.0"}, true},
		// An administrator's copy of the package's unit runs the package's
		// binary.
		{"an administrator's copy of a unit", map[string]string{"/etc/systemd/system/" + installer.PanelUnit: packageUnit}, []string{"install"}, false},
		{"an upgrade", map[string]string{ScriptBinary: "binary"}, []string{"upgrade", "1.0.0"}, false},
	}
	for _, shell := range scriptShells() {
		for _, tc := range cases {
			t.Run(shell+" "+tc.name, func(t *testing.T) {
				root := t.TempDir()
				for path, content := range tc.files {
					mkdir(t, filepath.Dir(filepath.Join(root, path)))
					write(t, filepath.Join(root, path), content, 0o644)
				}
				cmd := exec.Command(shell, append([]string{script}, tc.args...)...)
				cmd.Env = []string{"PATH=/usr/bin:/bin", "DPKG_ROOT=" + root}
				out, err := cmd.CombinedOutput()
				if tc.refuse {
					if err == nil || !strings.Contains(string(out), "a server uses the package or the install script, not both") {
						t.Errorf("preinst %s = %v\n%s; want a refusal", strings.Join(tc.args, " "), err, out)
					}
					return
				}
				if err != nil || len(out) > 0 {
					t.Errorf("preinst %s = %v\n%s", strings.Join(tc.args, " "), err, out)
				}
			})
		}
	}
}

// The package's preinst looks for the binary where the install script puts
// it.
func TestScriptBinaryIsTheInstallScripts(t *testing.T) {
	script, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "\nBINARY="+ScriptBinary+"\n") {
		t.Errorf("install.sh doesn't install racksail as %s", ScriptBinary)
	}
}

// nfpm packages every maintainer script packaging/deb writes.
func TestNfpmPackagesEveryScript(t *testing.T) {
	config, err := os.ReadFile("../nfpm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Render(Binary)
	if err != nil {
		t.Fatal(err)
	}
	scripts, err := p.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{"preinst": "preinstall", "postinst": "postinstall", "prerm": "preremove", "postrm": "postremove"}
	for name := range scripts {
		key, ok := keys[name]
		if !ok {
			t.Errorf("%s isn't a script nfpm knows", name)
			continue
		}
		if want := "  " + key + ": build/deb/scripts/" + name + "\n"; !strings.Contains(string(config), want) {
			t.Errorf("nfpm.yaml doesn't package %s: want %q", name, want)
		}
	}
}
