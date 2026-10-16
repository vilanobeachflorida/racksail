---
title: "Publishing add-ons"
description: "Package an add-on, list it in a catalog, and how panels check what they install."
---

People install add-ons from catalogs: Racksail's official catalog, or any catalog an admin trusts. A catalog lists the add-on's manifest and a recipe that pins its program, and signs both, so a panel installs exactly what the catalog offered.

## Build the archive

The panel installs an add-on's program from an archive:

- with one executable called `addon` at its top, or below up to four leading directories that the installation drops;
- as `tar.gz`, `tar.xz`, `tar.zst` or `zip`, up to 512 MiB, downloaded over HTTPS;
- one for each architecture you support, `amd64` and `arm64`.

The program must run on a Debian 13 server as it is, so build it into a single executable or bring its runtime along; see [SDKs](../sdks/#package-it). Build the same archive from the same source every time, so anyone can check that what a catalog pins is what the source builds. Racksail's own add-ons, in [addons/](https://github.com/vilanobeachflorida/racksail/tree/main/addons), are built that way by `make addons`, as `<id>-<version>-linux-<arch>.tar.gz`, and published with every Racksail release.

The panel downloads the archive, checks its size and SHA-256 against what the add-on was installed with before it unpacks anything, and unpacks it into a directory of its own for that version, under `/opt/racksail/modules/addons/<id>/`. Once a new version runs, the directories of older ones are removed.

## List it in a catalog

An add-on item is a directory named after the add-on's id, in the catalog's `addons/` directory:

```
addons/site-guard/
  addon.yaml
  recipe.yaml
  icon.svg
```

`addon.yaml` is the [manifest](../manifest/). `recipe.yaml` carries what every catalog item has, and pins the program, one download for each architecture:

```yaml
format: 1
kind: addon
id: site-guard
name: Site guard
version: 1.2.0
summary: Refuse deploys that contain known web shells.
description: >-
  Checks every deploy for known web shells and refuses the ones that contain any, and shows what it
  found on each site's Security tab.
license: MIT
licenseClass: open-source
homepage: https://github.com/example/site-guard
source: https://github.com/example/site-guard
icon: icon.svg
categories: [security]
maintainers: [{name: Example Hosting, github: example}]
downloads:
  - url: https://github.com/example/site-guard/releases/download/v1.2.0/site-guard-1.2.0-linux-amd64.tar.gz
    sha256: 4c1d3f0a5e8b2c7d9f6a1b3e5c7d9f1a3b5c7e9d1f3a5b7c9e1d3f5a7b9c1e3d
    size: 4718592
  - url: https://github.com/example/site-guard/releases/download/v1.2.0/site-guard-1.2.0-linux-arm64.tar.gz
    sha256: 9e7c5a3f1d9b7e5c3a1f9d7b5e3c1a9f7d5b3e1c9a7f5d3b1e9c7a5f3d1b9e7c
    size: 4456448
health:
  - {type: unit, target: racksail-addon@site-guard.service}
tests:
  timeoutSeconds: 120
```

- The recipe's id, version and license are the manifest's.
- It has no inputs and creates no resources: installing the item installs the add-on itself, with the permissions the person installing it approves.
- Its health check is a `unit` check of the add-on's service, `racksail-addon@<id>.service`, and `tests` gives its install test the time it needs.
- `go run ./catalog/tool pin -write my-catalog/addons/site-guard` downloads each archive and corrects its checksum and size in the recipe, changing nothing else, so a new release only needs its URLs written; without `-write`, it reports the downloads that don't match.

[The recipe format](../../recipes/format/) has every field of a recipe, and [Testing recipes](../../recipes/testing/) the checks every item passes. Check the item before you publish it:

```sh
go run ./catalog/tool validate -dir my-catalog
```

It checks the manifest as the panel does, that it and the recipe name the same add-on, version and license, and the rest of what every item needs: maintainers, a license class, an icon that can't run anything, health checks and an install test.

### In the official catalog

The official catalog is [catalog/](https://github.com/vilanobeachflorida/racksail/tree/main/catalog) in Racksail's repository: propose an add-on in a pull request that adds its directory under `catalog/addons/`. Items there are licensed under MIT, and an item labelled open source only installs software under a license the Open Source Initiative approved. Official items are reviewed and tested on every release, and an item whose install test keeps failing is marked broken; see [Catalogs](../../recipes/catalogs/#maintenance-flags).

### In a catalog of your own

Anyone can publish a catalog, with the same layout, built and signed with the catalog tool and served from any web server; [Catalogs](../../recipes/catalogs/#catalogs-of-your-own) says how, and how admins add one to their panels.

## What panels check

An add-on that names the catalog and item it comes from is installed only when:

- its manifest is exactly the `addon.yaml` the catalog's signed index lists for the item, and
- its program is one of the downloads the item's recipe pins, with the same URL, checksum and size.

Otherwise the change is refused with `addons.not_in_catalog`, saying which of them doesn't match. Every add-on an account owner installs names its catalog, so accounts only ever run what a catalog signed. A server admin may also install an add-on from no catalog, giving its manifest and program themselves, as with any other resource:

```sh
racksail addon create --file site-guard.yaml --dry-run
```

where `site-guard.yaml` holds the request, with the add-on's settings under `spec`: its `id`, its `manifest`, the `package` with the archive's `url`, `sha256`, `size` and `format`, the `approved` permissions and whether it is `enabled`. The preview says what the panel will download and set up.

## New versions

A new version is a new manifest and a new archive, published in the catalog the same way. Moving an installed add-on to it changes the add-on's manifest and package: the panel checks them against the catalog again, asks for any permission the new version adds to be approved, and restarts the add-on with a new token. An add-on can't move to a program its catalog never pinned, though undoing an update brings back the one that ran before.

When the hook API changes, list every version the add-on speaks in `apiVersions`, so it keeps working on panels on either side of the change.
