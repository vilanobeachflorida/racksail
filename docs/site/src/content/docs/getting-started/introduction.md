---
title: "Introduction"
description: "What Racksail is, what it manages and the principles it's built on."
---

Racksail turns a Debian 13 server into a self-hosted platform for websites, email, DNS, apps, file storage, containers and virtual machines. You manage all of it from one web panel, the `racksail` command and an API. It's free and open source, all of it: there are no paid tiers, license keys, telemetry, ads or upgrade prompts.

## What it manages

- **Websites**: static, PHP, Node.js, Python, Ruby, Go and container sites, with deploys from Git, staging copies, per-site rules and automatic HTTPS.
- **Email**: mail domains, mailboxes, aliases, spam filtering, deliverability checks and webmail.
- **DNS**: zones and records with DNSSEC, templates for common mail providers, and secondary servers.
- **Databases**: MariaDB, MySQL, Percona Server, PostgreSQL, Valkey and Memcached, side by side.
- **Apps**: containers, compose stacks and a catalog of apps you install with a preview first.
- **Virtual machines**: VMs and system containers through Incus, with consoles in the browser.
- **Storage**: disks, pools, parity arrays, network shares and backups with restic.
- **Hosting business**: plans, resellers, white-labelling and billing integration.
- **Operations**: monitoring, analytics, automations, migrations from other panels, and several servers managed as one.
- **An optional assistant** that works through the same API as you, with local models or a provider you choose.

Each area is a [module](../../modules/overview/). Every module ships in the binary, but a module installs nothing, opens no ports and runs nothing until you use it. A server with every module off idles in under 100 MB of memory.

## How it works

You describe what should exist: a site with these domains, a mailbox with this quota, a nightly backup to this bucket. Racksail stores that as desired state, and an agent on each server keeps the machine matching it. Configuration files are generated from the desired state and never edited by hand, so applying the same state twice changes nothing.

Every change, whether it comes from the panel, the command line, the API, an automation, an add-on or the assistant, goes through one pipeline. It is checked, previewed with everything it will do to the machine, applied, verified and recorded, and it can be undone. You always see a change before it happens.

Sites, workers and scheduled jobs run as systemd services, each account in its own sandbox with its own limits. The web server, mail, DNS and databases don't depend on the panel, so your sites keep running even when the panel is stopped or being updated.

## Principles

1. **Desired state, not scripts.** Racksail stores what should exist, and the agent keeps the machine matching it.
2. **systemd runs everything.** Sites, workers and scheduled jobs are units, slices, sockets and timers.
3. **The panel can go down; the sites can't.** Nothing a visitor reaches depends on the panel process.
4. **Nothing runs until it's needed.** No software, ports or processes until something needs them.
5. **One change pipeline.** Every change, from any source, is validated, previewed, applied and recorded the same way, and can be undone.
6. **Free and open source, all of it.** Anything that isn't is labelled and never installed by default.
7. **Plain, fast and honest.** Every change is previewed, and every error says what happened and what to do next.

## Where to go next

- [Quick start](../quick-start/): install Racksail and put a first site online.
- [Concepts](../concepts/): the ideas the panel is built on.
- [Try it without a server](../try-without-a-server/): run the whole panel on your own computer.
- [Requirements](../../install/requirements/): what a server needs.
