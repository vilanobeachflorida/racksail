package main

import (
	"fmt"
	"math"
	"strings"
)

// Layer is one filled path of a drawing. Fill is a brand color; Class, when
// set, lets the drawing's style sheet recolor the layer.
type Layer struct {
	Path  Path
	Fill  string
	Class string
}

// Box is a rectangle in artwork units.
type Box struct{ X, Y, W, H float64 }

// Drawing is a logo, icon or lockup: layers in a view box, optionally on a
// rounded background.
type Drawing struct {
	Title      string
	View       Box
	Background string
	// Radius rounds the background's corners, in artwork units.
	Radius float64
	// Style is a style sheet for layers with a class, such as the favicon's
	// switch to light ink in dark mode.
	Style  string
	Layers []Layer
}

// Palette says how the mark's shapes and the wordmark are colored.
type Palette struct {
	Ink, Sea, Word string
}

// The four ways the logo is colored.
var (
	// Color is the full-color logo for light backgrounds.
	Color = Palette{Ink: Navy, Sea: Blue, Word: Navy}
	// Dark is the full-color logo for dark backgrounds.
	Dark = Palette{Ink: White, Sea: Blue, Word: White}
	// Mono is one color, navy, for single-color printing and engraving.
	Mono = Palette{Ink: Navy, Sea: Navy, Word: Navy}
	// Reversed is one color, white, for photos and colored backgrounds.
	Reversed = Palette{Ink: White, Sea: White, Word: White}
)

// markLayers places the mark scaled by s with its top left corner at (x, y).
func markLayers(pal Palette, s, x, y float64) []Layer {
	t := Transform{Scale: s, DX: x, DY: y}
	var ink, sea Path
	for _, shape := range mark {
		if shape.Ink {
			ink = append(ink, shape.Path.Apply(t)...)
		} else {
			sea = append(sea, shape.Path.Apply(t)...)
		}
	}
	if pal.Ink == pal.Sea {
		return []Layer{{Path: append(ink, sea...), Fill: pal.Ink}}
	}
	return []Layer{{Path: ink, Fill: pal.Ink, Class: "ink"}, {Path: sea, Fill: pal.Sea, Class: "sea"}}
}

// SVG writes the drawing as a standalone SVG file.
func (d Drawing) SVG() string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s %s %s %s" role="img" aria-label="%s">`,
		num(d.View.X), num(d.View.Y), num(d.View.W), num(d.View.H), d.Title)
	if d.Style != "" {
		fmt.Fprintf(&b, "<style>%s</style>", d.Style)
	}
	if d.Background != "" {
		if d.Radius > 0 {
			fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`,
				num(d.View.X), num(d.View.Y), num(d.View.W), num(d.View.H), num(d.Radius), d.Background)
		} else {
			fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`,
				num(d.View.X), num(d.View.Y), num(d.View.W), num(d.View.H), d.Background)
		}
	}
	for _, l := range d.Layers {
		if d.Style != "" && l.Class != "" {
			fmt.Fprintf(&b, `<path class="%s" d="%s"/>`, l.Class, l.Path.D())
		} else {
			fmt.Fprintf(&b, `<path fill="%s" d="%s"/>`, l.Fill, l.Path.D())
		}
	}
	b.WriteString("</svg>\n")
	return b.String()
}

// Render rasterizes the drawing at w by h pixels, scaled uniformly to fit
// and centered. Classes are ignored: rasters show the light-mode colors.
func (d Drawing) Render(w, h int) *Canvas {
	s := math.Min(float64(w)/d.View.W, float64(h)/d.View.H)
	t := Transform{
		Scale: s,
		DX:    (float64(w)-d.View.W*s)/2 - d.View.X*s,
		DY:    (float64(h)-d.View.H*s)/2 - d.View.Y*s,
	}
	c := NewCanvas(w, h)
	if d.Background != "" {
		c.Fill(roundedRect(d.View, d.Radius).Apply(t), Hex(d.Background))
	}
	for _, l := range d.Layers {
		c.Fill(l.Path.Apply(t), Hex(l.Fill))
	}
	return c
}

// roundedRect is a rectangle with circular corners drawn as cubic curves.
func roundedRect(b Box, r float64) Path {
	r = math.Min(r, math.Min(b.W, b.H)/2)
	x0, y0, x1, y1 := b.X, b.Y, b.X+b.W, b.Y+b.H
	if r <= 0 {
		return Path{
			{MoveTo, []Point{{x0, y0}}}, {LineTo, []Point{{x1, y0}}},
			{LineTo, []Point{{x1, y1}}}, {LineTo, []Point{{x0, y1}}}, {Op: Close},
		}
	}
	// The usual constant for approximating a quarter circle with a cubic.
	k := r * 0.5522847498
	return Path{
		{MoveTo, []Point{{x0 + r, y0}}},
		{LineTo, []Point{{x1 - r, y0}}},
		{CubicTo, []Point{{x1 - r + k, y0}, {x1, y0 + r - k}, {x1, y0 + r}}},
		{LineTo, []Point{{x1, y1 - r}}},
		{CubicTo, []Point{{x1, y1 - r + k}, {x1 - r + k, y1}, {x1 - r, y1}}},
		{LineTo, []Point{{x0 + r, y1}}},
		{CubicTo, []Point{{x0 + r - k, y1}, {x0, y1 - r + k}, {x0, y1 - r}}},
		{LineTo, []Point{{x0, y0 + r}}},
		{CubicTo, []Point{{x0, y0 + r - k}, {x0 + r - k, y0}, {x0 + r, y0}}},
		{Op: Close},
	}
}
