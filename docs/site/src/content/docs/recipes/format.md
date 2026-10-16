---
title: "Recipe format"
description: "The fields of a recipe: what it installs, the inputs it asks for, the resources it creates, its downloads, health checks, upgrades and install test."
---

A recipe is a YAML file, `recipe.yaml`, in the item's directory of a [catalog](../catalogs/), next to its icon and any file it refers to. [`spec/schemas/recipe.schema.json`](https://github.com/vilanobeachflorida/racksail/blob/main/spec/schemas/recipe.schema.json) is its schema, and the panel and the catalog tool read recipes with the same code.

```yaml
format: 1
kind: app
id: wordpress
name: WordPress
version: "6.8.3"
summary: The blogging platform and content management system.
license: GPL-2.0-or-later
licenseClass: open-source
homepage: https://wordpress.org/
icon: icon.svg
categories: [blog, cms]
maintainers: [{name: Racksail maintainers, github: vilanobeachflorida}]
requires: {modules: [web, databases], memoryMB: 256, diskMB: 100}
inputs:
  - {name: domain, type: domain, label: Domain, required: true}
  - {name: adminPassword, type: password, label: Admin password, required: true}
resources:
  - name: db
    kind: databases.database
    spec: {name: wordpress}
  - name: site
    kind: web.site
    spec: {domains: ["${inputs.domain}"], database: "${resources.db.id}", admin: {password: "${inputs.adminPassword}"}}
    dependsOn: [db]
health:
  - {type: http, target: "https://${inputs.domain}/wp-login.php", expect: "200"}
upgrade: {strategy: snapshot-then-replace}
uninstall: {keepData: true}
tests:
  inputs: {domain: wordpress.example.test, adminPassword: correct-horse-battery}
  timeoutSeconds: 600
```

## About the item

| Field | |
|---|---|
| `format` | `1` |
| `kind` | `app`, `runtime`, `service`, `stack`, `dns-template`, `automation`, `tuning-preset`, `model`, `theme` or `addon` |
| `id` | Unique in its catalog, and the name of the item's directory |
| `name`, `summary`, `description` | What the marketplace shows |
| `version` | The version of the software the recipe installs |
| `revision` | The revision of the recipe itself, for changes that keep the version |
| `license` | The software's license, as an SPDX expression |
| `licenseClass` | `open-source`, `free-to-use` or `proprietary`. Only software under licenses the Open Source Initiative approved is `open-source`; anything else needs its license accepted before it is installed |
| `homepage`, `source` | Where the software and its source code are |
| `icon` | An SVG or PNG file in the item's directory; never a URL |
| `categories` | Words the marketplace groups and filters by |
| `maintainers` | Who looks after the recipe: a `name`, and a `github` account for at least one of them |
| `model` | For assistant models: family, parameters, quantization, format, context length, size class and capabilities |

## Requirements

`requires` lists what the server must have before the item can be installed:

- `modules`: modules that must be on, such as `web` and `databases`
- `memoryMB` and `diskMB`: memory the server must have, and disk the item takes
- `arch`: the architectures it runs on, `amd64` and `arm64`
- `features`: hardware it needs, such as `kvm`, `iommu`, `tpm`, `virtualization` or a processor flag like `avx2`

## Inputs

Inputs are what installing asks for:

| Field | |
|---|---|
| `name` | How resources refer to it: `${inputs.<name>}` |
| `type` | `string`, `text`, `integer`, `boolean`, `select`, `domain`, `email`, `password`, `secret` or `resource` |
| `label`, `description` | What the form shows |
| `required`, `default` | Whether a value must be given, and the one used otherwise |
| `options` | For `select`: a `value` and a `label` each |
| `kind` | For `resource`: the kind to pick a resource of, such as `databases.server` |
| `pattern`, `min`, `max` | Limits on the value |

`password` and `secret` inputs can only go into the secret fields of the resources the recipe creates, where they are kept in the secret store. A recipe that puts one anywhere else is refused, so a password never ends up in plain settings.

## Resources

Each resource has a `name` the recipe refers to it by, a `kind`, a `spec` and optionally `labels` and `dependsOn`. Resources are created in one change set, after what they depend on, so a site can name the database created for it.

Strings in a spec may use `${inputs.<name>}` and `${resources.<name>.id}`. A string that is exactly one input reference takes the input's value with its type, so `port: "${inputs.port}"` gives a number. Health check targets may use input references too.

## Downloads

`downloads` pins the upstream files the recipe uses by `url` (https only), `sha256` and `size`. The panel checks every one before anything of an install or an upgrade is applied, and refuses a file that changed, leaving the server as it was. Where upstream signs its releases, add the signature:

```yaml
downloads:
  - url: https://example.org/tool-1.4.0.tar.gz
    sha256: 2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae
    size: 4813210
    signature: {type: minisign, url: https://example.org/tool-1.4.0.tar.gz.minisig, key: RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3}
```

| `type` | `key` |
|---|---|
| `minisign` | The upstream's minisign public key. Prehashed signatures are checked too |
| `gpg` | The name of a vendor's OpenPGP key the panel has, which a module adds. Recipes name keys and never carry them |
| `sigstore` | The signer's identity, then after a space the OIDC issuer; GitHub Actions when there is none. The Sigstore bundle at `url` is checked against Sigstore's trusted root as Racksail pins it |

New upstream releases wait a few days before an official recipe offers them, unless they fix a security problem. `go run ./catalog/tool pin -write <item>` downloads what a recipe lists and fills in the checksums and sizes.

## Health checks

`health` says how the panel tells that what it installed works. Official recipes have at least one check.

| `type` | `target` | `expect` |
|---|---|---|
| `http` | A URL | The status code; anything below 400 when empty |
| `tcp` | `host:port` | |
| `tls` | `host:port` | The name the certificate must be valid for; the host when empty |
| `dns` | A name | An address it must resolve to |
| `unit` | A systemd unit | Its state; `active` when empty |
| `resource` | The name of one of the recipe's resources, or the recipe's id for all of them | |

A `resource` check passes once the resource is ready: its module applied it and its own checks pass. Items that run nothing of their own, such as DNS templates, automations, tuning presets, models and themes, use resource checks. `timeoutSeconds`, up to 600, is how long a check may take to pass.

## Upgrades and uninstalling

`upgrade.strategy` is `in-place`, `snapshot-then-replace` or `manual`, with `upgrade.notes` saying what to do for a manual upgrade. `uninstall.keepData` says whether uninstalling keeps what holds data unless the person uninstalling chooses otherwise; it does when the recipe doesn't say. See [How recipes work](../overview/#upgrading).

## Install test

`tests` describes the item's install test: `inputs` gives a value for every input the recipe requires, and `timeoutSeconds` how long installing and checking it may take. The test installs the item with those inputs on a fresh server and runs its health checks. Every official recipe has one; see [Testing recipes](../testing/).
