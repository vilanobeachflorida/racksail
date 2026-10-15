// Package theme reads, checks, layers and renders themes.
//
// A theme is a package of design tokens (light and dark palettes,
// typography, radius, density, shadows, motion and the focus ring), asset
// files, logic-less templates and a console palette. It contains no code
// and loads nothing from the network. Parse checks a theme against
// spec/schemas/theme.schema.json, for WCAG AA contrast (Pairs) and for
// status colors that keep their meaning (Meanings); ReadPackage and
// ReadArchive also check the files a theme refers to.
//
// Themes layer: the default Racksail theme, then the server theme, then a
// reseller theme (Layers.Resolve), then personal preferences, which the
// browser applies as attributes on the root element (Preferences). CSS
// renders the default theme's stylesheet, with the blocks preferences
// switch on and the protected palette of safety-critical screens, which no
// theme can change; LayerCSS renders a server or reseller theme to apply on
// top. Components take every color from these tokens.
//
// The package also renders a theme's templates (RenderTemplate), checks an
// admin's custom CSS (CheckCustomCSS) and draws the mark and the console
// palette in terminals (Console).
package theme
