---
title: "Colors"
description: "The brand navy and blue, and how themes build on them."
---

Racksail has two brand colors and white. Everything else, from the panel's surfaces to its chart colors, is chosen to sit well beside them and is checked for contrast.

## Brand colors

| Color | Hex | RGB | What it's for |
|---|---|---|---|
| Navy | `#01214F` | 1, 33, 79 | The ink: the mark's mainsail and hull, the wordmark, the app icon's tile, the browser's theme color, the console's background |
| Blue | `#007CFD` | 0, 124, 253 | The sea: the mark's jib and wave, focus rings, highlights and the accent of dark screens |
| White | `#FFFFFF` | 255, 255, 255 | Backgrounds, and the ink of the logo on dark backgrounds |

Navy carries the weight; blue is the accent. A page that's mostly blue is off brand.

## Pairings that are easy to read

Contrast ratios follow WCAG 2.2. Body text needs 4.5:1; large text (24 px, or 18.7 px bold) and graphics such as icons and focus rings need 3:1.

| Foreground | Background | Ratio | Use it for |
|---|---|---|---|
| Navy | White | 15.8:1 | Any text |
| White | Navy | 15.8:1 | Any text |
| `#E6EDF5` | Navy | 13.4:1 | Text on navy that's easier on the eyes than pure white |
| `#0062CC` | White | 5.8:1 | Links and small blue text on light backgrounds |
| `#00142F` | Blue | 4.6:1 | Labels on blue buttons and badges |
| Blue | White | 4.0:1 | Large text, icons and focus rings only |
| White | Blue | 4.0:1 | Large text and icons only |
| Blue | Navy | 4.0:1 | Graphics and large text only, as in the logo for dark backgrounds |

Brand blue isn't dark enough for small text on white, so the panel's links and buttons use `#0062CC`, a deeper blue of the same hue. Navy on blue reaches only 4:1; don't set text that way.

## The panel's palette

The default theme builds on the brand colors, with a palette for light mode and one for dark mode. Each has the tokens below; [Theme format](../../themes/format/) lists them all.

| Token | Light | Dark | What it colors |
|---|---|---|---|
| `surface` | `#F7F9FC` | `#0A1626` | The page |
| `surfaceRaised` | `#FFFFFF` | `#0F2036` | Cards, menus and dialogs |
| `surfaceSunken` | `#EEF2F7` | `#06101C` | Wells, table headers and the terminal |
| `text` | `#0B1B33` | `#E6EDF5` | Text |
| `textMuted` | `#4A5A70` | `#9FB0C4` | Secondary text |
| `brand` | `#01214F` | `#007CFD` | Brand areas and the logo's ink |
| `accent` | `#0062CC` | `#3D9BFF` | Links, primary buttons and selection |
| `focus` | `#007CFD` | `#3D9BFF` | The focus ring |
| `success` | `#1A7F37` | `#3FB950` | Things that worked or are running |
| `warning` | `#9A6700` | `#D29922` | Things that need attention soon |
| `danger` | `#CF222E` | `#F85149` | Failures and destructive actions |
| `info` | `#0062CC` | `#3D9BFF` | Neutral notices |

Dark mode isn't the light palette inverted. Its surfaces are navy-tinted, its blues are lighter so they read on them, and its buttons put dark text on bright fills.

## Status colors

Success is green, warning is amber, danger is red and info is blue, in every theme: the panel refuses a theme whose status colors lose those hues. Status color never works alone. A status always has words, and usually an icon or a symbol too, so it reads for people who can't tell the colors apart and in high contrast.

## Chart colors

Charts use up to eight series colors, always in the same order, and the theme can have up to twelve. The first is brand blue.

| Order | Light | Dark |
|---|---|---|
| 1 | `#007CFD` | `#3390F7` |
| 2 | `#E2622C` | `#D95926` |
| 3 | `#139A6B` | `#199E70` |
| 4 | `#B88200` | `#C98500` |
| 5 | `#D9558B` | `#D55181` |
| 6 | `#008300` | `#1F9A1F` |
| 7 | `#4A3AA7` | `#9085E9` |
| 8 | `#E34948` | `#E66767` |

Each reaches 3:1 against cards in its mode, and neighbors in the order stay distinguishable with the common kinds of color blindness. A series keeps its color when others are filtered out, every chart with more than one series has a legend, and every chart can show its data as a table. A ninth series goes into "Other" rather than getting a new color.

## Protected colors

Destructive confirmations, warnings in the change preview, security prompts, license labels and permission screens use a protected palette that no theme can change. It follows only light or dark mode and high contrast, so a theme can't make deleting something look harmless.
