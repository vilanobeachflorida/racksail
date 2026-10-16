---
title: "Security model"
description: "Privilege separation, sandboxing and what each process is allowed to do."
---

Racksail runs as root on servers connected to the internet and holds other people's websites, email and files. It's built so that a mistake or a break-in in one part can't take the rest: every process gets only the privileges its job needs, the one process that runs as root accepts nothing but typed, checked operations, and every change goes through one pipeline that checks who makes it and records it.

## Who runs what

| Process | Runs as | Can | Can't |
|---|---|---|---|
| The agent, `racksail-agent.service` | root | Run the operations of its allowlist: write files where Racksail keeps them, manage its units, users, packages, disks, the firewall and the network | Run a command it is given. There is no such operation |
| The panel, `racksail-panel.service` | the `racksail` user, with no capabilities | Serve the API and the web UI, keep the panel's state and secrets, and ask the agent and the edge for what it needs | Change the machine itself, or write anywhere but its own state and cache |
| The edge, `racksail-edge.service` | the `racksail-edge` user, allowed to bind ports below 1024 and nothing more | Serve websites on ports 80 and 443, obtain their certificates and pass requests on to sites and apps | Reach the agent, or the panel except through the questions the panel answers |
| An add-on, `racksail-addon@<id>.service` | a user systemd makes up for it | What its [permissions](../../addons/permissions/) grant, through the panel | Reach the agent, the edge or the panel's state, or the network unless its manifest declares hosts |
| A site's or app's service | its account's user, in the account's slice | Read and write the account's own files, and listen on its own socket | See other accounts, the panel's files or the agent's socket |

The agent, the panel and the edge run in `racksail.slice`, which keeps them responsive when an account is busy, and accounts in `racksail-accounts.slice` inside it, each in a slice of its own with its plan's limits.

## The agent

The agent is the only part of Racksail with root's privileges, so it is the part that trusts least.

- **A fixed allowlist.** It only runs operations compiled into the binary, such as writing a file, starting a unit or adding a firewall rule. Each operation checks every argument before it runs: paths absolute, clean and inside the roots Racksail manages, names against strict patterns, sizes within bounds. What may be touched is fixed when the agent starts, so a compromised panel can't widen it.
- **No shell.** Programs run with a fixed path and a list of arguments, never a command line put together from text.
- **Checked files.** Unit files are read as systemd reads them and may only use the directives Racksail writes, and an account's services must carry the sandbox below. Firewall rules may only be Racksail's own tables, and only data disks can be mounted, never the system disk.
- **Out of reach.** The agent's own state, its audit trail and the snapshots of the system are out of reach of every operation, so nothing that talks to the agent can rewrite the record of what it did.
- **An audit trail.** Every call is recorded, refusals included: who called, for which resource, what it touched, a hash of its input and how it ended, in `/var/log/racksail-agent/audit.jsonl`, which only root can read.
- **A small door.** Its socket, `/run/racksail/agent.sock`, only accepts root and the `racksail` group, which is the panel. Agents on other servers are reached over mutual TLS, with certificates that name who holds them, and they connect out to the panel, so they need no open management port.

## The panel

The panel holds the keys to everything, so it runs with as little as possible:

- as its own user with no capabilities and no way to gain any, a read-only view of the system with only `/var/lib/racksail` writable, no home directories, its own `/tmp`, no devices, and system calls limited to those services need;
- every request is authenticated, and every operation asks the central authorizer, which a test holds to every operation the API has;
- every change goes through the [change pipeline](../../architecture/change-pipeline/), where it's validated, run past rules and hooks, previewed and recorded in the audit log, whoever makes it;
- secrets are encrypted at rest, with a key sealed by `systemd-creds`, through the TPM when the server has one, and they never appear in settings, previews, events or logs; see [Secrets](../../architecture/secrets/);
- sessions are cookies other sites can't send or read, renewed when someone signs in, confirms it's them or gets other grants, and requests that change something from another site's page are refused; see [Signing in](../sign-in/).

Its address can be limited to your own networks with `panel.allow_from`; see [Hardening](../hardening/).

## The edge

The edge is what the internet talks to, so it can do the least: it runs as its own user, may bind the ports websites use and nothing more, and has the same read-only, unprivileged sandbox as the panel. It never calls the panel. Its admin socket, `/run/racksail/edge.sock`, belongs to the `racksail` group, so the panel reaches it without the edge being in that group, which would let it reach the agent too.

## Accounts

Each account is a Linux user and a systemd slice, so the kernel keeps accounts apart and holds each to its limits: memory, CPU, I/O, processes and disk space. Their sites and workers run as sandboxed services, as the account's user, with at least:

- a read-only system, a home with nothing but the account's own directory in it, and their own `/tmp`;
- no devices, no new privileges, no set-user-ID programs, no namespaces of their own, and no access to the kernel's tunables, modules or control groups;
- system calls limited to those services need, and memory that can't be both written and run where the runtime allows it;
- only their own socket or port to listen on, and the panel's and agent's sockets out of reach.

The agent refuses to install a service for an account without these settings.

## What it defends against

| Threat | Defenses |
|---|---|
| A customer attacks another customer on the same server | A Linux user, a slice and quotas per account, sandboxed services, private `/tmp`, limits on what each service may listen on |
| The internet attacks the panel | Authentication on everything, rate limits and lockouts, passkeys and second steps, confirmation before sensitive changes, protection against cross-site requests, strict content security policies, secure cookies, optional address allowlists |
| A compromised site tries to take the server | It runs as its account's user in a sandbox, with no access to other accounts, the panel or the agent's socket |
| A compromised panel tries to take the server | The panel runs unprivileged, and the agent only runs typed, checked operations, recording every one |
| A malicious or careless add-on, recipe or theme | Permissions enforced on every call, sandboxed processes, no network unless declared, previews and audit of every change, safe mode, signed catalogs, no code in themes |
| The supply chain | Pinned upstream sources checked against signatures and checksums, a wait before new upstream releases reach the catalog, signed Racksail releases with software bills of materials, dependency and license checks |

## Report a problem

Found a weakness? Report it privately, as [Reporting a vulnerability](../reporting/) describes.
