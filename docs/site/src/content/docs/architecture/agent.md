---
title: "The agent"
description: "The privileged process that changes the machine through a fixed list of typed operations."
---

The agent is the only part of Racksail that runs as root. The panel never changes the machine itself: it asks the agent, and the agent only does what one of its operations does. There is no operation that runs an arbitrary command.

## Operations

Every operation is a typed function with an input that validates every field before anything runs. Paths must be absolute, clean and inside the locations Racksail manages; names must match strict patterns; sizes are bounded. The operations cover:

- users and groups, including picking free account IDs;
- files and directories, archives (tar, tar.gz, tar.zst and zip) packed and unpacked without following links out of the directory;
- systemd units, slices, sockets, timers and drop-ins, starting and stopping them, their status, resource limits and the journal;
- packages from Debian and pinned vendor repositories, held packages, upgrades, and vendor archives checked against a checksum or the vendor's signature before they are unpacked into a versioned directory under `/opt/racksail`;
- Btrfs subvolumes, quotas, snapshots and send streams, mounts, disks and their SMART health;
- the firewall, network configuration, sysctl, processes and certificates;
- kernel and network defaults, time synchronization, automatic security updates and scheduled reboots;
- credentials sealed with systemd-creds.

Every operation can run in check mode, where it reports exactly what it would change, such as the packages and their download sizes, without changing anything. The panel's previews come from check mode. Operations are idempotent: running one again when the machine already matches changes nothing, so retries are always safe.

The whole list, with every input and output, is the agent protocol, published as `spec/agent-protocol.json`.

## The audit trail

The agent records every call: who asked, for which resource, what it touched, a hash of the input, how long it took and how it ended. Refused calls are recorded too. On a server the trail is in `/var/log/racksail/agent-audit.jsonl`, and the panel can read it back. Inputs that carry secrets aren't hashed.

## Drift

The agent remembers what it wrote for each resource. When a file or unit it wrote is changed or removed by hand, or one of Racksail's services fails, it tells the panel, which puts things back.

## Network changes that undo themselves

A network change that could cut the server off is applied on probation. Unless the panel confirms it in time, which it can only do while it can still reach the server, the agent puts the previous configuration back on its own, even if it restarts in between.

## Reaching other servers

On the panel's own server, the panel talks to the agent over a Unix socket only the `racksail` group can reach. Other servers are reached with mutual TLS, with certificates from a CA the panel keeps. A certificate says who holds it, the panel or the agent of a named server, and both sides check that rather than host names.

A server joins with `racksail agent join`, using a one-time token from the panel and the fingerprint of the panel's CA. Its agent then opens connections to the panel's hub and serves the panel over them, so the server needs no open management port, and it renews its certificate before it expires.
