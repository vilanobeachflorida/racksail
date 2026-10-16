---
title: "Iconography"
description: "Lucide icons: sizes, color, labels and when to use them."
---

Racksail's icons come from [Lucide](https://lucide.dev), an open-source set under the ISC license, through `lucide-react` in the panel. Lucide's icons are drawn on a 24-pixel grid with 2-pixel round strokes, which sits well beside Inter and the mark's simple shapes. Every icon in the panel, the docs and add-ons that use the panel's components comes from it.

## When to use an icon

Icons help people find their way and recognize things they've seen before. They don't replace words.

- **Navigation:** every entry in the icon rail has one, with its label beside or under it.
- **Actions in toolbars and rows:** short, common actions such as copy, refresh, more options and close.
- **Status:** a check, a warning triangle or a cross next to the words that say the status.
- **Empty states and alerts:** one icon that sets the tone, never several.

Leave them out of headings, body text, form labels and menus where every item would need one. A list where only some items have icons looks unfinished; give all or none.

## Size and stroke

Icons follow the text beside them, in theme tokens, so they grow with the text-size preference.

| Size | Pixels at the default text size | Used for |
|---|---|---|
| `size-3` | 12 | Inside badges |
| `size-4` | 16 | Buttons, menus, table rows, alerts, form fields: the default |
| `size-5` | 20 | The icon rail, the top bar's buttons, the theme preview's rail |
| `size-6` | 24 | Empty states, inside a 48-pixel circle |

Keep Lucide's 2-pixel stroke at every size; don't thicken or thin it to make an icon stand out. Don't fill outline icons, add shadows or recolor parts of an icon.

## Color

Icons take `currentColor`, so they use the color of the text around them, which comes from the theme. Status icons take the status token they go with: `text-success`, `text-warning`, `text-danger` or `text-info`. Like status colors themselves, they never carry a meaning alone. Icons in safety-critical areas take the protected palette, as everything there does.

## Labels

- **Icon and words:** the icon is decoration. Mark it `aria-hidden="true"` so screen readers only read the words.
- **Icon-only buttons** have an accessible name in the person's language (`aria-label`) and a tooltip with the same words, so people who can see it learn what it does too. Use them only for actions people already know from everywhere else: close, more, copy, search, the assistant.
- **Status icons** sit next to their words, or carry them as screen-reader text when space is tight, as a status dot does.

```tsx
<Button variant="ghost" size="icon" aria-label={intl.formatMessage(m.refresh)}>
  <RefreshCw aria-hidden="true" />
</Button>
```

## Direction

Icons that point mirror in right-to-left languages, with `rtl:-scale-x-100`: chevrons, arrows, the trend arrow on a stat tile, the assistant panel's icon. Icons that show real things don't: a clock, a globe, a padlock, a server.

## Icons that aren't in Lucide

The Racksail mark is a logo, not an icon: it never stands in for an action. When something needs an icon Lucide doesn't have, look for an existing one that reads well first. If there's none, draw it on Lucide's grid: 24 by 24 pixels, a 2-pixel stroke with round caps and joins, no fills, and the same corner radius as its neighbors, so it can't be told apart from the set.
