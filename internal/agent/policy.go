package agent

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// The agent's policy: what file, unit and package operations may touch. The
// core lists Racksail's own locations; modules extend the lists from init
// functions for the services they manage. The lists are fixed once the
// binary starts, so a compromised panel can't widen them.
var policy = struct {
	sync.RWMutex
	writeRoots []string
	readRoots  []string
	units      []string
	rootUnits  []string
	aptSources map[string]system.AptSource
	// signingKeys are vendors' armored OpenPGP keys by name.
	signingKeys map[string][]byte
}{
	writeRoots: []string{
		system.ConfigDir, system.StateDir, "/var/lib/racksail-db", "/srv/racksail", "/opt/racksail",
		system.RunDir, system.DataDisksDir, "/var/log/racksail", "/.snapshots",
	},
	units: []string{"racksail*", "systemd-networkd.service", "systemd-timesyncd.service", "nftables.service"},
	// Platform units run as root or as their own users by design.
	rootUnits:   []string{"racksail-panel.service", "racksail-agent.service", "racksail-edge.service", "racksail-addon@.service"},
	aptSources:  map[string]system.AptSource{},
	signingKeys: map[string][]byte{},
}

// AllowWrite lets file operations write below root, such as /etc/knot for
// the DNS module. Call it from an init function.
func AllowWrite(root string) { allow(&policy.writeRoots, root) }

// AllowRead lets file operations read below root.
func AllowRead(root string) { allow(&policy.readRoots, root) }

// AllowUnit lets unit operations control units matching the glob pattern,
// such as "knot.service" or "php*-fpm.service".
func AllowUnit(pattern string) { allow(&policy.units, pattern) }

// AllowRootUnit lets a Racksail service unit run without a User= setting.
// Account workloads never qualify; this is for platform services only.
func AllowRootUnit(name string) { allow(&policy.rootUnits, name) }

// RegisterAptSource adds a vendor repository the agent may enable. The
// panel can only refer to registered sources by name, so it can never add a
// repository the binary doesn't know about.
func RegisterAptSource(src system.AptSource) {
	policy.Lock()
	defer policy.Unlock()
	if err := src.Validate(); err != nil {
		registry.Lock()
		registry.errs = append(registry.errs, fmt.Errorf("agent: apt source %s: %w", src.Name, err))
		registry.Unlock()
		return
	}
	policy.aptSources[src.Name] = src
}

// RegisterSigningKey adds a vendor's armored OpenPGP public key that
// archive signatures may be checked against. Like apt sources, the panel
// names keys and never supplies them.
func RegisterSigningKey(name string, armored []byte) {
	policy.Lock()
	defer policy.Unlock()
	_, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err == nil && !signingKeyPattern.MatchString(name) {
		err = fmt.Errorf("the name must be lowercase letters, digits, dots and dashes")
	}
	if err != nil {
		registry.Lock()
		registry.errs = append(registry.errs, fmt.Errorf("agent: signing key %s: %w", name, err))
		registry.Unlock()
		return
	}
	policy.signingKeys[name] = bytes.Clone(armored)
}

var signingKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)

// signingKey returns a registered key.
func signingKey(name string) ([]byte, bool) {
	policy.RLock()
	defer policy.RUnlock()
	key, ok := policy.signingKeys[name]
	return key, ok
}

func allow(list *[]string, entry string) {
	policy.Lock()
	defer policy.Unlock()
	if !slices.Contains(*list, entry) {
		*list = append(*list, entry)
	}
}

func cleanAbs(p string) error {
	if !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsRune(p, 0) {
		return fmt.Errorf("path %q must be absolute and clean", p)
	}
	return nil
}

func under(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// checkWrite validates a path an operation will change.
func checkWrite(p string) error {
	if err := cleanAbs(p); err != nil {
		return err
	}
	policy.RLock()
	defer policy.RUnlock()
	if !under(p, policy.writeRoots) {
		return fmt.Errorf("%s is outside the locations Racksail manages", p)
	}
	return nil
}

// checkRead validates a path an operation will read.
func checkRead(p string) error {
	if err := cleanAbs(p); err != nil {
		return err
	}
	policy.RLock()
	defer policy.RUnlock()
	if !under(p, policy.writeRoots) && !under(p, policy.readRoots) {
		return fmt.Errorf("%s is outside the locations Racksail reads", p)
	}
	return nil
}

// isWriteRoot reports whether p is one of the roots themselves, which must
// never be removed.
func isWriteRoot(p string) bool {
	policy.RLock()
	defer policy.RUnlock()
	return slices.Contains(policy.writeRoots, p)
}

// checkNoSymlinks refuses paths that pass through a symbolic link below the
// managed root, so a link planted by an account can't redirect a write.
func checkNoSymlinks(ctx context.Context, files system.Files, p string) error {
	policy.RLock()
	var root string
	for _, r := range policy.writeRoots {
		if (p == r || strings.HasPrefix(p, r+"/")) && len(r) > len(root) {
			root = r
		}
	}
	policy.RUnlock()
	if root == "" {
		return fmt.Errorf("%s is outside the locations Racksail manages", p)
	}
	dir := root
	for _, part := range strings.Split(strings.TrimPrefix(path.Dir(p), root), "/") {
		if part == "" {
			continue
		}
		dir = path.Join(dir, part)
		info, err := files.Stat(ctx, dir)
		if err != nil {
			// Missing directories are reported by the operation itself.
			return nil
		}
		if info.IsSymlink() {
			return fmt.Errorf("%s goes through the symbolic link %s: %w", p, dir, system.ErrInvalid)
		}
	}
	return nil
}

func unitAllowed(name string) bool {
	policy.RLock()
	defer policy.RUnlock()
	for _, pattern := range policy.units {
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
		// Instances of allowed templates are allowed too.
		if at := strings.IndexByte(name, '@'); at > 0 {
			template := name[:at+1] + name[strings.LastIndexByte(name, '.'):]
			if ok, _ := path.Match(pattern, template); ok {
				return true
			}
		}
	}
	return false
}

func checkUnit(name string) error {
	if !system.ValidUnitName(name) {
		return fmt.Errorf("%q is not a unit name", name)
	}
	if !unitAllowed(name) {
		return fmt.Errorf("unit %s isn't one Racksail manages", name)
	}
	return nil
}

// checkOwnedUnit validates the name of a unit Racksail installs: racksail-*
// units, and mount units for Racksail's own locations.
func checkOwnedUnit(name string) error {
	if !system.ValidUnitName(name) {
		return fmt.Errorf("%q is not a unit name", name)
	}
	isMount := strings.HasSuffix(name, ".mount") || strings.HasSuffix(name, ".automount")
	switch {
	case strings.HasPrefix(name, "racksail"):
	case isMount && (strings.HasPrefix(name, "mnt-racksail") || strings.HasPrefix(name, "srv-racksail")):
	default:
		return fmt.Errorf("unit %s must be named racksail-*", name)
	}
	return nil
}

// checkUnitFile validates a unit file Racksail writes. Services must say
// which user they run as, so writing a unit can't start code as root unless
// the service is one of the platform's own.
func checkUnitFile(name string, content []byte) error {
	if err := checkOwnedUnit(name); err != nil {
		return err
	}
	if len(content) > 1<<20 || slices.Contains(content, 0) {
		return fmt.Errorf("unit %s is too large or not text", name)
	}
	if !strings.HasSuffix(name, ".service") {
		return nil
	}
	policy.RLock()
	rootOK := false
	for _, r := range policy.rootUnits {
		if ok, _ := path.Match(r, name); ok || templateOf(name) == r {
			rootOK = true
		}
	}
	policy.RUnlock()
	if rootOK {
		return nil
	}
	user := ""
	for _, line := range strings.Split(string(content), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "User="); ok {
			user = strings.TrimSpace(v)
		}
	}
	if user == "" || user == "root" || user == "0" {
		return fmt.Errorf("service %s must run as a user other than root", name)
	}
	return nil
}

func templateOf(name string) string {
	at := strings.IndexByte(name, '@')
	if at < 0 {
		return ""
	}
	return name[:at+1] + name[strings.LastIndexByte(name, '.'):]
}

var accountNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func checkAccountName(kind, name string) error {
	if !accountNamePattern.MatchString(name) {
		return fmt.Errorf("%s name %q must be lowercase letters, digits, '-' or '_', starting with a letter", kind, name)
	}
	return nil
}

// privilegedGroups are root-equivalent; Racksail never adds anyone to them.
var privilegedGroups = []string{
	"root", "sudo", "adm", "shadow", "disk", "docker", "incus-admin", "incus", "lxd", "libvirt", "kvm",
	"systemd-journal", "staff", "wheel", "racksail", "racksail-edge",
}

var allowedShells = []string{"/usr/sbin/nologin", "/bin/false", "/usr/bin/false", "/bin/bash", "/usr/bin/bash", "/bin/sh", "/usr/bin/git-shell"}

func checkOwner(o system.Owner) error {
	if o.User != "" {
		if err := checkAccountName("user", o.User); err != nil {
			return err
		}
	}
	if o.Group != "" {
		if err := checkAccountName("group", o.Group); err != nil {
			return err
		}
	}
	return nil
}

// Mode is a permission mode written in octal, such as "0644".
type Mode uint32

// MarshalText writes the mode in octal.
func (m Mode) MarshalText() ([]byte, error) { return []byte(fmt.Sprintf("%04o", uint32(m))), nil }

// UnmarshalText reads an octal mode.
func (m *Mode) UnmarshalText(b []byte) error {
	v, err := strconv.ParseUint(string(b), 8, 32)
	if err != nil || v > 0o7777 {
		return fmt.Errorf("mode %q must be octal, such as 0644", b)
	}
	*m = Mode(v)
	return nil
}

// FileMode converts the mode to io/fs bits.
func (m Mode) FileMode() fs.FileMode {
	mode := fs.FileMode(m & 0o777)
	if m&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}

// checkFileMode refuses set-user-ID and set-group-ID bits, which would let a
// file run with someone else's rights.
func checkFileMode(m Mode, dir bool) error {
	if m&0o6000 != 0 {
		return fmt.Errorf("mode %04o has set-user-ID or set-group-ID bits", uint32(m))
	}
	if m&0o1000 != 0 && !dir {
		return fmt.Errorf("mode %04o sets the sticky bit on a file", uint32(m))
	}
	return nil
}
