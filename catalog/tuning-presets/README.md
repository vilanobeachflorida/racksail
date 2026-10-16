# Tuning presets

Measured settings for popular apps: PHP workers, OPcache, database buffers and caches for WordPress, WooCommerce, Laravel or Magento. A preset's recipe takes the site it tunes as a `resource` input and creates a `performance.preset` resource for it; the performance module applies it as a change set with a before-and-after comparison.

Rules for tuning presets, besides the catalog's:

- Every value has a reason in `description`, and the measurements behind it are linked from `homepage` or the pull request that added it.
- Values stay within the account's plan limits; the performance module never raises a limit to apply a preset.
- A `resource` check of the preset it creates, and an install test with values for every required input.
- `requires.modules` includes `performance`.

```
tuning-presets/wordpress/
  recipe.yaml
  icon.svg
```
