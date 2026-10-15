package system

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Unit files the panel asks the agent to install are read the way systemd
// reads them and checked against what Racksail's renderers produce: only
// known sections and directives, and values that can't run anything as
// root, reach the platform's own state or stop the machine. A compromised
// panel can still write a unit, but only one that runs as an account.

// unitWhitespace is what systemd trims around keys, values and lines.
const unitWhitespace = " \t\n\r"

// ParseUnitFile reads a unit file as systemd does: lines whose first
// character other than blanks is # or ; are comments, even inside a
// continued line; a line ending in an unescaped backslash continues on the
// next one, joined by a space; keys and values are trimmed. It refuses what
// systemd would read differently from what a person reading the file sees:
// carriage returns, which systemd also takes as line breaks, other control
// characters, invalid UTF-8, a byte order mark, directives outside a
// section and lines without a key.
func ParseUnitFile(data []byte) (UnitFile, error) {
	var u UnitFile
	if !utf8.Valid(data) {
		return u, fmt.Errorf("the unit file isn't valid UTF-8: %w", ErrInvalid)
	}
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return u, fmt.Errorf("the unit file starts with a byte order mark: %w", ErrInvalid)
	}
	for _, r := range string(data) {
		if (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f {
			return u, fmt.Errorf("the unit file has the control character %U: %w", r, ErrInvalid)
		}
	}
	section := -1
	var continued string
	inContinuation := false
	for i, raw := range strings.Split(string(data), "\n") {
		if first := strings.TrimLeft(raw, unitWhitespace); first != "" && (first[0] == '#' || first[0] == ';') {
			continue
		}
		line := raw
		if inContinuation {
			line = continued + raw
		}
		escaped := false
		for j := 0; j < len(line); j++ {
			if escaped {
				escaped = false
			} else if line[j] == '\\' {
				escaped = true
			}
		}
		if escaped {
			continued, inContinuation = line[:len(line)-1]+" ", true
			continue
		}
		inContinuation = false
		if err := parseUnitLine(&u, &section, line); err != nil {
			return u, fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	if inContinuation {
		if err := parseUnitLine(&u, &section, continued); err != nil {
			return u, fmt.Errorf("the last line: %w", err)
		}
	}
	return u, nil
}

func parseUnitLine(u *UnitFile, section *int, line string) error {
	line = strings.Trim(line, unitWhitespace)
	if line == "" {
		return nil
	}
	if line[0] == '[' {
		if !strings.HasSuffix(line, "]") || !unitSectionPattern.MatchString(line[1:len(line)-1]) {
			return fmt.Errorf("section header %q: %w", line, ErrInvalid)
		}
		u.Sections = append(u.Sections, UnitSection{Name: line[1 : len(line)-1]})
		*section = len(u.Sections) - 1
		return nil
	}
	if *section < 0 {
		return fmt.Errorf("%q is outside a section: %w", line, ErrInvalid)
	}
	key, value, ok := strings.Cut(line, "=")
	key = strings.Trim(key, unitWhitespace)
	if !ok || !unitKeyPattern.MatchString(key) {
		return fmt.Errorf("%q isn't a directive: %w", line, ErrInvalid)
	}
	s := &u.Sections[*section]
	s.Entries = append(s.Entries, UnitEntry{Key: key, Value: strings.Trim(value, unitWhitespace)})
	return nil
}

// UnitRules are what the agent's policy adds to the checks of unit files.
type UnitRules struct {
	// Dependency reports whether a unit other than Racksail's own may be
	// named as a dependency, such as a distribution service a module
	// manages. Passive targets, Racksail's units and mounts are always
	// allowed.
	Dependency func(name string) bool
	// MountPoint reports whether a mount unit may mount a filesystem at a
	// path.
	MountPoint func(path string) bool
}

// UnitIdentity is who a service runs as, for the agent to check against
// the machine's users.
type UnitIdentity struct {
	User    string
	Group   string
	Dynamic bool
}

// CheckUnitFile checks a unit file Racksail installs on the panel's behalf:
// racksail-* services, sockets, timers, paths, slices and targets, and mount
// units for Racksail's own locations. Services must name the user they run
// as, which is returned; their commands can't take the prefixes that run
// them with full privileges, and nothing else in any unit can run a
// command.
func CheckUnitFile(name string, data []byte, rules UnitRules) (UnitIdentity, error) {
	if !ValidUnitName(name) {
		return UnitIdentity{}, fmt.Errorf("%q is not a unit name: %w", name, ErrInvalid)
	}
	typ := path.Ext(name)[1:]
	sections, ok := unitSections[typ]
	if !ok {
		return UnitIdentity{}, fmt.Errorf("unit %s: Racksail doesn't install %s units: %w", name, typ, ErrInvalid)
	}
	u, err := ParseUnitFile(data)
	if err != nil {
		return UnitIdentity{}, fmt.Errorf("unit %s: %w", name, err)
	}
	c := &unitChecker{unit: name, rules: rules}
	seen := map[string]bool{}
	for _, s := range u.Sections {
		keys, ok := sections[s.Name]
		if !ok {
			return UnitIdentity{}, fmt.Errorf("unit %s can't have a [%s] section: %w", name, s.Name, ErrInvalid)
		}
		seen[s.Name] = true
		if err := c.check(s, keys); err != nil {
			return UnitIdentity{}, err
		}
	}
	switch typ {
	case "service":
		if c.id.User == "" {
			return UnitIdentity{}, fmt.Errorf("service %s must say which user it runs as, other than root: %w", name, ErrInvalid)
		}
		if !c.hasExec {
			return UnitIdentity{}, fmt.Errorf("service %s has no command: %w", name, ErrInvalid)
		}
	case "mount", "automount":
		if c.where == "" {
			return UnitIdentity{}, fmt.Errorf("unit %s needs Where=: %w", name, ErrInvalid)
		}
		if typ == "mount" && (c.what == "" || !c.secureOptions) {
			return UnitIdentity{}, fmt.Errorf("mount %s needs What= and Options= with nosuid and nodev: %w", name, ErrInvalid)
		}
	case "socket":
		if !seen["Socket"] || !c.listens {
			return UnitIdentity{}, fmt.Errorf("socket %s doesn't listen on anything: %w", name, ErrInvalid)
		}
	}
	return c.id, nil
}

// CheckDropIn checks a drop-in for a unit: it may tune resources, limits,
// restarts and dependencies on mounts and Racksail's units, and nothing
// that changes what the unit runs, as whom or with which environment.
func CheckDropIn(unit string, data []byte, rules UnitRules) error {
	if !ValidUnitName(unit) {
		return fmt.Errorf("%q is not a unit name: %w", unit, ErrInvalid)
	}
	u, err := ParseUnitFile(data)
	if err != nil {
		return fmt.Errorf("drop-in for %s: %w", unit, err)
	}
	c := &unitChecker{unit: unit, rules: rules}
	for _, s := range u.Sections {
		keys, ok := dropInSections[s.Name]
		if !ok {
			return fmt.Errorf("a drop-in for %s can't have a [%s] section: %w", unit, s.Name, ErrInvalid)
		}
		if err := c.check(s, keys); err != nil {
			return err
		}
	}
	return nil
}

// valueCheck checks one directive's value.
type valueCheck func(c *unitChecker, v string) error

type unitChecker struct {
	unit  string
	rules UnitRules
	id    UnitIdentity

	hasExec       bool
	listens       bool
	what, where   string
	secureOptions bool
}

func (c *unitChecker) check(s UnitSection, keys map[string]valueCheck) error {
	for _, e := range s.Entries {
		check, ok := keys[e.Key]
		if !ok {
			return fmt.Errorf("unit %s: %s= isn't allowed in [%s]: %w", c.unit, e.Key, s.Name, ErrInvalid)
		}
		if err := check(c, e.Value); err != nil {
			return fmt.Errorf("unit %s: %s=%s: %w", c.unit, e.Key, e.Value, err)
		}
	}
	return nil
}

func refuse(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrInvalid)...)
}

// Value checks.

func anything(*unitChecker, string) error { return nil }

func boolean(_ *unitChecker, v string) error {
	switch strings.ToLower(v) {
	case "yes", "no", "true", "false", "on", "off", "1", "0":
		return nil
	}
	return refuse("not a boolean")
}

func oneOf(values ...string) valueCheck {
	return func(_ *unitChecker, v string) error {
		if slices.Contains(values, v) {
			return nil
		}
		return refuse("must be one of %s", strings.Join(values, ", "))
	}
}

func matching(re *regexp.Regexp) valueCheck {
	return func(_ *unitChecker, v string) error {
		if re.MatchString(v) {
			return nil
		}
		return refuse("not an accepted value")
	}
}

func intBetween(min, max int64) valueCheck {
	return func(_ *unitChecker, v string) error {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < min || n > max {
			return refuse("must be a number from %d to %d", min, max)
		}
		return nil
	}
}

var (
	timeSpanPattern  = regexp.MustCompile(`^(infinity|([0-9]+(\.[0-9]+)?[ \t]*[a-zA-Z]{0,7}[ \t]*)+)$`)
	resourcePattern  = regexp.MustCompile(`^(infinity|[0-9]+(\.[0-9]+)?[KMGTPE%]?)$`)
	cpuQuotaPattern  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?%$`)
	weightPattern    = regexp.MustCompile(`^(idle|[0-9]{1,5})$`)
	cpuSetPattern    = regexp.MustCompile(`^[0-9]+(-[0-9]+)?([ ,][0-9]+(-[0-9]+)?)*$`)
	ioLimitPattern   = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9/_.:-]*[ \t]+(infinity|[0-9]+[KMGT]?)$`)
	ioWeightPattern  = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9/_.:-]*[ \t]+[0-9]{1,5}$`)
	rlimitPattern    = regexp.MustCompile(`^(infinity|[0-9]+[KMGTPE]?)(:(infinity|[0-9]+[KMGTPE]?))?$`)
	signalPattern    = regexp.MustCompile(`^(SIG)?[A-Z][A-Z0-9+]{1,15}$|^[0-9]{1,2}$`)
	exitListPattern  = regexp.MustCompile(`^([0-9]{1,3}|(SIG)?[A-Z][A-Z0-9]{1,15})([ \t]+([0-9]{1,3}|(SIG)?[A-Z][A-Z0-9]{1,15}))*$`)
	modePattern      = regexp.MustCompile(`^0?[0-7]{3,4}$`)
	envNamesPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*([ \t]+[A-Za-z_][A-Za-z0-9_]*)*$`)
	addressFamilies  = regexp.MustCompile(`^~?(none|AF_[A-Z0-9]+([ \t]+AF_[A-Z0-9]+)*)$`)
	syscallPattern   = regexp.MustCompile(`^~?[@a-z0-9_:+ \t-]*$`)
	errnoPattern     = regexp.MustCompile(`^([A-Z][A-Z0-9]{1,15}|[0-9]{1,4}|kill|log)$`)
	capsPattern      = regexp.MustCompile(`^~?([A-Z_]+([ \t]+[A-Z_]+)*)?$`)
	bindRuleList     = regexp.MustCompile(`^(((ipv4|ipv6):)?((tcp|udp):)?(any|[0-9]{1,5}(-[0-9]{1,5})?)([ \t]+|$))*$`)
	ipListPattern    = regexp.MustCompile(`^((any|localhost|link-local|multicast|[0-9a-fA-F.:]+(/[0-9]{1,3})?)([ \t]+|$))*$`)
	fsListPattern    = regexp.MustCompile(`^~?[@a-z0-9_ \t-]*$`)
	ifaceListPattern = regexp.MustCompile(`^~?[A-Za-z0-9_.:@ \t-]*$`)
	descriptorName   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,255}$`)
	instancePattern  = regexp.MustCompile(`^[A-Za-z0-9:_.-]{1,64}$`)
	pathOptionWord   = regexp.MustCompile(`^[a-z0-9,=]*$`)
)

func timeSpan(c *unitChecker, v string) error         { return matching(timeSpanPattern)(c, v) }
func resourceAmount(c *unitChecker, v string) error   { return matching(resourcePattern)(c, v) }
func signal(c *unitChecker, v string) error           { return matching(signalPattern)(c, v) }
func octalMode(c *unitChecker, v string) error        { return checkModeValue(v) }
func rlimit(c *unitChecker, v string) error           { return matching(rlimitPattern)(c, v) }
func optionalTimeSpan(c *unitChecker, v string) error { return emptyOr(timeSpan)(c, v) }

func emptyOr(check valueCheck) valueCheck {
	return func(c *unitChecker, v string) error {
		if v == "" {
			return nil
		}
		return check(c, v)
	}
}

func checkModeValue(v string) error {
	if !modePattern.MatchString(v) {
		return refuse("must be an octal mode such as 0750")
	}
	m, _ := strconv.ParseUint(v, 8, 32)
	if m&0o6000 != 0 {
		return refuse("set-user-ID and set-group-ID bits aren't allowed")
	}
	return nil
}

// words splits a list value on blanks.
func words(v string) []string { return strings.Fields(v) }

// absolutePaths checks a list of paths, such as BindPaths= with its
// optional destination and options after colons. With prefixes, each path
// may start with the - + ! or | that conditions and sandbox settings take.
func absolutePaths(allowPrefix bool) valueCheck {
	return func(_ *unitChecker, v string) error {
		for _, w := range words(v) {
			if allowPrefix {
				w = strings.TrimLeft(w, "-+!|")
			}
			if strings.Contains(w, "%") {
				return refuse("%q has specifiers", w)
			}
			parts := strings.Split(w, ":")
			if !path.IsAbs(parts[0]) {
				return refuse("%q isn't an absolute path", parts[0])
			}
			for _, p := range parts[1:] {
				if !path.IsAbs(p) && !pathOptionWord.MatchString(p) {
					return refuse("%q isn't a path or mount options", p)
				}
			}
		}
		return nil
	}
}

// User and group names. Specifiers are refused: a template's instance name
// could otherwise choose the user.
var unitNamePart = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// platformUsers are the identities a unit written for the panel never runs
// as: root, the panel, whose group reaches the agent's socket, and the
// edge, which holds every site's certificate keys.
var platformUsers = []string{"root", "racksail", "racksail-edge"}

func unitUser(c *unitChecker, v string) error {
	if !unitNamePart.MatchString(v) || slices.Contains(platformUsers, v) {
		return refuse("the user must be named, not root or one of Racksail's own")
	}
	c.id.User = v
	return nil
}

func unitGroup(c *unitChecker, v string) error {
	if !unitNamePart.MatchString(v) || slices.Contains(platformUsers, v) {
		return refuse("the group must be named, not root or one of Racksail's own")
	}
	c.id.Group = v
	return nil
}

func dynamicUser(c *unitChecker, v string) error {
	if err := boolean(c, v); err != nil {
		return err
	}
	switch strings.ToLower(v) {
	case "yes", "true", "on", "1":
		c.id.Dynamic = true
	default:
		c.id.Dynamic = false
	}
	return nil
}

// execCommand checks an Exec*= line. Each command's program may only take
// the - (ignore failure), @ (own argv[0]) and : (no expansion) prefixes; +
// and ! run the command with full privileges whatever User= says. The
// program word can't be quoted or escaped, so what systemd unquotes is what
// is checked.
func execCommand(c *unitChecker, v string) error {
	if v == "" {
		// An empty assignment clears the list.
		return nil
	}
	tokens, err := execTokens(v)
	if err != nil {
		return err
	}
	first := true
	for _, t := range tokens {
		if t.separator {
			first = true
			continue
		}
		if !first {
			continue
		}
		first = false
		if t.quoted {
			return refuse("the program %s can't be quoted or escaped", t.raw)
		}
		program := strings.TrimLeft(t.raw, "-@:+!")
		if prefix := t.raw[:len(t.raw)-len(program)]; strings.ContainsAny(prefix, "+!") {
			return refuse("the prefix %q runs the command with full privileges", prefix)
		}
		if program == "" {
			return refuse("no program")
		}
		c.hasExec = true
	}
	return nil
}

type execToken struct {
	raw       string
	quoted    bool
	separator bool
}

// execTokens splits a command line into words as systemd's
// extract_first_word does with unquoting and C escapes, keeping each
// word's text as written and noting whether it used quotes or escapes. A
// lone ; separates commands.
func execTokens(v string) ([]execToken, error) {
	var out []execToken
	i := 0
	for {
		for i < len(v) && strings.IndexByte(" \t\n", v[i]) >= 0 {
			i++
		}
		if i >= len(v) {
			return out, nil
		}
		start, quoted := i, false
		var quote byte
		for ; i < len(v); i++ {
			ch := v[i]
			if quote != 0 {
				switch ch {
				case quote:
					quote = 0
				case '\\':
					i++
				}
				continue
			}
			if strings.IndexByte(" \t\n", ch) >= 0 {
				break
			}
			switch ch {
			case '"', '\'':
				quote, quoted = ch, true
			case '\\':
				quoted = true
				i++
			}
		}
		if quote != 0 || i > len(v) {
			return nil, refuse("an unterminated quote or escape")
		}
		raw := v[start:min(i, len(v))]
		out = append(out, execToken{raw: raw, quoted: quoted, separator: raw == ";" && !quoted})
	}
}

// Units the checks treat specially.

// passiveTargets are synchronization points: being ordered against them or
// pulling them in does nothing on its own.
var passiveTargets = []string{
	"basic.target", "default.target", "local-fs.target", "multi-user.target", "network.target",
	"network-online.target", "network-pre.target", "nss-lookup.target", "nss-user-lookup.target",
	"paths.target", "remote-fs.target", "slices.target", "sockets.target", "sysinit.target",
	"time-set.target", "time-sync.target", "timers.target",
}

// racksailUnit reports whether name is one of Racksail's units: its own
// platform units included.
func racksailUnit(name string) bool {
	return strings.HasPrefix(name, "racksail") && !strings.Contains(name, "%")
}

// accountUnit reports whether name is a racksail unit other than the
// platform's own.
func accountUnit(name string) bool { return racksailUnit(name) && !IsPlatformUnit(name) }

func mountUnit(name string) bool {
	return strings.HasSuffix(name, ".mount") || strings.HasSuffix(name, ".automount")
}

// dependencies checks a list of units a unit depends on or is ordered
// against.
func dependencies(c *unitChecker, v string) error {
	for _, n := range words(v) {
		if !ValidUnitName(n) {
			return refuse("%q isn't a unit name", n)
		}
		switch {
		case slices.Contains(passiveTargets, n), racksailUnit(n), mountUnit(n):
		case c.rules.Dependency != nil && c.rules.Dependency(n):
		default:
			return refuse("%s isn't a unit Racksail manages", n)
		}
	}
	return nil
}

// stopping checks units this unit stops or starts on its own: only
// Racksail's account units, never the platform's or the system's.
func stopping(_ *unitChecker, v string) error {
	for _, n := range words(v) {
		if !ValidUnitName(n) || !accountUnit(n) {
			return refuse("%s can only name Racksail's own account units", n)
		}
	}
	return nil
}

func installTargets(_ *unitChecker, v string) error {
	for _, n := range words(v) {
		if !ValidUnitName(n) || !(slices.Contains(passiveTargets, n) || accountUnit(n)) {
			return refuse("%s isn't a target a Racksail unit is installed into", n)
		}
	}
	return nil
}

func accountUnits(suffix string) valueCheck {
	return func(_ *unitChecker, v string) error {
		for _, n := range words(v) {
			if !ValidUnitName(n) || !accountUnit(n) || (suffix != "" && !strings.HasSuffix(n, suffix)) {
				return refuse("%s isn't one of Racksail's account units", n)
			}
		}
		return nil
	}
}

func unitSlice(_ *unitChecker, v string) error {
	if !ValidUnitName(v) || !strings.HasSuffix(v, ".slice") || !racksailUnit(v) {
		return refuse("services run in Racksail's slices")
	}
	return nil
}

// Directories systemd creates for a service and gives to its user. Their
// names must start with racksail- and stay clear of the platform's, since
// systemd hands over a directory that already exists, with everything in
// it, and removes runtime directories when the service stops.
var serviceDirPattern = regexp.MustCompile(`^racksail-[a-z0-9][a-z0-9_.-]{0,54}(/[a-z0-9][a-z0-9_.-]{0,63}){0,3}$`)

var platformDirs = []string{"racksail-agent", "racksail-edge", "racksail-panel"}

// CheckServiceDir checks the name of a runtime, state, cache or logs
// directory a service gets.
func CheckServiceDir(v string) error {
	first, _, _ := strings.Cut(v, "/")
	if !serviceDirPattern.MatchString(v) || strings.Contains(v, "..") || slices.Contains(platformDirs, first) {
		return refuse("directory %q must be racksail-<name> and not one of the platform's", v)
	}
	return nil
}

func serviceDirs(_ *unitChecker, v string) error {
	for _, d := range words(v) {
		if err := CheckServiceDir(d); err != nil {
			return err
		}
	}
	return nil
}

// CheckEnvironmentFile checks a file systemd reads, as root, into a
// service's environment. It must be in Racksail's configuration, which
// accounts can't write: a link an account plants there would otherwise
// hand it any root-only file of KEY=VALUE lines.
func CheckEnvironmentFile(p string) error {
	if !path.IsAbs(p) || path.Clean(p) != p || !strings.HasPrefix(p, ConfigDir+"/") || strings.ContainsAny(p, " \t%$") {
		return refuse("environment file %q must be under %s", p, ConfigDir)
	}
	return nil
}

func environmentFile(_ *unitChecker, v string) error {
	if v == "" {
		return nil
	}
	return CheckEnvironmentFile(strings.TrimPrefix(v, "-"))
}

var reservedPorts = []int{8443, 8444}

// CheckListenAddress checks where a socket unit listens: a Unix socket in
// Racksail's directories for sites, or a port of 1024 or above that isn't
// one of Racksail's.
func CheckListenAddress(v string) error {
	if strings.HasPrefix(v, "/") {
		if path.Clean(v) != v || strings.ContainsAny(v, " \t%$") ||
			!(strings.HasPrefix(v, RunDir+"/sites/") || strings.HasPrefix(v, "/run/racksail-")) {
			return refuse("socket %q must be under %s/sites or /run/racksail-*", v, RunDir)
		}
		return nil
	}
	port := v
	if i := strings.LastIndexByte(v, ':'); i >= 0 {
		host := v[:i]
		if !addressPattern.MatchString(v) || host == "" {
			return refuse("listen address %q", v)
		}
		port = v[i+1:]
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1024 || n > 65535 || slices.Contains(reservedPorts, n) {
		return refuse("port %q must be 1024 or above and not one of Racksail's", port)
	}
	return nil
}

func listen(c *unitChecker, v string) error {
	if err := CheckListenAddress(v); err != nil {
		return err
	}
	c.listens = true
	return nil
}

// Mounts.

var (
	mountDevicePattern = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9/_.:+=-]*$`)
	mountFSTypes       = []string{"btrfs", "ext4", "xfs", "vfat", "exfat", "f2fs"}
	mountOptionPattern = regexp.MustCompile(`^[a-z0-9_.-]+(=[A-Za-z0-9_.:/@+-]+)?$`)
	// mountOptions are the options a mount may use; anything that gives
	// files on the filesystem more power than an account's, such as suid,
	// dev or a loop device, is missing on purpose.
	mountOptions = []string{
		"defaults", "rw", "ro", "noatime", "relatime", "nodiratime", "lazytime", "nosuid", "nodev", "noexec", "exec",
		"nofail", "discard", "ssd", "nossd", "autodefrag", "noautodefrag", "acl", "noacl", "user_xattr", "barrier",
		"compress", "compress-force", "space_cache", "subvol", "subvolid", "commit", "discard", "errors", "data",
		"uid", "gid", "umask", "fmask", "dmask", "x-systemd.device-timeout", "x-systemd.mount-timeout",
	}
)

// CheckMountOptions checks a mount's options and adds nosuid and nodev
// when they're missing, so nothing on a data disk can run with privileges
// it brought along.
func CheckMountOptions(options []string) ([]string, error) {
	out := slices.Clone(options)
	for _, o := range options {
		key, _, _ := strings.Cut(o, "=")
		if !mountOptionPattern.MatchString(o) || !slices.Contains(mountOptions, key) {
			return nil, refuse("mount option %q isn't allowed", o)
		}
	}
	for _, o := range []string{"nosuid", "nodev"} {
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out, nil
}

// CheckMount checks a filesystem the agent mounts: a block device with a
// filesystem from a short list, and options that keep its files from
// gaining privileges. The returned mount has nosuid and nodev set.
func CheckMount(m Mount) (Mount, error) {
	if !mountDevicePattern.MatchString(m.Source) || strings.Contains(m.Source, "..") {
		return m, refuse("mount source %q must be a block device", m.Source)
	}
	if !slices.Contains(mountFSTypes, m.FSType) {
		return m, refuse("filesystem type %q isn't one Racksail mounts", m.FSType)
	}
	if !path.IsAbs(m.Target) || path.Clean(m.Target) != m.Target {
		return m, refuse("mount point %q must be absolute and clean", m.Target)
	}
	opts, err := CheckMountOptions(m.Options)
	if err != nil {
		return m, err
	}
	m.Options = opts
	return m, nil
}

func mountWhat(c *unitChecker, v string) error {
	if !mountDevicePattern.MatchString(v) || strings.Contains(v, "..") {
		return refuse("must be a block device")
	}
	c.what = v
	return nil
}

func mountWhere(c *unitChecker, v string) error {
	if !path.IsAbs(v) || path.Clean(v) != v || v == "/" {
		return refuse("must be an absolute, clean path")
	}
	if want := UnitNameForPath(v) + path.Ext(c.unit); want != c.unit {
		return refuse("a unit mounting %s is named %s", v, want)
	}
	if c.rules.MountPoint == nil || !c.rules.MountPoint(v) {
		return refuse("%s isn't a location Racksail mounts filesystems at", v)
	}
	c.where = v
	return nil
}

func mountType(_ *unitChecker, v string) error {
	if !slices.Contains(mountFSTypes, v) {
		return refuse("filesystem type %q isn't one Racksail mounts", v)
	}
	return nil
}

func mountOptionsValue(c *unitChecker, v string) error {
	opts := strings.Split(v, ",")
	if _, err := CheckMountOptions(opts); err != nil {
		return err
	}
	c.secureOptions = slices.Contains(opts, "nosuid") && slices.Contains(opts, "nodev")
	return nil
}

// UnitNameForPath escapes a path into the name systemd gives the mount or
// automount unit for it, without the suffix.
func UnitNameForPath(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return "-"
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		ch := p[i]
		switch {
		case ch == '/':
			b.WriteByte('-')
		case ch == '.' && i == 0:
			// systemd never starts a unit name with a dot.
			fmt.Fprintf(&b, `\x%02x`, ch)
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == ':', ch == '_', ch == '.':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, `\x%02x`, ch)
		}
	}
	return b.String()
}

// Sections and their directives, by unit type.

var unitKeys = map[string]valueCheck{
	"Description": anything, "Documentation": anything,
	"Requires": dependencies, "Requisite": dependencies, "Wants": dependencies, "BindsTo": dependencies,
	"PartOf": dependencies, "Upholds": dependencies, "After": dependencies, "Before": dependencies,
	"OnFailure": dependencies, "OnSuccess": dependencies, "ReloadPropagatedFrom": dependencies,
	"StopPropagatedFrom": dependencies, "PropagatesReloadTo": stopping, "PropagatesStopTo": stopping,
	"Conflicts":         stopping,
	"RequiresMountsFor": absolutePaths(false), "WantsMountsFor": absolutePaths(false),
	"DefaultDependencies": boolean, "StopWhenUnneeded": boolean, "RefuseManualStart": boolean,
	"RefuseManualStop": boolean, "IgnoreOnIsolate": boolean,
	"CollectMode":           oneOf("inactive", "inactive-or-failed"),
	"StartLimitIntervalSec": timeSpan, "StartLimitBurst": intBetween(0, 1<<16),
	"JobTimeoutSec": timeSpan, "JobRunningTimeoutSec": timeSpan,
	"ConditionPathExists": absolutePaths(true), "ConditionPathIsDirectory": absolutePaths(true),
	"ConditionPathIsMountPoint": absolutePaths(true), "ConditionFileNotEmpty": absolutePaths(true),
	"ConditionDirectoryNotEmpty": absolutePaths(true), "AssertPathExists": absolutePaths(true),
	"AssertPathIsDirectory": absolutePaths(true),
}

var installKeys = map[string]valueCheck{
	"WantedBy": installTargets, "RequiredBy": accountUnits(""), "UpheldBy": accountUnits(""),
	"Also": accountUnits(""), "DefaultInstance": matching(instancePattern),
}

// resourceKeys are cgroup settings: limits and weights, which only decide
// how much of the machine a unit gets.
var resourceKeys = map[string]valueCheck{
	"CPUAccounting": boolean, "MemoryAccounting": boolean, "IOAccounting": boolean, "TasksAccounting": boolean,
	"IPAccounting": boolean,
	"CPUWeight":    matching(weightPattern), "StartupCPUWeight": matching(weightPattern),
	"CPUQuota": emptyOr(matching(cpuQuotaPattern)), "CPUQuotaPeriodSec": optionalTimeSpan,
	"AllowedCPUs": emptyOr(matching(cpuSetPattern)), "AllowedMemoryNodes": emptyOr(matching(cpuSetPattern)),
	"MemoryMin": resourceAmount, "MemoryLow": resourceAmount, "MemoryHigh": resourceAmount, "MemoryMax": resourceAmount,
	"MemorySwapMax": resourceAmount, "MemoryZSwapMax": resourceAmount,
	"IOWeight": matching(weightPattern), "StartupIOWeight": matching(weightPattern),
	"IODeviceWeight": emptyOr(matching(ioWeightPattern)), "IOReadBandwidthMax": emptyOr(matching(ioLimitPattern)),
	"IOWriteBandwidthMax": emptyOr(matching(ioLimitPattern)), "IOReadIOPSMax": emptyOr(matching(ioLimitPattern)),
	"IOWriteIOPSMax": emptyOr(matching(ioLimitPattern)),
	"TasksMax":       resourceAmount,
	"ManagedOOMSwap": oneOf("auto", "kill"), "ManagedOOMMemoryPressure": oneOf("auto", "kill"),
}

// sandboxKeys confine a service further. A service that runs as an account
// can't gain anything with them, so they're accepted as written.
var sandboxKeys = map[string]valueCheck{
	"ProtectSystem":  oneOf("yes", "no", "true", "false", "full", "strict"),
	"ProtectHome":    oneOf("yes", "no", "true", "false", "read-only", "tmpfs"),
	"PrivateTmp":     oneOf("yes", "no", "true", "false", "disconnected"),
	"PrivateDevices": boolean, "PrivateNetwork": boolean, "PrivateUsers": oneOf("yes", "no", "true", "false", "self", "identity"),
	"PrivateIPC": boolean, "PrivateMounts": boolean, "ProtectHostname": boolean, "ProtectClock": boolean,
	"ProtectKernelTunables": boolean, "ProtectKernelModules": boolean, "ProtectKernelLogs": boolean,
	"ProtectControlGroups": oneOf("yes", "no", "true", "false", "private", "strict"),
	"ProtectProc":          oneOf("default", "noaccess", "invisible", "ptraceable"),
	"ProcSubset":           oneOf("all", "pid"),
	"RestrictNamespaces":   matching(regexp.MustCompile(`^(yes|no|true|false|~?[a-z]+([ \t]+[a-z]+)*)$`)),
	"RestrictRealtime":     boolean, "RestrictSUIDSGID": boolean, "LockPersonality": boolean,
	"MemoryDenyWriteExecute": boolean, "NoNewPrivileges": boolean, "RemoveIPC": boolean,
	"CapabilityBoundingSet":   matching(capsPattern),
	"SystemCallArchitectures": matching(regexp.MustCompile(`^[a-z0-9_ \t-]*$`)),
	"SystemCallFilter":        matching(syscallPattern), "SystemCallErrorNumber": matching(errnoPattern),
	"RestrictAddressFamilies": emptyOr(matching(addressFamilies)),
	"RestrictFileSystems":     matching(fsListPattern), "RestrictNetworkInterfaces": matching(ifaceListPattern),
	"ReadWritePaths": absolutePaths(true), "ReadOnlyPaths": absolutePaths(true), "InaccessiblePaths": absolutePaths(true),
	"ExecPaths": absolutePaths(true), "NoExecPaths": absolutePaths(true), "TemporaryFileSystem": absolutePaths(true),
	"BindPaths": absolutePaths(true), "BindReadOnlyPaths": absolutePaths(true),
	"SocketBindAllow": emptyOr(matching(bindRuleList)), "SocketBindDeny": emptyOr(matching(bindRuleList)),
	"IPAddressAllow": matching(ipListPattern), "IPAddressDeny": matching(ipListPattern),
	"Delegate": matching(regexp.MustCompile(`^(yes|no|true|false|[a-z]+([ \t]+[a-z]+)*)$`)),
	"UMask":    octalMode,
}

var serviceKeys = map[string]valueCheck{
	"Type":      oneOf("simple", "exec", "notify", "notify-reload", "oneshot", "forking", "idle"),
	"ExecStart": execCommand, "ExecStartPre": execCommand, "ExecStartPost": execCommand,
	"ExecCondition": execCommand, "ExecReload": execCommand, "ExecStop": execCommand, "ExecStopPost": execCommand,
	"User": unitUser, "Group": unitGroup, "DynamicUser": dynamicUser,
	"Slice": unitSlice, "Sockets": accountUnits(".socket"),
	"RuntimeDirectory": serviceDirs, "StateDirectory": serviceDirs, "CacheDirectory": serviceDirs, "LogsDirectory": serviceDirs,
	"RuntimeDirectoryMode": octalMode, "StateDirectoryMode": octalMode, "CacheDirectoryMode": octalMode, "LogsDirectoryMode": octalMode,
	"RuntimeDirectoryPreserve": oneOf("no", "yes", "restart"),
	"WorkingDirectory":         matching(regexp.MustCompile(`^(~|-?/[^%]*)$`)),
	"Environment":              anything, "EnvironmentFile": environmentFile,
	"PassEnvironment": emptyOr(matching(envNamesPattern)), "UnsetEnvironment": emptyOr(matching(envNamesPattern)),
	"Restart":    oneOf("no", "on-success", "on-failure", "on-abnormal", "on-watchdog", "on-abort", "always"),
	"RestartSec": timeSpan, "RestartSteps": intBetween(0, 1<<16), "RestartMaxDelaySec": timeSpan,
	"TimeoutSec": timeSpan, "TimeoutStartSec": timeSpan, "TimeoutStopSec": timeSpan, "TimeoutAbortSec": timeSpan,
	"RuntimeMaxSec": timeSpan, "RuntimeRandomizedExtraSec": timeSpan, "WatchdogSec": timeSpan,
	"RestartPreventExitStatus": emptyOr(matching(exitListPattern)), "RestartForceExitStatus": emptyOr(matching(exitListPattern)),
	"SuccessExitStatus": emptyOr(matching(exitListPattern)),
	"RemainAfterExit":   boolean, "GuessMainPID": boolean,
	"PIDFile":      matching(regexp.MustCompile(`^/run/[A-Za-z0-9_./-]+$`)),
	"NotifyAccess": oneOf("none", "main", "exec", "all"),
	"KillMode":     oneOf("control-group", "mixed", "process"),
	"KillSignal":   signal, "RestartKillSignal": signal, "FinalKillSignal": signal, "WatchdogSignal": signal,
	"ReloadSignal": signal, "SendSIGKILL": boolean, "SendSIGHUP": boolean,
	"LogNamespace": matching(namespacePattern), "SyslogIdentifier": matching(identPattern),
	"SyslogFacility": matching(regexp.MustCompile(`^[a-z0-9]{2,8}$`)),
	"SyslogLevel":    oneOf("emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"),
	"LogLevelMax":    oneOf("emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"),
	"StandardInput":  oneOf("null", "socket"),
	"StandardOutput": oneOf("inherit", "null", "journal", "socket"),
	"StandardError":  oneOf("inherit", "null", "journal", "socket"),
	"Nice":           intBetween(0, 19), "OOMScoreAdjust": intBetween(0, 1000),
	"OOMPolicy":         oneOf("continue", "stop", "kill"),
	"IOSchedulingClass": oneOf("best-effort", "idle", "2", "3"), "IOSchedulingPriority": intBetween(0, 7),
	"CPUSchedulingPolicy": oneOf("other", "batch", "idle"),
	"LimitNOFILE":         rlimit, "LimitNPROC": rlimit, "LimitCORE": rlimit, "LimitAS": rlimit, "LimitDATA": rlimit,
	"LimitFSIZE": rlimit, "LimitSTACK": rlimit, "LimitCPU": rlimit, "LimitMSGQUEUE": rlimit,
	"FileDescriptorStoreMax": intBetween(0, 4096),
}

var socketKeys = map[string]valueCheck{
	"ListenStream": listen, "ListenDatagram": listen, "ListenSequentialPacket": listen,
	"SocketUser": matching(unitNamePart), "SocketGroup": matching(unitNamePart),
	"SocketMode": octalMode, "DirectoryMode": octalMode,
	"Service": accountUnits(".service"), "Accept": boolean, "RemoveOnStop": boolean,
	"MaxConnections": intBetween(1, 1<<20), "MaxConnectionsPerSource": intBetween(1, 1<<20),
	"Backlog": intBetween(1, 1<<20), "FileDescriptorName": matching(descriptorName),
	"BindIPv6Only": oneOf("default", "both", "ipv6-only"), "KeepAlive": boolean, "NoDelay": boolean,
	"ReusePort": boolean, "PassCredentials": boolean, "PassSecurity": boolean, "DeferAcceptSec": timeSpan,
	"TriggerLimitIntervalSec": timeSpan, "TriggerLimitBurst": intBetween(0, 1<<16),
	"PollLimitIntervalSec": timeSpan, "PollLimitBurst": intBetween(0, 1<<16),
}

var timerKeys = map[string]valueCheck{
	"OnCalendar": matching(calendarPattern), "OnActiveSec": timeSpan, "OnBootSec": timeSpan,
	"OnStartupSec": timeSpan, "OnUnitActiveSec": timeSpan, "OnUnitInactiveSec": timeSpan,
	"AccuracySec": timeSpan, "RandomizedDelaySec": timeSpan, "FixedRandomDelay": boolean,
	"Persistent": boolean, "WakeSystem": boolean, "RemainAfterElapse": boolean,
	"OnClockChange": boolean, "OnTimezoneChange": boolean, "Unit": accountUnits(".service"),
}

var pathKeys = map[string]valueCheck{
	"PathExists": absolutePaths(false), "PathExistsGlob": absolutePaths(false), "PathChanged": absolutePaths(false),
	"PathModified": absolutePaths(false), "DirectoryNotEmpty": absolutePaths(false),
	"Unit": accountUnits(".service"), "TriggerLimitIntervalSec": timeSpan, "TriggerLimitBurst": intBetween(0, 1<<16),
}

var mountKeys = map[string]valueCheck{
	"What": mountWhat, "Where": mountWhere, "Type": mountType, "Options": mountOptionsValue,
	"SloppyOptions": boolean, "LazyUnmount": boolean, "ReadWriteOnly": boolean, "ForceUnmount": boolean,
	"DirectoryMode": octalMode, "TimeoutSec": timeSpan,
}

var automountKeys = map[string]valueCheck{
	"Where": mountWhere, "DirectoryMode": octalMode, "TimeoutIdleSec": timeSpan,
}

func merged(tables ...map[string]valueCheck) map[string]valueCheck {
	out := map[string]valueCheck{}
	for _, t := range tables {
		for k, v := range t {
			out[k] = v
		}
	}
	return out
}

var unitSections = map[string]map[string]map[string]valueCheck{
	"service":   {"Unit": unitKeys, "Service": merged(serviceKeys, sandboxKeys, resourceKeys), "Install": installKeys},
	"socket":    {"Unit": unitKeys, "Socket": socketKeys, "Install": installKeys},
	"timer":     {"Unit": unitKeys, "Timer": timerKeys, "Install": installKeys},
	"path":      {"Unit": unitKeys, "Path": pathKeys, "Install": installKeys},
	"slice":     {"Unit": unitKeys, "Slice": resourceKeys, "Install": installKeys},
	"target":    {"Unit": unitKeys, "Install": installKeys},
	"mount":     {"Unit": unitKeys, "Mount": mountKeys, "Install": installKeys},
	"automount": {"Unit": unitKeys, "Automount": automountKeys, "Install": installKeys},
}

// dropInTuning are the service settings a drop-in may change besides
// resources: how hard it may work, how it restarts and how long it waits.
var dropInTuning = map[string]valueCheck{
	"Slice": unitSlice, "LimitNOFILE": rlimit, "LimitNPROC": rlimit,
	"Restart":    oneOf("no", "on-success", "on-failure", "on-abnormal", "on-watchdog", "on-abort", "always"),
	"RestartSec": timeSpan, "TimeoutStartSec": timeSpan, "TimeoutStopSec": timeSpan, "OOMPolicy": oneOf("continue", "stop", "kill"),
}

var dropInSections = map[string]map[string]valueCheck{
	"Unit": {
		"After": dependencies, "Before": dependencies, "Wants": dependencies, "Requires": dependencies,
		"BindsTo": dependencies, "PartOf": dependencies,
		"RequiresMountsFor": absolutePaths(false), "WantsMountsFor": absolutePaths(false),
	},
	"Service": merged(resourceKeys, dropInTuning),
	"Socket":  resourceKeys,
	"Slice":   resourceKeys,
	"Mount":   resourceKeys,
}
