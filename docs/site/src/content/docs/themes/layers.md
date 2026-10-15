---
title: "Theme layers"
description: "How the default, server and reseller themes combine with personal preferences."
---

What someone sees is built up in layers. Each layer changes only what it sets and leaves the rest to the layers below it.

| Layer | Set by | Seen by |
|---|---|---|
| 1. The default theme | Racksail | Everyone, until a layer above changes something |
| 2. The server theme | An admin | Everyone on the server |
| 3. A reseller theme | A reseller | That reseller's customers |
| 4. Personal preferences | Each person | Only that person, in their browser and on their account |

## How themes combine

A server or reseller theme replaces the tokens and files it sets. A reseller that only changes the accent color and the logo keeps the server's fonts, radius, templates and everything else.

Two groups always go together:

- **A font.** When a layer names a font family, the font's files come from that layer too, or the font is one the panel bundles. A family never gets another layer's files.
- **The brand files:** the light and dark logos, the mark and the favicon. A layer with any of them replaces all of them, so a reseller's customers never see the server's logo, even in a mode the reseller didn't make a logo for. Without a dark logo, the light one is used in dark mode.

The combined theme is checked again for contrast, so layers that are each fine can't add up to unreadable text. The panel refuses a theme whose combination with the layers below fails.

## Personal preferences

Preferences sit on top of every theme and are set in **Settings → Appearance**. Light or dark can also be switched from the appearance menu in the top bar, and light, dark and high contrast from the command bar.

| Preference | Choices | What it does |
|---|---|---|
| Light or dark | Match the system, light, dark | Picks the theme's light or dark palette |
| Contrast | Match the system, standard, high | High contrast swaps in a palette of black, white and strong colors, darker borders, a 3-pixel focus ring and outlines instead of shadows, on top of any theme |
| Density | The theme's, comfortable, compact | Compact makes controls shorter and tables tighter |
| Text size | Small, default, large, larger, largest | Scales all text and the controls around it: 87.5%, 100%, 112.5%, 125% or 150% |
| Motion | Match the system, reduce, show all | Reduce turns off animations and transitions |

"Match the system" follows the operating system's settings for dark mode, more contrast and reduced motion, and changes when they do.

Preferences apply the moment they're chosen, without reloading, and are remembered in the browser so the next visit starts the same way, before anything else loads. A change in one tab reaches the others.

## What no layer can change

The protected palette of safety-critical screens, the destructive confirmations, warnings in the change preview, security prompts, license labels and permission screens, is above every layer. It follows only light or dark mode and the contrast preference.

## In CSS

The panel applies the layers as CSS custom properties in [cascade layers](https://developer.mozilla.org/docs/Web/CSS/@layer), in this order from lowest to highest:

1. `rs-theme`: the default theme, then the server and reseller themes as one combined theme
2. `rs-custom`: an admin's custom CSS, inside `@scope (:root) to ([data-critical])`, so its rules stop at safety-critical areas
3. `rs-preferences`: density, text size, high contrast and reduced motion, switched on by attributes on the page's root element
4. `rs-protected`: the protected palette

Because the order is fixed by the layers, custom CSS can't outrank a person's preferences or the protected palette, however specific its selectors are; that's also why it can't use `!important`, which turns the order of layers around. Components only use the custom properties, which start with `--rs-`, so everything follows the theme.

A layer sets every chart color slot, up to twelve: the ones it has no color for are cleared, so a theme with six chart colors never shows a seventh from the layer below that was never checked against its surfaces.
