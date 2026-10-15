# Installer image

This directory is for the configuration Debian's live-build turns into Racksail's installer ISO, and the branding a machine shows while it boots and installs.

The ISO is a small network installer for amd64 and arm64 that boots with BIOS or UEFI. It boots Debian's own signed shim, GRUB and kernel, so Secure Boot works, and installs Debian 13 from the official mirrors. A second image adds Debian's non-free firmware for hardware that needs it, and says so in its name. Both are built in GitHub Actions and published on GitHub Releases, signed like every release (decisions R1 and R2 in [docs/architecture/decisions.md](../docs/architecture/decisions.md)).

## Layout

| Path | For |
|---|---|
| `config/` | The live-build configuration tree: package lists, hooks and files included in the live system |
| `branding/grub/` | The boot menu theme |
| `branding/plymouth/` | The boot splash |
| `branding/console/` | The palette and messages of the installer, the console status screen and the SSH login message |

The image carries no installer logic of its own. It runs `racksail install` from the binary it ships, so the terminal installer, the browser mode with its one-time code, unattended installs from an answers file ([spec/schemas/answers.schema.json](../spec/schemas/answers.schema.json)), rescue mode and restoring from a backup all belong to `internal/installer` and are tested there.

## Requirements

- Under 2 GiB, with nothing fetched at boot that Debian's mirrors don't provide.
- Branding comes from the default theme's console palette and the files in [brand/](../brand/), so the installer looks like the rest of Racksail.
- Every image is tested by the end-to-end harness in [test/](../test/): it boots in QEMU, installs unattended, reboots and runs the API and UI suites.
