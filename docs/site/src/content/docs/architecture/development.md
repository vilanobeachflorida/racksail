---
title: "Developing Racksail"
description: "Build, test and run Racksail from source."
---

Racksail builds and runs on macOS and Linux without root and without a Debian server: `racksail dev` simulates one. This page covers the everyday commands; [docs/architecture/internals.md](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/internals.md) explains how the code fits together and where to add things.

## What you need

- Go 1.27
- Node.js 22.22.2 or later with npm (24.15 or later on Node.js 24)
- git and make
- Python 3, for the Python add-on SDK's tests

The product is pure Go with CGO turned off, so every binary cross-compiles from any machine.

## Run it

```sh
git clone https://github.com/vilanobeachflorida/racksail.git
cd racksail
make dev
```

`make dev` starts `racksail dev`, the panel, agent and edge in one process against a simulated server, on http://127.0.0.1:7070, and the UI's development server with hot reload on http://127.0.0.1:5173. Stopping make stops both. To run several checkouts side by side, give each its own addresses and data directory:

```sh
make dev DEV_ADDR=127.0.0.1:7170 DEV_EDGE_HTTP=127.0.0.1:0 DEV_EDGE_HTTPS=127.0.0.1:0 DEV_DATA=.dev-2
```

## Check your work

```sh
make check
```

It runs gofmt, go vet and staticcheck for macOS and Linux, builds for darwin, linux/amd64 and linux/arm64, runs the Go tests, the UI's type check, lint, tests and build, and the add-on SDKs' tests. A pull request needs it to pass on Linux, on amd64 and arm64, and on macOS. CI also runs:

| Target | What it checks |
|---|---|
| `make test-race` | The Go tests with the race detector, which needs a C compiler |
| `make fuzz` | Every fuzz target, exploring new inputs for `FUZZ_TIME` each, or only `FUZZ_TARGET`, such as `catalog/tool:FuzzCheckSVG`; `make fuzz-list` lists them. CI fuzzes each for five minutes once a day, in a job of its own, and an input that fails is saved in the package's `testdata/fuzz` for `go test` to replay |
| `make gen-check` | That the generated files match what `make gen` writes |
| `make licenses` | That every Go module and npm package is under a license approved by the Open Source Initiative and compatible with AGPL-3.0-or-later |
| `make vulncheck` | Known vulnerabilities, with govulncheck for the Go code and osv-scanner for every lockfile; advisories that don't affect Racksail are listed with their reasons in `packaging/osv-scanner.toml` |
| `make docs` | That this site builds |
| `make catalog-check` | Every item of the official catalog |
| `make e2e-local` | The end-to-end suites against `racksail dev`, comparing every screen with its visual baseline |
| `make e2e-pseudo` | Every screen in the pseudo-locales en-XA and ar-XB: text that skipped the message catalogs, layouts that can't take longer text, and right-to-left layouts that don't mirror |
| `make ci-lint` | The workflows, with actionlint, and the shell scripts, with shellcheck |

Pull requests also need every commit signed off, which CI checks; see [Contributing](../../community/contributing/).

## Generated files

Some files are generated from the code and committed: the OpenAPI documents in `spec/`, the UI's API types and its theme CSS. After changing an API operation, an agent operation, the add-on protocol or the default theme, run `make gen` and commit the result. Tests fail when they drift.

Renderers have golden files in `testdata/` directories. After an intended change to what they render, rewrite them with `go test ./path/to/package -update` and review the difference.

## End-to-end tests

The browser and API suites live in [test/](https://github.com/vilanobeachflorida/racksail/tree/main/test).

```sh
npm --prefix test/e2e ci
npm --prefix test/e2e run browsers   # Chromium for Playwright, once
make e2e-local
```

`make e2e-local` builds the binary with the UI, starts `racksail dev` on 127.0.0.1:7170, runs both suites against it and stops it. To work on browser tests, run `npx playwright test --ui` in `test/e2e`.

Besides each module's flows, the browser suite holds every panel to the rules in the [conventions](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/conventions.md): its screens meet WCAG 2.2 AA as far as axe can tell, on a desktop and on a phone, and the first screen loads within the bundle budget of 250 KiB of code and styles, compressed. A module's tests run axe on its own screens the same way.

In CI, the suite also compares every screen with a screenshot from before, so a change that moves or restyles something shows up for review. The baselines are rendered on CI's Linux runner: when you change screens on purpose, run the **Screenshots** workflow on your branch, which commits the new ones, and check them in the pull request. `make e2e-pseudo` shows every screen in two pseudo-locales, an accented one a third longer than English and a right-to-left one, and fails on text that didn't come from a message catalog, on text that doesn't fit, and on layouts that don't mirror right to left. [test/README.md](https://github.com/vilanobeachflorida/racksail/blob/main/test/README.md) has the details of both.

On Linux with KVM, the QEMU harness installs Racksail from the ISO onto a virtual machine, checks that Racksail's own services idle within the memory budget, 100 MiB with every module off, and runs the same suites against the real thing:

```sh
make cross iso
make e2e E2E_FLAGS="-firmware secureboot"
```

It also sets up Debian's cloud image the ways people set up a server they already have, by converting it or by installing the Debian package, and tests upgrades: it installs an earlier release's package, upgrades it to a new one and checks that the server comes back on the new version with its state.

```sh
make cross deb cloud-image
go run ./test/qemu -cloud-image build/cloud/debian-13-genericcloud-amd64.qcow2 \
  -deb dist/racksail_0.0.0-dev_amd64.deb -version 0.0.0-dev
```

CI runs every installation every night, and no release is published until servers on the previous release of its channel, and on the newest stable release, upgrade to it in this test.

## The ISO and packages

| Target | Builds |
|---|---|
| `make build` | The UI and `bin/racksail` for this machine |
| `make cross` | `bin/racksail-linux-amd64` and `bin/racksail-linux-arm64`, with the UI |
| `make deb` | The Debian packages, into `dist/` |
| `make iso` | The installer ISO with live-build, in a Debian 13 container with Docker or Podman; `ISO_ARCH=arm64` and `ISO_VARIANT=firmware` choose the others |
| `make catalog` | The official catalog's index, unsigned, into `bin/catalog/` |
| `make release VERSION=1.2.3` | Everything a release publishes, into `dist/`, unsigned |

Releases are signed and published by CI, never from a laptop: tag `v1.2.3` on `main` and the release workflow builds, signs and publishes it. Pre-releases are tagged `v1.3.0-beta.1` or `v1.3.0-rc.1`, and nightly builds come from `main` every day, named to sort between the releases around them, such as `1.3.0-alpha.nightly.20261015.42` before the first beta. A stable release becomes GitHub's latest release, which `releases/latest/download/install.sh` serves, only when it is the newest one, not when it patches an older release line.

## Where things are

| Path | Holds |
|---|---|
| `cmd/racksail/` | The binary and its roles |
| `internal/` | The core: store, change pipeline, agent, system adapters, API, hooks and the rest |
| `modules/<id>/` | One package per module |
| `ui/` | The web UI |
| `spec/` | The OpenAPI documents and the JSON Schemas of the files people write |
| `catalog/` | The official catalog and the tool that checks and signs it |
| `sdk/` | The add-on SDKs for Go, TypeScript and Python |
| `iso/` | The installer image's live-build configuration and boot branding |
| `packaging/` | The install script, the Debian package, cloud-init examples and the release tooling |
| `test/` | The end-to-end suites, the QEMU harness and shared fixtures |
| `docs/` | The architecture documents and this site |
