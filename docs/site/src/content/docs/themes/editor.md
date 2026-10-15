---
title: "The theme editor"
description: "Edit a theme with a live preview on real screens and a contrast checker."
---

The theme editor is in **Settings → Appearance → Theme editor**. It changes every token of a theme with a preview on real components in light and dark mode, shows what the panel would refuse before you save anything, and exports the result as a `theme.yaml`.

## Start

The editor opens with the theme in effect: the default, or the server's or your reseller's theme. **Start from** switches to a built-in theme, and **Import** reads a `theme.yaml` or JSON file. **Start over** goes back to the theme in effect.

An imported file is checked the way the panel checks a theme when it's saved: a file that isn't valid YAML or JSON, is larger than 256 KiB, or sets protected colors is refused with the reason. A file that reads fine but has problems opens, with the problems listed.

## Edit

The tabs on the left hold every token:

- **Colors:** each color token for light mode and for dark mode, with a color picker and a hex field, and the chart colors, from 6 to 12. A field that isn't a color says so and keeps the last good value. Only the overlay takes an alpha.
- **Type:** the text and code fonts, the base size, the type scale and the four weights. A font has to be one the panel bundles, Inter Variable or JetBrains Mono Variable, or one the theme's package will bring: list its files, one path per line, and put them in the package.
- **Shape and motion:** density, corner radius, shadows, motion speeds and the focus ring.
- **Console:** the colors of the installer, the console status screen and the CLI, with a terminal drawn in them. A color left empty is Racksail's.
- **Details:** the name, ID, version, license, author and description.

## Preview

On the right, the same example screen shows in light and dark mode, built from the panel's own components: a heading, stat tiles, a chart, buttons, badges, form fields, a progress bar, an alert and a sample of the protected colors. Every change shows at once.

**Try on the whole panel** applies the theme to every screen, in your browser only, until you stop trying it or leave the editor. Nobody else sees it. It's only offered while every check passes, and trying stops by itself when a change brings a problem, so a theme can't make the panel unreadable, the button that stops it included.

## Check

Above the tabs, the editor lists everything the panel would refuse: fields that don't fit the format, contrasts below WCAG 2.2 AA, with the ratio and what's needed, and status colors that lost their meaning. Each problem names the colors and the mode, in words:

> Light mode: Text on Page surface has a contrast of 1.05:1 and needs 4.5:1. Make one of them lighter or darker.

The **contrast checker** below the preview lists every pair the panel checks, for each mode and for the console: the two colors, what the pair is used for, the ratio, the minimum and whether it passes. **Only failing checks** narrows the list to what needs fixing.

Exporting stays off until every check passes.

## Export

- **Export theme.yaml** saves the theme in the format theme packages use.
- **Export JSON** saves the same theme as JSON.

To add logos, fonts or templates, put the exported `theme.yaml` in a folder with those files, add their paths to it, and check the folder:

```sh
racksail theme validate ./acme-theme
```

Then zip the folder to install or share it. See [Theme format](../format/) for the files a package can hold and the rules they follow.
