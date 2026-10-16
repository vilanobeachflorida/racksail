# Installer image

This directory is for the configuration Debian's live-build turns into Racksail's installer ISO, and the branding a machine shows while it boots and installs.

The ISO is a small network installer for amd64 and arm64 that boots with BIOS or UEFI. It boots Debian's own signed shim, GRUB and kernel, so Secure Boot works, and installs Debian 13 from the official mirrors. A second image adds Debian's non-free firmware for hardware that needs it, and says so in its name and on its boot menu. Both are built in GitHub Actions and published on GitHub Releases, signed like every release (decisions R1 and R2 in [docs/architecture/decisions.md](../docs/architecture/decisions.md)).

## Layout

| Path | For |
|---|---|
| `build.sh` | Builds one image as root on Debian 13: copies the configuration, renders the branding, adds the binary and runs live-build |
| `in-container.sh` | Runs `build.sh` in a Debian 13 container, for `make iso` on any machine |
| `auto/config` | The `lb config` options: architecture, archive areas, bootloaders, Secure Boot, mirrors |
| `config/` | The live-build configuration tree: package lists, hooks, the boot menu and files included in the live system |
| `branding/grub/` | The boot menu theme: its layout and background |
| `branding/plymouth/` | The boot splash |
| `branding/console/` | The console's colors and the message other consoles show |

```sh
make iso                                      # amd64, from bin/racksail-linux-amd64, in a container
make iso ISO_ARCH=arm64 ISO_VARIANT=firmware VERSION=1.2.3
sudo iso/build.sh --arch amd64 --binary bin/racksail-linux-amd64 --version 1.2.3   # on Debian 13
```

The image goes to `dist/racksail-<version>-<arch>.iso`, or `racksail-<version>-<arch>-firmware.iso`. Every file in it carries the commit's time (`SOURCE_DATE_EPOCH`), so building a commit twice gives the same image. `build.sh` refuses an image of 2 GiB or more.

## What the image does

- GRUB shows the boot menu in the Racksail theme, also on the first serial port, and starts the installer after ten seconds. The entries install with the splash, install on the serial console, install with basic graphics (`nomodeset`), and under Utilities check the medium, open the firmware settings, boot from a disk, restart or shut down. On arm64 the menu stays on the firmware's console, which reaches the serial port.
- The kernel runs with `console=tty0` and the serial console (`ttyS0`, or `ttyAMA0` on arm64), and Plymouth shows the Racksail splash.
- The live system has no live-config and no users: root is locked, and the first console runs `racksail install` in place of its login prompt, as does every serial console the kernel was started with. systemd-networkd asks for an address on every wired interface, and systemd-resolved and systemd-timesyncd provide names and the time.
- Nothing listens on the network until the installer starts its browser mode.

## What the image expects of racksail install

The image carries no installer logic of its own. It runs `racksail install` from the binary it ships, at `/usr/local/bin/racksail`, so the terminal installer, the browser mode with its one-time code, unattended installs from an answers file ([spec/schemas/answers.schema.json](../spec/schemas/answers.schema.json)), rescue mode and restoring from a backup all belong to `internal/installer` and are tested there. The image gives it:

- `racksail install` with no arguments, as root, on each console it runs on, with `TERM` set for that console (`linux` or `vt220`), `LANG=C.UTF-8`, and `RACKSAIL_CONSOLE` naming the console, such as `tty1` or `ttyS0`. One instance runs per console, so the installer coordinates between them. It is started again when it exits. Its standard output and error go to that console, so what it says when it stops stays in front of whoever is installing, and in the serial console's log the end-to-end harness keeps; its own logs belong in the journal.
- `/etc/racksail/installer-release`, in the format of os-release: `NAME`, `VERSION`, `VARIANT_ID` (`standard` or `firmware`) and `ARCHITECTURE`. Its presence is how the installer knows it runs from the image, where the install and restore modes may erase disks.
- Answers for an unattended install come from `answers.yaml` at the root of a FAT or ISO 9660 volume labelled `RACKSAIL`, such as a USB stick, or from an `http` or `https` URL given as `racksail.answers=` on the kernel command line, which the boot menu lets you add with `e`. The end-to-end harness uses the USB stick and names disks by their `/dev/disk/by-id/` paths.
- The tools the install engine runs: mmdebstrap and Debian's archive keys, partitioning and file system tools for Btrfs, FAT, ext4 and XFS, mdadm, LVM and cryptsetup to recognize existing volumes, efibootmgr, restic and rsync for restores, and the console's keyboard and font tools.
- The Racksail splash in `/usr/share/plymouth/themes/racksail/` and the boot theme in `/boot/grub/racksail/` on the medium, which an install can copy to the server it sets up.

## Requirements

- Under 2 GiB, with nothing fetched at boot that Debian's mirrors don't provide.
- Branding comes from the default theme's console palette and the files in [brand/](../brand/), so the installer looks like the rest of Racksail. `go test ./iso` checks that the console colors match the theme, that the splash and the boot menu draw the mark as `brand/logo/racksail-mark-dark.svg` does, that the boot menu survives live-build's substitutions, and every image's `lb config`.
- Every image is tested by the end-to-end harness in [test/](../test/): it boots in QEMU, with Secure Boot among the firmware it tries, installs unattended, reboots and runs the API and UI suites.
