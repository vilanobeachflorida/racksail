---
title: "Testing recipes"
description: "Check a recipe with the catalog tool, try it on the simulated server with racksail dev, then on a real one."
---

A recipe is tested in three steps: the catalog tool checks it as the panel will read it, the simulated server shows what installing it does, and a real server shows that what it installs works.

## Check it

From a checkout of the repository:

```sh
go run ./catalog/tool validate -dir my-catalog
```

The tool checks every item against the schemas and the catalog's rules, with the same code the panel reads recipes with, and reports every problem it finds at once, with the file it's in:

- the recipe's fields, inputs, references and dependencies, and that password and secret inputs only go into secret fields
- the icon: an SVG or PNG file without scripts, event handlers or references to anything outside it
- the item's files: at most 64, of at most 2 MiB each and 8 MiB in all, without symbolic links
- at least one health check, and an install test that gives every required input a value the recipe accepts
- for a theme or an add-on, that its package and its recipe agree on its id, version and license

For the official catalog, `make catalog-check` does the same, and `make check` runs it with the Go tests.

When the recipe downloads files, let the tool fill in their checksums and sizes rather than copying them by hand:

```sh
go run ./catalog/tool pin -write my-catalog/apps/notes
```

It downloads each file the recipe lists, and corrects the recipe where the checksum or size differ. Without `-write` it only reports. Check the signature of what it downloaded yourself the first time, the way upstream says to.

## Try it on the simulated server

[`racksail dev`](../../getting-started/try-without-a-server/) runs the panel against a simulated server on your own computer, which is the quickest way to see the preview of an install, the resources it creates and what each one does to the server.

1. Make a signing key for your catalog, if you have none yet, and build the catalog:

   ```sh
   go run ./catalog/tool keygen -private dev.key -public dev.pub
   go run ./catalog/tool index -dir my-catalog -name mine -key dev.key -out public/
   python3 -m http.server 8000 -d public
   ```

2. Run `bin/racksail dev`, turn on the marketplace module and the modules the recipe needs in **Settings → Modules**, and add the catalog with your public key:

   ```sh
   curl -X POST "http://127.0.0.1:7070/api/v1/core/catalogs" -d '{
     "spec": {"name": "mine", "title": "My recipes", "url": "http://127.0.0.1:8000/", "keys": ["'"$(tail -n 1 dev.pub)"'"]}
   }'
   ```

3. Preview the install, then apply it:

   ```sh
   curl -X POST "http://127.0.0.1:7070/api/v1/catalog/install?dryRun=true" -d '{
     "catalog": "cat_...", "item": "notes", "scope": {"level": "server"}, "inputs": {"domain": "notes.example.test"}
   }'
   ```

After changing the recipe, build the index again and fetch the catalog again with its **Fetch now** action. Each build is newer than the last, so the panel takes it.

The simulated server installs packages, writes files and starts services the way a real one would, so `resource` and `unit` checks pass or fail as they would on a server. Checks that go over the network, such as an `http` check of the app's address, need the app to run for real.

## Try it on a real server

Install Racksail on a test server or a virtual machine, such as one from the [cloud images](../../install/cloud-images/), add your catalog the same way, with an `https` URL, and install the item with the values of its install test. The install's health checks run as part of applying it, so a change set that applies is an install that works; the installation's status then shows them again every five minutes.

## The install test

`tests` in the recipe is its install test:

```yaml
tests:
  inputs: {domain: notes.example.test, adminPassword: correct-horse-battery}
  timeoutSeconds: 300
```

The catalog tool checks that every input the recipe requires has a value there that the recipe accepts, and that `timeoutSeconds` is set. To run the test yourself, install the item with those inputs on a fresh server, as in [Try it on a real server](#try-it-on-a-real-server): it passes when the change set applies, since the install ends with its health checks and gives each of them its whole timeout to pass.

Every official recipe has an install test. When an item's install doesn't work, its maintainers list it under `broken` in the catalog's `maintenance.yaml`, with why and since when; panels show it as broken and don't install it until it's fixed.
