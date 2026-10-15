// Package builder builds runtimes: recipes for PHP with chosen extensions and
// for Node, Python and Ruby, era-matched build roots from snapshot.debian.org
// for old versions, relocatable bundles in a content-addressed cache, prebuilt
// popular recipes from the project's registry, and atomic switches with
// rollback when a site's extensions change. It turns itself on when a recipe
// needs a custom build.
//
// Its screens live in ui/src/modules/builder and its documentation in
// docs/site/src/content/docs/modules/builder.md.
package builder
