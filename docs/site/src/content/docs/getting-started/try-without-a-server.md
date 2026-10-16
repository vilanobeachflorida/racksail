---
title: "Try it without a server"
description: "Run the whole panel on your own computer against a simulated server with racksail dev."
---

`racksail dev` runs the panel, the agent and the edge in one process on your own computer, against a simulated Debian 13 server. Nothing touches your machine: the simulated server and everything the panel holds live in memory, so every start begins with a freshly installed server. It works on macOS and Linux, needs no root, and is how Racksail is developed.

## Run it

You need Go 1.27, Node.js 22.22.2 or later with npm, git and make.

```sh
git clone https://github.com/vilanobeachflorida/racksail.git
cd racksail
make build
bin/racksail dev
```

Open http://127.0.0.1:7070. You're signed in as the admin of the simulated server.

To work on Racksail itself, `make dev` runs the same thing plus the UI's development server with hot reload on http://127.0.0.1:5173.

## What's simulated

The simulated server looks like a fresh install with a few empty disks and a network interface. When the panel installs a package, writes a file, starts a service or creates a disk pool, the simulated server changes the way a real one would, so previews, jobs, health checks and undo all behave as they do on a server. What reaches the outside world, such as certificates and email delivery, is simulated too.

## Options

| Option | What it does |
|---|---|
| `--addr` | Where the panel listens, 127.0.0.1:7070 by default |
| `--edge-http`, `--edge-https` | Where the edge serves sites once a module adds a route; port 0 picks free ports |
| `--data` | The directory for its sockets and working files, `.dev` by default |

To run two instances side by side, give each its own addresses and data directory:

```sh
bin/racksail dev --addr 127.0.0.1:7170 --edge-http 127.0.0.1:0 --edge-https 127.0.0.1:0 --data .dev-2
```

## Limits

`racksail dev` signs everyone who reaches it in as the admin, so it only listens on loopback addresses: 127.0.0.1, ::1 or localhost. It refuses anything else. Use a real install for anything other people reach.
