package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/theme"
)

// FileKind says how a generated file is compared with the one on disk.
type FileKind int

// Raster files are compared by their pixels, with a little tolerance, since
// floating point may round an antialiased edge differently on another
// processor; everything else must match byte for byte.
const (
	Text FileKind = iota
	Raster
	Icon
)

// File is one generated file, with its path from the repository root.
type File struct {
	Path string
	Kind FileKind
	Data []byte
}

// palettes names the logo's color variants as they appear in file names.
var palettes = []struct {
	suffix string
	pal    Palette
}{{"", Color}, {"-dark", Dark}, {"-mono", Mono}, {"-white", Reversed}}

// squareMark is the mark centered in a square, as favicons need.
func squareMark(pal Palette) Drawing {
	d := markDrawing(pal)
	d.View = Box{0, -(markWidth - markHeight) / 2, markWidth, markWidth}
	return d
}

// adaptiveFavicon is the favicon that follows the browser's light or dark
// mode: on dark tab strips the navy ink turns white.
func adaptiveFavicon() Drawing {
	d := squareMark(Color)
	d.Style = fmt.Sprintf(".ink{fill:%s}.sea{fill:%s}@media (prefers-color-scheme:dark){.ink{fill:%s}}", Navy, Blue, White)
	return d
}

// smallFavicon is the 16-pixel favicon, drawn from the small mark.
func smallFavicon() Drawing {
	var layers []Layer
	for _, s := range smallMark {
		fill := Blue
		if s.Ink {
			fill = Navy
		}
		layers = append(layers, Layer{Path: s.Path, Fill: fill})
	}
	return Drawing{Title: "Racksail", View: Box{0, 0, smallMarkSize, smallMarkSize}, Layers: layers}
}

// tile is the app icon: the mark for dark backgrounds on a navy square.
// width is the share of the tile the mark spans; a radius of 0 leaves the
// corners square for platforms that round them themselves.
func tile(width, radius float64) Drawing {
	const size = 100
	s := size * width / markWidth
	x := (size - markWidth*s) / 2
	y := (size - markHeight*s) / 2
	return Drawing{
		Title:      "Racksail",
		View:       Box{0, 0, size, size},
		Background: Navy,
		Radius:     radius,
		Layers:     markLayers(Dark, s, x, y),
	}
}

// App icon proportions. The maskable icon keeps the mark inside the circle
// of 80% that Android may cut it to.
var (
	appIcon      = tile(0.64, 22)
	touchIcon    = tile(0.62, 0)
	maskableIcon = tile(0.56, 0)
)

// renderWidth renders a drawing at a pixel width, with the height that
// keeps its proportions.
func renderWidth(d Drawing, w int) *Canvas {
	return d.Render(w, int(math.Round(float64(w)*d.View.H/d.View.W)))
}

// outputs lists every generated file.
func outputs() []File {
	var files []File
	text := func(path, data string) { files = append(files, File{path, Text, []byte(data)}) }
	raster := func(path string, c *Canvas) { files = append(files, File{path, Raster, c.PNG()}) }

	for _, v := range palettes {
		text("brand/logo/racksail-mark"+v.suffix+".svg", markDrawing(v.pal).SVG())
		text("brand/logo/racksail-logo-horizontal"+v.suffix+".svg", horizontalDrawing(v.pal).SVG())
		text("brand/logo/racksail-logo-stacked"+v.suffix+".svg", stackedDrawing(v.pal).SVG())
	}
	text("brand/logo/racksail-wordmark.svg", wordmarkDrawing(Color).SVG())
	text("brand/logo/racksail-wordmark-white.svg", wordmarkDrawing(Reversed).SVG())
	raster("brand/logo/racksail-mark.png", renderWidth(markDrawing(Color), 1024))
	raster("brand/logo/racksail-logo.png", renderWidth(stackedDrawing(Color), 1024))
	raster("brand/logo/racksail-logo-dark.png", renderWidth(stackedDrawing(Dark), 1024))
	raster("brand/logo/racksail-wordmark.png", renderWidth(wordmarkDrawing(Color), 1024))

	favicon := adaptiveFavicon().SVG()
	ico := ICO(smallFavicon().Render(16, 16), squareMark(Color).Render(32, 32), squareMark(Color).Render(48, 48))
	icons := []File{
		{"favicon.svg", Text, []byte(favicon)},
		{"favicon.ico", Icon, ico},
		{"favicon-32.png", Raster, squareMark(Color).Render(32, 32).PNG()},
		{"apple-touch-icon.png", Raster, touchIcon.Render(180, 180).PNG()},
		{"app-icon.svg", Text, []byte(appIcon.SVG())},
		{"app-icon-192.png", Raster, appIcon.Render(192, 192).PNG()},
		{"app-icon-512.png", Raster, appIcon.Render(512, 512).PNG()},
		{"app-icon-maskable-512.png", Raster, maskableIcon.Render(512, 512).PNG()},
	}
	for _, f := range icons {
		files = append(files, File{"brand/icons/" + f.Path, f.Kind, f.Data})
	}

	// The panel serves its icons from the root of its own origin.
	for _, f := range icons {
		if f.Path == "app-icon.svg" {
			continue
		}
		files = append(files, File{"ui/public/" + f.Path, f.Kind, f.Data})
	}
	text("ui/src/design/brand.ts", uiModule())

	// The documentation site shows the mark in its header and as its favicon.
	text("docs/site/src/assets/mark-light.svg", markDrawing(Color).SVG())
	text("docs/site/src/assets/mark-dark.svg", markDrawing(Dark).SVG())
	text("docs/site/public/favicon.svg", favicon)

	small, large := terminalGrids()
	console := theme.Default().Console
	text("brand/terminal/racksail-mark.ans", console.DrawPixels(large, theme.TrueColor))
	text("brand/terminal/racksail-mark-256.ans", console.DrawPixels(large, theme.Color256))
	text("brand/terminal/racksail-mark-16.ans", console.DrawPixels(large, theme.Color16))
	text("brand/terminal/racksail-mark.txt", console.DrawPixels(large, theme.NoColor))
	text("brand/terminal/racksail-mark-small.ans", console.DrawPixels(small, theme.TrueColor))
	text("brand/terminal/racksail-mark-small.txt", console.DrawPixels(small, theme.NoColor))
	files = append(files, File{"internal/theme/mark_grid.go", Text, markGridSource(small, large)})
	return files
}

// uiModule is the TypeScript module the panel's logo components draw from.
func uiModule() string {
	var b strings.Builder
	b.WriteString("// Generated by `go run ./brand/gen` from the artwork in brand/gen. Don't edit by hand.\n")
	b.WriteString("// Shapes are SVG path data: ink is the mainsail and hull, sea the jib and wave, and\n")
	b.WriteString("// word the wordmark. Components color them with theme tokens.\n\n")
	shapes := func(name, doc string, d Drawing) {
		fmt.Fprintf(&b, "/** %s */\nexport const %s = {\n", doc, name)
		fmt.Fprintf(&b, "  viewBox: %q,\n", fmt.Sprintf("%s %s %s %s", num(d.View.X), num(d.View.Y), num(d.View.W), num(d.View.H)))
		for _, l := range d.Layers {
			fmt.Fprintf(&b, "  %s: %q,\n", l.Class, l.Path.D())
		}
		b.WriteString("} as const;\n\n")
	}
	shapes("mark", "The mark.", markDrawing(Color))
	small := smallFavicon()
	small.Layers = []Layer{
		{Path: append(append(Path{}, smallMark[0].Path...), smallMark[2].Path...), Class: "ink"},
		{Path: append(append(Path{}, smallMark[1].Path...), smallMark[3].Path...), Class: "sea"},
	}
	shapes("smallMark", "The mark redrawn on a 16-pixel grid, for sizes up to 24 pixels.", small)
	shapes("wordmark", "The wordmark.", wordmarkDrawing(Color))
	shapes("horizontalLogo", "The mark with the wordmark to its right.", horizontalDrawing(Color))
	shapes("stackedLogo", "The mark above the wordmark.", stackedDrawing(Color))
	return strings.TrimSuffix(b.String(), "\n")
}
