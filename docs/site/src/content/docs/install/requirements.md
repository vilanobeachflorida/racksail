---
title: "Requirements"
description: "The hardware, network and Debian release Racksail needs."
---

## The machine

| | Minimum | Recommended |
|---|---|---|
| CPU | 1 core, amd64 or arm64 | 2 cores |
| Memory | 1 GB | 4 GB |
| Disk | 20 GB | 40 GB, or two disks for a mirror |

Racksail runs on Debian 13 (trixie) on amd64 or arm64, and nothing else. The ISO installs Debian 13 for you; to convert a server that already runs Debian, it must be Debian 13.

What the server needs beyond the minimum depends on what you run on it. The panel shows the expected memory and disk of every change before you apply it, including turning a module on. Some modules need hardware of their own: virtual machines need hardware virtualization (Intel VT-x or AMD-V, or KVM on arm64), and GPU or PCI passthrough needs an IOMMU. The panel explains what's missing when a module can't be turned on.

UEFI and BIOS machines both boot the ISO, and Secure Boot can stay on: the ISO boots Debian's signed bootloader and kernel. Hardware that needs non-free firmware, such as some network cards, storage controllers and Wi-Fi, needs the [firmware ISO](../iso/#the-firmware-iso).

## The network

The server needs to reach:

- a Debian mirror, `deb.debian.org` by default, to install Debian and its packages
- `github.com`, for Racksail's releases and the official catalog
- the upstream sources of the software you choose to install, such as PHP or database packages; every download is shown, with its source, before it happens

A public IPv4 address, IPv6, or both work. Servers behind NAT, as on most clouds and home networks, work too; the setup wizard detects the public address.

## Ports

Racksail's firewall denies everything by default and opens a port only while a module that needs it is active.

| Port | For | Open when |
|---|---|---|
| 22/tcp | SSH and SFTP | Open, so you can always reach the server |
| 8443/tcp | The panel | Always |
| 80/tcp, 443/tcp, 443/udp | Sites, and the panel under its hostname | Web hosting is active |
| 25, 465, 587, 993, 995/tcp | Mail | Mail is active |
| 53/tcp, 53/udp | DNS | DNS is active |

If your provider has a firewall of its own, open 8443 there for the panel, and the others as you turn modules on.

## A domain

You can start without one: the panel works on the server's address, and every site gets a temporary address. To use your own domains, point them at the server, or let Racksail host their DNS.

Sending mail needs a hostname whose reverse DNS matches the server's address, and a provider that allows outgoing connections to port 25. The setup wizard checks both.
