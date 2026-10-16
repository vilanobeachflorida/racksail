package agent

import (
	"bytes"
	"context"
	"errors"
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
	aptSources map[string]system.AptSource
	// signingKeys are vendors' armored OpenPGP keys by name.
	signingKeys map[string][]byte
}{
	writeRoots: []string{
		system.ConfigDir, system.StateDir, "/var/lib/racksail-db", "/srv/racksail", "/opt/racksail",
		system.RunDir, system.DataDisksDir, "/var/log/racksail", SnapshotDir,
	},
	units:       []string{"racksail*", "systemd-networkd.service", "systemd-timesyncd.service", "nftables.service"},
	aptSources:  map[string]system.AptSource{},
	signingKeys: map[string][]byte{},
}

// agentOnly are the agent's own: its state, its audit trail and the
// snapshots of the system's subvolumes. No operation reaches them by path,
// whatever roots modules add, so the panel can neither rewrite the record
// of what it did nor read what the system keeps from it through a
// snapshot.
var agentOnly = []string{system.AgentStateDir, system.AgentLogDir, SystemSnapshotDir}

// accountsArea is where accounts keep their data; it may move to a data
// disk of its own.
const accountsArea = "/srv/racksail"

// AllowWrite lets file operations write below root, such as /etc/knot for
// the DNS module. Call it from an init function.
func AllowWrite(root string) { allow(&policy.writeRoots, root) }

// AllowRead lets file operations read below root.
func AllowRead(root string) { allow(&policy.readRoots, root) }

// AllowUnit lets unit operations control units matching the glob pattern,
// such as "knot.service" or "php*-fpm.service".
func AllowUnit(pattern string) { allow(&policy.units, pattern) }

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

// reserved reports whether p is, is inside or holds one of the agent's own
// locations.
func reserved(p string) bool {
	if p == "/" {
		return true
	}
	for _, r := range agentOnly {
		if p == r || strings.HasPrefix(p, r+"/") || strings.HasPrefix(r, p+"/") {
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
	if reserved(p) {
		return fmt.Errorf("%s is the agent's own", p)
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
	if reserved(p) {
		return fmt.Errorf("%s is the agent's own", p)
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

// managedRoot returns the longest root of the policy that holds p: a write
// root, or for reads a read root too.
func managedRoot(p string, read bool) (string, bool) {
	if reserved(p) {
		return "", false
	}
	policy.RLock()
	defer policy.RUnlock()
	best := ""
	consider := func(roots []string) {
		for _, r := range roots {
			if (p == r || strings.HasPrefix(p, r+"/")) && len(r) > len(best) {
				best = r
			}
		}
	}
	consider(policy.writeRoots)
	if read {
		consider(policy.readRoots)
	}
	return best, best != ""
}

// beneathRoot returns the view of files an operation on p works through:
// everything below p's managed root, reached without following a symbolic
// link, so a link an account plants in its own directory can't send a read
// or a write the agent does as root anywhere else.
func beneathRoot(files system.Files, p string, read bool) (system.Files, error) {
	root, ok := managedRoot(p, read)
	if !ok {
		return nil, fmt.Errorf("%s is outside the locations Racksail manages: %w", p, system.ErrInvalid)
	}
	return files.Beneath(root), nil
}

// beneath is beneathRoot for an operation's environment.
func beneath(env *Env, p string, read bool) (system.Files, error) {
	return beneathRoot(env.System.Files, p, read)
}

// checkNoSymlinks refuses a path that is or passes through a symbolic link
// below its managed root, for operations that hand a path to a program,
// such as btrfs, rather than work on it themselves. A path that doesn't
// exist is left for the operation to report.
func checkNoSymlinks(ctx context.Context, env *Env, p string) error {
	files, err := beneath(env, p, true)
	if err != nil {
		return err
	}
	info, err := files.Stat(ctx, p)
	switch {
	case errors.Is(err, system.ErrNotFound):
		return nil
	case err != nil:
		return err
	case info.IsSymlink():
		return fmt.Errorf("%s is a symbolic link, which isn't followed here: %w", p, system.ErrInvalid)
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

// unitRules are what the policy adds to the system layer's checks of unit
// files and drop-ins: the units Racksail manages may be named as
// dependencies, and mounts go where filesystems may be mounted.
func unitRules() system.UnitRules {
	return system.UnitRules{
		Dependency: unitAllowed,
		MountPoint: func(p string) bool { return checkMountPoint(p) == nil },
	}
}

// checkUnitFile validates a unit file written for the panel and returns
// what is left to check against the machine: who a service runs as and
// what a mount unit mounts. Racksail's own units are rendered by the agent
// itself, so they can't be written this way.
func checkUnitFile(name string, content []byte) (system.CheckedUnit, error) {
	if err := checkOwnedUnit(name); err != nil {
		return system.CheckedUnit{}, err
	}
	if system.IsPlatformUnit(name) {
		return system.CheckedUnit{}, fmt.Errorf("unit %s is one of Racksail's own; InstallPlatformUnits installs those", name)
	}
	if len(content) > 1<<20 {
		return system.CheckedUnit{}, fmt.Errorf("unit %s is larger than 1 MiB", name)
	}
	return system.CheckUnitFile(name, content, unitRules())
}

// serviceUsers are the users a service written for the panel may run as:
// accounts, and Racksail's own service users other than the platform's.
// The user and its groups must not be privileged, since membership in the
// racksail group alone reaches the agent's socket.
func checkServiceUser(ctx context.Context, users system.Users, id system.UnitIdentity, check bool) error {
	if id.User == "" {
		return nil
	}
	groups, err := users.ListGroups(ctx)
	if err != nil {
		return err
	}
	privilegedGID := func(gid int) bool {
		for _, g := range groups {
			if g.GID == gid && slices.Contains(privilegedGroups, g.Name) {
				return true
			}
		}
		return gid == 0
	}
	u, err := users.LookupUser(ctx, id.User)
	switch {
	case errors.Is(err, system.ErrNotFound) && id.Dynamic:
		// systemd allocates the user when the service starts; a static
		// user of the same name would be used instead, so there must be
		// none.
		if !strings.HasPrefix(id.User, "racksail-") {
			return fmt.Errorf("a dynamic user's name must start with racksail-, not %s", id.User)
		}
	case errors.Is(err, system.ErrNotFound) && check:
		// An earlier step of the plan creates the user.
	case err != nil:
		return fmt.Errorf("the service's user %s: %w", id.User, err)
	case id.Dynamic:
		return fmt.Errorf("%s exists, so systemd would run the service as that user instead of a dynamic one", id.User)
	default:
		account := u.UID >= system.AccountUIDMin && u.UID <= system.AccountUIDMax
		service := u.System && strings.HasPrefix(u.Name, "racksail-") && !slices.Contains(platformUsers, u.Name)
		if !account && !service {
			return fmt.Errorf("user %s isn't an account or one of Racksail's service users", u.Name)
		}
		if privilegedGID(u.GID) {
			return fmt.Errorf("user %s has a privileged primary group", u.Name)
		}
		for _, g := range u.Groups {
			if slices.Contains(privilegedGroups, g) {
				return fmt.Errorf("user %s is in the privileged group %s", u.Name, g)
			}
		}
	}
	if id.Group == "" {
		return nil
	}
	if slices.Contains(privilegedGroups, id.Group) {
		return fmt.Errorf("group %s is privileged", id.Group)
	}
	g, err := users.LookupGroup(ctx, id.Group)
	switch {
	case errors.Is(err, system.ErrNotFound) && (id.Dynamic || check):
	case err != nil:
		return fmt.Errorf("the service's group %s: %w", id.Group, err)
	case privilegedGID(g.GID):
		return fmt.Errorf("group %s is privileged", id.Group)
	case !(g.GID >= system.AccountUIDMin && g.GID <= system.AccountUIDMax) && !strings.HasPrefix(g.Name, "racksail-"):
		return fmt.Errorf("group %s isn't an account's or one of Racksail's service groups", g.Name)
	}
	return nil
}

// platformUsers are the platform's own users, which no unit written for
// the panel runs as.
var platformUsers = []string{"racksail", "racksail-edge"}

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
