---
title: "The system layer"
description: "How the agent touches the machine, the simulated server, and the defaults Racksail applies."
---

The agent's operations reach the machine through the system layer: one interface per area, such as users, files, systemd, packages, Btrfs, disks, the firewall, the network and the journal. There are two implementations of every interface.

## Linux

The Linux implementation drives Debian 13. It talks to systemd over D-Bus, reads the kernel's view of disks, network interfaces, CPUs, Secure Boot and the TPM, and reads SMART data from `smartctl`. External programs are run with a fixed path and a list of arguments, never through a shell.

Where accounts can change files, the agent works through views of a directory that open every path one directory at a time, refusing any symbolic link on the way, even one swapped in while the agent works. The directories trees open for archives work the same way.

## The simulated server

The simulated server implements the same interfaces without touching anything: a Debian 13 server with disks, network interfaces, CPUs, packages, services, logs and metrics, all in memory. Every operation changes it the way it would change a real server. `racksail dev` keeps one between runs, with the timings of a small real server, so the whole product works on a laptop without root.

## Rendering

Every file Racksail writes, such as systemd units, network and firewall configuration, apt sources and kernel settings, is produced by a pure function and checked against a golden copy in the tests. What a change will write can be shown before anything is written.

Unit files go the other way too: a parser reads them exactly as systemd does, with its comments, continued lines and trimming, and a checker holds each type of unit to the directives the renderers use, with their values. The units the renderers produce for accounts pass it; a unit that would run a command as root, hand a service another account's directories or stop the machine doesn't.

## Platform units

Racksail runs as three services in `racksail.slice`, with every account's workloads in its own slice inside `racksail-accounts.slice`:

- the agent, as root, the only process that changes the machine;
- the panel, as `racksail`, with a read-only view of the system and no capabilities;
- the edge, as `racksail-edge`, allowed to listen on ports 80 and 443 and nothing more.

The platform's slice outweighs the accounts' for CPU and I/O, so the panel and the edge stay responsive when a site is busy. The agent shares the host's mounts, so a data disk it mounts is there for every service. The unit files ship in `packaging/systemd`.

## Defaults

Racksail tunes the kernel when it installs: BBR congestion control with the fq scheduler, larger connection queues and buffers, a connection tracking table sized to the memory, hardened routing settings, and compressed swap in memory with zram. Only the parameters on Racksail's list can be changed, so nothing can turn off the kernel's own protections. The clock is kept in UTC and synchronized, Debian's security updates install automatically, and a reboot a kernel update needs can be scheduled for a maintenance window. Full upgrades take Btrfs snapshots of the system first.
