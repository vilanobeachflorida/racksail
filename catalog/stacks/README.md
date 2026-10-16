# Stacks

Combinations installed together: an app with the runtime, database and cache it works best with, such as a WooCommerce shop with an object cache, or a Laravel starter with its queue worker. A stack's recipe lists every resource it creates, ordered with `dependsOn`, so the whole stack is one change set you preview, apply and undo together.

Rules for stacks, besides the catalog's:

- Health checks for each part someone would notice failing, such as the site's address and the queue worker's unit.
- An install test with values for every required input.
- The license is the expression of everything the stack installs, such as `GPL-2.0-or-later AND BSD-3-Clause`.

```
stacks/woocommerce/
  recipe.yaml
  icon.svg
```
