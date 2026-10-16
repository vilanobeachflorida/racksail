---
title: "How Racksail is built"
description: "The binary's roles, the change pipeline, modules and the agent."
---

Racksail is one Go program, `racksail`, that runs in several roles on a Debian 13 server, and a web UI built into it. This page is the map; the pages after it go into the [change pipeline](../change-pipeline/), the [agent](../agent/) and [modules](../modules/). The full design is in [docs/architecture](https://github.com/vilanobeachflorida/racksail/tree/main/docs/architecture) in the repository.

## Roles

| Role | Runs as | Does |
|---|---|---|
| `panel` | the `racksail` system user | The API, the web UI, the desired state, the change pipeline, scheduling and modules |
| `agent` | root, one per server | Changes the machine, through a fixed list of typed operations only |
| `edge` | the `racksail-edge` user, allowed to bind ports below 1024 | The web server in front of sites: TLS, routing, static files, proxying, waking sleeping sites |
| `cli` | whoever runs it | The command-line client of the API |
| `installer` | root, from the ISO or on Debian 13 | Installs Racksail, or converts a running Debian 13 server |
| `dev` | a developer | Panel, agent and edge in one process against a simulated server |

On a server they run as systemd services in `racksail.slice`: `racksail-agent.service`, `racksail-panel.service`, and `racksail-edge.service`, which starts the first time the panel sends it routes through `racksail-edge.socket`. Accounts' sites and workers run in `racksail-accounts.slice`, one slice per account.

The panel talks to the agent over HTTP and JSON on a Unix socket, `/run/racksail/agent.sock`, and to agents on other servers over mutual TLS, with each server connecting out to the panel so no management port has to be open.

The panel isn't on the path of anything visitors use. Sites, mail, DNS, databases and VMs keep working when the panel is stopped or updating.

## Resources and desired state

Everything Racksail manages is a resource of a registered kind, such as `web.site` or `dns.zone`, with a spec (what should exist) and a status (what was last seen). All kinds live in one store, so history, permissions, hooks, undo and backups work the same for every kind, and registering a kind gives a module its API operations, CLI commands, assistant tools, hook points, events and a generic screen without extra code.

Each kind has a reconciler. Its **plan** is pure: desired state and the machine's facts in, a list of typed agent operations out. Its **apply** sends those operations to the agent and writes the status. Plans are idempotent, so running one twice changes nothing; the panel re-runs every resource's plan every 15 minutes, which puts back anything that drifted.

## The change pipeline

Every change goes through one pipeline, whoever makes it:

```
request → authenticate → authorize → validate → plan → preview → apply → verify → record
```

Because there is one path, every change can be previewed, hooked, audited and undone. See [The change pipeline](../change-pipeline/).

## The system layer

The agent's operations work through system adapters: interfaces for users, files, units, packages, Btrfs, mounts, disks, the firewall, the network, certificates and running programs. There are two implementations: `linux`, the real one for Debian 13, and `sim`, a simulated server used in development, tests and demos on any operating system. Rendering configuration files is separate from applying them and is covered by golden tests that run anywhere.

## Storage on a server

| Path | Holds |
|---|---|
| `/etc/racksail/racksail.toml` | The small hand-edited settings file: listen addresses, paths, the update channel |
| `/etc/racksail/generated/` | Generated configuration, never edited by hand |
| `/var/lib/racksail/` | The panel's state database, keys and caches |
| `/srv/racksail/accounts/<account>/` | Each account's data |
| `/opt/racksail/runtimes/`, `/opt/racksail/modules/` | Runtimes and module payloads, in versioned directories that never change |
| `/run/racksail/` | Sockets |
| `/mnt/racksail/` | Data disks |

The system disk uses Btrfs subvolumes for the system, logs, Racksail's state, account data, databases and snapshots, so risky changes can take a snapshot first.

## The web UI

The UI is a React and TypeScript single-page app built into the binary, so the panel makes no requests to other servers. Its API client is generated from the OpenAPI document, live data arrives over Server-Sent Events, and forms are generated from each kind's JSON Schema until a module ships its own screen. Colors, spacing and type come from theme tokens only, and every string goes through translation catalogs.
