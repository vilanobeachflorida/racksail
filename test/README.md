# End-to-end tests

This directory is for tests that need a whole Racksail: running against the simulated server, or installed on a Debian 13 machine. Unit tests, golden files and simulated flows live next to the code they test and run with `make check`.

## Layout

| Path | For |
|---|---|
| [e2e/api/](e2e/api/) | The API suite: Go tests that run against a panel at `RACKSAIL_E2E_URL` |
| [e2e/specs/](e2e/specs/) | The browser suite: Playwright tests of the panel, with [playwright.config.ts](e2e/playwright.config.ts) |
| [e2e/pseudo/](e2e/pseudo/) | The pseudo-locale suite, with [pseudo.config.ts](e2e/pseudo.config.ts): the UI in en-XA and ar-XB |
| [qemu/](qemu/) | The harness that installs Racksail on a virtual machine and runs both suites against it |
| [fixtures/](fixtures/) | Shared test data: answers files, and later synthetic servers to migrate from (CWP, cPanel, DirectAdmin, HestiaCP, Plesk, Unraid), zone files, backup archives and recipes |

## Running against racksail dev

```sh
make e2e-local
```

It builds `bin/racksail` with the UI, starts `racksail dev` on 127.0.0.1:7170 with its own data directory, `.dev-e2e`, so it runs next to a development instance, runs the API suite and the browser suite against it, and stops it. `E2E_ADDR` and `E2E_DATA` choose another address and directory. The browser suite needs Chromium once: `npm --prefix test/e2e run browsers`.

To work on the browser tests, run Playwright in [e2e/](e2e/) directly. It starts `racksail dev` itself, or uses the panel at `RACKSAIL_E2E_URL`, which it treats as an installed server unless `RACKSAIL_E2E_DEV=1` says it is a `racksail dev` you started, where everyone is signed in:

```sh
cd test/e2e
npm ci
npx playwright test --ui
```

The suites check what holds for every panel: the API's health, version, document and errors, the UI's security headers, that it loads nothing from other origins and starts without errors, that every screen meets WCAG 2.2 AA as far as [axe](https://github.com/dequelabs/axe-core) can tell, on a desktop and on a phone: the first screen, each one the navigation reaches, modules' screens included, and the page for addresses that don't exist, that the first screen loads within the bundle budget of 250 KiB of code and styles compressed, and, where a signed-in session is available, the shell's landmarks, keyboard access and pages. Each module adds its own flows next to them, and runs axe the same way on what the navigation doesn't reach, such as a detail page or a dialog.

## Visual regression

With `RACKSAIL_E2E_VISUAL=1`, as CI runs `make e2e-local`, the browser suite also takes a screenshot of every screen the navigation reaches, on a desktop and on a phone, and compares it with the baseline in `e2e/specs/__screenshots__/`. A screen that looks different fails the run, and Playwright's report shows the baseline, the new screenshot and their difference. [visual.spec.ts](e2e/specs/visual.spec.ts) pins what changes from build to build, such as the Go version the panel reports and the browser's clock, and leaves out `<time>` elements; anything else that differs from run to run belongs there too.

The baselines are rendered on CI's Linux runner, the only place the comparison runs. When a change alters screens on purpose, run the **Screenshots** workflow on its branch: it renders the screens there with `make e2e-screenshots` and commits the new baselines, whose differences the pull request then shows. Until the first baselines are committed, the suite compares nothing and CI says so.

## Pseudo-locales and right to left

```sh
make e2e-pseudo
```

It builds the UI with two pseudo-locales next to English and checks every screen in each, on a desktop and on a phone, against a `racksail dev` of its own on 127.0.0.1:7270:

- **en-XA** accents every letter of every message, puts it in brackets and makes it a third longer. Any unaccented word left on a screen, apart from what the API sent, didn't come from a message catalog. Text a third longer must neither make the page scroll sideways nor get cut off without an ellipsis.
- **ar-XB** makes every word read right to left, and since the UI takes it for Arabic, the whole panel lays itself out right to left. The suite checks that each landmark sits where it would left to right, mirrored, and that nothing overflows.

[e2e/pseudo/pseudo.mjs](e2e/pseudo/pseudo.mjs) rewrites the messages, leaving their arguments, plurals and tags alone, and [e2e/pseudo/serve.mjs](e2e/pseudo/serve.mjs) builds the UI with them from a copy of `ui/` in `build/pseudo/`, so the source tree stays as it is. `make check` runs the rewriter's own tests over every catalog the UI has. Playwright's report, in `build/e2e-pseudo/`, has a screenshot of every screen in both pseudo-locales; CI keeps it for two weeks. What the API sends is shown as it is and doesn't count. Other text that isn't words to translate, such as a key's fingerprint the UI works out, goes in an element with `translate="no"`, which the suite and browsers' translators both leave alone.

## Installing on a virtual machine

```sh
make iso                        # builds dist/racksail-<version>-amd64.iso in a Debian container
make e2e ISO=dist/racksail-0.0.0-dev-amd64.iso
```

The harness, `go run ./test/qemu`, does what a person would:

1. creates empty disks, an SSH key, and a USB stick labelled `RACKSAIL` with `answers.yaml` at its root, made from an answers file in [fixtures/answers/](fixtures/answers/) with the harness's key and the machine's disks put in,
2. boots the ISO, from which the installer finds the answers file and installs unattended; the install ends when the installer reboots, which ends QEMU's first run,
3. boots the installed system without the ISO, waits for the panel's health endpoint and for SSH,
4. lets the server settle for a minute and measures what Racksail's own services hold: the memory of each service in `racksail.slice`, as its cgroup counts it, without the inactive file cache the kernel drops first. The median of five samples must stay under the idle memory budget, 100 MiB with every module off (decision R5 in [decisions.md](../docs/architecture/decisions.md)),
5. runs the API and browser suites against the panel through a forwarded port, with `RACKSAIL_E2E_INSECURE=1` for the panel's own certificate,
6. saves everything to `build/e2e/logs/`: both serial consoles, QEMU's output, the answers it used, the memory samples, the suites' output and Playwright's report, and from the server its journal, Racksail's units, disks and network. When the panel never answers, the harness still signs in over SSH, giving it two more minutes if the boot's time is spent, and collects the server's diagnostics before it stops.

Options choose the machine: `-arch amd64|arm64`, `-firmware bios|uefi|secureboot`, `-disks N`, `-disk-bus virtio|sata|nvme`, `-answers FILE`, `-memory`, `-cpus`. With `-firmware secureboot` it boots OVMF with Microsoft's keys enrolled, which enforces Secure Boot, so the installer image only boots when its shim, GRUB and kernel are signed, and then checks that the installed system booted with Secure Boot on too. `-memory-budget MiB` and `-settle` change the idle memory check, and `-memory-budget 0` skips it. `-keep` leaves the machine running after the suites, and prints how to reach it.

The harness gives disks to the installer as `/dev/disk/by-id/` paths, so a USB stick or the CD-ROM can never take a disk's name. It needs QEMU, `qemu-img`, the UEFI firmware (`ovmf`, `qemu-efi-aarch64`), `dosfstools`, `mtools` and OpenSSH, and runs fastest on Linux with KVM; on macOS it uses Hypervisor.framework for guests of the Mac's own architecture. CI runs the installs from the ISO, the conversion and the package install every night, on the matrix in [.github/workflows/e2e.yml](../.github/workflows/e2e.yml), and the upgrade tests before every release, nightly builds included (features.md, section 37).

## Servers people already have

With `-cloud-image` the harness boots one of Debian's cloud images instead of the ISO, with a cloud-init seed that creates the admin it signs in as, and sets the server up one of two ways:

- `-binary bin/racksail-linux-amd64`: the seed downloads the binary from the harness and converts the server with `racksail install --convert`, as the install script does.
- `-deb dist/racksail_<version>_amd64.deb`: the harness waits for cloud-init, downloads the package onto the server and installs it with `apt-get install`, as people install the Debian package. Before the suites, it removes the package with `apt-get remove`, checks that Racksail's services stopped, installs the package again and checks that they come back enabled and running, with the panel serving the certificate it had, so its state survived. apt's output is in the logs as `apt-remove.txt` and `apt-reinstall.txt`.

`make cloud-image` downloads Debian's image into `build/cloud/` and checks it against Debian's published checksums.

```sh
make cross deb cloud-image
go run ./test/qemu -cloud-image build/cloud/debian-13-genericcloud-amd64.qcow2 -deb dist/racksail_0.0.0-dev_amd64.deb -version 0.0.0-dev
```

## Upgrades

`-upgrade-from` names an earlier release's package. The harness installs it first and checks it: the panel must report a version older than `-version`, and the agent, the panel and the edge's socket must be enabled and running. Then it installs `-deb` over it with apt, as an upgrade, and checks that

- the panel comes back reporting `-version`,
- it serves the same certificate as before, which it keeps in its state directory, so the panel's state carried over and browsers that trusted the certificate don't warn again,
- its services are still enabled and running,

before it runs the suites against the upgraded server. apt's output from both installs is in the logs, as `apt-install.txt` and `apt-upgrade.txt`.

```sh
gh release download v1.2.4 --pattern 'racksail_1.2.4_amd64.deb*' --pattern 'SHA256SUMS*' --dir build/earlier
go run ./test/qemu -cloud-image build/cloud/debian-13-genericcloud-amd64.qcow2 \
  -upgrade-from build/earlier/racksail_1.2.4_amd64.deb -deb dist/racksail_1.3.0_amd64.deb -version 1.3.0
```

The release workflow runs this before it publishes anything, from the newest earlier release on the new release's channel and from the newest stable release, each downloaded from GitHub Releases and checked against its signature and checksum first; `go run ./packaging/release previous` picks them.

## Rules

- Fixtures are synthetic. Nothing here comes from a real server, customer or mailbox.
- Every migration source has fixtures, and every parser that reads outside input has a fuzz test next to its code.
- Tests clean up after themselves and don't depend on the order they run in.
- Answers files in [fixtures/answers/](fixtures/answers/) are ones the installer accepts, and those in `invalid/` each break one rule, with part of the error it must cause on a `# want:` line; `go test ./test/fixtures` checks both.
