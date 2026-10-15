package sim

import (
	"bytes"
	"context"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

func (m *Machine) seedNetwork() {
	m.ifaces = []system.Interface{
		{Name: "lo", Index: 1, MTU: 65536, Type: "loopback", Up: true, Carrier: true,
			Addresses: []system.Address{{CIDR: "127.0.0.1/8", Scope: "host"}, {CIDR: "::1/128", Scope: "host"}}},
		{Name: "enp1s0", Index: 2, MAC: "52:54:00:12:34:56", MTU: 1500, Type: "ether", Up: true, Carrier: true, SpeedMbps: 1000,
			Addresses: []system.Address{
				{CIDR: "203.0.113.10/24", Scope: "global"},
				{CIDR: "2001:db8::10/64", Scope: "global"},
				{CIDR: "fe80::5054:ff:fe12:3456/64", Scope: "link"},
			}},
	}
	m.routes = []system.Route{
		{Destination: "0.0.0.0/0", Gateway: "203.0.113.1", Interface: "enp1s0", Metric: 100},
		{Destination: "203.0.113.0/24", Interface: "enp1s0", Metric: 100},
		{Destination: "::/0", Gateway: "fe80::1", Interface: "enp1s0", Metric: 1024},
		{Destination: "2001:db8::/64", Interface: "enp1s0", Metric: 256},
	}
}

type simFirewall struct{ m *Machine }

// checkRuleset does the structural checks nft would.
func checkRuleset(ruleset []byte) error {
	_, err := system.CheckRuleset(ruleset)
	return err
}

func (f simFirewall) Check(ctx context.Context, ruleset []byte) error {
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("firewall", "check", ""); err != nil {
		return err
	}
	return checkRuleset(ruleset)
}

func (f simFirewall) Apply(ctx context.Context, ruleset []byte) error {
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("firewall", "apply", ""); err != nil {
		return err
	}
	if err := checkRuleset(ruleset); err != nil {
		return err
	}
	m.ruleset = bytes.Clone(ruleset)
	// Sets that still exist keep their elements, as with a real reload of
	// unchanged sets; sets that disappeared are dropped.
	for key := range m.sets {
		if !m.rulesetHasSet(key) {
			delete(m.sets, key)
		}
	}
	return nil
}

func (f simFirewall) Ruleset(ctx context.Context) ([]byte, error) {
	m := f.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	return bytes.Clone(m.ruleset), nil
}

func setKey(s system.SetRef) string { return s.Family + " " + s.Table + " " + s.Set }

// rulesetHasSet reports whether the applied ruleset declares the set. The
// caller holds m.mu.
func (m *Machine) rulesetHasSet(key string) bool {
	parts := strings.Fields(key)
	if len(parts) != 3 {
		return false
	}
	table := regexp.MustCompile(`table\s+` + regexp.QuoteMeta(parts[0]) + `\s+` + regexp.QuoteMeta(parts[1]) + `\b`)
	set := regexp.MustCompile(`\bset\s+` + regexp.QuoteMeta(parts[2]) + `\s*\{`)
	return table.Match(m.ruleset) && set.Match(m.ruleset)
}

func validElement(v string) bool {
	if _, err := netip.ParseAddr(v); err == nil {
		return true
	}
	_, err := netip.ParsePrefix(v)
	return err == nil
}

func (f simFirewall) AddElements(ctx context.Context, set system.SetRef, elems ...system.SetElement) error {
	if !set.Valid() {
		return invalid("set %s", setKey(set))
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("firewall", "add_elements", setKey(set)); err != nil {
		return err
	}
	key := setKey(set)
	if !m.rulesetHasSet(key) {
		return notFound("set " + key)
	}
	if m.sets[key] == nil {
		m.sets[key] = map[string]time.Time{}
	}
	for _, e := range elems {
		if !validElement(e.Value) {
			return invalid("set element %q", e.Value)
		}
		var expires time.Time
		if e.Timeout > 0 {
			expires = m.now().Add(e.Timeout)
		}
		m.sets[key][e.Value] = expires
	}
	return nil
}

func (f simFirewall) DeleteElements(ctx context.Context, set system.SetRef, elems ...system.SetElement) error {
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("firewall", "delete_elements", setKey(set)); err != nil {
		return err
	}
	key := setKey(set)
	if !m.rulesetHasSet(key) {
		return notFound("set " + key)
	}
	for _, e := range elems {
		delete(m.sets[key], e.Value)
	}
	return nil
}

func (f simFirewall) Elements(ctx context.Context, set system.SetRef) ([]system.SetElement, error) {
	m := f.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	key := setKey(set)
	if !m.rulesetHasSet(key) {
		return nil, notFound("set " + key)
	}
	now := m.now()
	var out []system.SetElement
	for v, expires := range m.sets[key] {
		if !expires.IsZero() && !expires.After(now) {
			delete(m.sets[key], v)
			continue
		}
		e := system.SetElement{Value: v}
		if !expires.IsZero() {
			e.Timeout = expires.Sub(now)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out, nil
}

type simNetwork struct{ m *Machine }

func (n simNetwork) Interfaces(ctx context.Context) ([]system.Interface, error) {
	m := n.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	out := make([]system.Interface, len(m.ifaces))
	for i, ifc := range m.ifaces {
		ifc.Addresses = slices.Clone(ifc.Addresses)
		out[i] = ifc
	}
	return out, nil
}

func (n simNetwork) Routes(ctx context.Context) ([]system.Route, error) {
	m := n.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	return slices.Clone(m.routes), nil
}

var networkdFilePattern = regexp.MustCompile(`^50-racksail[A-Za-z0-9._-]*\.(network|netdev|link)$`)

func (n simNetwork) ApplyConfig(ctx context.Context, files map[string][]byte) error {
	for name := range files {
		if !networkdFilePattern.MatchString(name) {
			return invalid("networkd file name %q must start with 50-racksail", name)
		}
	}
	m := n.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("network", "apply_config", ""); err != nil {
		return err
	}
	for p := range m.files {
		if path.Dir(p) == system.NetworkdDir && networkdFilePattern.MatchString(path.Base(p)) {
			delete(m.files, p)
		}
	}
	for name, data := range files {
		if err := m.writeFile(path.Join(system.NetworkdDir, name), data, 0o644, system.Owner{}); err != nil {
			return err
		}
	}
	return nil
}

func (n simNetwork) Config(ctx context.Context) (map[string][]byte, error) {
	m := n.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	out := map[string][]byte{}
	for p, f := range m.files {
		if path.Dir(p) == system.NetworkdDir && networkdFilePattern.MatchString(path.Base(p)) {
			out[path.Base(p)] = bytes.Clone(f.data)
		}
	}
	return out, nil
}

func (n simNetwork) Hostname(ctx context.Context) (string, error) {
	m := n.m
	if err := m.lock(ctx); err != nil {
		return "", err
	}
	defer m.mu.Unlock()
	return m.hostname, nil
}

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (n simNetwork) SetHostname(ctx context.Context, name string) error {
	if !hostnamePattern.MatchString(name) {
		return invalid("hostname %q", name)
	}
	m := n.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("network", "set_hostname", name); err != nil {
		return err
	}
	m.hostname = name
	m.facts.Hostname = name
	m.sysctl["kernel.hostname"] = name
	return m.writeFile("/etc/hostname", []byte(name+"\n"), 0o644, system.Owner{})
}

type simSysctl struct{ m *Machine }

func (s simSysctl) Get(ctx context.Context, key string) (string, error) {
	if !system.ValidSysctlKey(key) {
		return "", invalid("sysctl key %q", key)
	}
	m := s.m
	if err := m.lock(ctx); err != nil {
		return "", err
	}
	defer m.mu.Unlock()
	v, ok := m.sysctl[key]
	if !ok {
		return "", notFound("sysctl " + key)
	}
	return v, nil
}

func (s simSysctl) Set(ctx context.Context, key, value string) error {
	if !system.ValidSysctlKey(key) || strings.ContainsAny(value, "\n\x00") {
		return invalid("sysctl %s=%q", key, value)
	}
	m := s.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("sysctl", "set", key); err != nil {
		return err
	}
	m.sysctl[key] = value
	return nil
}

type simProcesses struct{ m *Machine }

func (p simProcesses) List(ctx context.Context, filter system.ProcessFilter) ([]system.Process, error) {
	m := p.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	var out []system.Process
	for _, proc := range m.procs {
		if filter.User != "" && proc.User != filter.User {
			continue
		}
		if filter.Unit != "" && proc.Unit != filter.Unit {
			continue
		}
		c := *proc
		c.Args = slices.Clone(proc.Args)
		c.CPUTime = m.now().Sub(proc.Started) / 200
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

func (p simProcesses) Signal(ctx context.Context, pid int, sig system.Signal) error {
	if !sig.Valid() {
		return invalid("signal %q", sig)
	}
	m := p.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("processes", "signal", string(sig)); err != nil {
		return err
	}
	if pid == 1 {
		return invalid("refusing to signal PID 1")
	}
	if _, ok := m.procs[pid]; !ok {
		return notFound("process")
	}
	m.signal(pid, sig)
	return nil
}
