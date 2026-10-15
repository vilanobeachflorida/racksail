---
title: "Convert an existing server"
description: "Turn a fresh Debian 13 server into a Racksail server with the same install engine."
---

If a server already runs Debian 13, such as a cloud server or a VPS, Racksail can set it up as it is, without reinstalling. The conversion uses the same install engine as the ISO, so the server ends up the same.

Start from a fresh Debian 13 install. Racksail manages the server's web server, mail, DNS and firewall itself, and doesn't take over software someone already set up by hand.

## With the install script

```sh
curl -fsSLO https://github.com/vilanobeachflorida/racksail/releases/latest/download/install.sh
less install.sh
sudo sh install.sh
```

The script:

1. checks that it runs as root on Debian 13, on amd64 or arm64
2. finds the newest release on the stable channel, or the one you ask for
3. downloads the `racksail` binary for the machine, the release's `SHA256SUMS` and their Sigstore signatures
4. verifies both signatures against Racksail's release workflow, and the binary against its checksum; it uses `cosign` from the server when there is one, and otherwise downloads the cosign release it names, checked against the checksums written in the script
5. checks that the binary reports the version it was meant to be, so a mirror can't pass off an older release
6. shows what it will do and asks you to confirm
7. installs `/usr/local/bin/racksail` and runs `racksail install --convert`

Nothing it downloads runs before it has been verified, and if any check fails, nothing is installed.

### Options

| Option | What it does |
|---|---|
| `--version 1.2.3` | Installs that release instead of the newest |
| `--channel beta` | Follows the beta channel, or `nightly`; `stable` is the default |
| `--hostname panel.example.com` | Sets the server's hostname |
| `--mirror https://mirror.example.com/racksail` | Downloads everything from a mirror, [below](#through-a-mirror); it needs `--version` |
| `--dry-run` | Shows what converting the server would do, and stops |
| `--yes` | Doesn't ask before converting, for automation |
| `--reinstall` | Replaces a `racksail` binary that is already installed, and restarts Racksail's services so they run it |

Run it with `--help` for the full list.

If a check fails, the script says which and why. When cosign can't verify a signature, its own output comes first, so you can tell a network problem or a server clock that is off from a release that isn't Racksail's. If `/tmp` is mounted `noexec`, the script can't run what it downloaded; point `TMPDIR` at a directory that allows it.

### Through a mirror

On a network that reaches Racksail's releases only through a mirror of your own, `--mirror` downloads everything from it, so installing needs nothing from GitHub or Sigstore. The mirror serves:

| Path | Holds |
|---|---|
| `v1.2.3/` | Each release's files under its tag, as they are on GitHub: `v1.2.3/racksail-linux-amd64`, `v1.2.3/SHA256SUMS` and the rest |
| `cosign/<version>/` | The cosign release the install script names in `COSIGN_VERSION`: `cosign-linux-amd64` and `cosign-linux-arm64`, from [cosign's releases](https://github.com/sigstore/cosign/releases) |
| `sigstore/` | Sigstore's trusted root, the keys the signatures are checked against, that the script names in `SIGSTORE_ROOT_SHA256`, as `<checksum>.trusted_root.json` |

To fill it from a machine that can reach GitHub and Sigstore:

```sh
version=1.2.3
gh release download "v$version" --repo vilanobeachflorida/racksail --dir "mirror/v$version" \
  --pattern 'install.sh*' --pattern 'racksail-linux-*' --pattern 'SHA256SUMS*'
cosign=$(sed -n 's/^COSIGN_VERSION=//p' "mirror/v$version/install.sh")
root=$(sed -n 's/^SIGSTORE_ROOT_SHA256=//p' "mirror/v$version/install.sh")
for arch in amd64 arm64; do
  curl -fsSL --create-dirs -o "mirror/cosign/$cosign/cosign-linux-$arch" \
    "https://github.com/sigstore/cosign/releases/download/$cosign/cosign-linux-$arch"
done
curl -fsSL --create-dirs -o "mirror/sigstore/$root.trusted_root.json" \
  "https://tuf-repo-cdn.sigstore.dev/targets/$root.trusted_root.json"
```

Then, on the server, use the install script from the mirror:

```sh
curl -fsSLO https://mirror.example.com/racksail/v1.2.3/install.sh
sudo sh install.sh --mirror https://mirror.example.com/racksail --version 1.2.3
```

The script checks cosign and the trusted root against the checksums written in it, and the release against its signatures and checksum, so the mirror can't change any of them unnoticed. The script itself is what you trust: copy it to your mirror from a machine where you checked its signature, `install.sh.sigstore.json`, with cosign. Keep older cosign releases and trusted roots on the mirror for as long as you install the releases that name them.

## What the conversion does

`racksail install --convert` sets up:

- the `racksail` and `racksail-edge` system users, which the panel and the edge run as
- Racksail's directories: `/etc/racksail`, `/var/lib/racksail`, `/srv/racksail`, `/opt/racksail`, `/var/log/racksail` and `/mnt/racksail`
- the hostname, when you gave one
- the systemd units: `racksail-agent.service`, `racksail-panel.service` and `racksail-edge.service` behind `racksail-edge.socket`, in `racksail.slice`, and `racksail-accounts.slice` for accounts
- and starts the agent, the panel and the edge's socket

To see the plan first:

```sh
sudo racksail install --convert --dry-run
```

Then open `https://<the server's address>:8443` for the [setup wizard](../../getting-started/setup-wizard/). Modules install the software they need the first time you use them, each change previewed first.

## With the Debian package

Every release also has a Debian package for each architecture, `racksail_<version>_amd64.deb` and `racksail_<version>_arm64.deb`. It sets the server up the same way: its units, users and directories come from the same plan the conversion carries out.

```sh
sudo apt install ./racksail_1.2.3_amd64.deb
```

The package installs the binary as `/usr/bin/racksail`, with shell completions, and starts the agent, the panel and the edge's socket. An upgrade restarts them, and leaves alone a service you disabled. Removing the package stops them, and installing it again starts them as they were; purging it keeps `/var/lib/racksail` and `/srv/racksail`, which hold the panel's state and your data. A server uses the package or the install script, not both: the install script refuses to run where the package is installed.

### From your own apt repository

If you keep the package in an apt repository of your own, the package carries templates for its source and its pinning in `/usr/share/racksail/apt/`. `racksail.sources.in` and `racksail.pref.in` add the repository and let it provide the `racksail` package and nothing else; `racksail-hold.pref.in` keeps the package on one release line. Fill in the `@...@` values and save them under `/etc/apt/`.

## Afterwards

Racksail keeps itself up to date from the panel; see [Updates and release channels](../updates/). The server stays on Debian 13 until you upgrade it from the panel, which snapshots the system first.
