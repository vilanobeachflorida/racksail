// Package edge is Racksail's front door: an embedded Caddy that handles
// TLS, routing, static files, proxying and FastCGI for every site and app.
//
// The panel owns the routing table and sends it to the edge process over
// the edge's admin socket; routes are grouped by the resource that owns
// them and replaced per owner. Caddy's configuration holds no routes:
// Racksail's router module serves them from the Table and compiles each
// one into Caddy handlers in a context of its own, so adding, changing or
// removing a site never reloads Caddy. Upstreams of sleeping sites are
// socket-activated by systemd, and WakeProxy forwards to them once they
// have started.
//
// Certificates come through ACME: HTTP-01 and TLS-ALPN-01 for names,
// DNS-01 for wildcards with records the panel's DNS module publishes, and
// short-lived certificates for IP addresses. Routes in the on-demand mode
// get certificates at their first handshake, after the panel approves the
// name when the route asks it to. Uploaded certificates are served as they
// are, and names no public CA certifies, as well as everything in
// development, get certificates from a local CA. The edge obtains the
// certificates of auto routes before anyone visits, renews them whether or
// not anyone does, and reports their expiry.
//
// The edge only serves once the table holds a route that isn't passive,
// and stops when the last one goes. It keeps its settings, certificates and
// the last routing table in its state directory, so a restarted edge serves
// its sites before the panel gets in touch.
package edge
