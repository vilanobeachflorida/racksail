package webhook

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// A webhook sends the panel's events to an address someone typed in and
// keeps the start of every answer in its delivery log. Pointed at the
// server's own services, the cloud's metadata service or the private network
// behind the server, it would read them for whoever set it up, so deliveries
// only go to public addresses unless racksail.toml lists the networks they
// may reach as well (webhooks.allow_networks).

// reserved are networks that aren't private but aren't the public internet
// either: shared, documentation, benchmarking and reserved space, and the
// IPv6 ranges that carry IPv4 addresses inside them.
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// public reports whether anyone on the internet could reach an address:
// it isn't loopback, private, link-local, multicast or set aside for special
// uses.
func public(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, p := range reserved {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// guard decides which addresses deliveries may connect to: public ones, and
// those in the networks racksail.toml allows.
type guard struct {
	allow []netip.Prefix
}

func (g guard) allowed(addr netip.Addr) bool {
	addr = addr.Unmap()
	if public(addr) {
		return true
	}
	for _, p := range g.allow {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// blockedError is a connection the guard refused.
type blockedError struct {
	addr netip.Addr
}

func (e *blockedError) Error() string {
	return fmt.Sprintf("%s isn't a public address; list its network under webhooks.allow_networks in racksail.toml to send events there", e.addr)
}

// control runs once the address to connect to is resolved, so a name that
// resolves to a private address is refused however it was written.
func (g guard) control(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", address, err)
	}
	if !g.allowed(ap.Addr()) {
		return &blockedError{addr: ap.Addr().Unmap()}
	}
	return nil
}

// checkURL refuses a URL whose host is written as an address the guard
// wouldn't connect to, or is a name that always means this machine. Names
// are only resolved when deliveries are sent, where control checks them.
func (g guard) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		// Either loopback address will do: connecting tries both.
		v4 := netip.AddrFrom4([4]byte{127, 0, 0, 1})
		if g.allowed(v4) || g.allowed(netip.IPv6Loopback()) {
			return nil
		}
		return &blockedError{addr: v4}
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return nil
	}
	if !g.allowed(addr) {
		return &blockedError{addr: addr.Unmap()}
	}
	return nil
}

// client returns the HTTP client deliveries go out with: it waits ten
// seconds for an answer, follows no redirects, uses no proxy, so the guard
// sees the receiver's own address, and connects only where the guard lets
// it.
func (g guard) client() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: g.control}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Timeout:       10 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
