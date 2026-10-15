package theme

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Color is a hex color: #RRGGBB, or #RRGGBBAA for the overlay, the one
// token that may be translucent.
type Color string

// RGB is a color's channels from 0 to 255, and its alpha from 0 to 1.
type RGB struct {
	R, G, B float64
	A       float64
}

// Parse reads the color.
func (c Color) Parse() (RGB, error) {
	s := strings.TrimPrefix(string(c), "#")
	if !strings.HasPrefix(string(c), "#") || len(s) != 6 && len(s) != 8 {
		return RGB{}, fmt.Errorf("color %q must be #RRGGBB or #RRGGBBAA", c)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("color %q isn't hexadecimal", c)
	}
	out := RGB{A: 1}
	if len(s) == 8 {
		out.A = float64(v&0xFF) / 255
		v >>= 8
	}
	out.R, out.G, out.B = float64(v>>16), float64(v>>8&0xFF), float64(v&0xFF)
	return out, nil
}

// HasAlpha reports whether the color is written with an alpha channel,
// #RRGGBBAA.
func (c Color) HasAlpha() bool {
	return len(c) == 9
}

// Hex formats a color as #RRGGBB, ignoring alpha.
func (c RGB) Hex() Color {
	ch := func(v float64) int { return int(math.Round(math.Max(0, math.Min(255, v)))) }
	return Color(fmt.Sprintf("#%02X%02X%02X", ch(c.R), ch(c.G), ch(c.B)))
}

// Mix returns a share of color a over b, as CSS color-mix in srgb does.
func Mix(a, b Color, share float64) (Color, error) {
	ca, err := a.Parse()
	if err != nil {
		return "", err
	}
	cb, err := b.Parse()
	if err != nil {
		return "", err
	}
	m := func(x, y float64) float64 { return float64(x*share) + float64(y*(1-share)) }
	return RGB{R: m(ca.R, cb.R), G: m(ca.G, cb.G), B: m(ca.B, cb.B), A: 1}.Hex(), nil
}

// ErrTranslucent is returned when the contrast of a translucent color is
// asked for: it depends on whatever shows through it, so it can't be
// checked.
var ErrTranslucent = errors.New("a translucent color has no contrast of its own")

// Luminance is a color's relative luminance as WCAG defines it, from 0 for
// black to 1 for white. Translucent colors have none; see ErrTranslucent.
func Luminance(c Color) (float64, error) {
	rgb, err := c.Parse()
	if err != nil {
		return 0, err
	}
	if rgb.A < 1 {
		return 0, fmt.Errorf("%s: %w", c, ErrTranslucent)
	}
	lin := func(v float64) float64 {
		x := v / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(rgb.R) + 0.7152*lin(rgb.G) + 0.0722*lin(rgb.B), nil
}

// Contrast returns the WCAG contrast ratio of two opaque colors, from 1 to
// 21.
func Contrast(a, b Color) (float64, error) {
	la, err := Luminance(a)
	if err != nil {
		return 0, err
	}
	lb, err := Luminance(b)
	if err != nil {
		return 0, err
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), nil
}

// Hue returns a color's hue in degrees, from 0 to 360, and its saturation
// from 0 to 1, as in HSL.
func Hue(c Color) (hue, saturation float64, err error) {
	rgb, err := c.Parse()
	if err != nil {
		return 0, 0, err
	}
	r, g, b := rgb.R/255, rgb.G/255, rgb.B/255
	hi, lo := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	d := hi - lo
	if d == 0 {
		return 0, 0, nil
	}
	l := (hi + lo) / 2
	saturation = d / (1 - math.Abs(2*l-1))
	switch hi {
	case r:
		hue = math.Mod((g-b)/d, 6)
	case g:
		hue = (b-r)/d + 2
	default:
		hue = (r-g)/d + 4
	}
	hue *= 60
	if hue < 0 {
		hue += 360
	}
	return hue, saturation, nil
}
