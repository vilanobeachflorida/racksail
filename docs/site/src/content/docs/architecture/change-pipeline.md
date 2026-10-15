---
title: "The change pipeline"
description: "How every change is validated, previewed, applied, recorded and undone."
---

Every change to a Racksail server takes the same path, whether it comes from a screen, the `racksail` command, the API, a recipe, an automation, an add-on or the assistant. Nothing writes settings any other way. That one path is why every change can be previewed, why every change is in the history with who made it, and why every change can be undone.

A request holds one or more changes: create a resource, change its settings, delete it, or run one of its actions. Together they become a change set, which is previewed, applied, recorded and undone as a whole.

## The stages

**1. Who's asking.** The change set records who made it and through what: a person in the panel, a token, an automation, an add-on. Each change is checked against that actor's permissions, on the resource it touches, before anything else happens. A panel with no way to sign people in refuses changes outright.

**2. Is it valid.** The settings are checked against the kind's schema, which comes from the same code that defines the kind, then by the kind's own rules, such as a domain that another site already uses. Then validate hooks run: the security module looking for web shells in a deploy, say, or an add-on that enforces your naming rules. Any of them can refuse the change with a reason. Field errors say which change and which field they belong to, so a screen can highlight them.

**3. What will it do.** Each resource's reconciler plans the steps that make the server match the new settings, and the agent runs them in check mode, so the plan lists exactly what applying it would download, install, write, open, start and restart, and the memory and disk it needs. Changes are planned in order, each seeing what the ones before it create or delete, so a site can be planned against a database that doesn't exist yet.

**4. What else should happen.** Modify hooks run once the plan is known. They can adjust the settings of the changes in the set and add changes and effects of their own, and nothing more: a module can add a standby destination to every new backup policy, for example, or the DNS records a new mail domain needs. What they add is labelled with where it came from and goes through stages 1 to 3 like the rest, and two hooks changing the same field are reported as a conflict instead of one silently winning.

**5. Preview.** With `dryRun=true` the pipeline stops here and answers with the change set: field-by-field differences, what it does to the server, what hooks added and how risky it is. The store writes are made in a transaction that's thrown away, so even a name that's already taken shows up in the preview. Previews can be applied for an hour; applying one carries out exactly what it showed, or answers with a fresh preview if things changed since.

**6. Apply.** When a change deletes data, the pipeline first takes read-only snapshots of what it removes, where the storage allows it. Then all the changes are written in one transaction: either every setting changes or none does. An apply job carries out the plans on the server, with live progress and a log, and runs the health checks of each plan afterwards: a site that answers, a service that's running, a certificate that's valid. A check that doesn't pass fails the change set, which can then be undone.

**7. Record.** Events go out as things happen (`web.site.created`, `core.changeset.applied`), to the timeline, live streams, webhooks and automations. The audit log records who did what, through what and from where, including attempts that were refused. Every version of a resource's settings stays in its history.

**8. Undo.** Undoing a change set makes a new one that puts back the settings it replaced: creates become deletes, deletes come back with the same ID, and changed settings return to what they were, secrets included. The undo goes through every stage above, so it's checked, previewed and recorded like any other change. Bringing back deleted data is a restore from the snapshots stage 6 took.

## Hooks

Hooks attach to points named after the kind, the operation and the stage: `web.site.create.validate`, `web.site.deploy.plan`, `dns.zone.delete.applied`. Every kind has a point for every operation and stage, so modules and add-ons can join any change without the kind's module knowing about them.

| Stage | Hooks | Can |
|---|---|---|
| `validate` | Validate | Allow or refuse a change |
| `plan` | Modify | Adjust settings and add changes and effects, labelled as their own |
| `applied` | Observe | React afterwards, in the background |

Hooks run in a fixed order and each gets a deadline. A hook that fails or runs out of time lets the change through, unless it's set to block, as security hooks are. A hook that fails five times in a row is switched off, and `core.hook.disabled` says so. The [add-on guide](../../addons/hooks/) shows how to write one.

## Secrets

Passwords, keys and tokens never enter settings, so they never appear in change sets, previews, history, events or hooks. A secret field holds a reference, and the value travels next to the settings in the request, to be stored encrypted when the change set is applied. Each change set stores its own version of a secret, so undoing a change also brings back the password it replaced.

## Safe to retry

A request can carry an `Idempotency-Key`. Sending it again with the same key returns the first change set instead of changing anything twice, so a script whose connection dropped can simply retry. Settings carry a revision too: a change made against an old revision fails with a conflict instead of overwriting someone else's work.

## Keeping the server in line

The pipeline isn't the only thing that runs reconcilers. The panel also reconciles every resource when it starts and every 15 minutes after. When the server no longer matches a resource, because something was changed by hand, the reconcile puts it back and `<kind>.drifted` says what it put back; drift the agent reports as it happens is put back at once. Changes whose apply job never ran, because the panel stopped, are finished the same way. Reconciles that fail are retried with a growing wait, and a limit on how many run at once, per kind and in all, keeps a large server responsive.
