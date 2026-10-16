package main

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/installer"
)

// Binary is where the package installs racksail.
const Binary = "/usr/bin/racksail"

// ScriptBinary is where Racksail's install script, packaging/install.sh,
// installs racksail. The units it converts a server with run it from there.
const ScriptBinary = "/usr/local/bin/racksail"

// Package is what the Debian package installs besides the binary, rendered
// from the plan "racksail install --convert" carries out, so a server set up
// from the package matches a converted one.
type Package struct {
	// Units maps unit names to their files.
	Units map[string][]byte
	// UnitOrder lists the units in the order the plan installs them.
	UnitOrder []string
	// Enable and Start are the units a first install enables and starts.
	Enable, Start []string
	// Sysusers and Tmpfiles create the users and directories.
	Sysusers, Tmpfiles []byte
}

// Render builds the package's files for a racksail binary at binary.
func Render(binary string) (*Package, error) {
	steps, err := installer.ConvertSteps(&installer.Answers{Format: 1, Mode: installer.ModeConvert}, binary)
	if err != nil {
		return nil, err
	}
	p := &Package{Units: map[string][]byte{}}
	var users, dirs bytes.Buffer
	users.WriteString(header)
	dirs.WriteString(header)
	for _, s := range steps {
		for _, op := range s.Ops {
			switch in := op.Input.(type) {
			case agent.EnsureUserInput:
				if err := sysusersLine(&users, in); err != nil {
					return nil, err
				}
			case agent.EnsureDirInput:
				tmpfilesLine(&dirs, in)
			case agent.EnsureUnitInput:
				p.Units[in.Name] = in.Content
				p.UnitOrder = append(p.UnitOrder, in.Name)
			case agent.PlatformUnitsInput:
				if op.Op != agent.InstallPlatformUnits.Name() {
					return nil, fmt.Errorf("the convert plan's step %q has an operation the package can't express: %s", s.Title, op.Op)
				}
				// The agent renders Racksail's own units from the binary's
				// path, so the package renders the same ones.
				units, err := installer.Units(in.Binary)
				if err != nil {
					return nil, err
				}
				for _, name := range installer.UnitNames() {
					p.Units[name] = units[name]
					p.UnitOrder = append(p.UnitOrder, name)
				}
			case agent.UnitsInput:
				if op.Op != agent.EnableUnits.Name() {
					return nil, fmt.Errorf("the convert plan's step %q has an operation the package can't express: %s", s.Title, op.Op)
				}
				p.Enable = append(p.Enable, in.Names...)
			case agent.UnitInput:
				if op.Op != agent.StartUnit.Name() {
					return nil, fmt.Errorf("the convert plan's step %q has an operation the package can't express: %s", s.Title, op.Op)
				}
				p.Start = append(p.Start, in.Name)
			default:
				// A new kind of step in the convert plan needs its counterpart
				// here, or the package would set servers up differently.
				return nil, fmt.Errorf("the convert plan's step %q has an operation the package can't express: %s", s.Title, op.Op)
			}
		}
	}
	p.Sysusers, p.Tmpfiles = users.Bytes(), dirs.Bytes()
	return p, nil
}

const header = "# Written by packaging/deb from the plan of racksail install --convert.\n"

func sysusersLine(b *bytes.Buffer, in agent.EnsureUserInput) error {
	if !in.System || in.UID != 0 || in.Shell != "" || !in.Locked {
		return fmt.Errorf("user %s: the package only creates locked system users without a shell", in.Name)
	}
	fmt.Fprintf(b, "u %s - %q %s -\n", in.Name, in.Comment, in.Home)
	for _, g := range in.Groups {
		fmt.Fprintf(b, "m %s %s\n", in.Name, g)
	}
	return nil
}

func tmpfilesLine(b *bytes.Buffer, in agent.EnsureDirInput) {
	user, group := in.Owner.User, in.Owner.Group
	if user == "" {
		user = "root"
	}
	if group == "" {
		group = "root"
	}
	fmt.Fprintf(b, "d %s %04o %s %s -\n", in.Path, uint32(in.Mode), user, group)
}

// Scripts renders the maintainer scripts dpkg runs around installing,
// upgrading and removing the package.
func (p *Package) Scripts() (map[string][]byte, error) {
	services := slices.DeleteFunc(slices.Clone(p.UnitOrder), func(u string) bool { return strings.HasSuffix(u, ".slice") })
	// Stop what accepts work first: the edge's socket and the edge, then the
	// panel, and the agent last.
	stop := slices.Clone(services)
	slices.Reverse(stop)
	data := map[string]any{
		"ScriptBinary": ScriptBinary,
		"Units":        strings.Join(p.UnitOrder, " "),
		"Enable":       strings.Join(p.Enable, " "),
		"Start":        strings.Join(p.Start, " "),
		"Services":     strings.Join(services, " "),
		"Stop":         strings.Join(stop, " "),
		"Restart":      strings.Join(slices.DeleteFunc(slices.Clone(services), func(u string) bool { return strings.HasSuffix(u, ".socket") }), " "),
	}
	out := map[string][]byte{}
	for name, text := range scripts {
		t, err := template.New(name).Parse(text)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, data); err != nil {
			return nil, err
		}
		out[name] = b.Bytes()
	}
	return out, nil
}

// The scripts follow what debhelper generates for systemd units, sysusers.d
// and tmpfiles.d: deb-systemd-helper keeps enablement right inside chroots,
// deb-systemd-invoke respects policy-rc.d, and with DPKG_ROOT set, as when
// dpkg installs into another root, nothing touches the running system.
var scripts = map[string]string{
	// Racksail's install script sets a server up with its own binary, run by
	// units in /etc/systemd/system that override the package's. apt would
	// then update a binary the services don't run, so the package refuses
	// such a server, as the script refuses one with the package.
	"preinst": `#!/bin/sh
# Written by packaging/deb from the plan of racksail install --convert.
set -e

if [ "$1" = install ]; then
	converted=
	[ ! -e "${DPKG_ROOT:-}{{.ScriptBinary}}" ] || converted=1
	for unit in {{.Units}}; do
		if grep -qsF {{.ScriptBinary}} "${DPKG_ROOT:-}/etc/systemd/system/$unit"; then
			converted=1
		fi
	done
	if [ -n "$converted" ]; then
		cat >&2 <<'END'
racksail: this server already runs Racksail from {{.ScriptBinary}},
which updates itself from the panel. Its services would keep running that
binary while apt updated the package's, so the package doesn't install here:
a server uses the package or the install script, not both.
END
		exit 1
	fi
fi
`,
	"postinst": `#!/bin/sh
# Written by packaging/deb from the plan of racksail install --convert.
set -e

case "$1" in
configure | abort-upgrade | abort-deconfigure | abort-remove) ;;
*) exit 0 ;;
esac

systemd-sysusers ${DPKG_ROOT:+--root="$DPKG_ROOT"} racksail.conf
systemd-tmpfiles ${DPKG_ROOT:+--root="$DPKG_ROOT"} --create racksail.conf

# Packages built before removal stopped masking the units left masks behind.
# deb-systemd-helper only lifts masks it set itself, never an administrator's.
deb-systemd-helper unmask {{.Services}} >/dev/null || true
# Enable each unit on the first install, and any unit a new version adds,
# but leave the ones an administrator disabled as they are.
for unit in {{.Enable}}; do
	if deb-systemd-helper --quiet was-enabled "$unit"; then
		deb-systemd-helper enable "$unit" >/dev/null || true
	else
		deb-systemd-helper update-state "$unit" >/dev/null || true
	fi
done

if [ -z "${DPKG_ROOT:-}" ] && [ -d /run/systemd/system ]; then
	systemctl --system daemon-reload >/dev/null || true
	# Over an earlier version, what runs restarts to run the new binary,
	# while the edge's socket keeps listening.
	if [ -n "$2" ]; then
		deb-systemd-invoke try-restart {{.Restart}} >/dev/null || true
	fi
	# Then whatever is enabled and stopped starts, such as after the package
	# was removed and is installed again; disabled units stay stopped.
	deb-systemd-invoke start {{.Start}} >/dev/null || true
fi
`,
	"prerm": `#!/bin/sh
# Written by packaging/deb from the plan of racksail install --convert.
set -e

if [ -z "${DPKG_ROOT:-}" ] && [ "$1" = remove ] && [ -d /run/systemd/system ]; then
	deb-systemd-invoke stop {{.Stop}} >/dev/null || true
fi
`,
	// Removing the package doesn't mask or disable the units: their files
	// go, and installing the package again brings them back as they were.
	"postrm": `#!/bin/sh
# Written by packaging/deb from the plan of racksail install --convert.
# Purging keeps /var/lib/racksail and /srv/racksail, which hold the panel's
# state and the accounts' data.
set -e

if [ -z "${DPKG_ROOT:-}" ] && [ "$1" = remove ] && [ -d /run/systemd/system ]; then
	systemctl --system daemon-reload >/dev/null || true
fi
# On a purge, init-system-helpers may already be gone.
if [ "$1" = purge ] && command -v deb-systemd-helper >/dev/null; then
	deb-systemd-helper purge {{.Services}} >/dev/null || true
	deb-systemd-helper unmask {{.Services}} >/dev/null || true
fi
`,
}

// Copyright renders the package's copyright file in the machine-readable
// format Debian uses, with the full text of the license, which Debian
// doesn't ship in /usr/share/common-licenses.
func Copyright(license []byte) []byte {
	var b bytes.Buffer
	b.WriteString("Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/\n")
	b.WriteString("Upstream-Name: racksail\n")
	b.WriteString("Source: https://github.com/vilanobeachflorida/racksail\n\n")
	b.WriteString("Files: *\n")
	b.WriteString("Copyright: The Racksail contributors\n")
	b.WriteString("License: AGPL-3.0-or-later\n")
	b.WriteString("Comment: The licenses of the software built into the binary are in\n")
	b.WriteString(" /usr/share/doc/racksail/third-party-licenses.txt.\n\n")
	b.WriteString("License: AGPL-3.0-or-later\n")
	for _, line := range strings.Split(strings.TrimRight(string(license), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			b.WriteString(" .\n")
			continue
		}
		b.WriteString(" " + line + "\n")
	}
	return b.Bytes()
}
