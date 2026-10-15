// Package recipes reads recipes and catalogs.
//
// A recipe describes a catalog item (an app, a runtime, a service, a stack, a
// DNS template, an automation, a tuning preset, a model, a theme or an
// add-on) as inputs to ask for and resources to create from them. The recipe
// engine turns a recipe and its inputs into change set requests, so
// installing an app is previewed, applied, audited and undone like any other
// change. Recipes are YAML validated by spec/schemas/recipe.schema.json.
//
// A catalog is a signed index of recipes. The official catalog lives in
// catalog/ in the repository; third-party catalogs are added from a URL with
// the key that signs them.
package recipes
