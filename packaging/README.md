# Packaging

This directory is for what turns a `racksail` binary into something people install: the install script for existing servers, the cloud images and the release tooling around them.

## systemd units

Racksail runs as three services in `racksail.slice`: `racksail-agent.service`, `racksail-panel.service` and `racksail-edge.service`, with account workloads in `racksail-accounts.slice` and one `racksail-accounts-<account>.slice` per account inside it. The edge sits behind `racksail-edge.socket`: systemd holds its admin socket for the panel and starts the edge the first time the panel sends it routes.

[`systemd/`](systemd/) has the unit files as they are installed with the binary at `/usr/bin/racksail`, for distribution packages. They aren't edited by hand: the system layer renders them (`system.PlatformUnits`), `racksail install` writes the same ones for wherever the binary is, and a test fails when the copies here drift (`go test ./internal/system -update` rewrites them). So a fresh install, a converted server, an upgrade and a package all get the same units.

- The panel runs as `racksail` with the full sandbox: a read-only system, no capabilities, its state in `/var/lib/racksail`, and the key its secrets are encrypted with imported from the systemd credential `racksail-secrets-key`.
- The agent runs as root, since it is what changes the machine, with its own state in `/var/lib/racksail-agent`. It is of type `notify`, so the panel starts once the agent answers.
- The edge runs as `racksail-edge` with `CAP_NET_BIND_SERVICE` only, for ports 80 and 443, and keeps its certificates in `/var/lib/racksail-edge`.
- The platform slice outweighs the accounts slice for CPU and I/O, so the panel and the edge stay responsive when a site is busy.

## Install script

The install script for existing servers, `install.sh`, goes here. It checks that the machine runs Debian 13 on amd64 or arm64, downloads the matching release from GitHub Releases, verifies its signature and checksum, and hands over to `racksail install` with a convert-mode answers file ([spec/schemas/answers.schema.json](../spec/schemas/answers.schema.json)). It never pipes a download into a shell and installs nothing Racksail itself wouldn't.

## Cloud images

Cloud images start from Debian's official images and run the same conversion through cloud-init on first boot, so their configuration goes here and they need no code of their own.

## Releases

Release binaries come from `make cross` with `VERSION` and `CHANNEL` set. Every artifact is signed with Sigstore and published on GitHub Releases with checksums and software bills of materials (decision R1 in [docs/architecture/decisions.md](../docs/architecture/decisions.md)).
