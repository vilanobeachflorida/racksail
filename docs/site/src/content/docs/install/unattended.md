---
title: "Unattended installs"
description: "Install without questions from an answers file."
---

An answers file answers the installer's questions ahead of time, so a server installs without anyone at the screen. It's how you install many servers alike, and how Racksail's own tests install it every night.

## Give the installer the file

The installer looks for answers in two places when it starts:

- **A USB stick**, or any volume, labelled `RACKSAIL` with the file `answers.yaml` at its root. FAT and ISO 9660 both work.
- **A URL** on the kernel command line: `racksail.answers=https://deploy.example.com/web1.yaml`. To add it, press <kbd>e</kbd> on the boot menu's first entry and append it to the line that starts with `linux`, or set it in your network boot configuration.

When it finds answers, it checks them before touching anything, shows them on the screen and the serial console, and installs without asking. A file that doesn't pass the checks stops the install with the reason, before any disk is changed.

To make the USB stick on Linux:

```sh
sudo mkfs.vfat -n RACKSAIL /dev/sdX1
sudo mount /dev/sdX1 /mnt
sudo cp answers.yaml /mnt/answers.yaml
sudo umount /mnt
```

## The file

Answers files are YAML. The schema is [spec/schemas/answers.schema.json](https://github.com/vilanobeachflorida/racksail/blob/main/spec/schemas/answers.schema.json).

```yaml
format: 1
mode: install
hostname: web1.example.com
locale: en-US
keyboard: us
channel: stable
network:
  mode: dhcp
disks:
  layout: single
  devices: [/dev/sda]
  confirmWipe: true
account:
  username: admin
  sshKeys:
    - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILOVCx0Utmhvafzmm0HYOckUK9zP6j0GTY3znySw9HGV admin@example.com
reboot: true
```

| Field | What it says |
|---|---|
| `format` | Always `1` |
| `mode` | `install` erases the chosen disks; `restore` installs and then restores a backup; `convert` sets up a running Debian 13 system and is used by the [install script](../existing-server/) |
| `hostname` | The server's name |
| `locale`, `keyboard` | Such as `en-US` and `us` |
| `mirror` | A Debian mirror, `deb.debian.org` by default |
| `channel` | The release channel to follow: `stable`, `beta` or `nightly` |
| `network` | `mode: dhcp`, or `mode: static` with `address` (with its prefix length), `gateway` and `dns`. Optionally `interface` (a name or MAC address), `vlan`, and `ipv6` with `mode: auto`, `static` or `off` |
| `disks` | `layout: single` (one disk), `mirror` (two disks in a Btrfs mirror) or `custom` (with `devices` and a Btrfs `profile`). `confirmWipe: true` is required to erase disks; `keepData: true` reinstalls the system and keeps the data of an earlier install |
| `account` | The admin's `username`, and at least one way to sign in: `passwordHash`, `sshKeys` or `githubKeys`, a GitHub user whose public keys are imported |
| `restore` | For `mode: restore`: the backup `repository` and optionally the `snapshot` |
| `reboot` | Reboot when the install is done |

Name disks so the name can't change between boots, such as `/dev/disk/by-id/nvme-Samsung_SSD_980_1TB_S64ANS0T123456`. Plain names such as `/dev/sda` can move when a USB stick is plugged in.

### Passwords

Answers files never hold passwords, only hashes, in yescrypt or SHA-512 crypt format. Make one with:

```sh
mkpasswd --method=yescrypt
```

or `openssl passwd -6`. SSH keys or `githubKeys` avoid passwords altogether.

### A static address

```yaml
network:
  mode: static
  address: 203.0.113.10/24
  gateway: 203.0.113.1
  dns: [9.9.9.9, 149.112.112.112]
  ipv6:
    mode: static
    address: 2001:db8::10/64
    gateway: 2001:db8::1
```

### Two disks as a mirror

```yaml
disks:
  layout: mirror
  devices:
    - /dev/disk/by-id/ata-WDC_WD40EFRX-68N32N0_WD-WCC7K0123456
    - /dev/disk/by-id/ata-WDC_WD40EFRX-68N32N0_WD-WCC7K0654321
  confirmWipe: true
```

## Checks the installer makes

Beyond the schema, the installer refuses a file when:

- a static address has no prefix length, or the gateway isn't on its network
- a DNS server isn't an IP address
- a single layout names more than one disk, a mirror doesn't name exactly two, or a custom layout has no profile
- a disk isn't a path under `/dev/`
- an install would erase disks without `confirmWipe: true`
- the admin has no password hash, SSH key or GitHub account to sign in with
