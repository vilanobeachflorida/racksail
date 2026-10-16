---
title: "Inside a module"
description: "How a module declares its kinds, reconcilers, API, screens and upstream software."
---

Everything beyond a small core is a module: web hosting, mail, DNS, databases, containers, VMs, storage and the rest. All 22 modules are compiled into the binary and stay dormant until they're turned on. Even then a module installs nothing until something needs it.

## What a module is

A module is a Go package in `modules/<id>/`, a UI entry in `ui/src/modules/<id>/`, and a docs page. It provides:

- **a manifest**, `manifest.yaml`: its name, version and license, the modules it depends on, the profiles it's on in, the hardware it needs, its expected memory and disk, the upstream software it can install with where it comes from and how it's verified, and the hook points it adds
- **kinds**: Go types for each kind's spec and status, from which its JSON Schema, API operations, CLI commands, assistant tools and generic screen are generated
- **reconcilers** that plan and apply each kind with agent operations
- **API operations** beyond the generated ones, if it needs them
- **backups**: what data belongs to each kind and how to pause writes for a consistent copy
- **health checks** for everything it runs
- **screens**, home cards, settings pages and command-bar actions, with every string in its message catalog
- **tests**: unit tests, golden files for what it renders, and flows against the simulated server

## States

| State | Meaning |
|---|---|
| `off` | Not visible. Nothing installed, no data, no network calls. |
| `on` | Visible and ready. Its kinds can be created. Nothing heavy is installed yet. |
| `active` | Something uses it, so its software is installed and its services run as needed. |
| `stopping`, `removing` | Turning off keeps data and settings; removing uninstalls its software and deletes its data, after a typed confirmation and an offer to export first. |

Every transition is a change set with a preview, listing each download with its source and size, each package, port, service and file, and the memory and disk the module will use. A module whose hardware requirements aren't met, such as VMs without hardware virtualization, says why and can't be turned on.

## Lazy software

The same rule holds inside modules. Turning on web hosting installs no PHP; creating the first PHP 8.4 site installs PHP 8.4, and the preview of that change says so. Runtimes live in versioned directories under `/opt/racksail/runtimes/` that never change, shared read-only by every site that uses them.

## Talking to other modules

Modules don't import each other. When one needs another's data, it reads that module's resources through the store; when it needs a capability another module or an add-on provides, such as a DNS provider or a notification channel, it asks for it through a provider hook point, so any implementation can stand in.

## Adding to a module

[docs/architecture/internals.md](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/internals.md) shows, with code, how to define a kind, write a reconciler, register an agent operation, add API operations and hooks, and add screens. [docs/architecture/modules.md](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/modules.md) lists every module, what it manages and what it installs.
