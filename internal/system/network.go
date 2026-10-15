package system

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Firewall applies nftables rulesets. Rulesets are rendered as nft syntax by
// the network module and may only define tables whose names start with
// "racksail"; applying one replaces all of those tables in one transaction.
type Firewall interface {
	// Check validates a ruleset without applying it.
	Check(ctx context.Context, ruleset []byte) error
	// Apply atomically replaces Racksail's tables with ruleset.
	Apply(ctx context.Context, ruleset []byte) error
	// Ruleset returns Racksail's tables as currently applied.
	Ruleset(ctx context.Context) ([]byte, error)
	// AddElements adds addresses to a set, such as the ban list, without
	// touching the rules.
	AddElements(ctx context.Context, set SetRef, elems ...SetElement) error
	DeleteElements(ctx context.Context, set SetRef, elems ...SetElement) error
	Elements(ctx context.Context, set SetRef) ([]SetElement, error)
}

var rulesetTablePattern = regexp.MustCompile(`(?m)^\s*(?:add\s+|create\s+|delete\s+|flush\s+)?table\s+(\S+)\s+(\S+)`)

// RulesetTable names a table in a ruleset.
type RulesetTable struct {
	Family string
	Name   string
}

// CheckRuleset does the structural checks every Firewall applies before
// handing a ruleset to nftables: it isn't empty, its braces balance and it
// only touches tables whose names start with "racksail". It returns the
// tables the ruleset names.
func CheckRuleset(ruleset []byte) ([]RulesetTable, error) {
	if len(bytes.TrimSpace(ruleset)) == 0 {
		return nil, fmt.Errorf("the ruleset is empty: %w", ErrInvalid)
	}
	depth := 0
	for _, c := range ruleset {
		switch c {
		case '{':
			depth++
		case '}':
			depth--
		}
		if depth < 0 {
			break
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced braces in the ruleset: %w", ErrInvalid)
	}
	var tables []RulesetTable
	for _, m := range rulesetTablePattern.FindAllSubmatch(ruleset, -1) {
		t := RulesetTable{Family: string(m[1]), Name: strings.TrimSuffix(string(m[2]), "{")}
		switch t.Family {
		case "inet", "ip", "ip6", "bridge", "netdev", "arp":
		default:
			return nil, fmt.Errorf("unknown table family %q: %w", t.Family, ErrInvalid)
		}
		if !strings.HasPrefix(t.Name, "racksail") {
			return nil, fmt.Errorf("table %s %s is not Racksail's: %w", t.Family, t.Name, ErrInvalid)
		}
		tables = append(tables, t)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("the ruleset defines no table: %w", ErrInvalid)
	}
	return tables, nil
}

// SetRef names an nftables set.
type SetRef struct {
	Family string `json:"family"`
	Table  string `json:"table"`
	Set    string `json:"set"`
}

// SetElement is an address or prefix in a set, optionally expiring.
type SetElement struct {
	Value   string        `json:"value"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

var nftNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// Valid reports whether the set reference names a set Racksail may change.
func (s SetRef) Valid() bool {
	switch s.Family {
	case "inet", "ip", "ip6":
	default:
		return false
	}
	return nftNamePattern.MatchString(s.Table) && nftNamePattern.MatchString(s.Set) && len(s.Table) >= 8 && s.Table[:8] == "racksail"
}

// Network reads interfaces and routes and applies systemd-networkd
// configuration rendered by the network module.
type Network interface {
	Interfaces(ctx context.Context) ([]Interface, error)
	Routes(ctx context.Context) ([]Route, error)
	// ApplyConfig replaces the networkd files Racksail manages (names starting
	// with "50-racksail") with files, keyed by file name, and reloads
	// networkd. Rolling back after a failed connectivity check is up to the
	// caller, which keeps the previous files from Config.
	ApplyConfig(ctx context.Context, files map[string][]byte) error
	Config(ctx context.Context) (map[string][]byte, error)
	Hostname(ctx context.Context) (string, error)
	SetHostname(ctx context.Context, name string) error
}

// Interface is a network interface.
type Interface struct {
	Name      string    `json:"name"`
	Index     int       `json:"index"`
	MAC       string    `json:"mac,omitempty"`
	MTU       int       `json:"mtu"`
	Type      string    `json:"type" doc:"ether, loopback, bond, vlan, bridge, wireguard or other"`
	Up        bool      `json:"up"`
	Carrier   bool      `json:"carrier"`
	SpeedMbps int       `json:"speedMbps,omitempty"`
	Addresses []Address `json:"addresses,omitempty"`
	// Master is the bond or bridge the interface belongs to.
	Master string `json:"master,omitempty"`
}

// Address is an IP address with its prefix length, such as 203.0.113.10/24.
type Address struct {
	CIDR    string `json:"cidr"`
	Scope   string `json:"scope,omitempty" doc:"global, link or host"`
	Dynamic bool   `json:"dynamic,omitempty" doc:"Assigned by DHCP or router advertisements"`
}

// Route is an entry of a routing table.
type Route struct {
	Destination string `json:"destination" doc:"A prefix such as 0.0.0.0/0"`
	Gateway     string `json:"gateway,omitempty"`
	Interface   string `json:"interface,omitempty"`
	Metric      int    `json:"metric,omitempty"`
	Table       int    `json:"table,omitempty"`
}

// Sysctl reads and sets kernel parameters at runtime. Persistent values are
// files in /etc/sysctl.d written through Files.
type Sysctl interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

var sysctlKeyPattern = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_-]+)+$`)

// ValidSysctlKey reports whether key looks like net.core.somaxconn.
func ValidSysctlKey(key string) bool { return sysctlKeyPattern.MatchString(key) }

// Processes lists and signals processes.
type Processes interface {
	List(ctx context.Context, filter ProcessFilter) ([]Process, error)
	Signal(ctx context.Context, pid int, sig Signal) error
}

// ProcessFilter narrows a process list. Empty fields match everything.
type ProcessFilter struct {
	User string `json:"user,omitempty"`
	Unit string `json:"unit,omitempty"`
}

// Process is a running process.
type Process struct {
	PID      int           `json:"pid"`
	PPID     int           `json:"ppid"`
	User     string        `json:"user"`
	UID      int           `json:"uid"`
	Command  string        `json:"command"`
	Args     []string      `json:"args,omitempty"`
	State    string        `json:"state"`
	RSSBytes int64         `json:"rssBytes"`
	CPUTime  time.Duration `json:"cpuTime"`
	Started  time.Time     `json:"started,omitzero"`
	// Unit is the systemd unit the process belongs to.
	Unit string `json:"unit,omitempty"`
}
