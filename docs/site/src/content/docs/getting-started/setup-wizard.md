---
title: "The setup wizard"
description: "What the first visit to the panel asks: profile, hostname, backups, notifications and the assistant."
---

The first time you open the panel after installing, the setup wizard walks you through what a new server needs. Every step previews what it changes, and you can come back to any of it later in **Settings**.

## Profile

Choose what the server is for. A profile is a set of modules to turn on; you can turn any module on or off later.

| Profile | Modules turned on |
|---|---|
| All-in-one | Network, web hosting, hosting business, databases, DNS, mail, containers, VMs (when the hardware supports them), storage, shares, marketplace, backups, monitoring, files, performance, automations |
| Web hosting | Network, web hosting, hosting business, databases, DNS, mail, marketplace, backups, monitoring, files, performance, automations |
| Home server | Network, storage, shares, containers, VMs (when supported), home extras, marketplace, backups, monitoring, files, automations |
| Virtualization host | Network, VMs, containers, storage, backups, monitoring, files |
| Mail server | Network, mail, DNS, backups, monitoring |
| DNS server | Network, DNS, backups, monitoring |
| Minimal | Network |

The assistant, migration, multi-server, builder, security scanning and home extras are offered on their own. Turning a module on installs nothing yet: software comes the first time something needs it.

## Hostname and nameservers

Give the server its name, such as `panel.example.com`. The wizard checks, live, that the name resolves to this server, and shows the records to create if it doesn't. If the server will host DNS, it also sets up the nameservers, such as `ns1.example.com` and `ns2.example.com`, and tells you the glue records to add at your registrar.

The wizard also detects:

- the server's addresses, IPv4 and IPv6, and whether it sits behind NAT, as cloud and home servers often do
- whether outgoing connections to port 25 work, which sending mail needs
- the reverse DNS of the main address, which mail deliverability needs

## Backups and the recovery key

Choose where backups go: a local disk, S3-compatible storage, SFTP, a rest-server or Backblaze B2. Backups are encrypted, and the wizard shows their **recovery key** once. Save it somewhere off the server; you need it to restore if the server is lost. The wizard asks you to confirm you saved it before it goes on.

## Notifications

Choose where alerts go: email, a webhook, Matrix, ntfy, Telegram, Discord or Slack. The wizard sends a test message so you know it works.

## The assistant

If you want the assistant, choose a provider or a local model. For a local model, the wizard suggests one that fits the server's memory and shows its download size first. You can skip this and turn the assistant on later, or never.

## What next

The wizard ends with three choices: create a site, install an app from the marketplace, or start a migration from another panel or server.
