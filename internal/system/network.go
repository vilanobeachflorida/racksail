package system

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
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

// RulesetTable names a table in a ruleset, with the sets and maps it
// declares.
type RulesetTable struct {
	Family string
	Name   string
	Sets   []string
}

var (
	nftFamilies             = []string{"inet", "ip", "ip6", "bridge", "netdev", "arp"}
	rulesetTableNamePattern = regexp.MustCompile(`^racksail[A-Za-z0-9_-]{0,56}$`)
	// nftDirectives read other files, or define variables that could hide
	// what a statement says from this check.
	nftDirectives = []string{"include", "define", "redefine", "undefine"}
)

// nftToken is a word, a quoted string, a brace or the end of a statement
// (a semicolon or a line break).
type nftToken struct {
	kind byte
	text string
	line int
}

const (
	nftWord   = 'w'
	nftString = 's'
	nftOpen   = '{'
	nftClose  = '}'
	nftEnd    = ';'
)

// nftBreaks end a word.
const nftBreaks = " \t\n#\"{};"

// nftTokens splits a ruleset the way nft's scanner does, as far as telling
// statements and blocks apart goes: # starts a comment outside strings,
// strings are double-quoted on one line, and a line break or semicolon ends
// a statement. Backslashes and variables are refused outside strings, since
// nothing Racksail renders has them.
func nftTokens(ruleset []byte) ([]nftToken, error) {
	if !utf8.Valid(ruleset) {
		return nil, fmt.Errorf("the ruleset isn't valid UTF-8: %w", ErrInvalid)
	}
	var out []nftToken
	line := 1
	for i := 0; i < len(ruleset); {
		c := ruleset[i]
		switch {
		case c == '\n':
			out = append(out, nftToken{kind: nftEnd, line: line})
			line++
			i++
		case c == ' ' || c == '\t':
			i++
		case c == '#':
			for i < len(ruleset) && ruleset[i] != '\n' {
				i++
			}
		case c == '"':
			end := bytes.IndexAny(ruleset[i+1:], "\"\n")
			if end < 0 || ruleset[i+1+end] != '"' {
				return nil, fmt.Errorf("line %d: a string isn't closed: %w", line, ErrInvalid)
			}
			out = append(out, nftToken{kind: nftString, text: string(ruleset[i+1 : i+1+end]), line: line})
			i += end + 2
		case c == '{' || c == '}' || c == ';':
			out = append(out, nftToken{kind: c, line: line})
			i++
		case c < 0x20 || c == 0x7f:
			return nil, fmt.Errorf("line %d: the ruleset has the control character %U: %w", line, rune(c), ErrInvalid)
		default:
			start := i
			for i < len(ruleset) && strings.IndexByte(nftBreaks, ruleset[i]) < 0 && ruleset[i] >= 0x20 && ruleset[i] != 0x7f {
				i++
			}
			word := string(ruleset[start:i])
			if strings.ContainsAny(word, "\\$") {
				return nil, fmt.Errorf("line %d: %q: backslashes and variables aren't allowed: %w", line, word, ErrInvalid)
			}
			out = append(out, nftToken{kind: nftWord, text: word, line: line})
		}
	}
	return out, nil
}

// CheckRuleset holds a ruleset to what Racksail renders before any
// Firewall hands it to nftables: nothing but table blocks, each defining a
// table whose name starts with "racksail", so applying it can't touch
// another program's tables or the ruleset as a whole. Statements outside a
// table block, such as flush ruleset or a rule added to someone else's
// chain, are refused, and so are include and define anywhere, since nft
// would read other files for one and quote what it read in its errors. It
// returns the tables the ruleset defines.
func CheckRuleset(ruleset []byte) ([]RulesetTable, error) {
	if len(bytes.TrimSpace(ruleset)) == 0 {
		return nil, fmt.Errorf("the ruleset is empty: %w", ErrInvalid)
	}
	tokens, err := nftTokens(ruleset)
	if err != nil {
		return nil, err
	}
	var tables []RulesetTable
	var statement []nftToken
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch t.kind {
		case nftEnd:
			if len(statement) > 0 {
				return nil, fmt.Errorf("line %d: %q isn't a table block; a ruleset only defines Racksail's tables: %w",
					statement[0].line, statementText(statement), ErrInvalid)
			}
		case nftClose:
			return nil, fmt.Errorf("line %d: unbalanced braces in the ruleset: %w", t.line, ErrInvalid)
		case nftOpen:
			table, err := tableHeader(statement, t.line)
			if err != nil {
				return nil, err
			}
			end, sets, err := tableBlock(tokens, i)
			if err != nil {
				return nil, err
			}
			table.Sets = sets
			tables = append(tables, table)
			statement = nil
			i = end
			if i+1 < len(tokens) && tokens[i+1].kind != nftEnd {
				return nil, fmt.Errorf("line %d: the table %s %s ends without a line break: %w", tokens[i+1].line, table.Family, table.Name, ErrInvalid)
			}
		default:
			statement = append(statement, t)
		}
	}
	if len(statement) > 0 {
		return nil, fmt.Errorf("line %d: %q isn't a table block; a ruleset only defines Racksail's tables: %w",
			statement[0].line, statementText(statement), ErrInvalid)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("the ruleset defines no table: %w", ErrInvalid)
	}
	return tables, nil
}

// tableHeader checks what comes before a block at the top of a ruleset:
// "table <family> <name>", and nothing else.
func tableHeader(statement []nftToken, line int) (RulesetTable, error) {
	if len(statement) != 3 || statement[0].kind != nftWord || statement[0].text != "table" {
		return RulesetTable{}, fmt.Errorf("line %d: %q isn't a table; a ruleset only defines Racksail's tables: %w",
			line, statementText(statement), ErrInvalid)
	}
	family, name := statement[1], statement[2]
	if family.kind != nftWord || !slices.Contains(nftFamilies, family.text) {
		return RulesetTable{}, fmt.Errorf("line %d: unknown table family %q: %w", line, family.text, ErrInvalid)
	}
	if name.kind != nftWord || !rulesetTableNamePattern.MatchString(name.text) {
		return RulesetTable{}, fmt.Errorf("line %d: table %s %s is not Racksail's: %w", line, family.text, name.text, ErrInvalid)
	}
	return RulesetTable{Family: family.text, Name: name.text}, nil
}

// tableBlock checks the block of a table, which opens at tokens[open], and
// returns where it closes and the sets and maps declared right inside it.
func tableBlock(tokens []nftToken, open int) (int, []string, error) {
	depth := 0
	var sets []string
	var statement []nftToken
	for i := open; i < len(tokens); i++ {
		t := tokens[i]
		switch t.kind {
		case nftOpen:
			if depth == 1 && len(statement) == 2 && statement[1].kind == nftWord &&
				(statement[0].text == "set" || statement[0].text == "map") {
				sets = append(sets, statement[1].text)
			}
			depth++
			statement = nil
		case nftClose:
			depth--
			statement = nil
			if depth == 0 {
				return i, sets, nil
			}
		case nftEnd:
			statement = nil
		case nftWord:
			if slices.Contains(nftDirectives, t.text) {
				return 0, nil, fmt.Errorf("line %d: %s isn't allowed in a ruleset: %w", t.line, t.text, ErrInvalid)
			}
			statement = append(statement, t)
		default:
			statement = append(statement, t)
		}
	}
	return 0, nil, fmt.Errorf("unbalanced braces in the ruleset: %w", ErrInvalid)
}

func statementText(statement []nftToken) string {
	words := make([]string, len(statement))
	for i, t := range statement {
		words[i] = t.text
		if t.kind == nftString {
			words[i] = `"` + t.text + `"`
		}
	}
	text := strings.Join(words, " ")
	if len(text) > 80 {
		text = text[:80] + "..."
	}
	return text
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
