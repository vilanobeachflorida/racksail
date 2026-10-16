# Decisions

Settled choices. Change one by opening a pull request that edits this file and explains why.

## Project

| # | Decision |
|---|---|
| P1 | Name: **Racksail**. Tagline: "Your server, your rules." Binary and CLI: `racksail`. |
| P2 | License: AGPL-3.0-or-later for the code. Catalog recipes: MIT. Documentation: CC BY 4.0. |
| P3 | Contributions use the Developer Certificate of Origin (sign-off), not a contributor license agreement, so no single party can relicense the project. |
| P4 | Everything the project ships or installs by default is free and open source. No paid tiers, license keys, telemetry, ads or upgrade prompts. Catalogs may list other software, labelled, never installed by default. |
| P5 | Go module path: `github.com/vilanobeachflorida/racksail`. |

## Platform

| # | Decision |
|---|---|
| T1 | Target OS: Debian 13 (trixie), amd64 and arm64. Nothing else is supported. |
| T2 | Server code: Go (current stable), CGO disabled, so every binary cross-compiles from any machine. |
| T3 | Panel state: SQLite in WAL mode through `modernc.org/sqlite`. One generic resource table plus per-module tables where needed. Optional Litestream replication. |
| T4 | IDs: TypeID (prefixed UUIDv7), for example `site_01j9x3k4m5n6p7q8r9s0t1u2v3`. |
| T5 | Public API: REST under `/api/v1`, built code-first with Huma v2 on `net/http`. The generated OpenAPI 3.1 document is committed to `spec/openapi.json`; CI fails if it drifts from the code. JSON fields use camelCase. Errors use RFC 9457 problem details with a stable `code` field. |
| T6 | Live updates: Server-Sent Events. The web terminal and VM consoles use WebSockets. |
| T7 | Panel ↔ agent and panel ↔ add-on: HTTP+JSON described by OpenAPI, over Unix sockets locally and mutual TLS between servers. No protobuf toolchain. |
| T8 | CLI: cobra. Its client commands are built when it runs from the binary's own OpenAPI document, so every operation API tokens may use has a command, modules' included, and operations only people in the panel may use, such as signing in and changing how someone signs in, have none. This replaced a generated Go client: built from the document, a new operation needs nothing written to get its command, flags, help and completion. |
| T9 | Edge: Caddy v2 embedded as a library, with Racksail modules for the routing table, wake-on-request, analytics tap and page cache. |
| T10 | Rules: CEL via `github.com/google/cel-go`. |
| T11 | Authentication: Argon2id passwords, passkeys (`github.com/go-webauthn/webauthn`), TOTP (`github.com/pquerna/otp`), recovery codes, sessions in HTTP-only secure cookies, scoped expiring API tokens. Users are `core.user` resources, owned by the server, a reseller or an account, and change through the change pipeline like any resource: previews, hooks, rules, history and undo. How they sign in, password hashes, passkeys, second steps, sessions and tokens, is kept apart in the identity tables and never part of a change set. Outside identity providers and further second steps come from add-ons through `auth.provider` and `auth.mfa`; the official catalog's LDAP and OpenID Connect add-ons are built from `addons/`. |
| T12 | Secrets at rest: encrypted with a key sealed by systemd-creds (TPM2 when present, host key otherwise). |
| T13 | Configuration file: TOML (`/etc/racksail/racksail.toml`). Authored metadata (manifests, recipes, themes, add-ons): YAML validated by JSON Schema in `spec/schemas/`. |
| T14 | Logging: `log/slog`, structured, never secrets. |
| T15 | systemd: D-Bus through `github.com/coreos/go-systemd/v22`. Firewall: rulesets rendered as nftables text, checked with `nft --check` and loaded in one transaction with `nft --file`, sets changed with `nft add element`. Interfaces, addresses and routes: read from `ip -json`. DNS parsing: `github.com/miekg/dns`. Incus: its Go client. Docker and Podman: their HTTP APIs. A rendered ruleset can be previewed, kept as a golden file and diffed, and loads atomically, which building it from `github.com/google/nftables` netlink calls can't offer; reading the network needs nothing that `ip -json` doesn't give, so neither `github.com/vishvananda/netlink` nor `github.com/google/nftables` is a dependency. Both programs are run with argument vectors, like every other. |
| T16 | External tools are always run with an argument vector through the agent, never through a shell. |

## Software Racksail manages

| # | Area | Choice |
|---|---|---|
| S1 | Network config | systemd-networkd, rendered by the agent; nftables for the firewall |
| S2 | Web | embedded Caddy; PHP-FPM from packages.sury.org (side-by-side versions); Apache per account for .htaccess sites; runtimes as versioned directories |
| S3 | Databases | MariaDB (default, from Debian), MySQL, Percona Server (vendor archives, side by side), PostgreSQL (apt.postgresql.org, side by side), Valkey, Memcached; Adminer for web access |
| S4 | DNS | Knot DNS (authoritative, automatic DNSSEC, catalog zones); Unbound as the local resolver |
| S5 | Mail | Postfix, Cyrus IMAP (IMAP, POP3, JMAP, Sieve, replication), Rspamd with Valkey, Roundcube as a managed site; a Racksail SASL service so imported password hashes keep working |
| S6 | Containers | Docker Engine (moby, from Debian) for apps; rootless Podman for per-account containers |
| S7 | VMs and system containers | Incus with QEMU, OVMF and swtpm |
| S8 | Storage | Btrfs by default; mdadm; ZFS opt-in (DKMS); SnapRAID + mergerfs for mixed-disk parity arrays; smartmontools; hd-idle |
| S9 | Shares | Samba (with Time Machine support), kernel NFS server |
| S10 | Backups | Btrfs snapshots plus restic |
| S11 | Home extras | Network UPS Tools, WireGuard, Headscale, lm-sensors |
| S12 | Assistant | llama.cpp for local models; providers: any OpenAI-compatible API, Anthropic, Google Gemini |
| S13 | Analytics | server-side from edge access logs; country data from the regional internet registries' delegation files; no tracking script |
| S14 | Malware scanning | ClamAV, opt-in |
| S15 | Billing | FOSSBilling module plus generic API and webhooks; other billing systems through catalog add-ons |

## UI

| # | Decision |
|---|---|
| U1 | React 19, TypeScript (strict), Vite, TanStack Router and TanStack Query, Tailwind CSS v4, shadcn/ui components on Radix primitives, lucide-react icons, uPlot charts, CodeMirror 6, xterm.js, react-intl (ICU messages). Biome for lint and format. Vitest and Testing Library for unit tests, Playwright for end-to-end tests. npm is the package manager. |
| U2 | Fonts: Inter and JetBrains Mono, both bundled. The UI makes no external requests. |
| U3 | Brand colors: navy `#01214F`, blue `#007CFD`. Every color reaches components through theme tokens. |
| U4 | API client: `openapi-typescript` types plus `openapi-fetch`, generated from `spec/openapi.json`. |
| U5 | Docs site: Astro Starlight in `docs/site/`, published to GitHub Pages. |

## Paths, ports and IDs

| # | Decision |
|---|---|
| D1 | `/etc/racksail/racksail.toml`, `/etc/racksail/generated/`, `/var/lib/racksail/`, `/var/lib/racksail-agent/` (the agent's own state, root only), `/var/log/racksail-agent/` (the agent's audit trail, root only), `/var/lib/racksail-edge/` (the edge's certificates, settings and last routing table), `/srv/racksail/accounts/<account>/`, `/opt/racksail/runtimes/`, `/opt/racksail/modules/`, `/run/racksail/`, `/mnt/racksail/` (data disks) |
| D2 | Panel listens on 8443 (HTTPS). When the edge is on, the panel is also served on 443 under its hostname. Dev mode listens on 127.0.0.1:7070 over HTTP. |
| D3 | Account slugs: lowercase letters and digits, 3–16 characters, starting with a letter. Linux user name: the slug. UIDs 20000–59999. Subordinate IDs for rootless containers start at 1,000,000, 65,536 per account. |
| D4 | systemd: `racksail.slice` for platform services, `racksail-accounts.slice` with one `racksail-accounts-<slug>.slice` per account. systemd nests a slice under the slice its name extends, so only this name puts an account inside `racksail-accounts.slice`; `racksail-acct-<slug>.slice` would land in an implicit `racksail-acct.slice` instead. |
| D5 | Temporary site URLs: `<site>-<account>.<server hostname>` through a wildcard record; `sslip.io` addresses when there's no domain yet. |
| D6 | Servers run on UTC. The UI shows times in the viewer's time zone. |

## Distribution

| # | Decision |
|---|---|
| R1 | Releases: GitHub Releases, signed with Sigstore (cosign keyless), with SBOMs (syft) and checksums. Prebuilt runtime builds and catalog indexes: GitHub Container Registry as OCI artifacts. |
| R2 | The ISO is built with Debian live-build in GitHub Actions, boots Debian's signed shim, GRUB and kernel, and installs Debian from the official mirrors. A separate firmware ISO adds Debian's non-free firmware. |
| R3 | Installer UI: Bubble Tea terminal app, a browser mode protected by a one-time code, and an unattended answers file (YAML). |
| R4 | CI: GitHub Actions. Dependency updates: Renovate. Vulnerability scanning: govulncheck and osv-scanner. License allowlist check on every change (`make licenses`): OSI-approved licenses compatible with AGPL-3.0, and the exceptions listed here, each with its reason, which `packaging/licenses/exceptions.yaml` lets through. `github.com/zeebo/blake3` is CC0-1.0, a public domain dedication the FSF lists as compatible with the GPL that the OSI never approved because it disclaims patent grants; `github.com/caddyserver/certmagic` needs it, so decision T9 does. The npm package `mdn-data` is CC0-1.0 too: CSS reference data that only the UI's and the docs site's build tools read, so none of it ships. |
| R5 | Resource targets: core idle under 100 MB; web-only under 250 MB; everything on and idle under 500 MB. Minimum server: 1 CPU, 1 GB RAM, 20 GB disk. Recommended: 2 CPUs, 4 GB, 40 GB. |

## To confirm before launch

What the maintainers still have to set up or confirm. Each item is done, and its row removed, before the first release.

| # | Item |
|---|---|
| L1 | `conduct@racksail.dev`, where code of conduct reports go as `CODE_OF_CONDUCT.md`, `GOVERNANCE.md` and the documentation site's community pages say, is a placeholder. The address has to exist and reach every maintainer listed in `GOVERNANCE.md` and nobody else, or another address takes its place everywhere it appears. |
| L2 | The official catalog's signing key doesn't exist yet. A maintainer creates it as `catalog/keys/README.md` describes and commits `catalog/keys/official.pub` with its copy in `internal/recipes/keys/`. Until then, panels don't add the official catalog and the Catalog workflow publishes nothing. |
| L3 | The browser suite has no visual baselines yet, so CI compares no screens. A maintainer runs the Screenshots workflow on `main` and reviews what it commits. |
| L4 | Translation through Weblate, as section 35 of `features.md` has it, needs the project's Weblate set up and connected to the repository by a maintainer. Until then, translations come as pull requests, as the documentation site's translations page says. |
