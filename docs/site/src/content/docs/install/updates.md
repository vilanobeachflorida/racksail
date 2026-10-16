---
title: "Updates and release channels"
description: "How Racksail updates itself, the stable, beta and nightly channels, rolling back, Debian's own updates and upgrading to Debian's next release."
---

Racksail updates itself from **Settings → Updates**. It finds releases on its channel, checks that Racksail's release workflow signed them, snapshots the system, installs the new release and checks that it comes back healthy. A release that doesn't is rolled back by itself.

## Channels

| Channel | Gets | For |
|---|---|---|
| Stable | Releases such as `1.4.2` | Servers people depend on |
| Beta | Betas and release candidates such as `1.5.0-beta.1` and `1.5.0-rc.1`, and stable releases | Trying the next release early |
| Nightly | A build of every day's changes, such as `1.5.0-alpha.nightly.20261015.42`, and everything above | Testing and development |

Nightly builds are named so that each sorts after everything before it and before the next release: an alpha of the next version, such as `1.5.0-alpha.nightly.20261015.42`, until its first beta, and after a beta or release candidate a build that follows it, such as `1.5.0-rc.1.nightly.20261020.47`. A server on the nightly channel therefore takes each beta, release candidate and release as it comes, and never moves backwards.

A server starts on the channel it was installed with, `updates.channel` in `racksail.toml`, and you can change it in **Settings → Updates**. A server never moves backwards, so leaving the nightly channel takes effect with the next release that's newer than what's installed.

## What a release contains

Every release is on [GitHub Releases](https://github.com/vilanobeachflorida/racksail/releases), with:

- the `racksail` binary for amd64 and arm64, with the web UI built in
- the Debian packages
- the installer ISOs, and the firmware ISOs
- the install script
- software bills of materials in SPDX format, for each binary and for the web UI
- the licenses of everything built into the binary
- `SHA256SUMS`, with the checksum of every file
- `release.json`, which lists every file with its size, checksum and signature, for the panel and for mirrors

Every file is signed keyless with Sigstore by Racksail's release workflow, and the signature is the `.sigstore.json` file next to it. The panel, the install script and anyone else can check that a file came from that workflow at that release's tag:

```sh
cosign verify-blob \
  --bundle racksail-linux-amd64.sigstore.json \
  --certificate-identity https://github.com/vilanobeachflorida/racksail/.github/workflows/release.yml@refs/tags/v1.2.3 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  racksail-linux-amd64
```

Nightly builds are signed by the same workflow running on `main`, so their identity ends in `@refs/heads/main`.

No release is published until it has passed an upgrade test: CI installs the previous release on the same channel, and the newest stable release, on test servers, upgrades them to the new one and checks that they come back with their state.

## Updating

The panel looks for releases on its channel every day, and **Check now** looks at once. When there is a newer one, **Settings → Updates** shows it with its release notes, and the panel publishes `core.update.available`, which reaches admins through their [notifications](../../notifications/).

The panel only offers a release whose `release.json` is signed by the release workflow for that release's tag, checked against Sigstore's trusted root as Racksail pins it, the same one the install script pins. **Preview installing it** shows what the update does: the download, the copy of the panel's database, the snapshots, the new binary and the services that restart. Applying it:

1. tells the modules, through the `core.update.before` hook point, so they can get ready;
2. copies the panel's database aside;
3. has the agent download the binary and check its checksum and signature again itself, against the trusted root built into it, so a panel that was taken over can't make it install anything else;
4. snapshots `/usr` and `/var/lib`;
5. keeps the running binary, puts the new one in its place and restarts Racksail's services onto it.

Mail, DNS, databases and the rest keep running. The edge that serves sites restarts with Racksail's other services, which takes a few seconds.

The new release then has ten minutes to report healthy. Once it does, it confirms the update, modules are told through `core.update.after`, and the panel publishes `core.update.installed`. If it doesn't, the release it replaced comes back by itself, with the panel's database from before the update, and the panel publishes `core.update.rolled_back`. A guard, run by the binary that was replaced since that one is known to work, does the same fifteen minutes after the update in case the new release doesn't start at all.

**Install new releases by themselves** installs them as the daily check finds them, only between the times you choose, in the time zone you choose.

A server where Racksail came from its Debian package updates it with apt, like its other packages; the panel doesn't replace a binary the package owns. Development builds don't update themselves.

## Rolling back

**Roll back** goes back to the release before the last update, with the binary and the panel's database as they were, and restarts Racksail. Changes made in the panel since the update are lost. The snapshots the update took of `/usr` and `/var/lib` are kept, and the update's record in the API names them, for anything else you want back.

## Debian's updates

Racksail keeps the server's own packages up to date too, as **Settings → Updates** says:

- **Debian's security updates** install as they come out, with unattended-upgrades. This is on unless you turn it off.
- **Every other update**, Debian's and those of vendors' repositories a module added, such as PHP's, installs once a day during the window when you turn it on, after snapshots of `/`, `/etc`, `/usr` and `/var/lib`. Held packages, such as a database version waiting for a tested upgrade, stay as they are. **Preview upgrading the packages** runs it now.
- **Reboots**: when an update needs one, such as a new kernel, the panel says why. With **Reboot by itself when an update needs it** on, the server reboots at the time you choose, in UTC.

The panel publishes `core.packages.upgraded` when packages were upgraded, `core.packages.failed` when an upgrade failed, with the snapshots to restore, and `core.reboot.scheduled` when the server will reboot by itself.

## Debian's next release

The server stays on its Debian release until you choose to move: apt's preferences keep packages of newer releases out. When Racksail knows the next release, **Settings → Updates** offers **Preview upgrading to** it, one release at a time:

1. Every module is asked first, through the `core.os_upgrade.check` hook point, and can refuse, for example while a database version the next release doesn't ship is in use. The server also needs 4 GiB free on its system disk.
2. Modules get ready, through `core.os_upgrade.before`.
3. `/`, `/etc`, `/usr` and `/var/lib` are snapshotted.
4. apt is pointed at the next release, its sources and Racksail's release pin alike. Each file is kept as it was beside it, with a `.racksail-<release>` suffix.
5. Every package is upgraded.
6. Modules check their services, through `core.os_upgrade.after`, and the panel publishes `core.os_upgrade.finished`.

The server then needs a reboot to finish. If the upgrade fails, apt is pointed back at the release before, modules are told, the panel publishes `core.os_upgrade.failed`, and the snapshots are there to restore.

## Mirrors

Servers that can't reach GitHub can update from mirrors, listed in **Settings → Updates** and tried before GitHub. A mirror serves:

- each release's files under its tag, such as `https://mirror.example.com/racksail/v1.2.3/racksail-linux-amd64`;
- Sigstore's trusted root under `sigstore/`, named by its checksum, as the install script expects;
- `releases.json`, a copy of `https://api.github.com/repos/vilanobeachflorida/racksail/releases?per_page=100`, so the panel can find new releases without GitHub. Without it, the panel lists releases from GitHub and downloads them from the mirror.

Signatures are still checked against Racksail's release workflow, so a mirror only needs to be reachable, not trusted: the worst a mirror can do is hold releases back.
