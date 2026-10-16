---
title: "Frequently asked questions"
description: "Short answers to common questions about Racksail."
---

## Is Racksail free?

Yes, all of it. The code is licensed under the GNU Affero General Public License, version 3 or later, and everything Racksail ships or installs by default is free and open source. There are no paid tiers, license keys, ads or upgrade prompts. Catalogs may offer software that isn't open source, always labelled with its license class and never installed unless you accept its license.

## Does it send anything about my server anywhere?

No telemetry, ever. The panel contacts the internet for what you can see it doing: GitHub, or the mirrors you list, once a day to look for new Racksail releases; the catalogs you trust, only while the marketplace module is on; Debian's mirrors and the sources of the software you install, each download shown with its source before it happens. The web panel loads nothing from anywhere but your server, fonts included.

## What does it run on?

Debian 13 (trixie), on amd64 or arm64, and nothing else. The server needs at least one CPU core, 1 GB of memory and 20 GB of disk; two cores, 4 GB and 40 GB are better. See [Requirements](../install/requirements/).

## Can I use a server I already have?

If it runs Debian 13, yes: the [install script](../install/existing-server/) converts it in place, or install the Debian package. Otherwise, install from the [ISO](../install/iso/), which erases the disks you choose, or start from a [cloud image](../install/cloud-images/).

## Can I try it without a server?

Yes. `racksail dev` runs the whole panel against a simulated server on your own computer, on macOS or Linux, without root and without changing anything on the machine. See [Try it without a server](../getting-started/try-without-a-server/).

## Do I need Docker?

No. Racksail runs sites, workers and scheduled jobs as systemd services with limits and sandboxes, and doesn't need a container engine for any of it. Containers are a module of their own, for when you want them.

## Will my sites go down when the panel does?

No. Websites, email, DNS, databases, apps and virtual machines never depend on the panel running: the edge that serves sites keeps its own copy of what to serve, and everything else is ordinary systemd services. The panel is where you change things, not what keeps them running.

## Why doesn't a fresh install install anything?

Because nothing runs until it's needed. Every module ships in the binary but installs nothing, opens no ports and starts nothing until it's turned on and something uses it, and even then only what that needs: no PHP version is installed until a site uses it. Every change says what it will download and install, and how much memory and disk it takes, before it happens.

## Can I undo a change?

Almost always. Every change is a change set you preview before applying it, and once applied it can be undone, which is another change set built from what was there before. When a change removes data kept on Btrfs, as on a server the ISO installed, the panel snapshots it first. See [Change sets](../getting-started/concepts/#change-sets).

## Can I script it?

Everything the panel does goes through its API, so everything has an API operation and a command: `racksail` is also a client for any panel, and every operation API tokens may use has a command. See [Using the API](../api/overview/) and [The racksail command](../api/cli/).

## Can I change how it looks?

Yes, with a [theme](../themes/overview/): your colors, fonts, logos and email templates, for the whole server or for a reseller's customers. Themes are design tokens and files, never code, and are checked for contrast so the panel stays readable.

## Can I extend it?

With [rules](../addons/rules/), one-line policies that refuse changes or fill in settings, and with [add-ons](../addons/overview/), programs that hook into every change, sign people in with another directory, send notifications somewhere new, or add screens and API routes, always with the permissions you approve.

## How does it update?

From **Settings → Updates**, on the stable, beta or nightly channel. The panel checks that the release workflow signed a release, snapshots the server, installs it and rolls back by itself when the new release doesn't come back healthy. It keeps Debian's own packages up to date too. See [Updates and release channels](../install/updates/).

## Where is my data?

Each account's files are under `/srv/racksail/accounts/<account>/`, the panel's state in `/var/lib/racksail/`, and the settings file in `/etc/racksail/racksail.toml`. Everything Racksail generates is in `/etc/racksail/generated/` and is never edited by hand.

## How do I report a security problem?

Privately, through GitHub's private vulnerability reporting, never in a public issue. See [Reporting a vulnerability](../security/reporting/).

## Who makes Racksail?

Its maintainers and contributors, in the open. Anyone can propose a change; [Governance](../community/governance/) says how decisions are made, and [Contributing](../community/contributing/) how to send yours.
