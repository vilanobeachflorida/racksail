---
title: "Extending Racksail"
description: "Themes, rules, automations and add-ons, and which one to reach for."
---

Racksail can be changed in four ways, from the lightest to the most powerful: themes, rules, automations and add-ons. All four take part through the same change pipeline as everything else, so whatever they do is previewed, recorded and can be undone, and catalogs distribute all of them.

## Which one to reach for

| You want to | Reach for | Code? |
|---|---|---|
| Make the panel, its emails and the pages visitors see look like your brand | A [theme](../../themes/overview/) | No: tokens, fonts, logos and logic-less templates |
| Refuse changes that break a policy, or fill in settings on create | A [rule](../rules/) | One line of CEL |
| Do something on a schedule, after an event or on request, such as a monthly restore test | An automation, from the [automations module](../../modules/automations/) | No: triggers and steps |
| Check changes with your own logic, react to them, add to them, sign people in with another directory, send notifications somewhere new or add screens and API routes | An add-on | A program in any language |

Prefer the lightest that does the job. A rule is a line of text anyone can read, while an add-on is a program that has to be trusted, approved and kept up to date.

## Add-ons

An add-on is a program from a catalog that runs as its own sandboxed service and talks to the panel over a Unix socket. It can:

- attach [hooks](../hooks/) to any stage of any operation on any kind: validate hooks that allow or refuse a change, modify hooks that add to it before it's previewed, observe hooks that react once it's applied, and system hooks that patch a service's generated configuration;
- provide a capability behind a standard interface, such as signing in with an LDAP directory or OpenID Connect, a second step for signing in or a notification channel;
- add [kinds of resources](../hooks/#kinds-an-add-on-adds) of its own, which it plans and applies, with the preview, history and undo of every kind;
- add [entries, screens and API routes](../ui-extensions/) to the panel.

Its [manifest](../manifest/), `addon.yaml`, says all of it, with every [permission](../permissions/) it needs: the hook levels, the kinds and fields it reads or writes, the hosts it reaches and the CPU and memory it may use. The person installing it approves them, and the panel enforces them on every call: an add-on only ever receives what it was granted, and only changes what it may.

### Who installs them

| Kind | Installed by | Reaches |
|---|---|---|
| `core.addon` | A server admin | Whatever its permissions grant, across the server |
| `core.account_addon` | An account's owner, from a catalog | Only its account: its changes, at points of the change pipeline, running in the account's slice within its plan |

Approving `override`, `secrets` or `system` hooks always takes a server admin. See [Permissions and sandboxing](../permissions/#add-ons-of-an-account) for what an account's add-on can't do.

### Install one

Add-ons are resources like any other, so installing one is a change with a preview: the panel shows what it will download, the sandbox it sets up and the permissions it grants, and nothing happens until you apply it. Applying it downloads the add-on's program and checks its checksum, writes the add-on's service, `racksail-addon@<id>.service`, and opens its socket. systemd starts the add-on the first time the panel calls it, and the panel attaches its hooks, providers and entries.

Installing an add-on is a high-risk change, since it runs code on the server, and its catalog decides what may be installed: an add-on that names its catalog must be exactly what the catalog's signed index offers. See [Publishing add-ons](../publishing/#what-panels-check).

With the racksail command:

```sh
racksail addon list
racksail addon get addon_01j9…
racksail addon update addon_01j9… --enabled=false
```

### While it runs

Its status says how it is doing:

| Field | What it says |
|---|---|
| `state` | `running`, `stopped`, `safe_mode` while safe mode keeps it from running, or `failed` |
| `version` | The version whose program runs |
| `health` | What it last answered when asked how it is: `ok`, `degraded`, or `unreachable` |
| `hooks` | How many hooks, providers and entries are attached |
| `hosts` | The hosts it may reach, and the addresses they resolved to |
| `slice`, `memoryMB`, `cpuPercent` | Where its service runs and what it may use |

Everything an add-on adds to a change is labelled with its name and version in the preview and the audit log, and undone with the change. A hook that keeps failing is switched off by the [circuit breaker](../hooks/#the-circuit-breaker), and admins hear about it.

### Turn one off, update or remove it

- **Turning it off**, `enabled: false`, stops its service and takes its hooks out of the pipeline, and keeps its settings.
- **Updating it** gives it a new manifest and program. A version that asks for more needs the new permissions approved; the add-on then starts again with a new token, and the directory of the old version is removed.
- **Removing it** stops it, removes its program and forgets its token. Like any change, it can be undone.

### When something goes wrong

[Safe mode](../hooks/#safe-mode) stops every add-on at once, from the console, as a boot option, from the panel, or by itself when an add-on keeps crashing as it starts, so a misbehaving add-on can never keep you out of your server. Modules and rules keep working in safe mode.

## Write your own

[SDKs](../sdks/) shows a complete add-on in Go, Python and TypeScript, and what it serves on its socket. [Publishing add-ons](../publishing/) says how to package it and offer it in a catalog.
