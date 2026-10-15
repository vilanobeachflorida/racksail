package system

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// GeneratedHeader starts every configuration file Racksail renders.
const GeneratedHeader = "# Managed by Racksail. Changes made here are overwritten."

// UnitFile is a systemd unit file: sections of directives in order.
type UnitFile struct {
	Sections []UnitSection
}

// UnitSection is one [Section] of a unit file.
type UnitSection struct {
	Name    string
	Entries []UnitEntry
}

// UnitEntry is one Key=Value line.
type UnitEntry struct {
	Key   string
	Value string
}

// Section returns the named section, adding it at the end when missing.
func (u *UnitFile) Section(name string) *UnitSection {
	for i := range u.Sections {
		if u.Sections[i].Name == name {
			return &u.Sections[i]
		}
	}
	u.Sections = append(u.Sections, UnitSection{Name: name})
	return &u.Sections[len(u.Sections)-1]
}

// Add appends one line per value with the same key.
func (s *UnitSection) Add(key string, values ...string) *UnitSection {
	for _, v := range values {
		s.Entries = append(s.Entries, UnitEntry{Key: key, Value: v})
	}
	return s
}

// AddEntries appends entries in order.
func (s *UnitSection) AddEntries(entries ...UnitEntry) *UnitSection {
	s.Entries = append(s.Entries, entries...)
	return s
}

var (
	unitKeyPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	unitSectionPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)
)

// Render returns the text of the unit file. It refuses keys and values that
// systemd would read differently from what was meant, such as values with
// line breaks or a trailing backslash.
func (u *UnitFile) Render() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(GeneratedHeader)
	b.WriteByte('\n')
	for _, s := range u.Sections {
		if !unitSectionPattern.MatchString(s.Name) {
			return nil, fmt.Errorf("unit section %q: %w", s.Name, ErrInvalid)
		}
		fmt.Fprintf(&b, "\n[%s]\n", s.Name)
		for _, e := range s.Entries {
			if !unitKeyPattern.MatchString(e.Key) {
				return nil, fmt.Errorf("unit key %q: %w", e.Key, ErrInvalid)
			}
			if strings.ContainsAny(e.Value, "\r\n\x00") || strings.HasSuffix(e.Value, `\`) {
				return nil, fmt.Errorf("value of %s: %w", e.Key, ErrInvalid)
			}
			fmt.Fprintf(&b, "%s=%s\n", e.Key, e.Value)
		}
	}
	return b.Bytes(), nil
}

var (
	// bindRulePattern is systemd's SocketBindAllow syntax: an optional
	// address family and protocol, then a port, a port range or "any". It
	// only covers IP sockets; Unix sockets are guarded by file permissions.
	bindRulePattern = regexp.MustCompile(`^((ipv4|ipv6):)?((tcp|udp):)?(any|[0-9]{1,5}(-[0-9]{1,5})?)$`)
	ipDenyPattern   = regexp.MustCompile(`^(any|localhost|link-local|multicast|[0-9a-fA-F.:]+(/[0-9]{1,3})?)$`)
)

// Sandbox describes the confinement of an account workload such as a site or
// a worker. Its entries always include the minimum settings required by the
// security rules; nothing here can turn them off.
type Sandbox struct {
	User  string
	Group string
	// BindPaths are bound into the sandbox read-write: the account's own data.
	BindPaths []string
	// BindReadOnlyPaths are bound read-only: the runtime directory.
	BindReadOnlyPaths []string
	// SocketBindAllow lists what the workload may bind, in systemd's syntax
	// ("tcp:8080"). Everything else is denied.
	SocketBindAllow []string
	// IPAddressDeny blocks addresses the workload must not reach.
	IPAddressDeny []string
	// AllowWriteExecute turns off MemoryDenyWriteExecute for runtimes with a
	// JIT compiler. It has no effect with Legacy.
	AllowWriteExecute bool
	// Legacy applies the strictest profile, for end-of-life runtimes.
	Legacy bool
}

// Entries returns the [Service] directives that confine the workload.
func (s Sandbox) Entries() ([]UnitEntry, error) {
	if s.User == "" || s.User == "root" || s.Group == "root" {
		return nil, fmt.Errorf("sandbox needs a non-root user: %w", ErrInvalid)
	}
	for _, p := range append(append([]string{}, s.BindPaths...), s.BindReadOnlyPaths...) {
		if !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, " \t\r\n:") {
			return nil, fmt.Errorf("sandbox path %q: %w", p, ErrInvalid)
		}
	}
	for _, v := range s.SocketBindAllow {
		if !bindRulePattern.MatchString(v) {
			return nil, fmt.Errorf("socket bind rule %q: %w", v, ErrInvalid)
		}
	}
	for _, v := range s.IPAddressDeny {
		if !ipDenyPattern.MatchString(v) {
			return nil, fmt.Errorf("denied address %q: %w", v, ErrInvalid)
		}
	}
	e := []UnitEntry{{"User", s.User}}
	if s.Group != "" {
		e = append(e, UnitEntry{"Group", s.Group})
	}
	e = append(e,
		UnitEntry{"ProtectSystem", "strict"},
		UnitEntry{"ProtectHome", "tmpfs"},
		UnitEntry{"PrivateTmp", "yes"},
		UnitEntry{"PrivateDevices", "yes"},
		UnitEntry{"NoNewPrivileges", "yes"},
		UnitEntry{"ProtectKernelTunables", "yes"},
		UnitEntry{"ProtectKernelModules", "yes"},
		UnitEntry{"ProtectKernelLogs", "yes"},
		UnitEntry{"ProtectControlGroups", "yes"},
		UnitEntry{"ProtectClock", "yes"},
		UnitEntry{"ProtectHostname", "yes"},
		UnitEntry{"RestrictSUIDSGID", "yes"},
		UnitEntry{"RestrictNamespaces", "yes"},
		UnitEntry{"RestrictRealtime", "yes"},
		UnitEntry{"LockPersonality", "yes"},
		UnitEntry{"CapabilityBoundingSet", ""},
		UnitEntry{"SystemCallArchitectures", "native"},
		UnitEntry{"SystemCallFilter", "@system-service"},
		// The agent and panel sockets live here.
		UnitEntry{"InaccessiblePaths", "-" + RunDir},
	)
	if !s.AllowWriteExecute || s.Legacy {
		e = append(e, UnitEntry{"MemoryDenyWriteExecute", "yes"})
	}
	if s.Legacy {
		e = append(e,
			UnitEntry{"ProtectProc", "invisible"},
			UnitEntry{"ProcSubset", "pid"},
			UnitEntry{"RestrictAddressFamilies", "AF_UNIX AF_INET AF_INET6"},
			UnitEntry{"RemoveIPC", "yes"},
			UnitEntry{"UMask", "0027"},
		)
	}
	for _, p := range s.BindPaths {
		e = append(e, UnitEntry{"BindPaths", p})
	}
	for _, p := range s.BindReadOnlyPaths {
		e = append(e, UnitEntry{"BindReadOnlyPaths", p})
	}
	e = append(e, UnitEntry{"SocketBindDeny", "any"})
	for _, v := range s.SocketBindAllow {
		e = append(e, UnitEntry{"SocketBindAllow", v})
	}
	for _, v := range s.IPAddressDeny {
		e = append(e, UnitEntry{"IPAddressDeny", v})
	}
	return e, nil
}
