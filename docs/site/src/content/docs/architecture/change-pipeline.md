---
title: "The change pipeline"
description: "How every change is validated, previewed, applied, recorded and undone."
---

Nothing writes to Racksail's state directly. The panel, the command line, the API, recipes, automations, add-ons and the assistant all hand their changes to one pipeline, which is what makes previews, hooks, audit and undo complete.

## A change set

A request holds one or more changes, and they become one **change set**: one preview, one transaction, one job and one undo. Installing an app that needs a site and a database is one change set, so it's previewed, applied and undone as a whole.

Each change creates, updates or deletes a resource, or runs one of a kind's actions, such as deploying a site.

## The stages

1. **Authenticate and authorize.** The caller is identified, and each change is checked against what they may do, including through grants on single resources.
2. **Validate.** Defaults are filled in, the spec is checked against the kind's JSON Schema and its own rules, then against the admin's rules and the validate hooks of modules and add-ons.
3. **Plan.** The kind's reconciler works out what the change does to the machine, as typed agent operations. Changes are planned in order, each seeing what the earlier ones create, so a site can refer to a database the same change set creates. Modify hooks may add to the plan; what they add is labelled with its source, and two of them changing the same field is a conflict that must be resolved.
4. **Preview.** The plan runs in the agent's check mode, which reports what each operation would change without changing it. The preview lists field-level differences, downloads with their sizes and sources, packages, files, services, ports, and the expected memory and disk.
5. **Apply.** The store is updated in one transaction, and a job runs the plan with live progress. A change set that removes data first takes read-only Btrfs snapshots of what it removes; if taking them fails, nothing changes.
6. **Verify.** Health checks confirm the result: HTTP, TCP, TLS, DNS and systemd unit checks, each retried until it passes or times out. A failed check fails the change set, which can then be undone.
7. **Record.** Events, the timeline and the audit log record who did what and through what: the UI, the CLI, an API token, an automation, an add-on or the assistant.

Reads never go through the pipeline.

## Dry runs

Every mutating API call accepts `?dryRun=true`, and the CLI `--dry-run`. A dry run goes through every stage up to the preview, and even makes the store writes in a transaction it throws away, so it reports problems such as a name that's already taken. It returns the change set without changing anything.

## Undo

Undo turns an applied change set into a new request: creates become deletes, deletes become creates with the same IDs, and updates put the previous specs back. The new request goes through every stage, so an undo is authorized, validated, hooked and previewed like any change. Bringing back data a change removed is a restore from the snapshot the change took.

## Retries

A request may carry an `Idempotency-Key` header. Sending the same request with the same key returns the first change set instead of making a second one, for 24 hours, so a client can retry safely after a network error.

## Secrets

Passwords and keys never enter specs, so they never reach change sets, history, events, hooks or previews. A secret field holds a reference to one of the resource's own secrets; the value travels beside the change and is stored encrypted when the change set is applied. Each change set stores new versions, so undo restores the previous reference and the previous value is still there.
