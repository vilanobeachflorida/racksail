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

Adding, changing or removing a site never reloads the edge: each route is set up on its own while everything else keeps serving. Responses are compressed with zstd or gzip, and HTTP/1.1, HTTP/2 and HTTP/3 are served.

The edge only listens once a module routes something, and stops when the last route goes. The panel keeps its own port; while the edge serves sites, the panel is also reachable on 443 under its hostname.

## Certificates

Each route says how it gets its certificate:

- **Automatic** (the default): the edge obtains it from Let's Encrypt before anyone visits and renews it whether or not anyone does. Names are validated over HTTP or TLS-ALPN; wildcards over DNS, with records Racksail's DNS module publishes; IP addresses get short-lived certificates.
- **On demand**: the certificate is obtained at the first visit. Routes that match names nobody listed, such as customers' own domains, have the panel approve each name first.
- **Uploaded**: a certificate you installed is served as it is.
- **Off**: the route is only served over plain HTTP.

The panel lists every certificate with its expiry and status, and can hand one to other services that serve the same names, such as mail. Names no public CA certifies, and everything in development, get certificates from a local CA.

## Sleeping sites

A site can sleep when nobody uses it. systemd holds its socket, so the first request starts it: the edge waits while the site starts, and the visitor gets the page. Once the site has been idle for a while, it stops again.

## State

The edge keeps its certificates, ACME accounts, settings and the last routing table in `/var/lib/racksail-edge`. A restarted edge serves its sites again before the panel gets in touch.
