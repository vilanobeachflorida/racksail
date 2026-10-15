# Official catalog

This directory is for what the marketplace module offers: apps, runtimes, services, stacks, DNS templates, automations, tuning presets, assistant models, themes and add-ons. Unlike the rest of the repository, everything in it is licensed under the [MIT license](LICENSE).

Panels never read this directory directly. CI builds an index of it, signs the index and publishes it as a release asset. A panel with the marketplace module on fetches the index daily, checks the signature and offers what it lists; with the module off, it never fetches anything.

## Layout

One directory per item, grouped by kind:

| Directory | For |
|---|---|
| `apps/<id>/` | Apps, such as WordPress or Nextcloud |
| `runtimes/<id>/` | PHP, Node.js, Python and other runtimes |
| `services/<id>/` | Database engines and other shared services |
| `stacks/<id>/` | Combinations of apps, runtimes and services |
| `dns-templates/<id>/` | Records for mail providers and other services |
| `automations/<id>/` | Ready-made workflows |
| `tuning-presets/<id>/` | Settings for popular apps |
| `models/<id>/` | Local models for the assistant |
| `themes/<id>/` | Themes and their assets |
| `addons/<id>/` | Add-ons |

An item's directory holds its `recipe.yaml` (a theme's holds `theme.yaml`, an add-on's `addon.yaml`), its icon, and any file the recipe refers to.

## Rules

- Recipes are checked against [spec/schemas/recipe.schema.json](../spec/schemas/recipe.schema.json), themes against [theme.schema.json](../spec/schemas/theme.schema.json) and add-on manifests against [addon.schema.json](../spec/schemas/addon.schema.json).
- Every item names its maintainers, the license of the software it installs and its license class: open source, free to use or proprietary. Nothing that isn't open source is ever installed by default.
- Every recipe has an install test that runs in CI and a health check. Recipes whose tests keep failing are marked broken, and unmaintained ones are eventually hidden.
- Downloads are pinned and verified against checksums or signatures. New upstream releases wait a few days before recipes offer them, except security fixes.
- Secrets a recipe asks for, such as an admin password, are inputs of type `password` or `secret`. They can only go into the secret fields of the resources the recipe creates, never into a plain field.

The full set of rules is in [docs/architecture/extensibility.md](../docs/architecture/extensibility.md#catalogs).
