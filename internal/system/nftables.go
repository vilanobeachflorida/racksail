package system

import (
	"bytes"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// FirewallTable is the nftables table Racksail renders its firewall into.
const FirewallTable = "racksail"

// FirewallPolicy is Racksail's firewall: default deny for incoming
// traffic, open ports per active module, allow and deny lists, ban sets the
// brute-force watchers fill, blocked address sets such as countries, rate
// limits on new connections, forwarding for VMs and containers, NAT, and an
// outbound address per account.
type FirewallPolicy struct {
	// Open are the ports reachable from outside.
	Open []PortRule `json:"open,omitempty"`
	// AllowFrom may reach every port; DenyFrom is dropped before anything
	// else. Both take addresses and prefixes.
	AllowFrom []string `json:"allowFrom,omitempty"`
	DenyFrom  []string `json:"denyFrom,omitempty"`
	// Bans are sets with timeouts that ban watchers add addresses to.
	Bans []BanSet `json:"bans,omitempty"`
	// BlockSets are sets of prefixes whose traffic is dropped, such as
	// countries; their elements are added separately because they are big.
	BlockSets []string `json:"blockSets,omitempty"`
	// RateLimits drop new connections from one address above a rate.
	RateLimits []RateLimit `json:"rateLimits,omitempty"`
	// Forward allows traffic between interfaces, such as from a VM bridge
	// to the uplink.
	Forward []ForwardRule `json:"forward,omitempty"`
	// Masquerade hides private networks behind the uplink's address.
	Masquerade []MasqueradeRule `json:"masquerade,omitempty"`
	// Outbound gives accounts their own outbound addresses.
	Outbound []OutboundRule `json:"outbound,omitempty"`
	// LogDrops logs dropped incoming packets, rate-limited.
	LogDrops bool `json:"logDrops,omitempty"`
}

// PortRule opens ports.
type PortRule struct {
	Protocol string `json:"protocol" enum:"tcp,udp"`
	// Ports are single ports or ranges such as "8000-8100".
	Ports []string `json:"ports"`
	// From limits the rule to these addresses and prefixes.
	From    []string `json:"from,omitempty"`
	Comment string   `json:"comment,omitempty"`
}

// BanSet is a set of banned addresses that expire.
type BanSet struct {
	Name    string        `json:"name" doc:"Set name, such as ssh or panel"`
	Timeout time.Duration `json:"timeout" doc:"How long a ban lasts unless the element says otherwise"`
}

// RateLimit drops new connections from an address beyond a rate.
type RateLimit struct {
	Protocol string `json:"protocol" enum:"tcp,udp"`
	Port     int    `json:"port"`
	// Rate is connections per Per, which is second, minute or hour.
	Rate  int    `json:"rate"`
	Per   string `json:"per" enum:"second,minute,hour"`
	Burst int    `json:"burst,omitempty"`
}

// ForwardRule lets traffic in on one interface go out on another.
type ForwardRule struct {
	In  string `json:"in"`
	Out string `json:"out,omitempty" doc:"Any interface when empty"`
}

// MasqueradeRule rewrites the source of traffic from a private network
// leaving through an interface.
type MasqueradeRule struct {
	Source string `json:"source"`
	Out    string `json:"out"`
}

// OutboundRule sends an account's traffic from its own addresses: sockets
// of the account's user are marked, the mark selects a routing table, and
// the source is rewritten to the account's address.
type OutboundRule struct {
	UID   int    `json:"uid"`
	Mark  uint32 `json:"mark"`
	SNAT4 string `json:"snat4,omitempty"`
	SNAT6 string `json:"snat6,omitempty"`
}

var (
	nftSetNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
	nftCommentPattern = regexp.MustCompile(`^[A-Za-z0-9 ._:/()-]{0,64}$`)
	nftPortPattern    = regexp.MustCompile(`^([0-9]{1,5})(-([0-9]{1,5}))?$`)
	nftProtocols      = []string{"tcp", "udp"}
	nftRateUnits      = []string{"second", "minute", "hour"}
)

// Render returns the ruleset in nft syntax. Applying it replaces every
// table whose name starts with racksail.
func (p FirewallPolicy) Render() ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n", GeneratedHeader)
	fmt.Fprintf(&b, "table inet %s {\n", FirewallTable)

	allow4, allow6, err := splitAddresses(p.AllowFrom)
	if err != nil {
		return nil, fmt.Errorf("allow list: %w", err)
	}
	deny4, deny6, err := splitAddresses(p.DenyFrom)
	if err != nil {
		return nil, fmt.Errorf("deny list: %w", err)
	}
	writeIntervalSet(&b, "allow_v4", "ipv4_addr", allow4)
	writeIntervalSet(&b, "allow_v6", "ipv6_addr", allow6)
	writeIntervalSet(&b, "deny_v4", "ipv4_addr", deny4)
	writeIntervalSet(&b, "deny_v6", "ipv6_addr", deny6)

	seen := map[string]bool{}
	for _, ban := range p.Bans {
		if !nftSetNamePattern.MatchString(ban.Name) || seen["ban_"+ban.Name] {
			return nil, fmt.Errorf("ban set %q: %w", ban.Name, ErrInvalid)
		}
		if ban.Timeout < time.Second {
			return nil, fmt.Errorf("ban set %s needs a timeout of at least a second: %w", ban.Name, ErrInvalid)
		}
		seen["ban_"+ban.Name] = true
		for _, fam := range [][2]string{{"v4", "ipv4_addr"}, {"v6", "ipv6_addr"}} {
			fmt.Fprintf(&b, "\tset ban_%s_%s {\n\t\ttype %s\n\t\tflags timeout\n\t\ttimeout %s\n\t}\n", ban.Name, fam[0], fam[1], Seconds(ban.Timeout.Truncate(time.Second)))
		}
	}
	for _, name := range p.BlockSets {
		if !nftSetNamePattern.MatchString(name) || seen["block_"+name] {
			return nil, fmt.Errorf("block set %q: %w", name, ErrInvalid)
		}
		seen["block_"+name] = true
		writeIntervalSet(&b, "block_"+name+"_v4", "ipv4_addr", nil)
		writeIntervalSet(&b, "block_"+name+"_v6", "ipv6_addr", nil)
	}
	for _, rl := range p.RateLimits {
		if err := rl.validate(); err != nil {
			return nil, err
		}
		for _, fam := range [][2]string{{"v4", "ipv4_addr"}, {"v6", "ipv6_addr"}} {
			fmt.Fprintf(&b, "\tset %s_%s {\n\t\ttype %s\n\t\tsize 65535\n\t\tflags dynamic,timeout\n\t\ttimeout 1h\n\t}\n", rl.setName(), fam[0], fam[1])
		}
	}

	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n")
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tct state vmap { established : accept, related : accept, invalid : drop }\n")
	// An explicit deny wins over everything; the allow list wins over bans
	// and blocked regions, so an admin can't lock themselves out by
	// mistyping a password.
	b.WriteString("\t\tip saddr @deny_v4 drop\n\t\tip6 saddr @deny_v6 drop\n")
	b.WriteString("\t\tip saddr @allow_v4 accept\n\t\tip6 saddr @allow_v6 accept\n")
	for _, ban := range p.Bans {
		fmt.Fprintf(&b, "\t\tip saddr @ban_%s_v4 drop\n\t\tip6 saddr @ban_%s_v6 drop\n", ban.Name, ban.Name)
	}
	for _, name := range p.BlockSets {
		fmt.Fprintf(&b, "\t\tip saddr @block_%s_v4 drop\n\t\tip6 saddr @block_%s_v6 drop\n", name, name)
	}
	// ICMP that the network needs to work, with echo rate-limited.
	b.WriteString("\t\tip protocol icmp icmp type { destination-unreachable, time-exceeded, parameter-problem } accept\n")
	b.WriteString("\t\tip protocol icmp icmp type echo-request limit rate 20/second burst 50 packets accept\n")
	b.WriteString("\t\tmeta l4proto ipv6-icmp icmpv6 type { destination-unreachable, packet-too-big, time-exceeded, parameter-problem, nd-router-solicit, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert, mld-listener-query, mld2-listener-report } accept\n")
	b.WriteString("\t\tmeta l4proto ipv6-icmp icmpv6 type echo-request limit rate 20/second burst 50 packets accept\n")
	// Replies to the DHCPv6 client.
	b.WriteString("\t\tip6 saddr fe80::/10 udp sport 547 udp dport 546 accept\n")
	for _, rl := range p.RateLimits {
		fmt.Fprintf(&b, "\t\tmeta nfproto ipv4 %s dport %d ct state new add @%s_v4 { ip saddr limit rate over %d/%s burst %d packets } drop\n",
			rl.Protocol, rl.Port, rl.setName(), rl.Rate, rl.Per, rl.burst())
		fmt.Fprintf(&b, "\t\tmeta nfproto ipv6 %s dport %d ct state new add @%s_v6 { ip6 saddr limit rate over %d/%s burst %d packets } drop\n",
			rl.Protocol, rl.Port, rl.setName(), rl.Rate, rl.Per, rl.burst())
	}
	for _, rule := range p.Open {
		lines, err := rule.render()
		if err != nil {
			return nil, err
		}
		for _, l := range lines {
			fmt.Fprintf(&b, "\t\t%s\n", l)
		}
	}
	if p.LogDrops {
		b.WriteString("\t\tlimit rate 5/minute burst 10 packets log prefix \"racksail drop: \" level info\n")
	}
	b.WriteString("\t}\n")

	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n")
	b.WriteString("\t\tct state vmap { established : accept, related : accept, invalid : drop }\n")
	for _, f := range p.Forward {
		if !ifaceNamePattern.MatchString(f.In) || (f.Out != "" && !ifaceNamePattern.MatchString(f.Out)) {
			return nil, fmt.Errorf("forward rule %s to %s: %w", f.In, f.Out, ErrInvalid)
		}
		if f.Out == "" {
			fmt.Fprintf(&b, "\t\tiifname %q accept\n", f.In)
		} else {
			fmt.Fprintf(&b, "\t\tiifname %q oifname %q accept\n", f.In, f.Out)
		}
	}
	b.WriteString("\t}\n")

	b.WriteString("\tchain output {\n\t\ttype filter hook output priority filter; policy accept;\n\t}\n")

	if len(p.Outbound) > 0 {
		// A route chain, so the mark is set before routing decides where
		// the packet goes.
		b.WriteString("\tchain mark_accounts {\n\t\ttype route hook output priority mangle; policy accept;\n")
		marks := map[uint32]bool{}
		for _, o := range p.Outbound {
			if o.UID < AccountUIDMin || o.UID > AccountUIDMax || o.Mark == 0 || marks[o.Mark] {
				return nil, fmt.Errorf("outbound rule for uid %d with mark %d: %w", o.UID, o.Mark, ErrInvalid)
			}
			marks[o.Mark] = true
			fmt.Fprintf(&b, "\t\tmeta skuid %d meta mark set 0x%08x\n", o.UID, o.Mark)
		}
		b.WriteString("\t}\n")
	}
	if len(p.Outbound) > 0 || len(p.Masquerade) > 0 {
		b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
		for _, o := range p.Outbound {
			if o.SNAT4 == "" && o.SNAT6 == "" {
				return nil, fmt.Errorf("outbound rule for uid %d needs an address: %w", o.UID, ErrInvalid)
			}
			if o.SNAT4 != "" {
				a, err := netip.ParseAddr(o.SNAT4)
				if err != nil || !a.Is4() {
					return nil, fmt.Errorf("outbound IPv4 address %q: %w", o.SNAT4, ErrInvalid)
				}
				fmt.Fprintf(&b, "\t\tmeta nfproto ipv4 meta mark 0x%08x snat ip to %s\n", o.Mark, a)
			}
			if o.SNAT6 != "" {
				a, err := netip.ParseAddr(o.SNAT6)
				if err != nil || !a.Is6() || a.Is4In6() {
					return nil, fmt.Errorf("outbound IPv6 address %q: %w", o.SNAT6, ErrInvalid)
				}
				fmt.Fprintf(&b, "\t\tmeta nfproto ipv6 meta mark 0x%08x snat ip6 to %s\n", o.Mark, a)
			}
		}
		for _, m := range p.Masquerade {
			src, err := netip.ParsePrefix(m.Source)
			if err != nil || !ifaceNamePattern.MatchString(m.Out) {
				return nil, fmt.Errorf("masquerade %s through %s: %w", m.Source, m.Out, ErrInvalid)
			}
			family := "ip"
			if src.Addr().Is6() {
				family = "ip6"
			}
			fmt.Fprintf(&b, "\t\t%s saddr %s oifname %q masquerade\n", family, src.Masked(), m.Out)
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	if _, err := CheckRuleset(b.Bytes()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (rl RateLimit) validate() error {
	if !slices.Contains(nftProtocols, rl.Protocol) || rl.Port < 1 || rl.Port > 65535 {
		return fmt.Errorf("rate limit on %s port %d: %w", rl.Protocol, rl.Port, ErrInvalid)
	}
	if rl.Rate < 1 || !slices.Contains(nftRateUnits, rl.Per) || rl.Burst < 0 {
		return fmt.Errorf("rate limit %d/%s: %w", rl.Rate, rl.Per, ErrInvalid)
	}
	return nil
}

func (rl RateLimit) setName() string { return "rate_" + rl.Protocol + "_" + strconv.Itoa(rl.Port) }

func (rl RateLimit) burst() int {
	if rl.Burst > 0 {
		return rl.Burst
	}
	return rl.Rate
}

// ParsePorts checks port specifications such as "22" and "8000-8100".
func ParsePorts(ports []string) ([]string, error) {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		m := nftPortPattern.FindStringSubmatch(p)
		if m == nil {
			return nil, fmt.Errorf("port %q: %w", p, ErrInvalid)
		}
		lo, _ := strconv.Atoi(m[1])
		if lo < 1 || lo > 65535 {
			return nil, fmt.Errorf("port %q: %w", p, ErrInvalid)
		}
		if m[3] != "" {
			hi, _ := strconv.Atoi(m[3])
			if hi <= lo || hi > 65535 {
				return nil, fmt.Errorf("port range %q: %w", p, ErrInvalid)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func (r PortRule) render() ([]string, error) {
	if !slices.Contains(nftProtocols, r.Protocol) {
		return nil, fmt.Errorf("protocol %q: %w", r.Protocol, ErrInvalid)
	}
	if len(r.Ports) == 0 {
		return nil, fmt.Errorf("open rule without ports: %w", ErrInvalid)
	}
	ports, err := ParsePorts(r.Ports)
	if err != nil {
		return nil, err
	}
	if !nftCommentPattern.MatchString(r.Comment) {
		return nil, fmt.Errorf("comment %q: %w", r.Comment, ErrInvalid)
	}
	set := ports[0]
	if len(ports) > 1 {
		set = "{ " + strings.Join(ports, ", ") + " }"
	}
	comment := ""
	if r.Comment != "" {
		comment = fmt.Sprintf(" comment %q", r.Comment)
	}
	if len(r.From) == 0 {
		return []string{fmt.Sprintf("%s dport %s accept%s", r.Protocol, set, comment)}, nil
	}
	v4, v6, err := splitAddresses(r.From)
	if err != nil {
		return nil, err
	}
	var lines []string
	if len(v4) > 0 {
		lines = append(lines, fmt.Sprintf("ip saddr { %s } %s dport %s accept%s", strings.Join(v4, ", "), r.Protocol, set, comment))
	}
	if len(v6) > 0 {
		lines = append(lines, fmt.Sprintf("ip6 saddr { %s } %s dport %s accept%s", strings.Join(v6, ", "), r.Protocol, set, comment))
	}
	return lines, nil
}

// splitAddresses parses addresses and prefixes into IPv4 and IPv6 lists of
// normalized prefixes and addresses.
func splitAddresses(list []string) (v4, v6 []string, err error) {
	for _, s := range list {
		var p netip.Prefix
		if addr, aerr := netip.ParseAddr(s); aerr == nil {
			p = netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen())
		} else if p, err = netip.ParsePrefix(s); err != nil {
			return nil, nil, fmt.Errorf("%q isn't an address or prefix: %w", s, ErrInvalid)
		}
		p = p.Masked()
		text := p.String()
		if p.IsSingleIP() {
			text = p.Addr().String()
		}
		if p.Addr().Is4() {
			v4 = append(v4, text)
		} else {
			v6 = append(v6, text)
		}
	}
	return v4, v6, nil
}

func writeIntervalSet(b *bytes.Buffer, name, typ string, elems []string) {
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n\t\tauto-merge\n", name, typ)
	if len(elems) > 0 {
		fmt.Fprintf(b, "\t\telements = { %s }\n", strings.Join(elems, ", "))
	}
	b.WriteString("\t}\n")
}

// FirewallSet returns the reference of one of the policy's sets, such as
// the IPv4 half of the ssh ban set.
func FirewallSet(name string, v6 bool) SetRef {
	suffix := "_v4"
	family := "inet"
	if v6 {
		suffix = "_v6"
	}
	return SetRef{Family: family, Table: FirewallTable, Set: name + suffix}
}

// BanSetRef returns the set an address is banned in.
func BanSetRef(ban string, addr netip.Addr) SetRef {
	return FirewallSet("ban_"+ban, addr.Is6() && !addr.Is4In6())
}
