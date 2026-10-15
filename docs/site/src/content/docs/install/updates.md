---
title: "Updates and release channels"
description: "How Racksail updates itself, the stable, beta and nightly channels, and rolling back."
---

Racksail updates itself from the panel. It downloads the new release, checks its signature, snapshots the system, applies the update and checks that everything came back; if anything fails, it rolls back to the snapshot.

## Channels

| Channel | Gets | For |
|---|---|---|
| Stable | Releases such as `1.4.2` | Servers people depend on |
| Beta | Betas and release candidates such as `1.5.0-beta.1` and `1.5.0-rc.1`, and stable releases | Trying the next release early |
| Nightly | A build of every day's changes, such as `1.5.0-nightly.20261015.42`, and everything above | Testing and development |

A server follows the channel it was installed with. You can change it in **Settings**; a server never moves backwards, so leaving the nightly channel takes effect with the next release that's newer than what's installed.

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

## Updating

When a release is available on the server's channel, the panel says so on the home screen and in **Settings**, with the release notes. Applying it is a change like any other: the preview shows the version, the download and the snapshot it takes. Sites, mail and DNS keep running while the panel restarts.

On several servers, the panel updates them one at a time, and stops if one doesn't come back.

## Rolling back

If an update fails its checks, Racksail rolls back to the snapshot on its own. To go back after an update that worked but that you don't want, restore the snapshot from **Backups**: it's listed with the update that took it.

## Software Racksail installs

- **Debian's security updates** install automatically.
- **Other Debian updates** install on the schedule you set, with a snapshot first, and the panel schedules a reboot when a kernel update needs one.
- **Software from vendors**, such as PHP versions or databases, gets patch updates automatically; new major versions wait until you choose them.
- **Debian's next release** is a change you start from the panel when you're ready. Racksail snapshots the server, upgrades it and checks it, and rolls back if the upgrade fails. Until then the server stays on its release: apt's preferences keep packages from newer releases out.

## Mirrors

Servers that can't reach GitHub can update from a mirror that serves each release's files under its tag, such as `https://mirror.example.com/racksail/v1.2.3/`. Signatures are still checked against Racksail's release workflow, so the mirror only needs to be reachable, not trusted.
