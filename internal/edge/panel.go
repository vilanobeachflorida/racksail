package edge

import (
	"fmt"
	"net"
	"uuid"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// PanelOwner owns the route that serves the panel under its hostname.
var PanelOwner = store.FromUUID("panel", uuid.UUID{})

// PanelRoute serves the panel on the edge under its own hostname. The route
// is passive: the panel keeps its own port and never starts the edge, but
// once modules route sites, the panel is reachable on 443 too. listen is
// the panel's own address; secure says whether it speaks HTTPS there.
func PanelRoute(hostname, listen string, secure bool) (Route, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return Route{}, fmt.Errorf("the panel's address %q: %w", listen, err)
	}
	// A panel listening on every address is reached on loopback; one on a
	// single address there, which must be a loopback one when it speaks
	// HTTPS.
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	r := Route{
		ID: "panel", Owner: PanelOwner, Hosts: []string{hostname}, Passive: true,
		Handler: Handler{Type: HandleProxy, Upstreams: []Upstream{{Network: "tcp", Address: net.JoinHostPort(host, port), TLS: secure}}},
	}
	return r, r.Validate()
}
