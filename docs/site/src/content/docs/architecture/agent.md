---
title: "The agent"
description: "The privileged process that changes the machine through a fixed list of typed operations."
---

The agent is the only part of Racksail that runs as root. It changes the machine on the panel's behalf, and it can only do what its list of typed operations allows. There is no operation that runs an arbitrary command.

## Typed operations

Each operation has a name, such as `WriteFile`, `EnsureUser`, `EnsureUnit`, `InstallPackages` or `ApplyFirewall`, and a typed input. Modules add their own, prefixed with the module, such as `dns.ReloadZones`.

Before an operation runs, it validates every field of its input:

- paths are absolute, clean, and inside the roots operations may touch
- names match strict patterns
- sizes are bounded

What file, unit and package operations may touch is fixed when the binary starts. Modules extend it, such as allowing writes under `/etc/knot` for DNS, and vendor package repositories are registered in the binary, so the panel can only name them.

External programs always run with a fixed path and a list of arguments, never through a shell and never from a string put together at run time.

## Check mode

Every operation can run in check mode, where it reports what it would change without changing it. The change pipeline runs plans in check mode to build its previews, which is why a preview can list exactly which packages, files, services and ports a change touches.

Operations also report whether they changed anything. Running an operation whose effect is already in place reports no change, which makes plans safe to run again and again.

## How the panel reaches it

On the same server, the panel calls the agent over HTTP and JSON on the Unix socket `/run/racksail/agent.sock`, which only the panel's group can open. Servers that join a cluster connect out to the panel over mutual TLS. The protocol is described by an OpenAPI document, [spec/agent-protocol.json](https://github.com/vilanobeachflorida/racksail/blob/main/spec/agent-protocol.json), generated from the operations themselves.

## The simulated server

Every operation works through system adapters, and there are two sets: `linux` for Debian 13, and `sim`, a simulated Debian 13 server held in memory with disks, network interfaces, packages, services, logs and metrics. `racksail dev` and the tests use the simulated one, so the whole product runs on a laptop and every feature is tested without root.

You can run a standalone agent against a simulated server, too:

```sh
racksail agent --sim
```

## Why it's built this way

A panel faces the internet and is among the most attacked kinds of software. Keeping the panel unprivileged and the agent small and strict means a compromised panel still can't run what it likes on the machine: it can only ask for operations that pass validation, and every change it makes went through the pipeline and the audit log.
