---
title: "Install from the ISO"
description: "Boot the installer, answer five questions and get a ready server."
---

The Racksail ISO is a small network installer. It boots, asks five questions, installs Debian 13 from Debian's mirrors with Racksail on top, and reboots into a server whose panel is ready in your browser. It erases the disks you choose.

## Download

Every release has two images for each architecture on [GitHub Releases](https://github.com/vilanobeachflorida/racksail/releases):

| Image | For |
|---|---|
| `racksail-<version>-amd64.iso` | Most PCs, servers and virtual machines |
| `racksail-<version>-arm64.iso` | Arm servers and virtual machines with UEFI |
| `racksail-<version>-<arch>-firmware.iso` | The same, with Debian's non-free firmware added |

Each image is under 2 GiB.

### Check the download

Each release lists the SHA-256 checksum of every file in `SHA256SUMS`, and signs every file, the checksums included, with Sigstore. To check an image:

```sh
sha256sum --check --ignore-missing SHA256SUMS
```

To check the signature as well, with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/):

```sh
cosign verify-blob \
  --bundle racksail-1.2.3-amd64.iso.sigstore.json \
  --certificate-identity https://github.com/vilanobeachflorida/racksail/.github/workflows/release.yml@refs/tags/v1.2.3 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  racksail-1.2.3-amd64.iso
```

## Write it to a USB stick

The image boots from a USB stick as it is. On Linux, with the stick at `/dev/sdX`:

```sh
sudo dd if=racksail-1.2.3-amd64.iso of=/dev/sdX bs=4M conv=fsync status=progress
```

On macOS, use the stick's raw device, such as `/dev/rdisk4`. On Windows, use a tool that writes images byte for byte, such as Rufus in DD mode. For a virtual machine, attach the ISO as a CD-ROM.

## Boot

Boot the machine from the stick. Secure Boot can stay on: the image boots Debian's signed shim, GRUB and kernel.

The boot menu offers:

| Entry | When |
|---|---|
| Install Racksail | The default, chosen after ten seconds |
| Install Racksail on the serial console | Headless servers reached through IPMI, a serial cable or a cloud console |
| Install Racksail with basic graphics | Screens that stay black with the first entry |
| Utilities | Check the medium for errors, open the firmware settings, boot from a disk, restart or shut down |

The installer runs on the screen and on the serial console at the same time, so you can use whichever you can reach.

## Answer five questions

1. **Welcome**: choose the keyboard layout, then **Install**. **Rescue** opens tools to repair an installed server, and **Restore** sets up a server from a backup.
2. **Network**: DHCP is chosen when it works. You can set a static address, IPv6 and a VLAN instead.
3. **Disks**: choose one disk, two disks as a mirror, or your own layout. The installer shows what each disk holds now, and can reinstall the system while keeping the data of an earlier Racksail install. Racksail uses Btrfs, with separate subvolumes for the system, logs, its state, your data and databases.
4. **Account**: the admin's name and password, with a strength meter, and their SSH keys, typed in or imported from a GitHub account. Also the server's hostname.
5. **Review**: everything you chose, and the disks that will be erased. Nothing is changed before you confirm.

The installer then installs Debian 13 from the mirrors, sets up Racksail and its services, and reboots. If something fails, it says what happened, lets you retry, and can save its log to a USB stick.

## After the reboot

The server's screen shows the [console](../console/): the panel's address with a QR code to open it on your phone, and the state of Racksail's services. Open the address in a browser to start the [setup wizard](../../getting-started/setup-wizard/).

## Other ways to install

- [From a browser](../browser/), when you'd rather answer the questions from another computer.
- [Unattended](../unattended/), with the answers in a file, for many servers or for automation.

## The firmware ISO

Debian keeps firmware that isn't free software out of its main archive. Most servers and virtual machines don't need it, but some network cards, storage controllers, Wi-Fi adapters and graphics cards do. The firmware ISO adds every firmware package from Debian's `non-free-firmware` area to the installer, including CPU microcode updates.

Its file name ends in `-firmware.iso`, and its boot menu says "with non-free firmware". The installer adds the firmware the machine's hardware needs to the server it installs, so the hardware keeps working after the reboot. Use it if the standard image doesn't find your network card or disks. Firmware is the only thing it adds: Racksail installs nothing else that isn't free software by default.
