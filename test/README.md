# End-to-end tests

This directory is for tests that need a whole Racksail: running against the simulated server, or installed on a Debian 13 machine. Unit tests, golden files and simulated flows live next to the code they test and run with `make check`.

## Layout

| Path | For |
|---|---|
| [e2e/api/](e2e/api/) | The API suite: Go tests that run against a panel at `RACKSAIL_E2E_URL` |
| [e2e/specs/](e2e/specs/) | The browser suite: Playwright tests of the panel, with [playwright.config.ts](e2e/playwright.config.ts) |
| [qemu/](qemu/) | The harness that installs Racksail on a virtual machine and runs both suites against it |
| [fixtures/](fixtures/) | Shared test data: answers files, and later synthetic servers to migrate from (CWP, cPanel, DirectAdmin, HestiaCP, Plesk, Unraid), zone files, backup archives and recipes |

## Running against racksail dev

```sh
make e2e-local
```

It builds `bin/racksail` with the UI, starts `racksail dev` on 127.0.0.1:7170 with its own data directory, `.dev-e2e`, so it runs next to a development instance, runs the API suite and the browser suite against it, and stops it. `E2E_ADDR` and `E2E_DATA` choose another address and directory. The browser suite needs Chromium once: `npm --prefix test/e2e run browsers`.

To work on the browser tests, run Playwright in [e2e/](e2e/) directly. It starts `racksail dev` itself, or uses the panel at `RACKSAIL_E2E_URL`:

```sh
cd test/e2e
npm ci
npx playwright test --ui
```

The suites check what holds for every panel: the API's health, version, document and errors, the UI's security headers, that it loads nothing from other origins and starts without errors, and, where a signed-in session is available, the shell's landmarks, keyboard access and pages. Each module adds its own flows next to them.

## Installing on a virtual machine

```sh
make iso                        # builds dist/racksail-<version>-amd64.iso in a Debian container
make e2e ISO=dist/racksail-0.0.0-dev-amd64.iso
```

The harness, `go run ./test/qemu`, does what a person would:

1. creates empty disks, an SSH key, and a USB stick labelled `RACKSAIL` with `answers.yaml` at its root, made from an answers file in [fixtures/answers/](fixtures/answers/) with the harness's key and the machine's disks put in,
2. boots the ISO, from which the installer finds the answers file and installs unattended; the install ends when the installer reboots, which ends QEMU's first run,
3. boots the installed system without the ISO, waits for the panel's health endpoint and for SSH,
4. runs the API and browser suites against the panel through a forwarded port, with `RACKSAIL_E2E_INSECURE=1` for the panel's own certificate,
5. saves everything to `build/e2e/logs/`: both serial consoles, QEMU's output, the answers it used, the suites' output and Playwright's report, and from the server its journal, Racksail's units, disks and network.

Options choose the machine: `-arch amd64|arm64`, `-firmware bios|uefi|secureboot`, `-disks N`, `-disk-bus virtio|sata|nvme`, `-answers FILE`, `-memory`, `-cpus`. With `-firmware secureboot` it boots OVMF with Microsoft's keys enrolled and checks that both the installer image and the installed system booted with Secure Boot on. With `-cloud-image` and `-binary` it boots one of Debian's cloud images instead, with a cloud-init seed that converts the server with `racksail install --convert`. `-keep` leaves the machine running after the suites, and prints how to reach it.

The harness gives disks to the installer as `/dev/disk/by-id/` paths, so a USB stick or the CD-ROM can never take a disk's name. It needs QEMU, `qemu-img`, the UEFI firmware (`ovmf`, `qemu-efi-aarch64`), `dosfstools`, `mtools` and OpenSSH, and runs fastest on Linux with KVM; on macOS it uses Hypervisor.framework for guests of the Mac's own architecture. CI runs it every night on the matrix in [.github/workflows/e2e.yml](../.github/workflows/e2e.yml) and before every release (features.md, section 37).

## Rules

- Fixtures are synthetic. Nothing here comes from a real server, customer or mailbox.
- Every migration source has fixtures, and every parser that reads outside input has a fuzz test next to its code.
- Tests clean up after themselves and don't depend on the order they run in.
- Answers files in [fixtures/answers/](fixtures/answers/) are ones the installer accepts, and those in `invalid/` each break one rule; `go test ./test/fixtures` checks both.
