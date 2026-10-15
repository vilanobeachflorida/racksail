# Modules

Everything beyond a small core is a module. Modules are compiled into the `racksail` binary, so they ship with every install, but they stay dormant until they're turned on, and even then they install nothing until something needs it.

## The core

The core is always present and is kept small. Target: under 100 MB of memory when idle with every module off.

- resource store, reconcilers and the change pipeline
- jobs, events, timeline and audit log
- module manager, recipe engine and catalog client
- hooks, rules (CEL policies) and the add-on runtime
- users, accounts, roles, sessions, passkeys, two-factor, API tokens
- API server, CLI, OpenAPI export
- agent and system adapters (`linux` and `sim`)
- the edge runtime (dormant until a module registers a route)
- theme engine, i18n, notifications (email, webhook, Matrix, ntfy, Telegram, Discord, Slack), basic health checks
- self-update and the console status screen
- the installer role

## Module states

| State | Meaning |
|---|---|
| `off` | Not visible. Nothing installed, no data, no network calls. |
| `on` | Visible in the UI and ready. Kinds can be created. Nothing heavy is installed yet. |
| `active` | At least one resource needs it, so its upstream software is installed and its services run as needed. |
| `stopping` / `removing` | Turning off keeps data and settings; removing uninstalls software and deletes data only after a typed confirmation and an offered export. |

Every transition is a change set with a preview. The preview lists every download (source and size), package, port, service, file and the estimated memory and disk cost. A module whose hardware requirements aren't met (for example VMs without hardware virtualization) explains why and can't be turned on.

## The module contract

Each module lives in `modules/<id>/` (Go) and `ui/src/modules/<id>/` (UI), with docs in `docs/site/src/content/docs/modules/<id>.md`. A module provides:

- `manifest.yaml` (embedded): id, name, description, version, license, category, dependencies, profiles it's on by default in, hardware requirements, resource cost estimates, upstream software it can install (with sources, pinning and verification), capabilities it provides and needs, settings schema, permissions it adds, hook points it adds beyond the generated ones, recipes it ships
- kinds: Go types for spec and status, JSON Schema (generated from the types), validation, actions with risk levels, UI hints
- reconcilers: plan and apply, using agent operations only
- API: anything beyond the generated kind operations, registered with the API server
- events it emits
- backup contributions: what data belongs to each kind and how to quiesce it
- metrics and health checks for anything that runs
- assistant hints: short descriptions and examples that make its tools easy to use
- UI: registry entry (navigation, routes, home cards, settings pages, command-bar actions), screens, ICU messages
- docs page
- tests: unit, golden renders, simulated end-to-end flows

## Bundled modules

| Id | Name | What it manages | Upstream software, installed on first use | Depends on |
|---|---|---|---|---|
| `network` | Network | IP addresses and their roles, NAT mapping, gateways and routing, IPv6, bonds, VLANs, bridges, firewall, brute-force bans, outbound IP per account, reverse DNS and blocklist checks | systemd-networkd, nftables | (required, always on) |
| `web` | Web hosting | sites, domains on sites, runtimes, deploys, releases, staging, previews, per-site rules, temporary URLs | Caddy (embedded), PHP from packages.sury.org, Node, Python (uv), Ruby, Bun, Deno, Apache (for .htaccess sites) | network |
| `hosting` | Hosting business | plans and limits, resellers, white-labelling, usage accounting, suspensions, billing integration (FOSSBilling), customer notifications | — | web |
| `databases` | Databases | database servers (engine and version), databases, users, grants, remote access, slow queries, Adminer | MariaDB, MySQL, Percona Server, PostgreSQL (PGDG), Valkey, Memcached, Adminer | network |
| `dns` | DNS | zones, records, DNSSEC, templates, secondaries, nameservers and glue, reverse zones | Knot DNS, Unbound | network |
| `mail` | Mail | mail domains, mailboxes, aliases, forwarders, catch-alls, autoresponders, filters, spam, outbound relay, deliverability, message trace, webmail | Postfix, Cyrus IMAP, Rspamd, Valkey, Roundcube | network; uses dns and web when on |
| `containers` | Containers | container apps, compose stacks, images, networks, volumes, app updates | Docker Engine (moby) or Podman | network |
| `vms` | Virtual machines | VMs, system containers, ISO library, templates, passthrough, consoles, snapshots | Incus, QEMU, OVMF, swtpm | network |
| `storage` | Storage | disks, SMART, pools (Btrfs, mdadm, ZFS), parity arrays (SnapRAID + mergerfs), cache tiering, spin-down, scrubs, disk replacement | btrfs-progs, mdadm, snapraid, mergerfs, smartmontools, hd-idle, zfs-dkms (opt-in) | — |
| `shares` | Shares | SMB, NFS and WebDAV shares, share users, permissions, Time Machine, recycle bin | Samba, nfs-kernel-server | storage |
| `home` | Home extras | UPS, WireGuard VPN, Headscale, Wake-on-LAN, dynamic DNS, sensors, single sign-on for apps | nut, wireguard-tools, Headscale, lm-sensors | network |
| `marketplace` | Marketplace | catalogs, browsing and installing recipes, third-party catalogs and add-ons, themes from catalogs | — | — |
| `backups` | Backups | snapshot schedules, restic repositories, policies, restores, key ceremony, restore tests, portable archives | restic | — |
| `monitoring` | Monitoring and analytics | metrics, logs, event timeline, alerts, site analytics, bot controls, security events, update tracking | — | — |
| `files` | Files and terminal | file manager and editor, SFTP users, SSH access, SSH keys, web terminal | OpenSSH (present) | — |
| `performance` | Performance | measured defaults, recommendations, app presets, before/after comparisons, capacity tests | oha (on first capacity test) | web or databases |
| `builder` | Builder | runtime build recipes, era-matched build roots, build cache, custom PHP builds | mmdebstrap and build tools inside build roots | web |
| `ai` | Assistant | chat, providers, local models, model downloads, MCP server, diagnostics | llama.cpp, model files | — |
| `automations` | Automations | workflows, triggers, runs, templates | — | — |
| `migration` | Migration | importers for CWP, cPanel, DirectAdmin, HestiaCP and VestaCP, Plesk, Unraid and any SSH server; migration plans; previews; cutover | rsync, openssh-client | target modules |
| `cluster` | Multi-server | servers, roles, placement, account moves, standby pairs, failover, panel standby | Litestream | — |
| `security` | Security scanning | malware and web-shell scanning, system file integrity | ClamAV (opt-in) | — |

## Profiles

Profiles are bundles of modules turned on by the setup wizard. They can be changed at any time.

| Profile | Modules turned on |
|---|---|
| All-in-one (default) | network, web, hosting, databases, dns, mail, containers, vms (when hardware supports it), storage, shares, marketplace, backups, monitoring, files, performance, automations |
| Web hosting | network, web, hosting, databases, dns, mail, marketplace, backups, monitoring, files, performance, automations |
| Home server | network, storage, shares, containers, vms (when supported), home, marketplace, backups, monitoring, files, automations |
| Virtualization host | network, vms, containers, storage, backups, monitoring, files |
| Mail server | network, mail, dns, backups, monitoring |
| DNS server | network, dns, backups, monitoring |
| Minimal | network |

The assistant, migration, cluster, builder, security and home extras are offered separately in the wizard or turned on later. Builder turns itself on when a recipe needs a custom build; migration turns itself on for an import.

## Registration

- Go: `modules/all/all.go` imports every module package. Each module registers itself in `init()` through `modules.Register`.
- UI: `ui/src/modules/registry.ts` lists every module with a lazy import of its entry file.
- Both registries list all 22 modules from day one, so adding work to a module never touches another module's files.
