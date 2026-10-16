package notify

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// Notifications go to addresses people type in: a webhook, a Matrix
// homeserver, an ntfy server. Pointed at the server's own services or the
// network behind it, a channel would reach them for whoever set it up, so
// notifications only go to public addresses, unless racksail.toml lists the
// networks they may reach as well (webhooks.allow_networks), as for a
// homeserver on the local network.

// reserved are networks that aren't private but aren't the public internet
// either.
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

// Guard decides which addresses notifications may connect to.
type Guard struct {
	// Allow are networks besides public addresses.
	Allow []netip.Prefix
}

// Allowed reports whether notifications may reach an address.
func (g Guard) Allowed(addr netip.Addr) bool {
	addr = addr.Unmap()
	if public(addr) {
		return true
	}
	for _, p := range g.Allow {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func (g Guard) control(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", address, err)
	}
	if !g.Allowed(ap.Addr()) {
		return fmt.Errorf("%s isn't a public address; list its network under webhooks.allow_networks in racksail.toml to send notifications there", ap.Addr().Unmap())
	}
	return nil
}

// Dialer connects only where the guard allows, checked once a name is
// resolved, so a name that resolves to a private address is refused
// however it was written.
func (g Guard) Dialer() *net.Dialer {
	return &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: g.control}
}

// Client returns the HTTP client notifications go out with: it waits
// fifteen seconds for an answer, follows no redirects and uses no proxy, so
// the guard sees the receiver's own address.
func (g Guard) Client() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = g.Dialer().DialContext
	return &http.Client{
		Timeout:       15 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
