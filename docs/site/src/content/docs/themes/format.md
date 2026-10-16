---
title: "Theme format"
description: "The tokens, assets and templates in a theme package."
---

A theme is a `theme.yaml` file, alone or in a package with the files it refers to. The panel reads YAML and JSON alike. The full schema is [`spec/schemas/theme.schema.json`](https://github.com/vilanobeachflorida/racksail/blob/main/spec/schemas/theme.schema.json); editors that understand JSON Schema can check a theme as you type.

## A package

```text
acme-theme/
├── theme.yaml
├── LICENSE
├── README.md
├── logo-light.svg
├── logo-dark.svg
├── mark.svg
├── favicon.svg
├── fonts/
│   └── acme-sans.woff2
└── templates/
    ├── email.html
    └── error.html
```

- `theme.yaml` is at the top of the package, or in a single folder that holds everything.
- Every other file is one the theme names, except a `LICENSE` and a `README` at the top. Files the theme doesn't use are refused, so nothing rides along unnoticed.
- No links, devices or other special files.
- At most 200 files, 2 MiB per file and 10 MiB in all. `theme.yaml` itself is at most 256 KiB.
- Upload the folder as a zip archive.

## theme.yaml

```yaml
format: 1
id: acme
name: Acme
version: 1.0.0
license: CC-BY-4.0
author: Acme Hosting
description: Acme's colors and logo.
tokens:
  light:
    surface: "#F7F9FC"
    surfaceRaised: "#FFFFFF"
    surfaceSunken: "#EEF2F7"
    text: "#0B1B33"
    textMuted: "#4A5A70"
    border: "#D8E0EA"
    borderStrong: "#7A889C"
    brand: "#5B21B6"
    brandText: "#FFFFFF"
    accent: "#5B21B6"
    accentText: "#FFFFFF"
    success: "#1A7F37"
    successText: "#FFFFFF"
    warning: "#9A6700"
    warningText: "#FFFFFF"
    danger: "#CF222E"
    dangerText: "#FFFFFF"
    info: "#0062CC"
    infoText: "#FFFFFF"
    focus: "#7C3AED"
    overlay: "#1E0B4766"
    chart: ["#5B21B6", "#E2622C", "#139A6B", "#B88200", "#D9558B", "#008300"]
  dark:
    # The same tokens, for dark mode.
  typography:
    sans: { family: Acme Sans, files: [fonts/acme-sans.woff2] }
    mono: { family: JetBrains Mono Variable }
  radius: { sm: 4px, md: 6px, lg: 10px, xl: 16px }
  density: comfortable
assets:
  logoLight: logo-light.svg
  logoDark: logo-dark.svg
  mark: mark.svg
  favicon: favicon.svg
templates:
  email: templates/email.html
  error: templates/error.html
console:
  background: "#1E0B47"
  foreground: "#EDE9FE"
  accent: "#A78BFA"
  success: "#3FB950"
  warning: "#D29922"
  danger: "#F85149"
```

| Field | Required | What it is |
|---|---|---|
| `format` | yes | Always `1` |
| `id` | yes | Lowercase letters, digits and hyphens, 2 to 63 characters, starting with a letter or digit |
| `name` | yes | What people see, up to 60 characters |
| `version` | yes | A semantic version, such as `1.2.0` |
| `license` | yes | An SPDX license expression, such as `MIT` or `CC-BY-4.0` |
| `author`, `description` | no | Shown where the theme is listed |
| `tokens` | yes | Colors, type and shape; see below |
| `assets` | no | Logos, favicon and images |
| `templates` | no | Emails and visitor pages |
| `console` | no | Terminal colors |

Exporting a built-in theme gives a complete file to start from: `racksail theme export racksail`.

## Colors

`tokens.light` and `tokens.dark` each have every one of these, as `#RRGGBB`. Only `overlay` may have an alpha, as `#RRGGBBAA`; any other color with one is refused, because a color you can see through has no contrast of its own to check.

| Token | What it colors | Checked against |
|---|---|---|
| `surface` | The page | |
| `surfaceRaised` | Cards, menus, dialogs | |
| `surfaceSunken` | Wells, table headings, the terminal | |
| `text` | Text | Every surface, 4.5:1 |
| `textMuted` | Secondary text | Every surface, 4.5:1 |
| `border` | Dividers and card edges | |
| `borderStrong` | Edges of inputs and other controls | Page and cards, 3:1 |
| `brand` | Brand areas, and the logo's ink in light mode | |
| `brandText` | Text on brand areas | `brand`, 4.5:1 |
| `accent` | Links, primary buttons, selection | Page and cards, 4.5:1 |
| `accentText` | Labels on primary buttons | `accent`, 4.5:1 |
| `success`, `warning`, `danger`, `info` | Status: icons, messages, badges | Cards, 4.5:1 |
| `successText`, `warningText`, `dangerText`, `infoText` | Labels on filled status badges and buttons | Their status color, 4.5:1 |
| `focus` | The focus ring | Every surface, 3:1 |
| `overlay` | Dims the page behind dialogs | |
| `chart` | Chart series, in order: 6 to 12 colors | Cards, 3:1 each |

The panel works out a few more from these: the backgrounds of selected rows and of status alerts (`accentSubtle`, `successSubtle` and so on), each 12% of its color over a card. Text on them is checked too.

Status colors keep their meaning: `danger` has to look red, `warning` amber or orange, `success` green and `info` blue, with enough saturation to tell.

The theme editor's contrast checker lists every pair that's checked, with its ratio and what it needs.

## Type

| Token | What it is |
|---|---|
| `typography.sans` | The text font: a `family` and, for a font the panel doesn't bundle, its `files` |
| `typography.mono` | The code font, the same way |
| `typography.baseSize` | The base text size, such as `16px` |
| `typography.scale` | How much larger each step of the type scale is, from 1 to 2; the default is 1.125 |
| `typography.weights` | `regular`, `medium`, `semibold` and `bold`, from 100 to 900 |

The panel bundles Inter (`Inter Variable`) and JetBrains Mono (`JetBrains Mono Variable`). Any other font comes with the theme as up to four variable font files in WOFF2 or WOFF format, listed under `files`; a theme that names another family without its files is refused, and so is a package without the files it lists. A family name has letters, digits, spaces, hyphens and underscores only. Themes never load fonts from the internet.

## Shape, density and motion

| Token | What it is |
|---|---|
| `radius` | Corner radius `sm`, `md`, `lg` and `xl`, in `px` or `rem` |
| `density` | `comfortable` or `compact`: how tall controls and table rows are |
| `shadows` | `sm`, `md` and `lg` box shadows in CSS syntax: offsets, blur, spread and a color |
| `motion` | `fast`, `normal` and `slow` durations in `ms` |
| `focusRing` | The focus ring's `width` and `offset`, in `px` or `rem` |

## Assets

| Field | Types | What it is |
|---|---|---|
| `logoLight` | SVG, PNG, JPEG, WebP | The logo for light mode, in the top bar and in emails |
| `logoDark` | SVG, PNG, JPEG, WebP | The logo for dark mode |
| `mark` | SVG, PNG, JPEG, WebP | A square symbol for narrow screens and small spaces |
| `favicon` | SVG, PNG, ICO | The browser tab's icon |
| `loginBackground` | SVG, PNG, JPEG, WebP | The image behind the sign-in form |
| `illustrations` | SVG, PNG, JPEG, WebP | Named images for empty states, such as `illustrations: { noSites: empty-sites.svg }` |

Paths are relative to the package, without `..` or URLs. Every file is checked against its type, and read the way browsers read it.

SVG files must be drawings only:

- **Elements:** SVG's shapes, text, groups, `defs`, `symbol` and `use`, gradients and patterns, clipping paths, masks, markers, filters, `style`, `title`, `desc` and `metadata`. No scripts, links, `foreignObject`, images (`image` and `feImage`), animations or fonts, and nothing from another namespace, such as an editor's own elements or HTML. Files exported for the web, or cleaned up with a tool such as SVGO, usually fit; an editor's own data, such as Inkscape's, has to go, so save those as plain SVG.
- **Attributes:** the ones that place and style drawings, presentation attributes such as `fill` and `stroke`, `class`, `id`, `style`, `aria-*` and `data-*`, and `xml:lang` and `xml:space`. No event handlers, `src`, `xml:base` or attributes from other namespaces.
- **References** only to parts of the same file: `href` and `xlink:href` start with `#`, and so does every `url()` in styles and presentation attributes. No `@import` or image functions such as `image-set()` that load anything else.
- **Styles:** a `style` element holds only CSS, read as one sheet however comments or CDATA sections split it.
- **Declarations:** a `<!DOCTYPE>` may name the SVG DTD but not declare entities or attribute defaults, and there are no processing instructions, such as `<?xml-stylesheet?>`, besides the XML declaration.

## Templates

Templates are HTML files with `{{variables}}` and nothing else: no conditions, loops, scripts, forms or event handlers, and nothing loaded from elsewhere. The panel reads them as browsers read HTML, so entities and the order of attributes can't hide anything.

- **Elements:** the ones of plain pages and emails: headings, paragraphs, lists, tables, links, images, `div`, `span`, `style`, `title` and `meta` with a name such as `viewport`. No scripts, frames, forms, inputs, SVG, `base`, `link`, `meta http-equiv` or conditional comments.
- **Images** come from `{{logo}}` or `data:` URLs.
- **Links** go to web and mail addresses (`https:`, `http:`, `mailto:` and `tel:`) or to a path. Following one is the reader's choice. A variable can be a whole address, which is checked when it's filled in, or come after the scheme or a path, as in `mailto:{{contact}}` or `https://example.com/?site={{siteName}}`.
- **Styles** in a `style` element or attribute refer to nothing outside the page: no `@import`, and `url()` and `image-set()` only with `data:` URLs. Comments are `/* */`; CSS has no `//` comments, and a `//` is refused.
- **Variables** go in text and attribute values. `{{style}}` goes only in a `style` element and `{{logo}}` only between elements; no other variable goes in styles or comments.

Every value is escaped for where it stands: as text, in an attribute, or in a link, where an address that isn't a web or mail address is dropped.

| Template | Used for | Variables |
|---|---|---|
| `email` | Notification emails | `brandName`, `title`, `preheader`, `body`, `actionLabel`, `actionUrl`, `footer` |
| `error` | The page visitors see when a site fails | `brandName`, `siteName`, `status`, `title`, `message`, `requestId` |
| `maintenance` | The page shown while a site is in maintenance | `brandName`, `siteName`, `title`, `message`, `until` |
| `suspended` | The page shown for a suspended account | `brandName`, `siteName`, `title`, `message`, `contact` |

Every template also has `lang` and `dir` for the reader's language and direction, `style` with the theme's colors as CSS for a `<style>` element, and `logo`, an image of the theme's logo, or nothing. The panel fills in the text in the reader's language. A theme without a template uses Racksail's, in the theme's colors.

```html
<!doctype html>
<html lang="{{lang}}" dir="{{dir}}">
  <head>
    <meta charset="utf-8">
    <title>{{title}} · {{siteName}}</title>
    <style>{{style}}</style>
  </head>
  <body>
    <main>
      {{logo}}
      <h1>{{title}}</h1>
      <p>{{message}}</p>
      <p class="muted">Request {{requestId}}</p>
    </main>
  </body>
</html>
```

## Console colors

`console` colors the installer, the console status screen, the SSH login message and the CLI: `background`, `foreground`, `accent`, `success`, `warning` and `danger`, each `#RRGGBB`. A color the theme leaves out is Racksail's. Every color is text on the background, so each needs 4.5:1 against it, and the status colors keep their meaning as the panel's do. Terminals that can't show 24-bit color get the nearest of their 256 or 16 colors, and with `NO_COLOR` set there's no color at all. `racksail theme mark ./my-theme` shows the mark in a theme's console colors.

## Protected colors

Themes can't set the protected palette that safety-critical screens use. A theme that tries, with a key starting with `critical` where tokens go (at the top of the file, in `tokens` or one of its groups, such as `tokens.light`, or in `console`), is refused. Names you choose, such as those of illustrations, can be anything.

## The command line

| Command | What it does |
|---|---|
| `racksail theme validate THEME` | Checks a theme file, a package folder or a zip archive as the panel would |
| `racksail theme export THEME` | Prints a theme as YAML, or JSON with `--format json`; a built-in theme's ID gives a complete file to start from |
| `racksail theme css [THEME]` | Prints the CSS the panel uses for a theme; `--layer` prints only the theme's own layer, as the panel applies server and reseller themes |
| `racksail theme list` | Lists the built-in themes |
| `racksail theme mark [THEME]` | Draws the mark in the terminal in a theme's console colors, `--size small` or `large` |
