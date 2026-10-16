---
title: "The console"
description: "The status screen and the recovery menu on the server's own screen."
---

A Racksail server's own screen, and its serial console, show a status screen instead of a login prompt. It's what you see on a monitor plugged into the server, in a virtual machine's console, or through IPMI.

## The status screen

- **The panel's address**, such as `https://203.0.113.10:8443`, with a QR code that opens it on your phone
- **Racksail's services**: the agent, the panel and the edge, and whether each is running
- **The server**: its hostname, addresses and Racksail's version

It updates on its own, so it's the first place to look when the panel doesn't answer.

## The menu

The menu offers:

| Item | What it does |
|---|---|
| Network | Changes the network setup, with an automatic rollback if the panel can't be reached afterwards |
| Reset password | Sets a new password for a panel admin |
| Shell | Opens a root shell |
| Safe mode | Restarts the panel with every add-on turned off |
| Reboot | Restarts the server |
| Shut down | Turns the server off |

Anything beyond showing the status needs a server admin's password, because whoever stands at the console shouldn't be able to take the server over. Resetting a password and safe mode are recorded in the audit log.

Safe mode is also a boot option, for an add-on that keeps the panel from starting, and Racksail turns it on by itself when an add-on crash-loops at startup.

## Over SSH

When you sign in over SSH, the login message shows the same status: the panel's address, the services and anything that needs attention.
