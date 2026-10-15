---
title: "Themes"
description: "Change colors, fonts, logos and templates without touching code."
---

A theme changes how Racksail looks: the colors of the panel in light and dark mode, its fonts, corners, shadows and spacing, the logos and favicon, the emails it sends and the pages visitors see when a site is down, and the colors of the installer and console. A theme is a package of settings and files. It contains no code and loads nothing from the internet.

## Built-in themes

| Theme | ID | What it's for |
|---|---|---|
| Racksail | `racksail` | The default: navy and blue on calm surfaces |
| High contrast | `high-contrast` | Black, white and strong colors with a thicker focus ring, for low vision and bright rooms |
| Compact | `compact` | The default colors with shorter controls and tighter tables, so more fits on the screen |

`racksail theme list` lists them. Anyone can also turn on high contrast or compact density for themselves, on top of any theme, in **Settings → Appearance**.

## Who sets a theme

Themes come in layers, each on top of the one before:

1. **The default theme**, the Racksail brand.
2. **The server theme**, set by an admin for everyone on the server.
3. **A reseller theme**, set by a reseller for their customers.
4. **Personal preferences**, set by each person for themselves: light or dark, high contrast, density, text size and reduced motion.

[Theme layers](../layers/) explains how they combine.

## What every theme is checked for

The panel checks a theme when it's imported, saved or installed, and refuses one that fails:

- **The format.** Every field matches the [theme schema](../format/).
- **Contrast.** Text, links, buttons, status colors, borders of controls, the focus ring and chart colors reach WCAG 2.2 AA against the surfaces they're used on, in light and dark mode. Combining layers is checked too, so a reseller's theme can't make the server's text unreadable.
- **Meaning.** Success stays green, warning amber or orange, danger red and info blue.
- **Files.** Every file the theme refers to is in the package, is the type it claims to be, and loads nothing else: no scripts in images, no remote fonts, no forms or trackers in templates.

Some screens can't be themed at all. Destructive confirmations, warnings in the change preview, security prompts, license labels and permission screens use a protected palette that follows only light or dark mode and high contrast, so no theme can make deleting something look harmless or hide a warning.

## Make a theme

The quickest way is the [theme editor](../editor/) in **Settings → Appearance → Theme editor**: start from a built-in theme, change colors with a live preview, fix what the contrast checker finds and export a `theme.yaml`.

You can also start from the command line:

```sh
racksail theme export racksail > theme.yaml
# edit theme.yaml
racksail theme validate theme.yaml
```

To add logos, fonts or templates, put `theme.yaml` in a folder with the files it names, check the folder with `racksail theme validate ./my-theme`, and zip it. See [Theme format](../format/).

## Custom CSS

An admin can add CSS of their own to restyle the panel on their own server. It's off by default and shows a warning when turned on, because it isn't checked for contrast the way themes are. Custom CSS can't load anything from the network, can't change the protected palette or restyle safety-critical screens, and can't be shared in a catalog: it stays on the server it was written for. It sits above themes and below personal preferences, so high contrast and larger text still work.

## Themes in catalogs

Themes are one of the kinds of items catalogs distribute, alongside recipes and add-ons. A theme from a catalog goes through the same checks as one you import.
