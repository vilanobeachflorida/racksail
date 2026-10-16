# Packaging

This directory is for what turns a `racksail` binary into something people install: the install script for existing servers, the Debian package, the cloud images and the release tooling around them.

| Path | For |
|---|---|
| [install.sh](install.sh) | Converts a fresh Debian 13 server into a Racksail server |
| [nfpm.yaml](nfpm.yaml) and [deb/](deb/) | The Debian package |
| [apt/](apt/) | apt sources and pinning templates |
| [cloud-init/](cloud-init/) | User data that converts a cloud server on its first boot |
| [release/](release/) | Release channels, nightly versions, checksums and the release manifest |
| [licenses/](licenses/) | The license allowlist check and the notices releases ship with |
| [spdx/](spdx/) | SPDX license expressions, shared with the catalog tool |

## systemd units

Racksail runs as three services in `racksail.slice`, `racksail-panel.service`, `racksail-agent.service` and `racksail-edge.service`, with account workloads in `racksail-accounts.slice`. The edge sits behind `racksail-edge.socket`: systemd holds its admin socket for the panel and starts the edge the first time the panel sends it routes. Their unit files aren't kept here by hand: `internal/installer` renders them (`installer.Units`, checked against `internal/installer/testdata/units.golden`), and `racksail install` writes them, so a fresh install, a converted server and an upgrade always get the same units.

## Install script

`install.sh` checks that the machine runs Debian 13 on amd64 or arm64, downloads the matching release from GitHub Releases or a mirror, verifies its Sigstore signatures and its SHA-256 checksum, and hands over to `racksail install --convert`. It never pipes a download into a shell, runs nothing it downloaded before verifying it, and installs nothing Racksail itself wouldn't.

- Signatures are checked with cosign against the identity of the release workflow: `.github/workflows/release.yml` at the release's tag, or at `main` for nightly builds. A cosign already on the server is used; otherwise the script downloads the cosign release it pins and checks it against the checksums written in the script. `go run ./packaging/release check-pins` compares those with cosign's published checksums; the [pins workflow](../.github/workflows/pins.yml) writes them into Renovate's pull requests that bump the version, and checks every other pull request that changes the script.
- The newest release on the chosen channel comes from GitHub's release list; `--version` picks one, and `--mirror` downloads from a mirror instead: each release's files under its tag, the cosign release under `cosign/<version>/`, and Sigstore's trusted root, the keys signatures are checked against, as `sigstore/<checksum>.trusted_root.json`. The script pins the trusted root by its checksum and gives it to cosign, so installing through a mirror needs nothing from GitHub or Sigstore. Without a mirror, cosign fetches Sigstore's current trusted root itself.
- `check-pins` only believes the trusted root that Sigstore's TUF repository lists when the repository's signatures lead to it from [release/sigstore-root.json](release/sigstore-root.json), a version of the repository's own root: every newer root must be signed by enough keys of the one before it and of its own, and the newest root's keys must have signed the timestamp, snapshot and targets that list the trusted root. Its tests follow Sigstore's real repository from its first root. Every day the pins workflow proposes Sigstore's current trusted root when it changes, with the newest TUF root it verified, and the release workflow checks every release's signatures against the pinned one before publishing.
- When cosign can't verify a signature, the script shows cosign's own output, so a network problem or a clock that is off isn't mistaken for a forged release. Programs it downloaded that won't run, as from a temporary directory mounted noexec, are reported as such.
- The binary must also report the version being installed, so a mirror can't serve an older signed release under a newer name.
- `RACKSAIL_ROOT` puts everything under another directory. The tests in [install_test.go](install_test.go) run the script that way, against stand-ins for curl, cosign and the binary, with every shell CI has.

## Debian package

`make deb` builds `racksail_<version>_<arch>.deb` for amd64 and arm64 with nfpm. [deb/](deb/) renders what goes in it besides the binary from the plan `racksail install --convert` carries out: the units, a sysusers.d file for the `racksail` and `racksail-edge` users, a tmpfiles.d file for Racksail's directories, and maintainer scripts that enable and start the same units a conversion does. A server installed from the package is set up the same way as a converted one; a test fails if the convert plan gains a step the package can't express.

The package installs the binary as `/usr/bin/racksail`, with shell completions, the copyright file, the licenses of everything built into the binary, and the templates in [apt/](apt/) under `/usr/share/racksail/apt/`. Its maintainer scripts do what debhelper's do for units: an upgrade restarts the services that run, a unit an administrator disabled stays disabled, removing the package stops the services without masking them, so installing it again brings them back, and purging it keeps `/var/lib/racksail` and `/srv/racksail`. [deb/scripts_test.go](deb/scripts_test.go) runs the scripts through each of dpkg's sequences, and CI and the end-to-end harness install, remove and install the package again on real systems.

## apt templates

[apt/](apt/) holds templates with `@...@` values to fill in:

- `racksail.sources.in` and `racksail.pref.in` add Racksail's package from your own apt repository or mirror, pinned so that repository can provide nothing else, the way Racksail pins every vendor repository it adds.
- `racksail-hold.pref.in` keeps the package on one release line.
- `debian-release.pref.in` keeps a server on its Debian release: its own suites, updates and security fixes, backports only by name, and no packages from another release until the server is upgraded.

## Cloud images

Cloud images start from Debian's official images and run the same conversion through cloud-init on first boot, so they need no code of their own. [cloud-init/](cloud-init/) has user data for the newest release, for a pinned release and for a mirror, and the meta-data of a NoCloud seed for Proxmox, libvirt and QEMU. The end-to-end harness in [test/qemu](../test/qemu/) boots Debian's cloud image with a seed like these.

## Releases

Release binaries come from `make cross` with `VERSION` and `CHANNEL` set. `make release VERSION=1.2.3` collects everything a release publishes in `dist/`: the binaries, the Debian packages, an SPDX software bill of materials for each binary and for the web UI, the third-party license notices, `install.sh`, `SHA256SUMS` and, once CI has signed every file with cosign, `release.json`, the manifest servers read to update themselves. The [release workflow](../.github/workflows/release.yml) signs each file keyless with Sigstore, attaches the installer ISOs and publishes a GitHub Release (decision R1 in [docs/architecture/decisions.md](../docs/architecture/decisions.md)).

[release/](release/) decides what the workflow publishes:

- `channel` names a version's channel: `1.2.3` is stable, `1.3.0-beta.1` and `1.3.0-rc.1` are beta, and nightly builds, such as `1.3.0-alpha.nightly.20261015.42`, are nightly.
- `nightly-version` names a nightly build so it sorts after everything released or built before it and before the next release: after 1.2.x it is an alpha of the next minor version, `1.3.0-alpha.nightly.20261015.42`, which sorts before `1.3.0-beta.1`; after a beta or release candidate it follows it, as `1.3.0-rc.1.nightly.20261015.42`. A release candidate of a patch release, such as `1.2.1-rc.1`, leaves nightly builds on the version they were on, so servers on the nightly channel never move backwards.
- `previous` reads release tags and names the newest release before a version on a channel, which is where the upgrade test starts: the workflow publishes nothing until servers on that release, and on the newest stable release, upgrade to the new one in the [QEMU harness](../test/README.md#upgrades).
- `latest` tells whether a stable release is newer than every one published, and so becomes GitHub's latest release, which `releases/latest/download/install.sh` serves; a patch to an older release line doesn't.
- `checksums` and `manifest` write `SHA256SUMS` and `release.json`, the second in the format `internal/updates` reads.

## Licenses

`make licenses` checks every Go module the code uses on every platform it builds for, tests included, and every package in every npm lockfile: each license must be approved by the Open Source Initiative and compatible with AGPL-3.0-or-later. A dependency whose license files can't be read, or the rare justified exception, goes in [licenses/exceptions.yaml](licenses/exceptions.yaml) with its reason. `go run ./packaging/licenses notices` writes the license texts of everything the release binary contains.

## Vulnerabilities

`make vulncheck` runs govulncheck on the Go code, which reports only vulnerabilities the code can reach, and osv-scanner on every `go.mod` and npm lockfile. An advisory that doesn't affect Racksail, such as one about parsing untrusted input in a tool that only reads the repository's own files at build time, goes in [osv-scanner.toml](osv-scanner.toml) with the reason and a date when it lapses, so it gets looked at again. Renovate reads the same advisories, from GitHub and from the OSV database, and proposes fixed versions as soon as they are published, without the usual wait.
