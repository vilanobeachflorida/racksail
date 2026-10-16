package system

import (
	"bytes"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// NetworkConfig is the systemd-networkd configuration Racksail manages:
// virtual devices (bonds, bridges, VLANs) and the networks on interfaces,
// rendered as 50-racksail-*.netdev and .network files.
type NetworkConfig struct {
	NetDevs  []NetDev      `json:"netdevs,omitempty"`
	Networks []NetworkFile `json:"networks,omitempty"`
}

// NetDev is a virtual network device.
type NetDev struct {
	Name string `json:"name" doc:"Interface name, such as bond0, br0 or vlan100"`
	// Kind is bond, bridge, vlan or dummy.
	Kind       string `json:"kind" enum:"bond,bridge,vlan,dummy"`
	MTU        int    `json:"mtu,omitempty"`
	MACAddress string `json:"macAddress,omitempty"`
	// VLANID is the tag of a VLAN.
	VLANID int `json:"vlanId,omitempty"`
	// BondMode is active-backup, 802.3ad, balance-alb, balance-tlb,
	// balance-rr, balance-xor or broadcast.
	BondMode string `json:"bondMode,omitempty"`
	// TransmitHashPolicy is layer2, layer3+4 or layer2+3 for 802.3ad and
	// balance-xor bonds.
	TransmitHashPolicy string `json:"transmitHashPolicy,omitempty"`
	// STP turns on the spanning tree protocol for a bridge.
	STP bool `json:"stp,omitempty"`
}

// NetworkFile configures the network on the interfaces it matches.
type NetworkFile struct {
	// Name names the file, 50-racksail-<name>.network.
	Name string `json:"name"`
	// MatchName and MatchMAC select the interfaces; at least one is needed.
	MatchName string `json:"matchName,omitempty" doc:"Interface name or glob"`
	MatchMAC  string `json:"matchMac,omitempty"`
	// DHCP is no, yes, ipv4 or ipv6.
	DHCP         string `json:"dhcp,omitempty"`
	IPv6AcceptRA *bool  `json:"ipv6AcceptRa,omitempty"`
	// Addresses are static addresses with their prefix length.
	Addresses []string `json:"addresses,omitempty" doc:"Addresses such as 203.0.113.10/24"`
	// Gateways are default gateways, IPv4 and IPv6.
	Gateways []string        `json:"gateways,omitempty"`
	DNS      []string        `json:"dns,omitempty"`
	Domains  []string        `json:"domains,omitempty"`
	Routes   []NetworkRoute  `json:"routes,omitempty"`
	Rules    []RoutingPolicy `json:"rules,omitempty" doc:"Policy routing rules"`
	// VLANs, Bond and Bridge attach the interface to virtual devices.
	VLANs  []string `json:"vlans,omitempty"`
	Bond   string   `json:"bond,omitempty"`
	Bridge string   `json:"bridge,omitempty"`
	MTU    int      `json:"mtu,omitempty"`
	// Optional interfaces don't hold up network-online.target.
	Optional bool `json:"optional,omitempty"`
}

// NetworkRoute is a static route.
type NetworkRoute struct {
	Destination string `json:"destination" doc:"A prefix such as 198.51.100.0/24"`
	Gateway     string `json:"gateway,omitempty"`
	// GatewayOnLink uses a gateway outside the interface's subnets, as some
	// cloud providers require.
	GatewayOnLink bool   `json:"gatewayOnLink,omitempty"`
	Table         int    `json:"table,omitempty"`
	Metric        int    `json:"metric,omitempty"`
	Source        string `json:"source,omitempty" doc:"Preferred source address"`
}

// RoutingPolicy sends traffic matching it to a routing table, such as
// traffic from an IP that has its own gateway.
type RoutingPolicy struct {
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	FwMark   uint32 `json:"fwMark,omitempty"`
	Table    int    `json:"table"`
	Priority int    `json:"priority,omitempty"`
}

var (
	ifaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
	ifaceGlobPattern = regexp.MustCompile(`^[A-Za-z0-9_.*?-]{1,15}$`)
	fileStemPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	macPattern       = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)
	domainPattern    = regexp.MustCompile(`^~?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.?$`)
	bondModes        = []string{"active-backup", "802.3ad", "balance-alb", "balance-tlb", "balance-rr", "balance-xor", "broadcast"}
	hashPolicies     = []string{"layer2", "layer3+4", "layer2+3", "encap2+3", "encap3+4"}
)

// NetworkdFileName returns the name of a file Racksail renders.
func NetworkdFileName(stem, ext string) string { return "50-racksail-" + stem + "." + ext }

// Render returns the files keyed by name, ready for Network.ApplyConfig.
func (c NetworkConfig) Render() (map[string][]byte, error) {
	out := map[string][]byte{}
	devs := map[string]NetDev{}
	for _, d := range c.NetDevs {
		data, err := d.render()
		if err != nil {
			return nil, err
		}
		name := NetworkdFileName(d.Name, "netdev")
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("netdev %s is listed twice: %w", d.Name, ErrInvalid)
		}
		out[name] = data
		devs[d.Name] = d
	}
	for _, n := range c.Networks {
		for _, attached := range append(slices.Clone(n.VLANs), n.Bond, n.Bridge) {
			if attached == "" {
				continue
			}
			if _, ok := devs[attached]; !ok {
				return nil, fmt.Errorf("network %s attaches to %s, which isn't one of the devices: %w", n.Name, attached, ErrInvalid)
			}
		}
		for _, v := range n.VLANs {
			if devs[v].Kind != "vlan" {
				return nil, fmt.Errorf("network %s: %s isn't a VLAN: %w", n.Name, v, ErrInvalid)
			}
		}
		if n.Bond != "" && devs[n.Bond].Kind != "bond" {
			return nil, fmt.Errorf("network %s: %s isn't a bond: %w", n.Name, n.Bond, ErrInvalid)
		}
		if n.Bridge != "" && devs[n.Bridge].Kind != "bridge" {
			return nil, fmt.Errorf("network %s: %s isn't a bridge: %w", n.Name, n.Bridge, ErrInvalid)
		}
		data, err := n.render()
		if err != nil {
			return nil, err
		}
		name := NetworkdFileName(n.Name, "network")
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("network %s is listed twice: %w", n.Name, ErrInvalid)
		}
		out[name] = data
	}
	return out, nil
}

func (d NetDev) render() ([]byte, error) {
	if !ifaceNamePattern.MatchString(d.Name) {
		return nil, fmt.Errorf("interface name %q: %w", d.Name, ErrInvalid)
	}
	if d.MTU != 0 && (d.MTU < 576 || d.MTU > 65535) {
		return nil, fmt.Errorf("MTU %d of %s: %w", d.MTU, d.Name, ErrInvalid)
	}
	if d.MACAddress != "" && !macPattern.MatchString(d.MACAddress) {
		return nil, fmt.Errorf("MAC address %q of %s: %w", d.MACAddress, d.Name, ErrInvalid)
	}
	var u UnitFile
	nd := u.Section("NetDev").Add("Name", d.Name).Add("Kind", d.Kind)
	if d.MTU != 0 {
		nd.Add("MTUBytes", strconv.Itoa(d.MTU))
	}
	if d.MACAddress != "" {
		nd.Add("MACAddress", d.MACAddress)
	}
	switch d.Kind {
	case "vlan":
		if d.VLANID < 1 || d.VLANID > 4094 {
			return nil, fmt.Errorf("VLAN %s needs an id from 1 to 4094: %w", d.Name, ErrInvalid)
		}
		u.Section("VLAN").Add("Id", strconv.Itoa(d.VLANID))
	case "bond":
		mode := d.BondMode
		if mode == "" {
			mode = "active-backup"
		}
		if !slices.Contains(bondModes, mode) {
			return nil, fmt.Errorf("bond mode %q: %w", mode, ErrInvalid)
		}
		b := u.Section("Bond").Add("Mode", mode).Add("MIIMonitorSec", "100ms")
		if d.TransmitHashPolicy != "" {
			if !slices.Contains(hashPolicies, d.TransmitHashPolicy) {
				return nil, fmt.Errorf("transmit hash policy %q: %w", d.TransmitHashPolicy, ErrInvalid)
			}
			b.Add("TransmitHashPolicy", d.TransmitHashPolicy)
		}
		if mode == "802.3ad" {
			b.Add("LACPTransmitRate", "fast")
		}
	case "bridge":
		u.Section("Bridge").Add("STP", yesNo(d.STP))
	case "dummy":
	default:
		return nil, fmt.Errorf("netdev kind %q: %w", d.Kind, ErrInvalid)
	}
	return u.Render()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (n NetworkFile) render() ([]byte, error) {
	if !fileStemPattern.MatchString(n.Name) {
		return nil, fmt.Errorf("network name %q: %w", n.Name, ErrInvalid)
	}
	if n.MatchName == "" && n.MatchMAC == "" {
		return nil, fmt.Errorf("network %s matches nothing: %w", n.Name, ErrInvalid)
	}
	var u UnitFile
	match := u.Section("Match")
	if n.MatchName != "" {
		if !ifaceGlobPattern.MatchString(n.MatchName) {
			return nil, fmt.Errorf("interface %q: %w", n.MatchName, ErrInvalid)
		}
		match.Add("Name", n.MatchName)
	}
	if n.MatchMAC != "" {
		if !macPattern.MatchString(n.MatchMAC) {
			return nil, fmt.Errorf("MAC address %q: %w", n.MatchMAC, ErrInvalid)
		}
		match.Add("MACAddress", n.MatchMAC)
	}
	if n.MTU != 0 || n.Optional {
		link := u.Section("Link")
		if n.MTU != 0 {
			if n.MTU < 576 || n.MTU > 65535 {
				return nil, fmt.Errorf("MTU %d: %w", n.MTU, ErrInvalid)
			}
			link.Add("MTUBytes", strconv.Itoa(n.MTU))
		}
		if n.Optional {
			link.Add("RequiredForOnline", "no")
		}
	}
	net := u.Section("Network")
	switch n.DHCP {
	case "", "no":
	case "yes", "ipv4", "ipv6":
		net.Add("DHCP", n.DHCP)
	default:
		return nil, fmt.Errorf("DHCP %q: %w", n.DHCP, ErrInvalid)
	}
	if n.IPv6AcceptRA != nil {
		net.Add("IPv6AcceptRA", yesNo(*n.IPv6AcceptRA))
	}
	for _, a := range n.Addresses {
		p, err := netip.ParsePrefix(a)
		if err != nil || p.Addr().Zone() != "" {
			return nil, fmt.Errorf("address %q: %w", a, ErrInvalid)
		}
		net.Add("Address", p.String())
	}
	for _, g := range n.Gateways {
		addr, err := netip.ParseAddr(g)
		if err != nil {
			return nil, fmt.Errorf("gateway %q: %w", g, ErrInvalid)
		}
		net.Add("Gateway", addr.String())
	}
	for _, d := range n.DNS {
		addr, err := netip.ParseAddr(d)
		if err != nil {
			return nil, fmt.Errorf("DNS server %q: %w", d, ErrInvalid)
		}
		net.Add("DNS", addr.String())
	}
	if len(n.Domains) > 0 {
		for _, d := range n.Domains {
			if !domainPattern.MatchString(d) {
				return nil, fmt.Errorf("search domain %q: %w", d, ErrInvalid)
			}
		}
		net.Add("Domains", strings.Join(n.Domains, " "))
	}
	for _, v := range n.VLANs {
		net.Add("VLAN", v)
	}
	if n.Bond != "" {
		net.Add("Bond", n.Bond)
	}
	if n.Bridge != "" {
		net.Add("Bridge", n.Bridge)
	}
	for _, r := range n.Routes {
		if err := r.add(&u); err != nil {
			return nil, err
		}
	}
	for _, rule := range n.Rules {
		if err := rule.add(&u); err != nil {
			return nil, err
		}
	}
	return u.Render()
}

func (r NetworkRoute) add(u *UnitFile) error {
	dst, err := netip.ParsePrefix(r.Destination)
	if err != nil {
		return fmt.Errorf("route destination %q: %w", r.Destination, ErrInvalid)
	}
	// Each route is its own [Route] section, so the section is appended
	// rather than looked up by name.
	u.Sections = append(u.Sections, UnitSection{Name: "Route"})
	s := &u.Sections[len(u.Sections)-1]
	s.Add("Destination", dst.String())
	if r.Gateway != "" {
		gw, err := netip.ParseAddr(r.Gateway)
		if err != nil {
			return fmt.Errorf("route gateway %q: %w", r.Gateway, ErrInvalid)
		}
		s.Add("Gateway", gw.String())
	}
	if r.GatewayOnLink {
		s.Add("GatewayOnLink", "yes")
	}
	if r.Source != "" {
		src, err := netip.ParseAddr(r.Source)
		if err != nil {
			return fmt.Errorf("route source %q: %w", r.Source, ErrInvalid)
		}
		s.Add("PreferredSource", src.String())
	}
	if r.Table < 0 || r.Table > 0x7fffffff || r.Metric < 0 {
		return fmt.Errorf("route table or metric: %w", ErrInvalid)
	}
	if r.Table != 0 {
		s.Add("Table", strconv.Itoa(r.Table))
	}
	if r.Metric != 0 {
		s.Add("Metric", strconv.Itoa(r.Metric))
	}
	return nil
}

func (p RoutingPolicy) add(u *UnitFile) error {
	u.Sections = append(u.Sections, UnitSection{Name: "RoutingPolicyRule"})
	s := &u.Sections[len(u.Sections)-1]
	if p.From == "" && p.To == "" && p.FwMark == 0 {
		return fmt.Errorf("a routing rule needs From, To or FwMark: %w", ErrInvalid)
	}
	for _, v := range []struct{ key, value string }{{"From", p.From}, {"To", p.To}} {
		if v.value == "" {
			continue
		}
		pre, err := netip.ParsePrefix(v.value)
		if err != nil {
			return fmt.Errorf("routing rule %s %q: %w", v.key, v.value, ErrInvalid)
		}
		s.Add(v.key, pre.String())
	}
	if p.FwMark != 0 {
		s.Add("FirewallMark", strconv.FormatUint(uint64(p.FwMark), 10))
	}
	if p.Table <= 0 || p.Table > 0x7fffffff {
		return fmt.Errorf("routing rule table %d: %w", p.Table, ErrInvalid)
	}
	s.Add("Table", strconv.Itoa(p.Table))
	if p.Priority != 0 {
		if p.Priority < 0 || p.Priority > 32765 {
			return fmt.Errorf("routing rule priority %d: %w", p.Priority, ErrInvalid)
		}
		s.Add("Priority", strconv.Itoa(p.Priority))
	}
	return nil
}

// networkdFilePattern is how the networkd files Racksail writes are named.
var networkdFilePattern = regexp.MustCompile(`^50-racksail-[a-z0-9][a-z0-9_.-]{0,62}\.(network|netdev)$`)

// networkdKeys are the sections and directives NetworkConfig.Render
// writes, by file type, with checks of their values.
var networkdKeys = map[string]map[string]map[string]valueCheck{
	"netdev": {
		"NetDev": {
			"Name": matching(ifaceNamePattern), "Kind": oneOf("bond", "bridge", "vlan", "dummy"),
			"MTUBytes": intBetween(576, 65535), "MACAddress": matching(macPattern),
		},
		"VLAN": {"Id": intBetween(1, 4094)},
		"Bond": {
			"Mode": oneOf(bondModes...), "MIIMonitorSec": timeSpan, "TransmitHashPolicy": oneOf(hashPolicies...),
			"LACPTransmitRate": oneOf("slow", "fast"),
		},
		"Bridge": {"STP": boolean},
	},
	"network": {
		"Match": {"Name": eachWord(matching(ifaceGlobPattern)), "MACAddress": eachWord(matching(macPattern))},
		"Link":  {"MTUBytes": intBetween(576, 65535), "RequiredForOnline": boolean},
		"Network": {
			"DHCP": oneOf("yes", "no", "ipv4", "ipv6"), "IPv6AcceptRA": boolean,
			"Address": prefixValue, "Gateway": addressValue, "DNS": eachWord(addressValue),
			"Domains": eachWord(matching(domainPattern)), "VLAN": matching(ifaceNamePattern),
			"Bond": matching(ifaceNamePattern), "Bridge": matching(ifaceNamePattern),
		},
		"Route": {
			"Destination": prefixValue, "Gateway": addressValue, "GatewayOnLink": boolean,
			"PreferredSource": addressValue, "Table": intBetween(1, 0x7fffffff), "Metric": intBetween(0, 0xffffffff),
		},
		"RoutingPolicyRule": {
			"From": prefixValue, "To": prefixValue, "FirewallMark": intBetween(1, 0xffffffff),
			"Table": intBetween(1, 0x7fffffff), "Priority": intBetween(0, 32765),
		},
	},
}

func prefixValue(_ *unitChecker, v string) error {
	if p, err := netip.ParsePrefix(v); err != nil || p.Addr().Zone() != "" {
		return refuse("not an address with its prefix length")
	}
	return nil
}

func addressValue(_ *unitChecker, v string) error {
	if _, err := netip.ParseAddr(v); err != nil {
		return refuse("not an address")
	}
	return nil
}

func eachWord(check valueCheck) valueCheck {
	return func(c *unitChecker, v string) error {
		words := strings.Fields(v)
		if len(words) == 0 {
			return refuse("empty")
		}
		for _, w := range words {
			if err := check(c, w); err != nil {
				return err
			}
		}
		return nil
	}
}

// CheckNetworkdFile holds a networkd file to what NetworkConfig.Render
// writes: a .network or .netdev file named 50-racksail-*, read the way
// networkd reads it, with only the sections and directives Racksail
// renders and values those directives take. A .network file must match
// interfaces by name or MAC address, since one without [Match] applies to
// every interface; a .netdev file must name its device and its kind.
func CheckNetworkdFile(name string, data []byte) error {
	m := networkdFilePattern.FindStringSubmatch(name)
	if m == nil {
		return fmt.Errorf("networkd file %q must be named 50-racksail-<name>.network or .netdev: %w", name, ErrInvalid)
	}
	sections := networkdKeys[m[1]]
	u, err := ParseUnitFile(data)
	if err != nil {
		return fmt.Errorf("networkd file %s: %w", name, err)
	}
	c := &unitChecker{unit: name, what: "networkd file"}
	seen := map[string]bool{}
	for _, sec := range u.Sections {
		keys, ok := sections[sec.Name]
		if !ok {
			return fmt.Errorf("networkd file %s can't have a [%s] section: %w", name, sec.Name, ErrInvalid)
		}
		if err := c.check(sec, keys); err != nil {
			return err
		}
		for _, e := range sec.Entries {
			seen[sec.Name+"."+e.Key] = true
		}
	}
	switch m[1] {
	case "network":
		if !seen["Match.Name"] && !seen["Match.MACAddress"] {
			return fmt.Errorf("networkd file %s must match interfaces by Name= or MACAddress=: %w", name, ErrInvalid)
		}
	case "netdev":
		if !seen["NetDev.Name"] || !seen["NetDev.Kind"] {
			return fmt.Errorf("networkd file %s must give the device's Name= and Kind=: %w", name, ErrInvalid)
		}
	}
	return nil
}

// RenderedNetworkd joins rendered files for golden tests and logs, sorted
// by name.
func RenderedNetworkd(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "### %s\n%s\n", n, files[n])
	}
	return b.Bytes()
}
