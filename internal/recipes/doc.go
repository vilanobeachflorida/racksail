// Package recipes reads recipes, installs them, and fetches and checks the
// catalogs that offer them.
//
// A recipe describes a catalog item (an app, a runtime, a service, a stack,
// a DNS template, an automation, a tuning preset, a model, a theme or an
// add-on) as inputs to ask for and resources to create from them. Recipes
// are YAML validated by spec/schemas/recipe.schema.json.
//
// The Engine turns a recipe into change sets, so installing, upgrading and
// uninstalling are previewed, applied, audited and undone like any other
// change. An install creates the recipe's resources in dependency order and
// a core.installation, or core.server_installation, that records where the
// recipe came from, the recipe itself and what it created; its health
// checks confirm the install worked and run again every few minutes. An
// upgrade snapshots the data first when the recipe asks, and keeps the
// passwords given at install. Uninstalling keeps what holds data unless
// asked not to. Upstream files recipes pin are checked against their
// checksum and signature: minisign, OpenPGP by a vendor key the panel
// knows, or Sigstore.
//
// A catalog, core.catalog, is a signed index of recipes. Catalogs keeps
// their indexes, checked against the keys trusted for each, fetches the
// files they list, checked against the index, and refuses an index older
// than the one it has. The official catalog's keys are built in from
// catalog/keys; third-party catalogs are added from a URL with the key that
// signs them. Catalogs are only fetched while the marketplace module is on.
package recipes
