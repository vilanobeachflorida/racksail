# Racksail brand files

The Racksail logo, icons and terminal mark, and the program that draws them. How to use them is in the brand guidelines on the docs site: [logo](../docs/site/src/content/docs/brand/logo.md), [colors](../docs/site/src/content/docs/brand/colors.md), [typography](../docs/site/src/content/docs/brand/typography.md), [voice](../docs/site/src/content/docs/brand/voice.md), [UI patterns](../docs/site/src/content/docs/brand/ui-patterns.md) and [applications](../docs/site/src/content/docs/brand/applications.md).

## The basics

- **Name:** Racksail. One word, capital R, everything else lowercase, in every context. The command is `racksail`.
- **Tagline:** Your server, your rules.
- **Colors:** navy `#01214F` and blue `#007CFD`, with white. Navy is the ink of the mark and the wordmark; blue is the sea.
- **Fonts:** Inter for text and JetBrains Mono for code. The wordmark is drawn, not typed.

## What's here

### `logo/`

Every lockup comes in four colorings:

| Suffix | Colors | Use it on |
|---|---|---|
| none | navy and blue | white and light backgrounds |
| `-dark` | white and blue | navy and other dark backgrounds |
| `-mono` | navy only | one-color printing, engraving, embroidery |
| `-white` | white only | photos and colored backgrounds |

| File | What it is |
|---|---|
| `racksail-logo-horizontal*.svg` | The mark with the wordmark beside it. The default lockup. |
| `racksail-logo-stacked*.svg` | The mark centered above the wordmark, for square spaces. |
| `racksail-mark*.svg` | The mark alone, for small spaces and where the name is already shown. |
| `racksail-wordmark.svg`, `racksail-wordmark-white.svg` | The wordmark alone, in navy or white. |
| `racksail-logo.png`, `racksail-logo-dark.png` | The stacked logo, 1024 pixels wide, for places that don't take SVG. |
| `racksail-mark.png`, `racksail-wordmark.png` | The mark and the wordmark, 1024 pixels wide. |

Use the SVG files wherever you can: they're small and sharp at every size.

### `icons/`

| File | What it is |
|---|---|
| `favicon.svg` | The mark on a square canvas. Its navy turns white when the browser is in dark mode. |
| `favicon.ico` | 16, 32 and 48 pixels; the 16-pixel image is redrawn on the pixel grid. |
| `favicon-32.png` | The 32-pixel favicon. |
| `apple-touch-icon.png` | 180 pixels, square corners; iOS rounds them. |
| `app-icon.svg`, `app-icon-192.png`, `app-icon-512.png` | The mark on a navy tile with rounded corners. |
| `app-icon-maskable-512.png` | The tile with square corners and the mark inside the safe circle Android may crop to. |

### `terminal/`

The mark drawn with Unicode half blocks for the installer, the console status screen and the CLI, in the default theme's console palette.

| File | What it is |
|---|---|
| `racksail-mark.ans` | 32 columns by 13 lines, in 24-bit color. |
| `racksail-mark-256.ans`, `racksail-mark-16.ans` | The same for terminals with 256 or 16 colors. |
| `racksail-mark.txt` | Without color codes. |
| `racksail-mark-small.ans`, `racksail-mark-small.txt` | 16 columns by 8 lines, with and without color. |

`cat` shows them in a terminal, and `racksail theme mark` draws them in the colors your terminal supports.

## Changing the artwork

Every file here is generated from the artwork in [`gen/`](gen): the mark's four shapes as cubic curves, the wordmark's outlines and the proportions of the lockups. Don't edit the generated files. Change the artwork and run, from the repository root:

```sh
go run ./brand/gen
```

`make gen` runs it along with the other generators. `go run ./brand/gen -check` writes nothing and fails if a file is out of date, and so do the tests.

The generator also writes the copies other parts of Racksail use:

- `ui/public/`: the favicons and app icons the panel serves
- `ui/src/design/brand.ts`: the shapes the panel's logo components draw in theme colors
- `docs/site/src/assets/mark-light.svg`, `mark-dark.svg` and `docs/site/public/favicon.svg`: the docs site's logo and favicon
- `internal/theme/mark_grid.go`: the pixel grids the console draws the mark from

The PNG and ICO files hold only pixels: no text chunks, dates, color profiles or other metadata.

## Credits

The wordmark is "Racksail" set in Poppins Bold, licensed under the SIL Open Font License 1.1, with its own kerning, and converted to outlines so no font is needed to show it.
