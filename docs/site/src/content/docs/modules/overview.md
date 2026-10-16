---
title: "Modules and profiles"
description: "How modules are turned on and off, what each profile includes and what a module costs."
---

Racksail is built from modules: web hosting, mail, DNS, databases, containers, virtual machines and the rest. Every module ships in the `racksail` binary, but a module that is off installs nothing, runs nothing, stores nothing and makes no network calls. Turning one on is a change you preview first, like any other.

Find them in **Settings → Modules**. Each card says what the module does, what it costs in memory and disk when idle and when in use, what it needs, what uses it, the software it installs once something needs it, and why it can't run on this server when that's the case. Cards follow the module's state as it changes, such as a module turning active when the first resource uses it.

## States

| State | What it means |
|---|---|
| Off | Not visible in the panel. Nothing is installed. |
| On | Visible and ready. Its software is installed the first time something needs it. |
| Active | Something uses it, such as a site, so its services run. |
| Turning off | Its services are stopping; its resources and data stay. |
| Being removed | Its software is being uninstalled. |

A module's state is a resource of its own, `core.module`, so turning it on or off goes through the change pipeline: hooks and rules see it, the audit log records it and the change can be undone.

## Turning a module on

**Turn on** shows a preview of everything the module would do: starting in the panel, with the memory it takes while idle, and what it does once something uses it, the software it downloads then, from where and how large, the packages that brings in, the services it runs, the ports it opens, and the memory and disk it takes. The sizes come from the server's package index where it can tell, and from the module's estimates otherwise. What the server said is kept with the module until it is asked again, so a preview applied later, even after the panel restarted, does what it showed. Modules it needs are turned on with it, in order. Nothing heavy is installed yet: a module installs its software the first time a resource needs it, so turning on the mail module costs nothing until you create a mailbox.

A module whose hardware requirements this server doesn't meet, such as virtual machines on a processor without hardware virtualization, can't be turned on, and its card says why.

## Turning a module off

**Turn off** stops the module's services and hides it, keeping its resources, settings and data. Turning it on again brings everything back. A module that resources still use can only be turned off when it can keep them dormant; otherwise remove what uses it first, or remove the module with its data. Required modules, such as the network module, stay on.

## Removing a module

**Remove** turns a module off and uninstalls its software, and the module shows as being removed while it does. It is offered once the module is off, and while it is on for a module that can't keep its resources while it is off, which is then removed with them. With **Keep its data**, its resources and their data stay, and come back when the module is turned on again; without it, they are deleted, after a snapshot, and **Download its resources** first keeps a copy of their settings as a JSON file. Fields that refer to secrets, such as passwords, are left out of it. You type the module's name to confirm, and the removal is previewed like any change. A removed module installs its software again the first time something needs it once it is on again.

Modules can share software: mail keeps Rspamd's statistics in Valkey, which the databases module also runs for sites. What another module still uses stays when a module is turned off or removed: its packages, its package repositories, its services and its ports. A module uses its software while it is on, and while it keeps the resources it was turned off with. The preview lists what stays and which module still uses it, and removing the last module that uses something uninstalls it.

## Profiles

A profile turns on several modules at once, as the setup wizard does. Modules this server's hardware can't run are left off and listed.

Every profile includes the network module, which is always on.

| Profile | Modules |
|---|---|
| All-in-one | Web, hosting, databases, DNS, mail, containers, virtual machines where the hardware allows, storage, shares, marketplace, backups, monitoring, files, performance, automations |
| Web hosting | Web, hosting, databases, DNS, mail, marketplace, backups, monitoring, files, performance, automations |
| Home server | Storage, shares, containers, virtual machines where the hardware allows, home extras, marketplace, backups, monitoring, files, automations |
| Virtualization host | Virtual machines, containers, storage, backups, monitoring, files |
| Mail server | Mail, DNS, backups, monitoring |
| DNS server | DNS, backups, monitoring |
| Minimal | The network module only |

The assistant, migration, the cluster, the builder and security scanning are in no profile: turn them on when you need them.

## From the API

```sh
api=https://panel.example.com:8443/api/v1
auth="Authorization: Bearer $RACKSAIL_TOKEN"
curl -H "$auth" $api/modules                                                 # every module, its state, needs and cost
curl -H "$auth" $api/modules/mail/software                                   # what it installs, from where, how large
curl -H "$auth" -X POST "$api/modules/mail/actions/turn_on?dryRun=true"        # preview turning it on, with what it needs
curl -H "$auth" -X POST "$api/modules/mail/actions/turn_off?dryRun=true"       # preview turning it off
curl -H "$auth" $api/modules/mail/export > mail.json                         # its resources, before removing it
curl -H "$auth" -X POST "$api/modules/mail/actions/remove?dryRun=true" -d '{"confirm": "Mail", "keepData": false}'
curl -H "$auth" -X POST "$api/modules/profiles/web-hosting?dryRun=true"       # preview a profile
```

The actions of a module's `core.module` resource, `POST /api/v1/core/modules/{id}/actions/turn_on` and the others, change that one module: turning it on that way needs the modules it needs on already, and removing it without its data needs its resources deleted in the same change set.

Every change answers with its change set; with `dryRun=true` it is the preview, which `POST /api/v1/changesets/{id}/apply` applies as it was shown.
