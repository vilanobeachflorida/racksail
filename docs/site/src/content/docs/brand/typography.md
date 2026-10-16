---
title: "Typography"
description: "Inter for text and JetBrains Mono for code, and the type scale."
---

Racksail uses two typefaces, both open source under the SIL Open Font License 1.1 and both bundled with the panel and the docs, so nothing is loaded from a font service.

| Typeface | Used for |
|---|---|
| [Inter](https://rsms.me/inter/) | Everything you read: the panel, the docs, emails and the pages visitors see |
| [JetBrains Mono](https://www.jetbrains.com/lp/mono/) | Code, configuration, terminals, commands, paths, IDs, hashes and log lines |

Both are variable fonts, so every weight comes from one file.

The wordmark isn't set in either: it's drawn, from Poppins Bold, and only ever used as the logo. Don't set the name in Poppins, or any other font, to imitate it. In text, write Racksail in the font around it.

## Type scale

The scale starts from a 16-pixel base and grows by 1.125 per step. Line height tightens as text grows.

| Step | Size | Line height | Used for |
|---|---|---|---|
| `xs` | 0.79 rem, 12.6 px | 1.5 | Badges, timestamps, chart axes, keyboard keys, menu group labels |
| `sm` | 0.89 rem, 14.2 px | 1.5 | Most of the panel: table cells, form labels and help, menus, buttons |
| `base` | 1 rem, 16 px | 1.5 | Reading text, card titles, docs, emails |
| `lg` | 1.13 rem, 18 px | 1.44 | Dialog titles, section headings, the name in the top bar |
| `xl` | 1.27 rem, 20.3 px | 1.38 | Headings inside long pages |
| `2xl` | 1.42 rem, 22.8 px | 1.32 | Page titles and the number on a stat tile |
| `3xl` | 1.6 rem, 25.6 px | 1.26 | One headline on a screen with little else, such as a welcome |
| `4xl` | 1.8 rem, 28.8 px | 1.2 | The same, on large screens |

Themes can change the base size and the ratio; the steps follow. People who want larger or smaller text choose a text size in their appearance settings, from 87.5% to 150%, and everything scales with it, controls included.

## Weights

| Weight | Value | Used for |
|---|---|---|
| Regular | 400 | Text |
| Medium | 500 | Labels, buttons, table headings, the current item in navigation |
| Semibold | 600 | Headings and titles |
| Bold | 700 | Sparingly: a number that is the point of a card |

Emphasis comes from weight, not from italics, color or capitals.

## Setting text

- Use sentence case everywhere: headings, buttons, menu items, tab names. Not Title Case, not ALL CAPS.
- Keep reading text between 60 and 80 characters a line.
- Use tabular figures for numbers that line up or change in place, such as table columns, counters and stats.
- Put code, commands, paths and values someone might copy in the monospace font: `racksail theme validate`, `/srv/racksail`, `site_01`.
- Left-align text (right-align in right-to-left languages). Center only short headings in empty states and dialogs that stand alone.
- Don't shrink text below the `xs` step to fit something in. Shorten it, wrap it, or truncate it and show the whole text on hover and focus.

## Languages

Inter covers Latin, Greek and Cyrillic scripts. For other scripts the panel falls back to the system's fonts, which is why sizes are set in rem and line heights leave room for taller glyphs. Text grows by a third or more in some languages, so layouts never depend on a word's width.
