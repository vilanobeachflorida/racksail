# Packaging

This directory is for what turns a `racksail` binary into something people install: the install script for existing servers, the cloud images and the release tooling around them.

## systemd units

Racksail runs as three services in `racksail.slice`, `racksail-panel.service`, `racksail-agent.service` and `racksail-edge.service`, with account workloads in `racksail-accounts.slice`. The edge sits behind `racksail-edge.socket`: systemd holds its admin socket for the panel and starts the edge the first time the panel sends it routes. Their unit files aren't kept here by hand: `internal/installer` renders them (`installer.Units`, checked against `internal/installer/testdata/units.golden`), and `racksail install` writes them, so a fresh install, a converted server and an upgrade always get the same units.

## Install script

The install script for existing servers, `install.sh`, goes here. It checks that the machine runs Debian 13 on amd64 or arm64, downloads the matching release from GitHub Releases, verifies its signature and checksum, and hands over to `racksail install` with a convert-mode answers file ([spec/schemas/answers.schema.json](../spec/schemas/answers.schema.json)). It never pipes a download into a shell and installs nothing Racksail itself wouldn't.

## Cloud images

Cloud images start from Debian's official images and run the same conversion through cloud-init on first boot, so their configuration goes here and they need no code of their own.

## Releases

Release binaries come from `make cross` with `VERSION` and `CHANNEL` set. Every artifact is signed with Sigstore and published on GitHub Releases with checksums and software bills of materials (decision R1 in [docs/architecture/decisions.md](../docs/architecture/decisions.md)).
