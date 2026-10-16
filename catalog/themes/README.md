# Themes

Themes for the panel, its emails and the pages visitors of hosted sites see: the default theme plus high-contrast and compact ones, and any others people contribute. A theme item holds the theme itself, `theme.yaml` with the fonts and images it refers to, next to a recipe that carries what every catalog item has: maintainers, license class, icon and categories. The recipe has no inputs or resources; the marketplace installs the theme package.

The [racksail](racksail/), [high-contrast](high-contrast/) and [compact](compact/) themes are built into Racksail. Their `theme.yaml` files aren't edited here: `make gen` writes them from `internal/theme`, and a test fails when they drift. Their recipes and icons are kept by hand, so a new version of a built-in theme needs its recipe's version changed too.

Rules for themes, besides the catalog's:

- `theme.yaml` passes [spec/schemas/theme.schema.json](../../spec/schemas/theme.schema.json) and the WCAG AA contrast checks, in light and dark mode.
- Its id, version and license are the recipe's.
- Every file it refers to is in the item's directory, which holds nothing else but the recipe, its icon and a README or license. Those files get the checks the panel gives an uploaded theme package. Themes never load anything from the network, contain no code and can't restyle destructive confirmations, change previews, security prompts, license labels or permission screens.
- Custom CSS is never distributed through a catalog.
- A `resource` check of the theme itself, `{type: resource, target: <id>}`, which is ready once a panel has installed the theme and checked its contrast, and an install test.

```
themes/high-contrast/
  recipe.yaml
  theme.yaml
  icon.svg
  logos/mark.svg
```
