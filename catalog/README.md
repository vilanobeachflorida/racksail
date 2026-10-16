# Official catalog

This directory is for what the marketplace module offers: apps, runtimes, services, stacks, DNS templates, automations, tuning presets, assistant models, themes and add-ons. Unlike the rest of the repository, the items in it are licensed under the [MIT license](LICENSE). The tool in [tool/](tool/) is part of Racksail's code and licensed under AGPL-3.0-or-later like the rest of it.

Panels never read this directory directly. CI builds an index of it, signs the index and publishes it. A panel with the marketplace module on fetches the index daily, checks the signature and offers what it lists; with the module off, it never fetches anything.

## Layout

One directory per item, named by the item's id and grouped by kind:

| Directory | For |
|---|---|
| [apps/](apps/) | Apps, such as WordPress or Nextcloud |
| [runtimes/](runtimes/) | PHP, Node.js, Python and other runtimes |
| [services/](services/) | Database engines and other shared services |
| [stacks/](stacks/) | Combinations of apps, runtimes and services |
| [dns-templates/](dns-templates/) | Records for mail providers and other services |
| [automations/](automations/) | Ready-made workflows |
| [tuning-presets/](tuning-presets/) | Settings for popular apps |
| [models/](models/) | Local models for the assistant |
| [themes/](themes/) | Themes and their assets |
| [addons/](addons/) | Add-ons |

Every item's directory holds its `recipe.yaml`, its icon and any file the recipe refers to. A theme's directory also holds the theme itself, `theme.yaml` with its fonts and images, and an add-on's holds its manifest, `addon.yaml`. The README in each kind's directory says what its items create and the rules particular to them.

Next to the kind directories are [maintenance.yaml](maintenance.yaml), which marks items as featured, broken or unmaintained, and [keys/](keys/), which holds the public key the index is signed with.

## Rules

- Recipes are checked against [spec/schemas/recipe.schema.json](../spec/schemas/recipe.schema.json), themes against [theme.schema.json](../spec/schemas/theme.schema.json) and add-on manifests against [addon.schema.json](../spec/schemas/addon.schema.json), by the same code the panel uses to read them.
- An item's id is unique across the whole catalog and is the name of its directory. A theme's or add-on's id, version and license are the same in its recipe and in its package.
- Every item names its maintainers, at least one of them with a GitHub account, the license of the software it installs and its license class: open source, free to use or proprietary. An item labelled open source only installs software under licenses the Open Source Initiative approved. Nothing that isn't open source is ever installed by default.
- Every item has an icon, an SVG or PNG file in its directory. Icons can't contain scripts, event handlers, HTML elements or references to anything outside the file, in their style included: `url()` only to their own elements, and no `@import`, `image-set()` or other function that loads images. Style is checked as browsers read it, with comments gone and escapes decoded.
- Apps, services and stacks have a health check. Every item that creates resources declares an install test: `tests.timeoutSeconds`, and in `tests.inputs` a value for every input the recipe requires. The test installs the item with those inputs on a fresh server and runs its health checks. Recipes whose tests keep failing are marked broken, and unmaintained ones are eventually hidden.
- Downloads are pinned and verified against checksums or signatures. New upstream releases wait a few days before recipes offer them, except security fixes.
- Secrets a recipe asks for, such as an admin password, are inputs of type `password` or `secret`. They can only go into the secret fields of the resources the recipe creates, never into a plain field.
- An item has at most 64 files of at most 2 MiB each and 8 MiB in all. Symbolic links aren't allowed.

The full set of rules is in [docs/architecture/extensibility.md](../docs/architecture/extensibility.md#catalogs).

## Checking your changes

```sh
make catalog-check                   # check every item
go run ./catalog/tool validate       # the same, from the repository's root
go run ./catalog/tool pin -write catalog/apps/wordpress   # refresh checksums after changing a download
```

`make check` checks the catalog too, as part of the Go tests. The tool reports every problem it finds at once, with the file it's in. For the install test, it checks that `tests.inputs` gives every input the recipe requires a value the recipe accepts. Running the test itself, installing the item and checking its health, is the marketplace module's job: CI runs the install tests once that module installs recipes.

## Maintenance flags

[maintenance.yaml](maintenance.yaml) lists ids under `featured`, and items under `broken` and `unmaintained` with a reason and the day it started:

```yaml
featured: [wordpress, nextcloud]
broken:
  - {id: magento, reason: The install test fails since 2.4.9., since: 2026-11-02}
unmaintained:
  - {id: example-app, reason: Its maintainer stepped down., since: 2026-06-01}
```

The index carries the flags to every panel. An item unmaintained for more than 180 days is left out of the index until someone takes it on.

## Publishing

On every change to `main`, CI checks the catalog and publishes it with `make catalog-publish`:

- `index.json`: every item with its kind, version, license class, requirements and flags, and every file it has with the file's size and SHA-256 checksum
- `index.json.sig`: the Ed25519 signature of `index.json`, 64 bytes
- `catalog.tar.gz`: the index, its signature and every file it lists, at the paths it lists

The three files are attached to the `catalog` release on GitHub and pushed to the GitHub Container Registry as an OCI artifact. A panel fetches `index.json` and its signature, checks the signature with the official public key, and then fetches the archive or individual files, checking each against the index. Mirrors only need to serve the three files.

Build the index locally without signing it:

```sh
make catalog                         # writes bin/catalog/ and bin/catalog.tar.gz
```

## Signing keys

The index is signed with an Ed25519 key. Its public half is [keys/official.pub](keys/), which panels trust for the official catalog; its private half is the `CATALOG_SIGNING_KEY` secret of the repository and never leaves CI. The publishing job refuses to sign with a key that doesn't match `keys/official.pub`. How the key is created and rotated is in [keys/README.md](keys/README.md).

Third-party catalogs work the same way with their own key. The catalog tool builds, signs and verifies any catalog laid out like this one:

```sh
go run ./catalog/tool keygen -private catalog.key -public catalog.pub
go run ./catalog/tool index -dir my-catalog -name my-catalog -key catalog.key -out public/ -archive public/catalog.tar.gz
go run ./catalog/tool verify -dir public/ -public catalog.pub
```

An admin adds a third-party catalog by its URL and the contents of its `.pub` file.
