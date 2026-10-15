package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
)

// RGBA is a color with straight alpha, each channel from 0 to 1.
type RGBA struct{ R, G, B, A float64 }

// Hex reads #RRGGBB or #RRGGBBAA.
func Hex(s string) RGBA {
	if len(s) != 7 && len(s) != 9 || s[0] != '#' {
		panic(fmt.Sprintf("color %q must be #RRGGBB or #RRGGBBAA", s))
	}
	ch := func(i int) float64 {
		v, err := strconv.ParseUint(s[i:i+2], 16, 8)
		if err != nil {
			panic(fmt.Sprintf("color %q: %v", s, err))
		}
		return float64(v) / 255
	}
	c := RGBA{ch(1), ch(3), ch(5), 1}
	if len(s) == 9 {
		c.A = ch(7)
	}
	return c
}

// Canvas is an image being drawn, with premultiplied channels.
type Canvas struct {
	W, H int
	px   []RGBA
}

// NewCanvas returns a transparent canvas.
func NewCanvas(w, h int) *Canvas {
	return &Canvas{W: w, H: h, px: make([]RGBA, w*h)}
}

// Fill paints the path in pixel coordinates with a color, anti-aliased with
// the nonzero rule.
func (c *Canvas) Fill(p Path, col RGBA) {
	cov := coverage(p, c.W, c.H)
	for i, a := range cov {
		if a == 0 {
			continue
		}
		a *= col.A
		d := c.px[i]
		c.px[i] = RGBA{
			R: col.R*a + d.R*(1-a),
			G: col.G*a + d.G*(1-a),
			B: col.B*a + d.B*(1-a),
			A: a + d.A*(1-a),
		}
	}
}

// Image returns the canvas as an image with straight alpha.
func (c *Canvas) Image() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, c.W, c.H))
	for i, p := range c.px {
		o := i * 4
		if p.A <= 0 {
			continue
		}
		img.Pix[o] = channel(p.R / p.A)
		img.Pix[o+1] = channel(p.G / p.A)
		img.Pix[o+2] = channel(p.B / p.A)
		img.Pix[o+3] = channel(p.A)
	}
	return img
}

func channel(v float64) uint8 {
	return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255))
}

// PNG encodes the canvas. Go's encoder writes no metadata, only the header,
// the pixels and the end marker, so the files carry nothing but the image.
func (c *Canvas) PNG() []byte {
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, c.Image()); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// At returns the color of a pixel with straight alpha.
func (c *Canvas) At(x, y int) color.NRGBA {
	p := c.px[y*c.W+x]
	if p.A <= 0 {
		return color.NRGBA{}
	}
	return color.NRGBA{channel(p.R / p.A), channel(p.G / p.A), channel(p.B / p.A), channel(p.A)}
}

// coverage returns how much of each pixel the path covers, from 0 to 1.
// Every edge adds the signed area it encloses to an accumulation buffer, and
// a running sum along each row turns that into coverage: exact for straight
// edges, and curves are flattened finely enough not to matter.
func coverage(p Path, w, h int) []float64 {
	stride := w + 2
	acc := make([]float64, stride*h)
	p.flatten(0.02, func(a, b Point) { accumulate(acc, stride, w, h, a, b) })
	out := make([]float64, w*h)
	for y := range h {
		sum := 0.0
		row := acc[y*stride:]
		for x := range w {
			sum += row[x]
			out[y*w+x] = math.Min(math.Abs(sum), 1)
		}
	}
	return out
}

func accumulate(acc []float64, stride, w, h int, p0, p1 Point) {
	if p0.Y == p1.Y {
		return
	}
	dir := 1.0
	if p0.Y > p1.Y {
		dir = -1
		p0, p1 = p1, p0
	}
	dxdy := (p1.X - p0.X) / (p1.Y - p0.Y)
	x := p0.X
	yStart := int(math.Floor(p0.Y))
	if p0.Y < 0 {
		x -= p0.Y * dxdy
		yStart = 0
	}
	yEnd := min(h, int(math.Ceil(p1.Y)))
	clamp := func(v float64) float64 { return math.Max(0, math.Min(float64(w), v)) }
	for y := yStart; y < yEnd; y++ {
		row := acc[y*stride : (y+1)*stride]
		dy := math.Min(float64(y+1), p1.Y) - math.Max(float64(y), p0.Y)
		xNext := x + dxdy*dy
		d := dy * dir
		// Everything left of the canvas counts at its first column, so the
		// running sum stays right; what's right of it is never summed.
		xa, xb := clamp(x), clamp(xNext)
		x0, x1 := math.Min(xa, xb), math.Max(xa, xb)
		x0f := math.Floor(x0)
		x0i := int(x0f)
		x1c := math.Ceil(x1)
		x1i := int(x1c)
		if x1i <= x0i+1 {
			mid := 0.5*(xa+xb) - x0f
			row[x0i] += d - d*mid
			row[x0i+1] += d * mid
		} else {
			s := 1 / (x1 - x0)
			f0 := x0 - x0f
			a0 := 0.5 * s * (1 - f0) * (1 - f0)
			f1 := x1 - x1c + 1
			am := 0.5 * s * f1 * f1
			row[x0i] += d * a0
			if x1i == x0i+2 {
				row[x0i+1] += d * (1 - a0 - am)
			} else {
				a1 := s * (1.5 - f0)
				row[x0i+1] += d * (a1 - a0)
				for xi := x0i + 2; xi < x1i-1; xi++ {
					row[xi] += d * s
				}
				a2 := a1 + float64(x1i-x0i-3)*s
				row[x1i-1] += d * (1 - a2 - am)
			}
			row[x1i] += d * am
		}
		x = xNext
	}
}
