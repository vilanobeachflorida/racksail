# Extensibility

Racksail can be changed and extended in four ways, from least to most powerful: themes, rules, automations and add-ons. Catalogs distribute all of them.

## Hooks

Every change flows through one pipeline, so hooks attach to every stage of every operation on every kind. Coverage is guaranteed by construction, and a test fails if an operation is added without its hook points.

### Hook points

Names follow `<kind>.<operation>.<stage>`:

- `web.site.create.validate`, `web.site.create.plan`, `web.site.create.applied`
- `mail.mailbox.delete.validate`, `storage.pool.scrub.applied`
- operations are `create`, `update`, `delete` and every declared action (`deploy`, `restart`, `suspend`, `restore`, ...)
- stages are `validate`, `plan` and `applied` for changes; reads have no hooks

Other hook points:

- `system.<service>.config`: structured patches to generated configuration (`system.caddy.config`, `system.postfix.main`, `system.mariadb.server`, `system.phpfpm.pool`, `system.knot.zone`, `system.samba.share`, ...)
- lifecycle: `core.boot`, `core.update.before`, `core.update.after`, `modules.<id>.on`, `modules.<id>.off`, `backups.run.before`, `backups.run.after`, `migration.cutover.before`, `migration.cutover.after`
- identity: `auth.login.validate` (login policies), `auth.provider` (external identity providers), `auth.mfa` (second-factor methods)
- providers: `provide.dns` (external DNS APIs), `provide.registrar`, `provide.backup-target`, `provide.notifier`, `provide.ai`, `provide.billing`, `provide.importer`
- UI extension points (declarative only): navigation entries, tabs on any kind's detail screen, home cards, command-bar actions, settings pages, table columns

### Hook kinds

| Kind | What it can do | Runs | Example |
|---|---|---|---|
| observe | React after something happened. Can't block | asynchronously | post to a chat channel when a deploy fails |
| validate | Allow or deny, with a reason shown to the user | synchronously, with a deadline | block deploys containing known web shells |
| modify | Add to or change the change set before preview | synchronously, with a deadline | send every backup to a second destination |
| system | Patch generated service configuration, or add new kinds with their own reconcilers | at plan time | add a CDN directive to every site |
| provide | Implement a capability behind a standard interface | on demand | LDAP or OIDC sign-in, Cloudflare DNS, a registrar |
| extend | Add API routes under `/api/v1/addons/<id>/`, screens, tabs, cards and settings | on demand | a "Security" tab on every site |

### Ordering, deadlines and failures

- Hooks run in a deterministic order: priority, then add-on id.
- Two hooks changing the same field is a conflict. The preview shows it and the change can't be applied until it's resolved.
- Every synchronous hook has a deadline (default 2 s, maximum 10 s). Each hook declares its failure mode: `block` (deny on timeout or error; the default for validate hooks marked as security policies) or `allow`.
- A hook that keeps failing is switched off by a circuit breaker and the admin is notified.
- Configuration patches go through the service's own configuration check before they're applied, and are rolled back automatically if the check or the health check fails.

### Invariants

Authentication, authorization checks, the audit log, minimum sandbox settings and the agent's operation allowlist can't be removed by hooks. Hooks can always make them stricter. Loosening any of them requires the `core.override` permission, which only a server admin can grant; it is shown in red and logged.

## Rules

Rules are one-line policies written in CEL (Common Expression Language), stored as `core.policy` resources and evaluated inside the panel. CEL has no loops, side effects or network access, and every evaluation has a cost limit.

- **Validate rules** deny a change with a message: `resource.kind == "web.site" && resource.spec.domains.exists(d, d.endsWith(".invalid"))`.
- **Default rules** fill or override fields on create: every new site gets security headers; every mailbox gets a 5 GB quota unless the plan says otherwise.
- Variables: `request` (operation, actor, source), `resource`, `oldResource`, `account`, `plan`, `user`, `server`.
- Scopes: server-wide (admins), reseller, or account.

## Automations

Workflows with triggers (schedule, event, webhook, manual), steps (API actions, conditions, waits, approvals, notifications, recipe runs), variables, retries and timeouts. They run deterministically, without the assistant in the loop. The assistant can draft them. See the automations section of [features.md](features.md).

## Add-ons

An add-on is a package from a catalog that runs as its own sandboxed process.

- **Process:** a systemd service `racksail-addon@<id>.service` with a dynamic user, its own cgroup limits, no network unless declared, and a read-only view of the system.
- **Protocol:** HTTP+JSON over a Unix socket. The panel calls the add-on for hooks (`POST /hooks/<hook-point>`); the add-on calls the panel API with a token limited to its granted permissions. The protocol is described by an OpenAPI document in `spec/addon-protocol.json`.
- **SDKs:** Go (`sdk/go`), TypeScript (`sdk/ts`) and Python (`sdk/python`), plus a local test harness that fakes the panel.
- **Manifest** (`addon.yaml`): id, name, version, license (SPDX), source URL, author, description, supported core API versions, hooks (point, kind, priority, deadline, failure mode), permissions, UI extensions, API routes, kinds it adds.

### Permissions

- Hook levels: `observe`, `validate`, `modify`, `system`.
- Data scopes: which kinds and which fields it may read or write. Secrets need a separate `secrets:read` permission with a red warning.
- Network: none by default; declared hosts only.
- Resources: CPU and memory limits.
- Tenancy: add-ons installed by an account owner affect only that account and can never hold `system` or `core.override`.
- Permissions are shown at install, approved by the person installing, and enforced at runtime: an add-on only ever receives data it was granted.

### Safety

- Everything an add-on changes is labelled in the preview ("added by Backup Policy 1.2"), recorded in the audit log and undoable.
- Safe mode turns off every add-on. It's available from the console menu, as a boot option, and automatically when an add-on crash-loops at startup.
- Add-on screens that need more than declarative UI run on their own origin inside a sandboxed frame and can never read the panel session.
- The hook API is versioned. Add-ons declare the versions they support, and deprecated versions keep working for a published period.

## Themes

A theme is a package of design tokens and asset files. It contains no code.

- **Tokens**, for light and dark: surfaces, text, borders, brand, accent, status colors (success, warning, danger, info), chart colors, focus ring; typography (bundled font files, type scale, weights); radius; spacing density; shadows; motion speed.
- **Assets:** logos (light, dark, mark, favicon), login background, illustrations.
- **Templates:** notification emails, and the error, maintenance and suspension pages shown to visitors of hosted sites. Templates are logic-less (variables only).
- **Console:** the terminal palette used by the installer, console status screen and CLI.

### Rules

- Every theme is validated against `spec/schemas/theme.schema.json` and checked for WCAG AA contrast. A theme that fails is rejected.
- Themes can't load anything from the network. Fonts and images are bundled.
- Themes can't hide or restyle safety-critical UI: destructive confirmations, warnings in the change preview, security prompts, license labels and permission screens.
- Custom CSS is allowed only when written by a server admin for their own server. It is off by default, shows a warning, and can never be distributed through a catalog.

### Layers

1. The default theme (the Racksail brand)
2. The server theme, set by an admin
3. A reseller theme, seen by that reseller's customers
4. Personal preferences: light, dark or system; high contrast; density; text size; reduced motion

A built-in theme editor shows a live preview on real screens with a contrast checker, and can import, export and publish themes.

## Catalogs

- **The official catalog** lives in `catalog/` in this repository. Its index is built and signed in CI and published as a release asset. Panels fetch it daily when the marketplace module is on, and never otherwise.
- **Third-party catalogs** are added by an admin from a URL. Their index must be signed by the key the admin trusted when adding them.
- **Item kinds:** `app`, `runtime`, `service`, `stack`, `dns-template`, `automation`, `tuning-preset`, `model`, `theme`, `addon`.
- **Licensing:** catalogs, including ours, may offer software that isn't open source. Every item shows its license class (open source, free to use, or proprietary). Nothing that isn't open source is ever installed by default, and installing it shows a clear notice first. AI models only need to be free to download and use.
- **Trust:** official items are reviewed and tested on every release. Third-party items are labelled as such.
- **Quality:** every official recipe has named maintainers, an install test in CI and a health check. Recipes whose tests keep failing are marked broken; unmaintained recipes are eventually hidden.
- New upstream releases wait a few days before catalog recipes offer them, except security fixes.
