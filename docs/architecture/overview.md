# Racksail architecture

Racksail turns a Debian 13 server into a self-hosted platform for websites, email, DNS, apps, file storage, containers and virtual machines. It is managed from one web panel, a command-line tool and an API, and it is 100% free and open source.

This document is the map. The details live next to it:

- [modules.md](modules.md): every bundled module and the module contract
- [extensibility.md](extensibility.md): hooks, rules, add-ons, themes and catalogs
- [security.md](security.md): threat model and security rules
- [decisions.md](decisions.md): technology choices, defaults and conventions we settled on
- [conventions.md](conventions.md): how we write code, tests and docs, and when a feature is done
- [features.md](features.md): the complete feature list

## Principles

1. **Desired state, not scripts.** Racksail stores what should exist. The agent on each server keeps making the machine match it. Generated configuration is never edited by hand, and running the same reconcile twice changes nothing.
2. **systemd runs everything.** Sites, workers, scheduled jobs and resource limits are systemd units, slices, sockets and timers. Hosting doesn't need a container daemon; containers are a module for people who want them.
3. **The panel can go down; the sites can't.** Web serving, mail, DNS, databases, apps and VMs never depend on the panel process being up.
4. **Nothing runs until it's needed.** Every module ships in the binary but installs nothing, opens no ports and starts no processes until something needs it. The same rule applies inside modules: no PHP version is installed until a site uses it, no image is pulled until an app needs it.
5. **Everything is a module, except a small core.** Hosting, mail, storage, containers, VMs and the rest all plug into the same module contract.
6. **One change pipeline.** Every change, whether it comes from the UI, the CLI, the API, a recipe, an automation, an add-on or the assistant, becomes a change set that is validated, previewed, applied and recorded the same way. That single path is what makes hooks, audit, undo and the assistant complete and safe.
7. **Free and open source, all of it.** Everything the project ships or installs by default is free and open source. Catalogs may offer other software, always labelled, never installed by default.
8. **Plain, fast and honest.** Every screen answers in under 100 ms, every change is previewed before it happens, and every error says what happened and what to do next.

## Process model

One Go binary, `racksail`, runs in several roles.

| Role | Runs as | Purpose |
|---|---|---|
| `panel` | `racksail` system user | API, web UI, state, change pipeline, scheduler, module manager |
| `agent` | root | Executes typed operations on the machine. One per server |
| `edge` | `racksail-edge` user with `CAP_NET_BIND_SERVICE` | Front-door web server (embedded Caddy): TLS, routing, static files, proxying, wake-on-request |
| `cli` | the calling user | Command-line client for the API |
| `installer` | root, from the live ISO or a Debian install | Installs Racksail or converts an existing Debian 13 system |
| `dev` | the developer | Panel, agent and edge in one process against the simulated backend |

- The panel talks to the agent over HTTP+JSON on a Unix socket (`/run/racksail/agent.sock`) on the same server, and over mutual TLS between servers.
- The agent accepts only typed operations from a fixed allowlist. There is no operation that runs an arbitrary command.
- systemd units: `racksail-panel.service`, `racksail-agent.service` and `racksail-edge.service`, all in `racksail.slice`. Account workloads live in `racksail-accounts.slice`.

## Resources

Everything Racksail manages is a resource of a registered kind, for example `web.site`, `dns.zone`, `mail.mailbox`, `storage.pool` or `core.user`. A resource has:

- `id`: a TypeID such as `site_01j9x3...`, immutable
- `kind`: `<module>.<name>`
- `scope`: the server, reseller or account it belongs to
- `spec`: the desired state, a typed Go struct stored as JSON
- `status`: the observed state written by reconcilers (phase, conditions, messages)
- `generation` and `observedGeneration`
- labels, timestamps and soft-delete markers

Registering a kind gives a module all of this without extra code:

- typed REST operations (list, get, create, update, delete and any declared actions) in the OpenAPI document
- CLI commands
- assistant tools, with a risk level taken from each action's metadata
- hook points for every operation and stage
- events, timeline entries and audit records
- coverage in backups and in the portable account archive (a kind can opt out only with a stated reason)
- a generic list, detail and edit screen generated from its JSON Schema until the module ships a custom screen

All kinds share one generic store (SQLite table with JSON columns and indexed generated columns where needed), so history, hooks, undo and backups work the same way for every kind.

## The change pipeline

```
request → authenticate → authorize → validate → plan → preview → apply → verify → record
```

- **Validate:** schema validation, the kind's own rules, policies (rules written in CEL) and validate hooks.
- **Plan:** reconcilers compute the system effects. Modify hooks can patch the plan. The result is a change set.
- **Preview:** the change set lists resource changes with field-level diffs and planned system effects: downloads with sizes and sources, packages, ports opened or closed, services started, files written, estimated memory and disk. Every contribution from a hook or add-on is labelled with its source.
- **Apply:** the store is updated in one transaction, then reconcilers run agent operations as a job with live progress. Risky changes take a filesystem snapshot first.
- **Verify:** health checks confirm the result. A failed verification offers rollback.
- **Record:** events, timeline and audit entries, attributed to the user and to whatever acted for them (UI, CLI, API token, automation, add-on, assistant).
- **Undo** is a new change set built from the recorded previous specs, plus the pre-change snapshot when system files were touched.
- Reads never go through the pipeline.

## Reconcilers and the agent

- Each kind has a reconciler with two halves. **Plan** is pure: desired state plus observed state in, a list of typed operations out. **Apply** sends those operations to the agent and writes status.
- Agent operations are small and typed: `EnsureUser`, `WriteFile`, `EnsureUnit`, `StartUnit`, `EnsureSubvolume`, `InstallPackages`, `ApplyFirewall` and so on. Each one validates its arguments. None of them builds a shell command from a string; external tools are always run with an argument vector.
- System adapters implement the operations. There are two implementations: `linux`, the real one, and `sim`, a simulated server used for development, tests and demos on any operating system.
- Rendering (config files, unit files, firewall rulesets) is pure and covered by golden tests that run anywhere.
- Reconcilers run on change, on a timer, and when the agent reports drift.

## Modules

Modules are compiled into the binary and are dormant until turned on. Each declares its kinds, API, UI, hooks, recipes, upstream software, resource cost and hardware requirements in a manifest. Turning a module on makes it visible and ready; heavy software is installed the first time something needs it, and the preview says exactly what will be downloaded. See [modules.md](modules.md).

## Edge

- Embedded Caddy with Racksail's own modules. The panel maintains a routing table; adding a site never reloads configuration.
- Certificates through ACME: HTTP-01, TLS-ALPN-01, DNS-01 through Racksail DNS (wildcards), on-demand TLS gated by an internal "ask" check, short-lived IP-address certificates, and uploaded certificates.
- HTTP/1.1, HTTP/2 and HTTP/3, compression, static files, FastCGI, reverse proxy to Unix sockets or ports, load balancing across instances.
- Wakes sleeping sites on the first request (systemd socket activation).
- The edge only starts when an enabled module first registers a route.
- The panel listens on its own port (8443 by default) so it stays reachable when the edge is off. When the edge is on, the panel is also reachable on 443 under its own hostname.

## Accounts and isolation

- An account is a Linux user (UID 20000–59999) plus a systemd slice under `racksail-accounts.slice`. Plan limits become kernel limits: `MemoryHigh`, `MemoryMax`, `CPUQuota`, `IOWeight`, `TasksMax`, plus filesystem quotas.
- Sites and workers run as sandboxed systemd services: `ProtectSystem=strict`, private `/tmp`, bind mounts of the account's own data and the chosen runtime only, `NoNewPrivileges`, `SocketBindAllow` for their own socket, system call filtering.
- Isolation ladder: sandboxed service, then a container (rootless Podman per account), then an Incus system container or VM.
- Runtimes are immutable, versioned directories under `/opt/racksail/runtimes/<runtime>/<version>/`, mounted read-only into each sandbox. One copy on disk and in page cache however many sites use it.
- Idle sites sleep. systemd holds the socket, the first request wakes the service and an idle timeout stops it. "Always on" is a per-site setting and a plan feature.

## Storage layout

- Btrfs subvolumes on the system disk: `@` (root), `@log` (`/var/log`), `@state` (`/var/lib/racksail`), `@srv` (`/srv/racksail`), `@db` (database files, copy-on-write off) and `@snapshots`.
- Paths:
  - `/etc/racksail/racksail.toml`: the small hand-edited settings file (listen addresses, data paths, update channel)
  - `/etc/racksail/generated/`: generated configuration, never edited by hand
  - `/var/lib/racksail/state.db`: panel state (SQLite in WAL mode), keys and caches
  - `/srv/racksail/accounts/<account>/`: account data (sites, app data, backups staging)
  - `/opt/racksail/runtimes/` and `/opt/racksail/modules/`: immutable runtime and module payloads
  - `/run/racksail/`: sockets
- Data disks are managed by the storage module and mounted under `/mnt/racksail/`.

## Panel state

SQLite in WAL mode with migrations embedded per module. Litestream can stream the database to S3-compatible storage or to a standby server. All timestamps are UTC.

## UI

- A React and TypeScript single-page app embedded in the binary.
- Shell: icon rail, top bar with the command bar (⌘K), content area, and the assistant dock on the right third (resizable, collapsible, a bottom sheet on phones, absent when the assistant module is off).
- Modules register navigation entries, routes, home cards, settings pages and command-bar actions through a typed registry. Screens are lazy-loaded.
- The API client is generated from `spec/openapi.json`. Live data arrives over Server-Sent Events.
- Forms are generated from JSON Schema plus UI hints, so every kind has a usable screen before it has a custom one.
- One change-preview component is used by forms, recipes, automations, add-ons and the assistant.
- Themes are token sets applied as CSS variables. No component uses a hard-coded color.
- Every string goes through ICU message catalogs, one per module. A pseudo-locale and right-to-left layout are checked in CI.

## Multi-server

- Every server runs an agent. One server (or a standby pair) runs the panel.
- A new server joins with a one-time token and connects out to the panel over mutual TLS, so no inbound management port is needed.
- Servers have roles (web, database, mail, DNS, storage, VMs, backups) and labels. New accounts are placed by capacity.
- DNS secondaries, live account moves, warm standby pairs and a standby panel are provided by the cluster module.

## Updates and distribution

- Racksail releases are signed with Sigstore and published on GitHub Releases. The panel downloads, verifies and applies them, taking a snapshot first and rolling back on failure.
- Everything else comes straight from upstream: Debian mirrors, vendor repositories pinned to their own packages, or vendor archives verified against signatures or checksums and unpacked into versioned directories.
- Update channels: stable, beta and nightly.
- The ISO is a small network installer published on GitHub Releases. A separate firmware ISO adds Debian's non-free firmware for hardware that needs it.

## Installer and first boot

- The ISO boots Debian's signed bootloader and kernel, so Secure Boot works.
- A terminal installer asks five questions (welcome, network, disks, account, review) and can be driven from a browser with a one-time code, or run unattended from an answers file.
- After install, the console shows a status screen with the panel address, a QR code, service status and a recovery menu.
- The first visit to the panel runs the setup wizard: profile (which modules to turn on), hostname and nameservers with live checks, backups and the recovery key, notifications, the assistant, then "create your first site" or "start a migration".
- The same install engine converts an existing Debian 13 server and prepares cloud images.

## Assistant

An optional module. A chat panel docked to the right of every screen, backed by local models (llama.cpp) or a provider the admin connects. Its tools are generated from the API and limited to the current user's permissions. It proposes change sets; people apply them. See the assistant section of [features.md](features.md) and the safety rules in [security.md](security.md).

## Development mode

`racksail dev` runs the panel, agent and edge in one process with the simulated backend and a data directory inside the repository, so the whole product works on macOS or Linux without root. `make dev` also starts the UI dev server with hot reload.
