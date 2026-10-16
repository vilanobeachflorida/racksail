---
title: "How recipes work"
description: "Recipes install apps, runtimes, services and stacks as change sets you preview first, record where they came from and keep checking that they work."
---

Everything the [marketplace](../../modules/marketplace/) offers is described by a recipe: an app such as WordPress, a runtime, a database engine, a stack of several, a set of DNS records, an automation, a tuning preset, a model for the assistant, a theme or an add-on. A recipe says what to ask for and which resources to create from the answers. It never runs a script: everything it does goes through the change pipeline, like a change you make by hand.

## Installing

Installing an item asks for the recipe's inputs, such as a domain and an admin password, and shows the preview of one change set: every resource the recipe creates, in the order it creates them, with what each downloads, installs, starts and opens. Applying it creates them all, or none.

Before the preview, the panel checks that the item can be installed here:

- The modules it needs are on; the error says which to turn on.
- The server has the memory, the architecture and the hardware features it needs, such as hardware virtualization.
- Its catalog doesn't mark it broken.
- Software that isn't open source needs its license accepted first. The panel shows its license class, free to use or proprietary, and its license, and installs nothing that isn't open source unless you accept it.

Passwords and other secrets you give go straight into the secret store, never into a resource's settings, the installation's record or the audit log.

## What the panel keeps

Every install also creates an installation, `core.installation` for what goes into an account and `core.server_installation` for what belongs to the server, such as a runtime or a model. It records:

- where the recipe came from: the catalog, whether it is Racksail's own, and the SHA-256 of the recipe and of the catalog index that offered it, or the module that ships it
- the recipe itself, so upgrading and uninstalling need nothing from the catalog
- the inputs given, without passwords and secrets
- the resources it created, which also carry the `racksail.dev/installation` label

## Health checks

A recipe's health checks confirm that what it installed works: an `http` check of the app's address, a `unit` check of its service, or a `resource` check that waits for what the recipe created to be ready. They run when the install is applied, where a check that fails fails the change set so it can be undone, and then every five minutes. The installation's status says how each went.

## Downloads

Files a recipe downloads from upstream, such as an app's release archive, are pinned by checksum and size, and many by the upstream's signature too. The panel checks them when the installation is applied, and refuses a file that changed since the recipe was written. See [the recipe format](../format/#downloads).

## Upgrading

While the marketplace is on, the panel looks in each installation's catalog for a newer version, and the installation's status names it when there is one. An upgrade is one change set too, in the way the recipe says:

| Strategy | What happens |
|---|---|
| `in-place` | The resources are updated to the new version |
| `snapshot-then-replace` | The data is snapshotted first, then the resources are updated |
| `manual` | The panel doesn't upgrade it; the recipe's notes say how |

Resources both versions have are updated, ones the new version adds are created, and ones it dropped are removed unless they hold data. Passwords given at install are kept; you only give values for inputs the new version adds.

## Uninstalling

Uninstalling removes what the installation created, in reverse order. What holds data, such as databases and site files, is kept unless you choose otherwise, or the recipe says its data isn't worth keeping. A kept resource stays where it is, with its data and the `racksail.dev/kept-from` label, and can be used again or removed later. Removing one takes a snapshot of its data first, as every change that deletes data does.

Like every change set, an install, an upgrade and an uninstall can be undone.

## From the API

```sh
api=https://panel.example.com:8443/api/v1
auth="Authorization: Bearer $RACKSAIL_TOKEN"
curl -H "$auth" "$api/catalog/items?q=wordpress"            # what the catalogs offer
curl -H "$auth" -X POST "$api/catalog/install?dryRun=true" -d '{
  "catalog": "cat_...", "item": "wordpress",
  "scope": {"level": "account", "id": "acct_..."},
  "inputs": {"domain": "blog.example.com", "adminPassword": "..."}
}'                                                           # preview the install
curl -H "$auth" -X POST "$api/catalog/installations/inst_.../upgrade?dryRun=true" -d '{}'
curl -H "$auth" -X POST "$api/catalog/installations/inst_.../uninstall?dryRun=true" -d '{"keepData": true}'
```

Every call answers with its change set; with `dryRun=true` it is the preview, which `POST /api/v1/changesets/{id}/apply` applies as it was shown.
