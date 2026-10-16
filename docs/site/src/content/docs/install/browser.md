---
title: "Install from a browser"
description: "Drive the installer from another computer with a one-time code."
---

When the server's screen is hard to reach, or you'd rather type on your own computer, the installer can be driven from a browser. It asks the same five questions as on the screen.

## Start it

1. Boot the server from the [ISO](../iso/). Its network must come up, with DHCP or with the address you set in the installer's **Network** step.
2. On the installer's **Welcome** screen, choose to continue in a browser.
3. The installer shows an address on the server's network and a **one-time code**, on the screen and on the serial console.
4. Open the address on a computer on the same network, enter the code, and answer the questions there.

The screen shows the installer's progress while you work in the browser, and you can go back to the screen at any time.

## The one-time code

The code is the only thing that lets a browser drive the installer, and it works once: after the browser that used it ends the session, a new code is needed. The installer never shows the code on the network, only on the server's own consoles, so someone who can reach the address but not the screen can't use it.

Use browser mode on a network you trust. The installer's address is reachable by anything on that network until the install finishes, and the code is what keeps others out.

## When it's done

The server reboots, and the browser shows the panel's address for the [setup wizard](../../getting-started/setup-wizard/).
