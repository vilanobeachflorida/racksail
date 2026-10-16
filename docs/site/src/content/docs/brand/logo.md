---
title: "Logo"
description: "How to use the Racksail logo, mark and wordmark."
---

The Racksail logo is a sailboat and the name beside it. The boat is the mark: a mainsail and a hull in navy, a jib and a wave in blue. The name is the wordmark, drawn from Poppins Bold with its own spacing. All the files are in the [`brand` folder](https://github.com/vilanobeachflorida/racksail/tree/main/brand) of the repository, generated from one set of artwork so every copy matches.

## Lockups

| Lockup | Files | Use it |
|---|---|---|
| Horizontal | `racksail-logo-horizontal*.svg` | By default: headers, title slides, the top of a page. The wordmark's baseline sits on the hull's waterline. |
| Stacked | `racksail-logo-stacked*.svg`, `racksail-logo.png` | In square or narrow spaces: splash screens, about dialogs, the README, avatars that show the name. |
| Mark | `racksail-mark*.svg` | Where the name is already shown or there's no room for it: favicons, app icons, the panel's top bar, the console. |
| Wordmark | `racksail-wordmark*.svg` | Rarely: where a picture would compete with something else, such as a list of project names. |

Don't build your own lockup by placing the mark and the name together; use one of these.

## Colors

Each lockup comes in four colorings. Pick the one that has the most contrast with what's behind it.

| Coloring | Suffix | Background |
|---|---|---|
| Full color | none | White and light backgrounds |
| Full color for dark backgrounds | `-dark` | Navy, black and other dark backgrounds. The ink turns white and the sea stays blue. |
| One color, navy | `-mono` | One-color printing, engraving, embroidery and stamps |
| One color, white | `-white` | Photos, brand blue and other colored backgrounds |

The full-color logo needs a plain background. On a photo or a busy pattern, use the white logo over a dark part of the image, or put the logo on a plain panel.

## Clear space

Keep a margin around the logo at least a quarter of the mark's width on every side, in every lockup. Nothing else goes inside it: no text, other logos, edges of the page or of a card.

## Minimum size

| Lockup | On screen | In print |
|---|---|---|
| Horizontal | 120 px wide | 30 mm wide |
| Stacked | 64 px wide | 18 mm wide |
| Mark | 16 px wide | 6 mm wide |

Below 24 pixels, use the mark redrawn on the 16-pixel grid, which the favicon and the panel's small mark use: its shapes are simplified so they stay crisp on screens.

## Don't

- Change the colors, or use colors other than the four colorings.
- Stretch, squash, rotate, slant or flip the logo.
- Add shadows, outlines, glows, gradients or textures.
- Rearrange the parts of the mark, or move the wordmark.
- Retype the name in a font to stand in for the wordmark.
- Put the navy logo on a dark background, or the full-color logo on brand blue.
- Put the logo inside a sentence, or use the mark as a letter.
- Use the logo in a way that suggests Racksail endorses or makes something it doesn't.

## In the panel

The panel draws the logo from the same artwork with theme colors instead of fixed ones: the ink takes the theme's brand color in light mode and its text color in dark mode, so the mark reads in every theme and in high contrast. A server or reseller theme can show its own logo, mark and favicon instead; see [Theme format](../../themes/format/).
