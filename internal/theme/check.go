package theme

import (
	"fmt"
	"strconv"
)

// Contrast minimums from WCAG 2.2 AA: 4.5:1 for text and 3:1 for
// everything else people need to see, such as control outlines, focus
// rings, icons and chart marks.
const (
	TextContrast    = 4.5
	NonTextContrast = 3
)

// SubtleShare is how much of a color goes into its subtle version: the
// tinted background of selected rows, badges and alerts.
const SubtleShare = 0.12

// Derivation is a token computed from two others: a share of From over
// Over. Renderers and checks compute it the same way.
type Derivation struct {
	Name  string  `json:"name"`
	From  string  `json:"from"`
	Over  string  `json:"over"`
	Share float64 `json:"share"`
}

// Derivations are the tokens themes don't set because they follow from
// the others.
var Derivations = []Derivation{
	{"accentSubtle", "accent", "surfaceRaised", SubtleShare},
	{"successSubtle", "success", "surfaceRaised", SubtleShare},
	{"warningSubtle", "warning", "surfaceRaised", SubtleShare},
	{"dangerSubtle", "danger", "surfaceRaised", SubtleShare},
	{"infoSubtle", "info", "surfaceRaised", SubtleShare},
}

// Pair is a contrast every theme's palettes must reach.
type Pair struct {
	Foreground string  `json:"foreground"`
	Background string  `json:"background"`
	Minimum    float64 `json:"minimum"`
	// Purpose says where the pair appears, to explain a failure.
	Purpose string `json:"purpose"`
}

// Pairs are the contrasts checked in both modes of every theme. Chart
// colors are checked too, each against raised surfaces, where charts sit.
var Pairs = []Pair{
	{"text", "surface", TextContrast, "body text on the page"},
	{"text", "surfaceRaised", TextContrast, "text on cards, menus and dialogs"},
	{"text", "surfaceSunken", TextContrast, "text in table headers and wells"},
	{"textMuted", "surface", TextContrast, "secondary text on the page"},
	{"textMuted", "surfaceRaised", TextContrast, "secondary text on cards"},
	{"textMuted", "surfaceSunken", TextContrast, "secondary text in table headers"},
	{"brandText", "brand", TextContrast, "text on brand-colored areas"},
	{"accentText", "accent", TextContrast, "labels of primary buttons"},
	{"accent", "surface", TextContrast, "links on the page"},
	{"accent", "surfaceRaised", TextContrast, "links on cards"},
	{"text", "accentSubtle", TextContrast, "text in selected rows and items"},
	{"successText", "success", TextContrast, "labels on success badges"},
	{"warningText", "warning", TextContrast, "labels on warning badges"},
	{"dangerText", "danger", TextContrast, "labels of destructive buttons"},
	{"infoText", "info", TextContrast, "labels on info badges"},
	{"success", "surfaceRaised", TextContrast, "success messages"},
	{"warning", "surfaceRaised", TextContrast, "warning messages"},
	{"danger", "surfaceRaised", TextContrast, "error messages and field errors"},
	{"info", "surfaceRaised", TextContrast, "informational messages"},
	{"danger", "surface", TextContrast, "error messages on the page"},
	{"text", "successSubtle", TextContrast, "text in success alerts"},
	{"text", "warningSubtle", TextContrast, "text in warning alerts"},
	{"text", "dangerSubtle", TextContrast, "text in error alerts"},
	{"text", "infoSubtle", TextContrast, "text in info alerts"},
	{"success", "successSubtle", NonTextContrast, "icons in success alerts"},
	{"warning", "warningSubtle", NonTextContrast, "icons in warning alerts"},
	{"danger", "dangerSubtle", NonTextContrast, "icons in error alerts"},
	{"info", "infoSubtle", NonTextContrast, "icons in info alerts"},
	{"focus", "surface", NonTextContrast, "the focus ring on the page"},
	{"focus", "surfaceRaised", NonTextContrast, "the focus ring on cards and dialogs"},
	{"focus", "surfaceSunken", NonTextContrast, "the focus ring in wells"},
	{"borderStrong", "surface", NonTextContrast, "outlines of inputs and controls"},
	{"borderStrong", "surfaceRaised", NonTextContrast, "outlines of inputs on cards"},
	{"accent", "surfaceSunken", NonTextContrast, "selected tabs and switches"},
}

// Meaning keeps a status color recognizable, so a theme can't make a
// destructive button look like a safe one.
type Meaning struct {
	Token string `json:"token"`
	// Hues are the ranges, in degrees, the color's hue must fall in.
	Hues [][2]float64 `json:"hues"`
	Name string       `json:"name"`
}

// Meanings are the status colors' required hues, generous enough for any
// shade of red, amber, green or blue.
var Meanings = []Meaning{
	{"danger", [][2]float64{{330, 360}, {0, 20}}, "red"},
	{"warning", [][2]float64{{20, 65}}, "amber or orange"},
	{"success", [][2]float64{{75, 175}}, "green"},
	{"info", [][2]float64{{180, 265}}, "blue"},
}

// Problem is something wrong with a theme's palette.
type Problem struct {
	// Mode is light or dark.
	Mode string `json:"mode"`
	// Kind is contrast, meaning or color.
	Kind       string  `json:"kind"`
	Foreground string  `json:"foreground"`
	Background string  `json:"background,omitempty"`
	Ratio      float64 `json:"ratio,omitempty"`
	Minimum    float64 `json:"minimum,omitempty"`
	Purpose    string  `json:"purpose,omitempty"`
}

func (p Problem) Error() string {
	switch p.Kind {
	case "meaning":
		return fmt.Sprintf("%s mode: %s must be %s so it keeps its meaning", p.Mode, p.Foreground, p.Purpose)
	case "color":
		return fmt.Sprintf("%s mode: %s isn't a color: %s", p.Mode, p.Foreground, p.Purpose)
	}
	return fmt.Sprintf("%s mode: %s on %s has contrast %.2f:1, needs at least %.1f:1 for %s",
		p.Mode, p.Foreground, p.Background, p.Ratio, p.Minimum, p.Purpose)
}

// Colors returns every color token of the palette by name: the ones themes
// set, chart1 to chartN, and the derived ones. Missing tokens are empty.
func (p Palette) Colors() map[string]Color {
	m := map[string]Color{
		"surface": p.Surface, "surfaceRaised": p.SurfaceRaised, "surfaceSunken": p.SurfaceSunken,
		"text": p.Text, "textMuted": p.TextMuted, "border": p.Border, "borderStrong": p.BorderStrong,
		"brand": p.Brand, "brandText": p.BrandText, "accent": p.Accent, "accentText": p.AccentText,
		"success": p.Success, "successText": p.SuccessText, "warning": p.Warning, "warningText": p.WarningText,
		"danger": p.Danger, "dangerText": p.DangerText, "info": p.Info, "infoText": p.InfoText,
		"focus": p.Focus, "overlay": p.Overlay,
	}
	for i, c := range p.Chart {
		m["chart"+strconv.Itoa(i+1)] = c
	}
	for _, d := range Derivations {
		if mixed, err := Mix(m[d.From], m[d.Over], d.Share); err == nil {
			m[d.Name] = mixed
		}
	}
	return m
}

// Check returns everything wrong with the theme's palettes: contrasts
// below WCAG AA and status colors that lost their meaning.
func (t *Theme) Check() []Problem {
	var out []Problem
	for _, mode := range []struct {
		name string
		p    Palette
	}{{"light", t.Tokens.Light}, {"dark", t.Tokens.Dark}} {
		out = append(out, checkPalette(mode.name, mode.p)...)
	}
	return out
}

func checkPalette(mode string, p Palette) []Problem {
	var out []Problem
	colors := p.Colors()
	for name, c := range colors {
		if c == "" {
			continue
		}
		if _, err := c.Parse(); err != nil {
			out = append(out, Problem{Mode: mode, Kind: "color", Foreground: name, Purpose: err.Error()})
		}
	}
	pairs := append([]Pair{}, Pairs...)
	for i := range p.Chart {
		pairs = append(pairs, Pair{"chart" + strconv.Itoa(i+1), "surfaceRaised", NonTextContrast, "chart series " + strconv.Itoa(i+1)})
	}
	for _, pr := range pairs {
		ratio, err := Contrast(colors[pr.Foreground], colors[pr.Background])
		if err != nil || ratio+1e-9 < pr.Minimum {
			out = append(out, Problem{
				Mode: mode, Kind: "contrast", Foreground: pr.Foreground, Background: pr.Background,
				Ratio: round2(ratio), Minimum: pr.Minimum, Purpose: pr.Purpose,
			})
		}
	}
	for _, m := range Meanings {
		hue, sat, err := Hue(colors[m.Token])
		if err != nil {
			continue
		}
		ok := sat >= 0.25
		if ok {
			ok = false
			for _, r := range m.Hues {
				if hue >= r[0] && hue <= r[1] {
					ok = true
				}
			}
		}
		if !ok {
			out = append(out, Problem{Mode: mode, Kind: "meaning", Foreground: m.Token, Purpose: m.Name})
		}
	}
	return out
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
