---
title: "The edge"
description: "The web server in front of every site and app: routing, certificates and sleeping sites."
---

The edge answers every request to the server's sites and apps. It is Caddy, embedded in the Racksail binary with Racksail's own modules, running as the unprivileged `racksail-edge` user with the right to listen on ports 80 and 443 and nothing more.

## Routes

The panel keeps a routing table and sends it to the edge. A route matches some host names (names, wildcards such as `*.example.com`, IP addresses, or any name no other route has) and optionally a path, and says what to do:

- serve static files, never following links out of the site's directory and never listing directories;
- proxy to a port or a Unix socket, across several upstreams with health checks;
- run PHP through FastCGI, with a front controller for paths that aren't files;
- redirect, or answer with a fixed response.

Adding or changing a site never reloads the edge: each route is set up on its own while everything else keeps serving, and removing a site drops its route the same way. The one time the edge reloads for a site is a couple of minutes after the last route for an automatic certificate goes away, to drop that certificate from its cache; it gathers a few such changes first. Responses are compressed with zstd or gzip, and HTTP/1.1, HTTP/2 and HTTP/3 are served.

What a route carries from the panel, such as custom headers or an application's environment, reaches the edge as plain text: nothing in it can read files or the environment through Caddy's placeholders. No route can serve the edge's own files, its state or the certificates it reads, even when a site's directory is made a link to them.

The edge doesn't even run until a module routes something through it: systemd holds its socket, and the panel only reaches it once there is a route to serve. From then on the edge starts at boot too, so sites come back after a restart without waiting for the panel. When the last route goes, the edge stops listening, no longer starts at boot and exits after a few idle minutes. The panel keeps its own port; while the edge serves sites, the panel is also reachable on 443 under its hostname.

## Certificates

Each route says how it gets its certificate:

- **Automatic** (the default): the edge obtains it from Let's Encrypt before anyone visits and renews it whether or not anyone does. Names are validated over HTTP or TLS-ALPN; wildcards over DNS, with records Racksail's DNS module publishes; IP addresses get short-lived certificates.
- **On demand**: the certificate is obtained at the first visit. Routes that match names nobody listed, such as customers' own domains, have the panel approve each name first. A certificate belongs to a name, not a path, so a host that only has routes for paths below its root, such as `/api`, gets its certificate from them.
- **Uploaded**: a certificate you installed is served as it is.
- **Off**: the route is only served over plain HTTP.

The panel lists every certificate with its expiry and status, and can hand one to other services that serve the same names, such as mail. Names no public CA certifies, and everything in development, get certificates from a local CA.

## Sleeping sites

A site can sleep when nobody uses it. systemd holds its socket, so the first request starts it: the edge waits while the site starts, and the visitor gets the page. Once the site has been idle for a while, it stops again.

## State

The edge keeps its certificates, ACME accounts, settings and the last routing table in `/var/lib/racksail-edge`. A restarted edge serves its sites again before the panel gets in touch.
