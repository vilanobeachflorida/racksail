package main

import "math"

// Proportions of the lockups, in mark units: the mark is 100 wide.
const (
	// In the horizontal lockup the wordmark's capitals are a little under
	// half the mark's height, and the baseline sits on the hull's lower edge,
	// so the boat and the name share a waterline.
	horizontalCapHeight = 36
	horizontalGap       = 20
	horizontalBaseline  = 70
	// In the stacked lockup the wordmark is a quarter wider than the mark,
	// as in the original drawing, and sits just below the wave.
	stackedWordmarkWidth = 125
	stackedGap           = 8
)

// markDrawing is the mark on its own.
func markDrawing(pal Palette) Drawing {
	return Drawing{Title: "Racksail", View: Box{0, 0, markWidth, markHeight}, Layers: markLayers(pal, 1, 0, 0)}
}

// wordmarkLayer places the wordmark scaled by s with its left edge at x and
// its baseline at y.
func wordmarkLayer(pal Palette, s, x, y float64) Layer {
	p := wordmark()
	lo, _ := p.Bounds()
	return Layer{Path: p.Apply(Transform{Scale: s, DX: x - lo.X*s, DY: y}), Fill: pal.Word, Class: "word"}
}

// wordmarkDrawing is the wordmark on its own, with its capitals 100 units tall.
func wordmarkDrawing(pal Palette) Drawing {
	s := 100.0 / wordmarkCapHeight
	l := wordmarkLayer(pal, s, 0, -wordmarkTop*s)
	return Drawing{Title: "Racksail", View: bounds([]Layer{l}), Layers: []Layer{l}}
}

// horizontalDrawing is the mark with the wordmark to its right.
func horizontalDrawing(pal Palette) Drawing {
	s := horizontalCapHeight / float64(wordmarkCapHeight)
	layers := append(markLayers(pal, 1, 0, 0), wordmarkLayer(pal, s, markWidth+horizontalGap, horizontalBaseline))
	return Drawing{Title: "Racksail", View: bounds(layers), Layers: layers}
}

// stackedDrawing is the mark centered above the wordmark.
func stackedDrawing(pal Palette) Drawing {
	p := wordmark()
	lo, hi := p.Bounds()
	s := stackedWordmarkWidth / (hi.X - lo.X)
	baseline := markHeight + stackedGap - wordmarkTop*s
	layers := append(markLayers(pal, 1, (stackedWordmarkWidth-markWidth)/2, 0), wordmarkLayer(pal, s, 0, baseline))
	return Drawing{Title: "Racksail", View: bounds(layers), Layers: layers}
}

// bounds is the box around every layer, rounded out to whole hundredths so
// the view box never clips antialiased edges.
func bounds(layers []Layer) Box {
	lo := Point{math.Inf(1), math.Inf(1)}
	hi := Point{math.Inf(-1), math.Inf(-1)}
	for _, l := range layers {
		a, b := l.Path.Bounds()
		lo = Point{math.Min(lo.X, a.X), math.Min(lo.Y, a.Y)}
		hi = Point{math.Max(hi.X, b.X), math.Max(hi.Y, b.Y)}
	}
	x0, y0 := math.Floor(lo.X*100)/100, math.Floor(lo.Y*100)/100
	x1, y1 := math.Ceil(hi.X*100)/100, math.Ceil(hi.Y*100)/100
	return Box{x0, y0, x1 - x0, y1 - y0}
}
