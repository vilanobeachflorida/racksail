---
title: "Cloud images"
description: "Start Racksail at a cloud provider from a prepared image."
---

At a cloud provider, start a server from the provider's Debian 13 image and give it cloud-init user data that converts it on its first boot. It's the same conversion as [on an existing server](../existing-server/), done for you, so a new server comes up with its panel ready.

## The user data

```yaml
#cloud-config
hostname: panel
fqdn: panel.example.com
prefer_fqdn_over_hostname: true

package_update: true
packages:
  - ca-certificates
  - curl

runcmd:
  - [curl, --fail, --silent, --show-error, --location, --proto, "=https", --output, /root/racksail-install.sh, "https://github.com/vilanobeachflorida/racksail/releases/latest/download/install.sh"]
  - [sh, /root/racksail-install.sh, --yes, --hostname, panel.example.com]
  - [rm, -f, /root/racksail-install.sh]
```

Change `panel` and `panel.example.com` to the name the panel will have. The install script verifies the release's signatures and checksums before anything runs, as it does when you run it by hand. `--yes` is needed because cloud-init has no terminal to ask on.

The repository's [packaging/cloud-init](https://github.com/vilanobeachflorida/racksail/tree/main/packaging/cloud-init) directory has this file and two others: one that pins a release, so every server of a fleet gets the same version, and one that installs [through a mirror](../existing-server/#through-a-mirror), for networks that can't reach GitHub or Sigstore.

## At your provider

Choose Debian 13 as the image, paste the user data where the provider asks for it, and allow port 8443 in the provider's firewall if it has one. The provider's Debian image must come with cloud-init, as Debian's official cloud images do.

| Provider | Where the user data goes |
|---|---|
| Hetzner Cloud | **Cloud config** when creating the server |
| DigitalOcean | **Advanced options**, then **Add initialization scripts (free)** |
| Vultr | **Additional features**, **Cloud-Init User-Data** |
| Akamai (Linode) | **Add user data**, with the Metadata service on |
| AWS EC2 | **Advanced details**, **User data**, with the Debian 13 image from Debian's AWS account |
| Proxmox, libvirt, QEMU | A NoCloud seed, below |

The server converts itself a minute or two after it starts. To follow along, sign in over SSH and run:

```sh
sudo journalctl -u cloud-final -f
```

The script's output also ends up in `/var/log/cloud-init-output.log`, with the panel's address at the end.

## Your own virtual machines

On Proxmox, libvirt or plain QEMU, start from Debian's official cloud images, such as `debian-13-genericcloud-amd64.qcow2` from [cloud.debian.org](https://cloud.debian.org/images/cloud/trixie/latest/), and give the user data on a NoCloud seed:

```sh
cloud-localds seed.iso user-data.yaml meta-data.yaml
```

`meta-data.yaml` names the machine:

```yaml
instance-id: racksail-panel
local-hostname: panel
```

Attach `seed.iso` to the machine as a CD-ROM and start it. Proxmox can also take the user data directly as a cloud-init snippet. Racksail's nightly tests start Debian's cloud image this way and check the converted panel.
