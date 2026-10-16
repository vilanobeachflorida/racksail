---
title: "The agent"
description: "The privileged process that changes the machine through a fixed list of typed operations."
---

The agent is the only part of Racksail that runs as root. The panel never changes the machine itself: it asks the agent, and the agent only does what one of its operations does. There is no operation that runs an arbitrary command.

## Operations

Every operation is a typed function with an input that validates every field before anything runs. Paths must be absolute, clean and inside the locations Racksail manages; names must match strict patterns; sizes are bounded. Each operation has a name, such as `WriteFile`, `EnsureUser`, `EnsureUnit`, `InstallPackages` or `ApplyFirewall`. Modules add their own, prefixed with the module, such as `dns.ReloadZones`, and extend what file, unit and package operations may touch, such as allowing writes under `/etc/knot` for DNS. Those lists are fixed when the binary starts, and vendor package repositories are registered in the binary, so the panel can only name them. External programs always run with a fixed path and a list of arguments, never through a shell and never from a string put together at run time.

The operations cover:

- users and groups, including picking free account IDs;
- files and directories, archives (tar, tar.gz, tar.zst and zip) packed and unpacked without following links out of the directory;
- systemd units, slices, sockets, timers and drop-ins, starting and stopping them, their status, resource limits and the journal;
- packages from Debian and pinned vendor repositories, held packages, upgrades, and vendor archives checked against a checksum or the vendor's signature before they are unpacked into a versioned directory under `/opt/racksail`;
- Btrfs subvolumes, quotas, snapshots and send streams, mounts, disks and their SMART health;
- the firewall, network configuration, sysctl, processes and certificates;
- kernel and network defaults, time synchronization, automatic security updates and scheduled reboots;
- credentials sealed with systemd-creds.

A few rules keep a panel that was taken over from taking the server:

- Paths are resolved one directory at a time below the location Racksail manages, never following a symbolic link on the way, so a link an account plants in its own directory can't send a read or a write the agent does as root anywhere else. Archives are packed and unpacked the same way.
- Unit files are read the way systemd reads them and may only use the sections and directives Racksail renders. Services run as an account or one of Racksail's own service users, never as root or the panel, and their commands can't use the prefixes that run them with full privileges; nothing but a service runs a command. An account's service keeps at least the sandbox every site gets. Racksail's own units are rendered by the agent itself, for a `racksail` executable only root can change. Drop-ins only tune resources, limits and restarts.
- Only data disks are mounted: a disk named by its stable link under `/dev/disk`, never the system disk, a disk with RAID or LVM on it, or a filesystem already mounted somewhere else. They go at `/mnt/racksail/<name>` or over the accounts' area, where nothing can plant a symbolic link, always with `nosuid` and `nodev`. Mount units are held to the same.
- A snapshot is only restored from a file the agent saved itself and that hasn't changed since. Whatever arrives through `btrfs receive` is confined to its directory and loses set-user-ID and set-group-ID bits, device nodes, file capabilities and security labels, and a stream may only build on snapshots Racksail could read anyway.
- Snapshots of the system itself, such as the root filesystem before an upgrade, are kept where no operation reads, sends or copies them; they can only be deleted.
- Firewall rulesets may only define Racksail's own tables, so they can't touch another program's or read other files, and network files may only use what Racksail renders.
- Kernel parameters can only be the ones Racksail tunes, never those that weaken the kernel's own defenses.

Every operation can run in check mode, where it reports exactly what it would change, such as the packages and their download sizes, without changing anything. The panel's previews come from check mode. Operations are idempotent: running one again when the machine already matches changes nothing, so retries are always safe.

The whole list, with every input and output, is the agent protocol, published as `spec/agent-protocol.json`.

## The audit trail

The agent records every call: who asked, for which resource, what it touched, a hash of the input, how long it took and how it ended. Refused calls are recorded too, including callers whose certificate may not call the agent and peers refused during the TLS handshake, with the address they came from; a flood of refusals from one address is counted rather than recorded one by one. On a server the trail is in `/var/log/racksail-agent/audit.jsonl`, which only root can read and no operation can change, so a panel that was taken over can't erase what it did; the panel can read it back. Inputs that carry secrets aren't hashed.

## Drift

The agent remembers what it wrote for each resource. Every minute it checks the machine against that: files by their size and modification time, with a few read in full each time so edits that kept both are caught too, and units and drop-ins whole. When something it wrote is changed or removed by hand, or one of Racksail's services fails, it tells the panel once, and a panel that connects later hears about it then. The panel records it in the timeline and reconciles the resource it was written for, which puts things back within seconds instead of at the next resync.

## Network changes that undo themselves

A network change that could cut the server off is applied on probation. Unless the panel confirms it in time, which it can only do while it can still reach the server, the agent puts the previous configuration back on its own, even if it restarts in between. If putting the previous configuration back fails, the agent keeps trying. A confirmation that arrives after the deadline is refused as a conflict, so it is never mistaken for one that worked.

## Reaching other servers

On the panel's own server, the panel calls the agent over HTTP and JSON on the Unix socket `/run/racksail/agent.sock`, which only the `racksail` group can reach. Other servers are reached with mutual TLS, with certificates from a CA the panel keeps. A certificate says who holds it, the panel or the agent of a named server, and both sides check that rather than host names.

A server joins with `racksail agent join`, using a one-time token from the panel and the fingerprint of the panel's CA. Its agent then opens connections to the panel's hub and serves the panel over them, so the server needs no open management port, and it renews its certificate before it expires. A server removed from the cluster is cut off at once: its connections close, and it can neither connect again nor renew its certificate.

## The simulated server

Every operation works through the [system layer](../system-layer/), which has two implementations: `linux` for Debian 13, and `sim`, a simulated Debian 13 server held in memory. `racksail dev` and the tests use the simulated one, so every operation is tested without root. You can run a standalone agent against a simulated server, too:

```sh
racksail agent --sim
```

## Why it's built this way

A panel faces the internet and is among the most attacked kinds of software. Keeping the panel unprivileged and the agent small and strict means a compromised panel still can't run what it likes on the machine: it can only ask for operations that pass validation, and every change it makes went through the pipeline and the audit log.
