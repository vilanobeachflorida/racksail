// Package theme validates, layers and renders themes.
//
// A theme is a package of design tokens (light and dark palettes,
// typography, radius, density, shadows and motion), asset files, logic-less
// templates and a console palette. It contains no code and loads nothing
// from the network. Every theme is checked against
// spec/schemas/theme.schema.json and for WCAG AA contrast before it is
// accepted.
//
// Themes layer: the default Racksail theme, then the server theme, then a
// reseller theme, then personal preferences. CSS renders a theme as CSS
// custom properties, which is how the UI applies it; no component uses a
// hard-coded color.
package theme
