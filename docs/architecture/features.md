# Features

The complete feature list for Racksail 1.0. Everything here ships in 1.0; there is no "later" list. Each item is done when it meets the definition of done in [conventions.md](conventions.md).

## 1. Core platform

- Single `racksail` binary with roles: panel, agent, edge, cli, installer, dev
- Generic resource store: TypeIDs, kinds, scopes, spec/status, generations, labels, soft delete, history of every spec version
- Kind registration that generates API operations, CLI commands, assistant tools, hook points, events, audit, backup coverage and generic screens
- Reconciler framework: plan/apply split, status conditions, retries with backoff, drift detection, periodic resync, per-kind concurrency limits
- Change sets: field-level diffs, planned system effects (downloads, packages, ports, services, files, memory and disk estimates), hook contributions labelled by source, conflict detection, dry run, apply as a job, undo, pre-change snapshots for risky changes
- Jobs: queue, progress, live logs over SSE, cancel, retry, history
- Events: typed events, timeline with retention, audit log with actor and source (UI, CLI, API token, automation, add-on, assistant), event streaming over SSE
- Lazy services: modules and their software start only when needed; idle detection
- Settings file (`racksail.toml`) plus runtime settings stored as resources
- Secrets store sealed with systemd-creds (TPM2 when available)
- Health endpoints and self-diagnostics ("what's wrong with this server" report)
- Resource budgets enforced in CI (idle memory targets)

## 2. API, CLI and integrations

- REST API under `/api/v1`, code-first with Huma, OpenAPI 3.1 committed to `spec/openapi.json` with a drift check
- RFC 9457 errors with stable codes; translatable messages
- Dry-run on every mutation, idempotency keys, cursor pagination, filtering, sorting, field selection
- Server-Sent Events for jobs, events, logs and metrics; WebSockets for terminals and VM consoles
- Generated Go and TypeScript clients
- `racksail` CLI covering every operation: profiles for multiple servers, JSON and table output, shell completion, `--dry-run`, live job progress
- Outgoing webhooks for every event type, signed, with retries and delivery logs
- API reference site generated from the OpenAPI document
- Rate limits per token and per IP

## 3. Identity and access

- Users, accounts (tenants), roles: admin, reseller, account owner, collaborator, read-only; per-object grants
- Accounts map to Linux users and systemd slices
- Sign-in: Argon2id passwords, passkeys, TOTP, recovery codes, remember-device
- Sessions: list, revoke, idle and absolute timeouts, rotation
- Step-up confirmation for sensitive actions
- Lockouts and rate limits; login alerts by notification channel
- API tokens: scoped to permissions and accounts, expiring, last-used tracking
- SSH keys per user
- Impersonation ("view as customer") with audit and a visible banner
- External identity providers through `auth.provider` hooks (LDAP and OIDC add-ons in the official catalog)
- Audit log viewer with filters and export
- Optional IP allowlist for the panel

## 4. Agent and system layer

- Agent server on a Unix socket (and mutual TLS for remote servers) with a typed operation allowlist
- Operations with strict argument validation: users and groups, files and directories, systemd units/slices/sockets/timers, unit control, packages (apt with pinned sources; vendor archives with signature or checksum verification), Btrfs subvolumes/quotas/snapshots/send-receive, mounts, firewall rulesets, network configuration, sysctl, processes, certificates, archives
- `linux` implementation and a complete `sim` implementation (simulated disks, NICs, CPUs, packages, services, logs and metrics) so every feature works in development
- Rendering separated from applying, with golden tests
- Sandbox profiles for account workloads (see security.md)
- Kernel and network defaults: BBR, fq, sensible connection limits, zram swap
- Time sync, UTC
- OS updates: unattended security updates; scheduled full upgrades with a snapshot first; reboot scheduling for kernel updates
- Drift reporting back to the panel

## 5. Edge and certificates

- Embedded Caddy with a live routing table (no reloads to add or change sites)
- ACME: HTTP-01, TLS-ALPN-01, DNS-01 through Racksail DNS (wildcards), on-demand TLS with an ask check, short-lived IP certificates for the panel, uploaded certificates, expiry tracking and alerts, distribution to mail and other services
- HTTP/1.1, 2 and 3; zstd and gzip compression; static files; FastCGI; reverse proxy to sockets and ports; load balancing and health checks
- Wake-on-request for sleeping sites
- Per-site rules: redirects, rewrites, headers with security presets, password-protected paths, IP allow and deny, CORS, hotlink protection, custom error pages, maintenance mode, canonical www and HTTPS redirects, HSTS
- Bot classification (humans, search engines, AI crawlers, other bots), per-class allow/rate-limit/block, robots.txt management
- Web application firewall (Coraza with OWASP CRS) per site, detect or block mode
- Page cache with purge API
- Structured access logs feeding analytics and bandwidth accounting
- Temporary URLs and the migration preview proxy (host rewrite plus URL rewriting in responses)
- The panel on 443 under its hostname when the edge is on

## 6. UI shell and design system

- Shell: icon rail, top bar, content area, assistant dock (right third, resizable, collapsible, bottom sheet on phones)
- Command bar (⌘K) merged with the assistant input: navigate, run actions, ask questions, `/` commands
- Home: the attention inbox (things that need you), quick actions, recent activity
- Design system: tokens, roughly 30 components on shadcn/ui and Radix, Lucide icons, Inter and JetBrains Mono bundled
- Schema-driven forms (JSON Schema plus UI hints) with grouping, defaults, inline validation, help text and an Advanced section
- Change-preview component (diffs, system effects, hook contributions, conflicts, apply/undo)
- Jobs drawer with live logs; toasts with undo
- Tables: virtualized, sortable, filterable, bulk actions, saved views
- Charts (uPlot): sparklines, time series with event-timeline overlays
- Skeleton loading, designed empty and error states
- Light, dark and system modes; high contrast; density; text size; reduced motion
- Keyboard navigation and shortcuts everywhere; WCAG 2.2 AA
- Live data over SSE
- Generic screens for every kind until a custom screen exists
- Responsive down to phone width
- Modules page: every bundled module with state, usage and cost, turn on/off/remove with preview
- Settings, profile, sessions, tokens, SSH keys, notification preferences
- Onboarding wizard (see section 33)

## 7. Themes

- Theme format: tokens (light and dark), bundled fonts, logos, favicon, login background, email and error-page templates, console palette
- Layering: default, server, reseller, personal preferences
- Validation: schema, WCAG AA contrast, no network references, protected safety-critical UI
- Theme editor with live preview on real screens and a contrast checker; import, export, publish to a catalog
- Admin-only custom CSS for their own server, off by default
- Branded emails, error, maintenance and suspension pages for hosted sites
- Roundcube and other bundled tools pick up theme colors where they support it

## 8. Extensibility

- Hook pipeline with every hook point listed in extensibility.md; a test fails if an operation lacks hook points
- Hook kinds: observe, validate, modify, system, provide, extend
- Ordering, deadlines, failure modes, conflict detection, circuit breaker
- Structured config patches (`system.<service>.config`) checked by the service's own config test, with rollback
- CEL rules (`core.policy`): validate and default rules, scoped to server, reseller or account, with a rule tester in the UI
- Add-on runtime: sandboxed systemd service per add-on, Unix socket HTTP+JSON protocol, scoped tokens, network allowlists, resource limits
- Add-on permissions screen; runtime enforcement; tenancy limits
- Declarative UI extensions; sandboxed-origin frames for complex add-on screens
- Safe mode (console, boot option, automatic on crash loop)
- Versioned hook API with deprecation policy
- SDKs for Go, TypeScript and Python with a local test harness and examples

## 9. Modules and profiles

- Module manager: manifests, dependencies, hardware requirement checks, cost estimates, states (off, on, active, stopping, removing), install on first use
- Turn on, off and remove with previews; removing offers export first and needs typed confirmation
- Profiles: All-in-one, Web hosting, Home server, Virtualization host, Mail server, DNS server, Minimal
- Required modules can't be turned off (network)
- A fresh install with every module off idles under 100 MB
- Every module passes the on → use → off → remove test with nothing left behind except data the user kept

## 10. Network

- Interface and IP inventory (IPv4 and IPv6), add and remove addresses
- IP roles: shared, dedicated (account or site), nameserver, outbound mail, reserved; assignment updates DNS records automatically
- NAT mapping of private to public addresses (cloud servers, home networks)
- Gateways per subnet and policy routing; IPv6 link-local gateways; static and DHCP
- Bonds, VLANs, bridges (used by VMs and containers)
- Floating and failover IPs with provider modules (Hetzner, DigitalOcean, Vultr, Linode) through `provide.*` hooks
- systemd-networkd rendering with safe apply (60-second automatic rollback unless the browser confirms)
- Outbound IP per account (cgroup-marked traffic and SNAT)
- Firewall: default deny, ports per active module, allow and deny lists, country blocking (registry data), rate limits, custom rules
- Brute-force bans for SSH, panel, SFTP, mail and WordPress logins (journal watchers into nftables sets with timeouts)
- Per-IP reverse DNS display, blocklist checks, outbound port 25 check
- Hostname and FQDN management

## 11. Web hosting

- Sites with stacks: static, PHP-FPM (per-site version and settings), Apache-compatible (.htaccess), Node, Python (uv), Ruby, Go and other compiled binaries, Bun, Deno, any container image (rootless Podman), Incus system container or VM
- Runtimes as immutable versioned directories, patch versions rolled forward automatically, majors pinned
- Framework detection and presets: WordPress, Laravel, Symfony, Drupal, Joomla, Magento, Next.js (standalone), Nuxt, Astro, SvelteKit, Express, NestJS, Django, Flask, FastAPI, Rails, Hugo, Jekyll, Eleventy
- Domains: attach, verify (DNS TXT or HTTP), aliases, subdomains, redirects, www handling; automatic DNS records when Racksail hosts the zone; instructions and live checks when it doesn't
- Temporary URL for every site
- Deploys: Git (any host, deploy keys, webhooks, branch selection), zip upload, direct edit mode; builds in capped sandboxes; releases; atomic switch; rollback; keep N releases; pre and post hooks; zero-downtime restarts
- Preview deployments per branch
- Environment variables and encrypted secrets
- Processes: web, workers, queues, instance count, always-on or sleep when idle, scheduled jobs as systemd timers
- Staging: one-click clone (snapshot, database clone, URL rewriting), push to production (files, database or both; WordPress search-replace through WP-CLI)
- PHP settings screen (memory, upload size, timeouts, OPcache) and an advanced php.ini editor
- WP-CLI, Composer and npm inside the sandbox
- Site logs (application output, PHP errors, access), live tail
- Suspension and maintenance pages
- Resource usage per site within the account's limits
- Legacy runtime policy: strictest sandbox, WAF blocking, warning badge

## 12. Builder

- Build recipes: runtime, version, extensions (with versions), flags, ini defaults; recipe hash as identity
- Extension availability matrix per runtime version
- Era-matched build roots from snapshot.debian.org for old versions (PHP 5.4 and up)
- Relocatable bundles with their own libraries, content-addressed cache, unused-build cleanup
- Prebuilt popular recipes published by CI to GitHub Container Registry, signed; custom builds compiled locally at low priority
- PHP, PECL extensions, Node, Python and Ruby builds
- Build form in the UI and live build logs
- Changing a site's extensions rebuilds and switches atomically, with rollback

## 13. Hosting business

- Plans: CPU, RAM, disk speed, processes, disk space, bandwidth, sites, domains, databases, mailboxes, mail storage, sending rate, backup policy, shell access, assistant budget, isolation level, dedicated IP, feature toggles
- Usage accounting (cgroups, quotas, edge bandwidth, mail volume) and over-limit handling: notify, throttle, suspend
- Account lifecycle: create, suspend, unsuspend, terminate with grace period and data retention
- Resellers: own plans within an allocation, own customers, white-label theme, panel hostname, vanity nameservers, reseller IPs
- Customer notification emails (welcome, limits, incidents), translatable and themed
- Billing: FOSSBilling provisioning module; generic provisioning API and signed webhooks; billing add-ons through the catalog

## 14. Databases

- Engines: MariaDB (several versions), MySQL, Percona Server, PostgreSQL (several versions), Valkey, Memcached
- Each capability has a conformance test suite every engine must pass
- Database servers: shared per server or dedicated per account, versions side by side, per-server settings and tuning
- Databases, users, grants, password rotation, remote access allowlist (with firewall rules), per-user connection and query-time limits
- Size tracking, quota warnings, write locking over quota
- SQL import and export, per-database dumps and restores, clones for staging
- Slow-query capture and digest; index suggestions
- Adminer with single sign-on from the panel
- Engine switch helper (MariaDB, MySQL, Percona) with compatibility checks
- Valkey per site as an object cache

## 15. DNS

- Knot DNS authoritative server and Unbound local resolver
- Zones and records: A, AAAA, CNAME, ALIAS (flattened at the apex), MX, TXT, SRV, CAA, NS, PTR, TLSA, HTTPS, SVCB, DS; TTLs; change history with undo
- Inline editor with linting (CNAME at the apex, conflicts, dangling records)
- Templates from the catalog (Google Workspace, Microsoft 365, Fastmail, Proton, Zoho, iCloud) and a default template for new domains
- DNSSEC: automatic signing and key rollover; DS record shown for the registrar
- Import from zone files, AXFR and a scan of current public records
- Propagation checker (opt-in external queries)
- Nameservers: ns1/ns2 addresses, glue instructions, vanity nameservers for resellers
- Secondaries: cluster servers through catalog zones; external free secondaries (for example Hurricane Electric) through TSIG-signed transfers
- Reverse zones for owned IP blocks
- DNS-01 provider for certificates
- External DNS providers through `provide.dns` add-ons

## 16. Mail

- Postfix, Cyrus IMAP, Rspamd and Valkey; Racksail SASL service
- Mail domains, mailboxes, aliases, forwarders, catch-alls, autoresponders, Sieve filters (ManageSieve), quotas, app passwords, suspension
- Automatic records and setup: DKIM generation and rotation, SPF, DMARC, MTA-STS (policy hosted on the edge), TLS-RPT, Thunderbird autoconfig, Outlook autodiscover, Apple configuration profiles
- Spam: per-mailbox settings, allow and block lists, training, quarantine
- Outbound: relay through a provider, per-account rate limits, per-IP sending, sender rewriting (SRS) for forwards, PHP mail tagged by account
- Deliverability checklist with one-click fixes (PTR, HELO, port 25, blocklists, alignment)
- DMARC and TLS report viewer
- Queue view and message trace ("where did my email go?")
- Webmail: Roundcube as a managed site with single sign-on and theme colors
- IMAP, POP3, submission and JMAP with certificates from the edge
- Migration support: Maildir import, password hash carry-over, IMAP-to-IMAP copy

## 17. Containers

- Docker Engine or Podman as the engine; rootless Podman for per-account containers
- Apps from catalog recipes, from any compose file, or from imported Unraid templates
- Settings: image, ports, volumes mapped to shares and storage, environment, CPU and memory limits, restart policy, health check
- Networking: bridge, own LAN address (macvlan/ipvlan), host network, custom networks, IPv6
- Web UIs published through the edge with HTTPS and optional panel sign-in in front (forward auth)
- GPU access: Intel and AMD through /dev/dri
- Updates: digest checks, snapshot of app data first, rollback, per-app auto-update policy
- Logs, metrics, shell into a container, start/stop/restart
- Privileged containers and Docker socket access only after an explicit warning
- Backups of app data

## 18. Virtual machines

- Incus VMs and system containers
- ISO library (upload or download by URL), templates (Windows 11 with virtual TPM and virtio drivers injected, Linux cloud images with cloud-init)
- CPU, memory, disks, networks (bridged, NAT, isolated), CPU pinning, hugepages
- PCI and GPU passthrough with an IOMMU group viewer; USB passthrough; hardware checks with clear explanations
- Browser console (VGA and serial)
- Snapshots, backups, clones, autostart order
- Live migration between servers (with the cluster module)
- Import of Unraid and libvirt VM definitions and disks

## 19. Storage

- Disk inventory: model, serial, size, type, temperature, health, SMART attributes and alerts
- Burn-in tests for new disks, guided disk replacement
- Pools: Btrfs (single, RAID1, RAID1C3, RAID10), mdadm (RAID 1, 5, 6, 10), ZFS (mirror, RAIDZ1/2/3; opt-in with the Secure Boot key step explained)
- Parity arrays for mixed disks: SnapRAID parity (1–6 disks) with mergerfs pooling, scheduled sync and scrub, per-disk spin-down
- Cache tiering with a scheduled mover
- Scrubs, rebalance, capacity forecasts
- Snapshots and quotas per dataset
- Typed confirmations for anything destructive; read-only first for imported disks
- Import of existing Unraid arrays (data disks mounted read-only, parity rebuilt)

## 20. Shares

- SMB (Samba), NFS and WebDAV shares
- Share users and groups, permissions, guest access, read-only shares
- Time Machine over SMB
- Recycle bin
- Per-share quotas and snapshots (previous versions in Windows Explorer)

## 21. Home extras

- UPS through Network UPS Tools: status, graceful shutdown, alerts
- WireGuard VPN: peers, QR codes for phones, split or full tunnel
- Headscale for self-hosted mesh networking
- Wake-on-LAN
- Dynamic DNS for changing home IPs (Racksail DNS or external providers)
- Hardware sensors: temperatures, fans, power
- Single sign-on for apps (forward auth now; OIDC provider for apps that support it)

## 22. Marketplace and catalog

- Recipe engine: validate, plan as a change set, apply, upgrade, uninstall keeping data, health checks, provenance
- Item kinds: app, runtime, service, stack, dns-template, automation, tuning-preset, model, theme, addon
- Official catalog in `catalog/`: index built and signed in CI, daily sync, verification
- Third-party catalogs from a URL with a trusted signing key
- License classes (open source, free to use, proprietary) shown on every item; notice before installing anything not open source; nothing non-free by default
- Trust tiers, permissions preview, fit check (RAM, disk, hardware)
- Catalog UI: grid, search, categories, detail, install wizard from the recipe's inputs, installed instances
- Upstream verification, release cooldown (security fixes exempt), Renovate bumps, CI install test per recipe, maintainers, broken and unmaintained flags, featured list
- Starter set: WordPress, WooCommerce, Laravel, Symfony, Drupal, Joomla, Magento, Ghost, Nextcloud, Umami, Forgejo, Uptime Kuma, static site, Next.js, Nuxt and Astro starters; PHP presets 5.4–8.5; Node LTS lines; Python; MariaDB, MySQL, Percona, PostgreSQL, Valkey, Memcached; DNS templates for Google Workspace, Microsoft 365, Fastmail, Proton, Zoho and iCloud; automations (staged WordPress updates, uptime alerts, monthly restore test, certificate watch, weekly database optimisation); tuning presets (WordPress, WooCommerce, Laravel, Magento); local AI models in three sizes; the default theme plus high-contrast and compact themes

## 23. Backups and disaster recovery

- Btrfs snapshots on a schedule with retention
- restic repositories: local disk, S3-compatible, SFTP, rest-server, Backblaze B2; multiple destinations; bandwidth limits
- Per-database dumps
- Portable account archive: export, import, customer download; shared with migration
- Policies per plan: frequency, retention (hourly, daily, weekly, monthly), destinations
- Restore anything (file, folder, database, DNS zone, mailbox, site, app, VM, account, server) from a point-in-time browser; restore to another location; customer self-service
- Key ceremony (show once, confirm saved) and key rotation
- Automated monthly restore tests and integrity checks
- Low-priority, spread-out scheduling
- Snapshot before risky changes
- Server-to-server replication for standby
- Whole-server restore from the installer

## 24. Monitoring and analytics

- Metrics: per account, site, app, VM and service (CPU, memory, I/O, pressure), system, edge requests, databases, mail
- Storage: SQLite rollups (per minute for 2 days, per hour for 90 days, per day after that)
- Event timeline across everything, overlaid on charts
- Services: inventory, health checks (HTTP, TCP, DNS, TLS), dependencies
- Logs: separate journal namespace per account, live tail, search, retention; access logs as compressed daily files with search
- Site analytics: visitors (privacy-preserving uniques), pages, referrers, countries, devices, status codes, bandwidth, response times; no tracking script, no cookies
- Bots view with AI-crawler controls wired to the edge
- Early warnings without the assistant: disk-full forecast, memory creep, restart loops, error spikes, login attacks, sudden email volume
- Alerts with channels (email, webhook, Matrix, ntfy, Telegram, Discord, Slack), per-user preferences, quiet hours
- Security events; pending Debian security updates; known vulnerabilities in runtimes (OSV)
- Prometheus endpoint and OpenTelemetry export

## 25. Performance

- Measured defaults for databases, PHP workers, OPcache and connection limits
- Collectors: PHP-FPM status, PHP and SQL slow logs, database status, OPcache and cache hit rates
- Rules engine producing suggestions with evidence and expected impact
- Apply through change sets; before-and-after comparison; optional automatic rollback when worse
- App presets as recipes; one-click object cache, page cache and image optimisation
- Optional auto-tune within plan limits
- Capacity test against your own site

## 26. Files and terminal

- File manager: browse, chunked uploads, zip and unzip, move, copy, rename, permissions, search, trash, previews, code editor (CodeMirror 6), diff against the last snapshot
- SFTP users chrooted to their directories; SSH keys; shell access inside the account sandbox (plan feature); no FTP (imported FTP users become SFTP users)
- Web terminal (xterm.js): sandboxed for customers; root terminal for admins with step-up confirmation, idle timeout and session logs
- Open a terminal or editor from any folder

## 27. Assistant

- Providers: any OpenAI-compatible API (OpenAI, OpenRouter, Mistral, Groq, Ollama, LM Studio, vLLM), Anthropic, Google Gemini
- Local models through llama.cpp as a socket-activated, capped, lowest-priority service
- Model picker: catalog of models (any model free to download and use), RAM-based recommendation, background resumable downloads, checksum verification, on-server speed test, switch and delete
- Model routing per job (command bar, chat, diagnosis, writing automations)
- Tools generated from the API, grouped and scoped to the current page, each with a risk level
- MCP server so outside assistant apps can use the API with the user's permissions
- Chat dock: context chip that follows navigation, @-mentions of resources, rich cards (change sets, charts, log excerpts), evidence links, model indicator, slash commands, conversation history with retention and deletion
- Inline actions: explain this error, fix this failed deploy, explain this log line, suggest an index
- Permission modes: ask every time (default), auto-approve reversible changes, read-only
- Diagnostics engine (site down, site slow, mail delivery, DNS, certificates, disk, migration) producing structured reports
- Docs search through SQLite full-text search
- Safety: untrusted content marked as data, no free-form command, HTTP or email tools, secrets redacted, optional admin shell tool with per-command approval
- Privacy: allowed providers per account, local-only mode, token budgets and usage per account
- Replies in the user's language
- Evaluation suite of scripted scenarios per model size, run on every release

## 28. Automations

- Triggers: schedule, events, webhooks, manual
- Steps: API actions, conditions, waits, approvals, notifications, recipe runs; variables, retries, timeouts, concurrency limits, dry run
- Run history with logs; failure alerts
- Writing automations in plain words with the assistant, then previewing and saving
- Templates from the catalog; step editor; import and export as files
- Runs under a scoped service identity, fully audited

## 29. Migration

- Migration wizard and dashboard: source, scan, plan with warnings, select, sync, preview, cutover, mail catch-up, finish or roll back
- CWP: live, read-only over SSH (accounts, packages as plans, domains, subdomains, aliases, document roots, PHP versions, databases with users and grants, mail domains, mailboxes, aliases, forwarders, autoresponders, password hashes, DNS zones, certificates, cron jobs, FTP users as SFTP users)
- cPanel: cpmove archives and live transfer through a WHM API token
- DirectAdmin and HestiaCP/VestaCP: backup archives
- Plesk: live import over SSH
- Unraid: settings from the flash drive, array and pool layout, shares, Docker templates, VM definitions and disks, users; data disks mounted read-only; parity rebuilt
- Any SSH server: files, databases and a mapping screen
- .htaccess translator with automatic fallback to the Apache-compatible stack
- DNS cutover options (lower TTLs, optional update on the old server, nameserver change), mail cutover with catch-up sync, IMAP-to-IMAP copy
- Preview of every migrated site before switching
- Parsers fuzz-tested; synthetic fixtures for every source

## 30. Multi-server

- Join with a one-time token; outbound mutual-TLS connection
- Server roles and labels; capacity-aware placement
- Cluster views: servers, health, capacity
- DNS secondaries on every DNS server
- Live account moves between servers
- Warm standby pairs (Btrfs send/receive, database replicas), failover by floating IP or DNS, failback
- Panel standby through Litestream with promote
- Version-skew policy and rolling upgrades
- Cross-server roles (a site on one server using a database on another)

## 31. Security scanning

- ClamAV scanning (opt-in), web-shell rules for PHP, scheduled and on-upload scans, quarantine
- System file integrity monitoring
- Findings in the attention inbox and as validate hooks (block deploys with known web shells)

## 32. Installer, ISO and images

- ISO built with live-build for amd64 and arm64; BIOS and UEFI; Secure Boot through Debian's signed chain; under 2 GiB
- Separate firmware ISO with Debian's non-free firmware, clearly labelled
- Branding: GRUB menu, Plymouth splash, installer theme, console status screen, SSH login message
- Terminal installer (Bubble Tea): Welcome (keyboard; Install, Rescue, Restore), Network (DHCP, static, IPv6, VLAN), Disks (single, mirror, custom; existing data detection; reinstall keeping data), Account (password with strength meter, SSH keys, GitHub key import, hostname), Review, Progress, Done
- Browser install mode with a one-time code; unattended answers file (USB, URL or boot parameter)
- Install engine: mmdebstrap from Debian mirrors with fallback, Btrfs subvolume layout, bootloader, users, network, Racksail binary and units
- Rescue mode (shell, mount, reset admin password, repair network) and restore-from-backup mode
- Errors explained with retry and log export to USB
- Convert an existing Debian 13 install (same engine)
- Cloud images from Debian's official images, with cloud-init
- Hardware and VM test matrix in CI

## 33. First boot and console

- Setup wizard: profile, hostname and nameservers with live checks, IP and NAT detection, port 25 and reverse DNS checks, assistant setup (model download pre-selected by RAM), backup destination and key ceremony, notification channel, then create a site, install an app or start a migration
- Console status screen: panel address, QR code, service status, menu (network, reset password, shell, safe mode, reboot, shut down)
- SSH login message with status

## 34. Updates and releases

- Signed releases with SBOMs and checksums on GitHub Releases; channels stable, beta, nightly
- Self-update with snapshot and automatic rollback; rolling updates across servers
- Upstream package updates: security updates automatic; major versions gated by tested manifests
- Catalog index updates
- Debian major upgrade orchestration (13 to 14) with snapshots
- Mirror list support for releases and catalogs

## 35. Translations

- ICU messages in every layer (panel, installer, console, CLI, emails)
- English at launch; translation through Weblate
- Pseudo-locale run in CI; right-to-left layouts
- Locale-aware dates, numbers and sizes
- API error codes translated in the UI

## 36. Docs, brand and community

- Docs site: getting started, install, every module, migration guides, recipe authoring, add-on development, themes, API reference, architecture, security, FAQ
- In-panel help linked to docs, sharing the assistant's docs search
- Brand guidelines: logo usage, colors, typography, voice, UI patterns, applications (panel, installer, console, docs, README)
- README with screenshots
- CONTRIBUTING (DCO), CODE_OF_CONDUCT, SECURITY, GOVERNANCE, issue and pull request templates

## 37. Quality

- `make check` (format, lint, vet, build for darwin and Linux amd64/arm64, unit tests, UI build and tests)
- CI on every change; nightly end-to-end runs in QEMU; upgrade tests from the previous release
- Fuzzing, race detector, axe accessibility checks, visual regression, bundle budget, idle memory budget
- License allowlist and vulnerability scanning
- Hook coverage, permission coverage, backup coverage and translation coverage tests
