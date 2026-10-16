---
title: "Hooks"
description: "Where hooks attach, the six kinds, the order they run in, deadlines, failures and the circuit breaker."
---

Every change in Racksail goes through one pipeline, so hooks can attach to every stage of every operation on every kind of resource. Modules, add-ons and rules all take part through hooks; the pipeline treats them alike.

## Hook points

Points are named `<kind>.<operation>.<stage>`:

- `web.site.create.validate`, `web.site.create.plan`, `web.site.create.applied`
- `mail.mailbox.delete.validate`, `storage.pool.scrub.applied`

Operations are `create`, `update`, `delete` and every action a kind declares, such as `deploy` or `restart`. Stages are `validate`, before anything is planned; `plan`, while the change set is assembled; and `applied`, once it was carried out. Reads have no hooks. A test fails when a kind, an action or an API operation is added without its points, so nothing escapes them.

Validate, modify and observe hooks can name several points with a pattern, where `*` stands for one part of the name: `web.site.*.validate` or `*.*.create.plan`.

Other points:

| Points | When |
|---|---|
| `system.<service>.config` | A service's generated configuration is being written: `system.caddy.config`, `system.postfix.main`, `system.mariadb.server` and others modules declare |
| `core.boot`, `modules.<id>.on`, `modules.<id>.off` | The panel started; a module was turned on or off |
| `core.update.before`, `core.update.after` | Racksail is about to update itself, and did |
| `core.os_upgrade.check`, `core.os_upgrade.before`, `core.os_upgrade.after` | An upgrade of Debian to its next release is planned (validate hooks can refuse it), starts, and finished or failed |
| `backups.run.before`, `backups.run.after`, `migration.cutover.before`, `migration.cutover.after` | Around a backup and a migration's cutover |
| `auth.login.validate`, `auth.provider`, `auth.mfa` | Sign-in policies, identity providers and second factors |
| `provide.<capability>` | Providers: `provide.dns`, `provide.registrar`, `provide.backup-target`, `provide.notifier`, `provide.ai`, `provide.billing`, `provide.importer` |
| `ui.nav`, `ui.tab`, `ui.card`, `ui.command`, `ui.settings`, `ui.column`, `api.routes` | An add-on's entries in the panel and the API routes it serves |

**Settings → Rules** and `GET /api/v1/hooks/points` list every point; `GET /api/v1/hooks` lists the hooks attached to them, in the order they run.

## Kinds of hooks

| Kind | What it can do | Runs | Example |
|---|---|---|---|
| observe | React after something happened; it can't block | in the background | post to a chat channel when a deploy fails |
| validate | Allow or refuse, with a reason shown to the person making the change | in line, with a deadline | refuse deploys that contain known web shells |
| modify | Add to or change the change set before it is previewed | in line, with a deadline | send every backup to a second destination |
| system | Patch a service's generated configuration | while the change is planned | add a CDN directive to every site |
| provide | Implement a capability behind a standard interface | when asked | Cloudflare DNS, LDAP sign-in |
| extend | Add API routes under `/api/v1/addons/<id>/`, screens, tabs, cards and settings | when asked | a "Security" tab on every site |

What a modify hook adds is labelled with where it came from in the preview, and the audit log records it as the hook's.

## Order, deadlines and failures

- Hooks run in a fixed order: by priority, then by owner, so the order never depends on what was installed first.
- Two hooks setting the same field to different values is a conflict. The preview shows it, and the change can't be applied until it's resolved.
- Every in-line hook has a deadline: two seconds unless it asks for more, ten at most.
- A hook that errs or runs out of time follows its failure mode: `allow` lets the change through, and is the default; `block` stops it, and is the default for validate hooks marked as security policies.
- A modify hook that changes more than it may, such as another resource, has failed, and its work is thrown away.

### The circuit breaker

A hook that fails five times in a row is switched off. The panel publishes `core.hook.disabled`, which reaches admins through their notifications, and records it in the audit log. It stays off across restarts until an admin switches it back on, from **Settings → Rules** or with `POST /api/v1/hooks/enable`.

## Configuration patches and invariants

System hooks patch a copy of a service's configuration. The patched configuration goes through the service's own check, such as `postfix check` or `caddy validate`, before it is written; when the check fails, or the service doesn't come back healthy, the previous configuration is put back.

Some settings can't be loosened by any hook: authentication, authorization checks, the audit log, the minimum sandbox of services and the agent's allowlist of operations. Hooks can always make them stricter. Loosening one needs the `core.override` permission, which only a server admin can grant; every time it is used, the panel logs it and records it in the audit log. Rules never hold it. **Settings → Rules** marks the hooks that hold it in red.

## Add-ons of an account

An add-on an account owner installs, a `core.account_addon`, affects only that account:

- its hooks attach only to points of the change pipeline, `<kind>.<operation>.<stage>` or patterns of them such as `web.site.*.validate`, never to `core.boot`, `auth.login.validate`, `modules.<id>.on` or any other point that belongs to the whole server;
- it hears only about the changes to its account: a change set that also changes other accounts reaches it with those changes left empty, and without its title, effects and estimate;
- it can't provide a capability, since what add-ons provide serves the whole server, patch configuration with system hooks, or hold `core.override`.

An add-on whose manifest asks for any of these can only be installed by a server admin, as a `core.addon`.

## Safe mode

Safe mode takes every add-on out of the pipeline and stops it, so a misbehaving add-on can't keep the panel from working. It comes on:

- with the `racksail.safe_mode` boot option, until the server boots without it: at the boot menu, press <kbd>e</kbd>, add `racksail.safe_mode` to the end of the line that starts with `linux`, and press <kbd>Ctrl</kbd>+<kbd>X</kbd> to boot;
- from the server's console, with `racksail safe-mode on`, until `racksail safe-mode off`;
- from the panel, in **Settings → Rules**, or with `POST /api/v1/safe-mode`;
- by itself, when an add-on crashes three times within two minutes of starting.

Modules and rules keep working in safe mode. `racksail safe-mode status` says why it's on.
