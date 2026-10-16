---
title: "Concepts"
description: "Resources, change sets, modules, accounts and the other ideas the panel is built on."
---

A few ideas run through everything in Racksail. Once you know them, every screen, command and API call works the same way.

## Resources

Everything Racksail manages is a **resource**: a site, a DNS zone, a mailbox, a storage pool, a user. Each resource has a **kind**, written as module and name, such as `web.site`, `dns.zone` or `mail.mailbox`, and an ID that never changes, such as `site_01j9x3k4m5n6p7q8r9s0t1v2w3`.

A resource holds two things:

- its **spec**, what you want: a site's domains and runtime, a mailbox's quota
- its **status**, what Racksail last saw on the machine: running, failed, waiting for DNS, with a message that says why

Racksail keeps every version of every spec, so you can see who changed what and when, and go back.

## Desired state

You never edit configuration files or run commands on the server. You change a resource's spec, and Racksail makes the machine match it: it writes the configuration, installs what's missing, starts or stops services. It checks again every few minutes and puts back anything that drifted, so the server stays the way you described it.

## Change sets

Every change becomes a **change set**, whether you make it in the panel, with the `racksail` command, through the API, from an automation, an add-on or the assistant. A change set can hold several changes that belong together, such as an app's site and its database.

Before anything happens, the change set is checked and **previewed**. The preview lists:

- every resource it creates, updates or deletes, with the fields that change
- what it does to the machine: downloads with their sizes and sources, packages, files, services, ports opened or closed
- the memory and disk it is expected to use
- anything an add-on or a rule added, labelled with where it came from

When you apply it, the change runs as a **job** with live progress, and health checks confirm the result. Changes that remove data take a snapshot first. Every applied change can be **undone**: undo is a new change set that puts the previous specs back, and goes through the same checks and preview.

## Modules

Everything beyond a small core is a **module**: web hosting, mail, DNS, databases, containers, VMs, storage, backups and the rest. Modules ship in the binary, and each is in one of these states:

| State | Meaning |
|---|---|
| Off | Hidden. Nothing installed, no data, no network calls. |
| On | Visible and ready to use. Still nothing heavy installed. |
| Active | Something uses it, so its software is installed and runs as needed. |

Turning a module on or off is a change set with a preview, like any other. See [Modules](../../modules/overview/).

## Accounts and users

An **account** is a tenant: the sites, mailboxes, databases and files that belong together, with the limits of its **plan**. On the server, an account is a Linux user and a systemd slice, so its limits are enforced by the kernel and its services can't see other accounts.

**Users** sign in to the panel. A user can be an admin of the whole server, a reseller with their own customers, the owner of an account, a collaborator on some of its resources, or read-only. What each user can do is checked on every request, whichever way it comes in.

## Jobs and events

Long work, such as installing a runtime or restoring a backup, runs as a **job** you can follow, cancel or retry. Everything that happens is an **event** on the timeline and in the audit log, with who did it and from where: the panel, the command line, an API token, an automation, an add-on or the assistant.

## Recipes and catalogs

A **recipe** describes an app, runtime, service or template as the resources it creates from a few questions, such as a domain and an admin password. Installing a recipe is one change set, previewed and undone as a whole. Recipes come from **catalogs**: the official one, which is signed, and others an admin chooses to trust.

## Hooks and rules

**Hooks** let modules and add-ons take part in changes: check them, add to them, react after them. **Rules** are one-line policies an admin writes, such as "no site may use an `.invalid` domain". Both work on every change, because every change goes through the same pipeline.
