---
title: "Applications"
description: "The brand in the panel, the installer, the console, the docs and the README."
---

The same few elements appear everywhere Racksail does: the mark, navy and blue, Inter and JetBrains Mono, and the voice. This page shows how they come together in each place, so a new screen or page fits in from the start.

## The panel

- **Browser tab:** the favicon is the mark. Its SVG version turns the navy white when the browser is in dark mode, and the 16-pixel version is redrawn on the pixel grid so it stays crisp.
- **Installed app:** the web app manifest names Racksail, uses navy for the theme and splash colors, and gives the app icon: the mark for dark backgrounds on a navy tile, with a maskable version for Android.
- **Top bar:** the mark, with the name beside it on wide screens, links home. It's drawn in theme colors, so it follows light, dark and high contrast.
- **Loading:** while the panel starts, a skeleton of the frame shows with the mark in the top bar, so the first thing people see is already Racksail.
- **Colors and type** come from theme tokens: the default theme is the brand palette, and every theme is checked for contrast. See [Colors](../colors/) and [Theme format](../../themes/format/).

### White-labeling

A server or reseller theme can bring its own logo, mark and favicon along with its colors, so a hosting company's customers see that company's brand: its logo replaces Racksail's mark and name in the top bar (with its mark on narrow screens), its favicon shows in the browser tab, and its name goes in the page title. The Racksail name stays where it identifies the software, such as the version on the status card. Protected colors on safety-critical screens stay the same in every theme.

## Emails and visitor pages

Notification emails, and the error, maintenance and suspension pages visitors of hosted sites see, come from templates in the theme. They take the theme's colors and logo, so they match the server's brand, or Racksail's by default. They follow the [voice](../voice/): a short title, what happened, what to do, and who to contact. See [templates](../../themes/format/#templates).

## The installer, the console and the CLI

Terminals use the theme's console palette:

| Role | Default |
|---|---|
| Background | `#01214F` navy |
| Text | `#E6EDF5` |
| Accent | `#3D9BFF` |
| Success | `#3FB950` |
| Warning | `#D29922` |
| Danger | `#F85149` |

- **Installer:** the welcome screen shows the large terminal mark, 32 columns by 13 lines, above the name and the tagline. Screens keep to the palette: navy behind, light text, blue for the selected option and for focus.
- **Console status screen and SSH login message:** the small mark, 16 columns by 8 lines, beside the panel address and service status.
- **CLI:** color marks status and emphasis only; output reads the same without it. `racksail theme mark` draws the mark in your terminal.

Terminals with fewer colors get the nearest ones: 24-bit color, then 256 colors, then the 16 colors of the Linux console. With `NO_COLOR` set, or a terminal that has no colors, there are none, and the mark is drawn in plain half blocks.

## Boot screens

The boot menu and the splash screen are navy with the mark for dark backgrounds in the middle and nothing else. Text on them uses `#E6EDF5`. Progress, if shown, is a thin bar in brand blue, not an animation.

## The docs site

The docs use Inter and JetBrains Mono, bundled with the site, and map navy and blue onto the docs theme's color scale. The header shows the mark in the coloring for the current mode, and the favicon is the panel's. Dark mode is the default; light mode uses the panel's light palette. Pages follow the [voice](../voice/): sentence-case headings, plain words, short paragraphs and examples.

## The README

The README opens with the stacked logo, 220 pixels wide and centered, and the tagline in bold under it. Screenshots of the panel use the default theme in light mode, at a common laptop size, with example data such as example.com and documentation IP addresses (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24).

## Anything else

For slides, stickers, social images or a page about Racksail:

- Use a logo file from the [`brand` folder](https://github.com/vilanobeachflorida/racksail/tree/main/brand); don't recreate it.
- Navy or white backgrounds work best. On navy, use the logo for dark backgrounds.
- Set text in Inter, in sentence case.
- Keep the tagline as it is: "Your server, your rules."
