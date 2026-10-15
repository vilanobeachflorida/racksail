// Package edge is Racksail's front door: TLS, routing, static files,
// proxying and FastCGI for every site and app.
//
// The panel owns the routing table and pushes it to the edge process over
// the edge's Unix socket; routes are grouped by the resource that owns them
// and replaced per owner, so adding a site never reloads anything. Table is
// the routing table with host and path matching. Server answers requests
// from it: static files, reverse proxying to Unix sockets or ports with
// round-robin balancing, FastCGI with a front controller, redirects and
// fixed responses. Upstreams of sleeping sites are socket-activated by
// systemd, so the first request wakes them.
//
// The edge only starts when an enabled module first registers a route.
// Certificates come from the certificate store; ACME issuance, HTTP/3 and the
// web application firewall come with the embedded Caddy runtime, which serves
// the same table.
package edge
