---
title: "Quick start"
description: "Install Racksail on a server and put your first site online."
---

This page takes you from an empty server to a site served over HTTPS. It takes about fifteen minutes, most of it waiting for downloads.

## 1. Get a server

You need a machine with at least 1 CPU, 1 GB of memory and 20 GB of disk, on amd64 or arm64; 2 CPUs, 4 GB and 40 GB are better. It can be a cloud server, a virtual machine or hardware in your home or rack. See [Requirements](../../install/requirements/) for the network and the ports.

## 2. Install Racksail

Pick the way that fits the machine:

- **A machine you can boot from a USB stick or an ISO**: [install from the ISO](../../install/iso/). It erases the disks you choose and sets up Debian 13 and Racksail together.
- **A server that already runs a fresh Debian 13**, such as a cloud server: [convert it](../../install/existing-server/) with the install script:

  ```sh
  curl -fsSLO https://github.com/vilanobeachflorida/racksail/releases/latest/download/install.sh
  sudo sh install.sh
  ```

- **A new cloud server**: give it [cloud-init user data](../../install/cloud-images/) that runs the conversion on its first boot.

At the end, the installer shows the panel's address, such as `https://203.0.113.10:8443`.

## 3. Finish the setup in the browser

Open the panel's address. Your browser warns about the certificate the first time, because the panel starts with a certificate of its own; once the server has a hostname, the panel gets a trusted certificate through ACME.

The [setup wizard](../setup-wizard/) asks which modules to turn on, checks the hostname and nameservers, sets up backups and their recovery key, and connects a notification channel. Each step shows what it will change before it does.

## 4. Create a site

At the end of the wizard, choose **Create a site**, or press <kbd>⌘K</kbd> (<kbd>Ctrl+K</kbd>) anywhere and type "new site".

1. Enter the domain, such as `blog.example.com`. If the domain doesn't point at the server yet, the site still gets a temporary address under the server's hostname, so you can work on it now.
2. Choose a stack: static files, PHP, Node.js, Python or another runtime. Racksail installs the runtime the first time a site uses it.
3. Review the preview. It lists everything the change does: the account directories, the service, the runtime it downloads with its size, and the certificate.
4. Apply it. The job's progress appears in the jobs drawer, and the site's page shows its address when it's live.

When the domain's DNS points at the server, Racksail gets the certificate on its own. If Racksail hosts the domain's DNS, it creates the records for you.

## Next steps

- Deploy the site from Git, or upload files with the file manager or SFTP.
- Add mailboxes for the domain in **Mail**.
- Install an app such as WordPress or Nextcloud from the **Marketplace**.
- Read [Concepts](../concepts/) to see how changes, previews and undo work everywhere in the panel.
