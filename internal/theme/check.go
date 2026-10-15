package theme

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Contrast minimums from WCAG 2.2 AA: 4.5:1 for text and 3:1 for
// everything else people need to see, such as control outlines, focus
// rings, icons and chart marks.
const (
	TextContrast    = 4.5
	NonTextContrast = 3
)

// How many chart colors a theme has, from the schema.
const (
	MinChartColors = 6
	MaxChartColors = 12
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

// ConsolePairs are the contrasts of the console palette. Everything a
// terminal shows is text on its background.
var ConsolePairs = []Pair{
	{"foreground", "background", TextContrast, "text in the installer, the console and the CLI"},
	{"accent", "background", TextContrast, "highlights and the selected option"},
	{"success", "background", TextContrast, "messages about things that worked"},
	{"warning", "background", TextContrast, "warnings"},
	{"danger", "background", TextContrast, "errors"},
}

// TranslucentTokens are the only color tokens that may have an alpha
// channel. Everything else is measured for contrast, and a translucent
// color's contrast depends on whatever shows through it.
var TranslucentTokens = []string{"overlay"}

// BundledFonts are the font families the panel serves itself. Any other
// family a theme names comes with its font files.
var BundledFonts = []string{"Inter Variable", "JetBrains Mono Variable"}

// Meaning keeps a status color recognizable, so a theme can't make a
// destructive button look like a safe one.
type Meaning struct {
	Token string `json:"token"`
	// Hues are the ranges, in degrees, the color's hue must fall in.
	Hues [][2]float64 `json:"hues"`
	Name string       `json:"name"`
}

// Meanings are the status colors' required hues, generous enough for any
// shade of red, amber, green or blue. The console palette's status colors
// keep theirs too.
var Meanings = []Meaning{
	{"danger", [][2]float64{{330, 360}, {0, 20}}, "red"},
	{"warning", [][2]float64{{20, 65}}, "amber or orange"},
	{"success", [][2]float64{{75, 175}}, "green"},
	{"info", [][2]float64{{180, 265}}, "blue"},
}

// Where a problem is: one of the palettes, or the type tokens.
const (
	ModeLight      = "light"
	ModeDark       = "dark"
	ModeConsole    = "console"
	ModeTypography = "typography"
)

// Kinds of problems.
const (
	ProblemContrast = "contrast"
	ProblemMeaning  = "meaning"
	ProblemColor    = "color"
	ProblemAlpha    = "alpha"
	ProblemFont     = "font"
)

// Problem is something wrong with a theme's tokens.
type Problem struct {
	// Mode is where the problem is: light, dark, console or typography.
	Mode string `json:"mode"`
	// Kind is contrast, meaning, color, alpha or font.
	Kind string `json:"kind"`
	// Foreground is the token with the problem: a color, or sans or mono
	// for a font.
	Foreground string  `json:"foreground"`
	Background string  `json:"background,omitempty"`
	Ratio      float64 `json:"ratio,omitempty"`
	Minimum    float64 `json:"minimum,omitempty"`
	// Purpose says more: where the pair appears, the hue a status color
	// needs, why a color doesn't parse, or the font family.
	Purpose string `json:"purpose,omitempty"`
}

func (p Problem) where() string {
	switch p.Mode {
	case ModeConsole:
		return "console palette"
	case ModeTypography:
		return "typography"
	}
	return p.Mode + " mode"
}

func (p Problem) Error() string {
	switch p.Kind {
	case ProblemMeaning:
		return fmt.Sprintf("%s: %s must be %s so it keeps its meaning", p.where(), p.Foreground, p.Purpose)
	case ProblemColor:
		return fmt.Sprintf("%s: %s isn't a color: %s", p.where(), p.Foreground, p.Purpose)
	case ProblemAlpha:
		return fmt.Sprintf("%s: %s can't be translucent; only %s may have an alpha channel", p.where(), p.Foreground, strings.Join(TranslucentTokens, ", "))
	case ProblemFont:
		return fmt.Sprintf("tokens.typography.%s: the panel doesn't bundle %q, so the theme package has to bring its font files", p.Foreground, p.Purpose)
	}
	return fmt.Sprintf("%s: %s on %s has contrast %.2f:1, needs at least %.1f:1 for %s",
		p.where(), p.Foreground, p.Background, p.Ratio, p.Minimum, p.Purpose)
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

// Colors returns the console palette by name, for checks.
func (c Console) Colors() map[string]Color {
	return map[string]Color{
		"background": c.Background, "foreground": c.Foreground, "accent": c.Accent,
		"success": c.Success, "warning": c.Warning, "danger": c.Danger,
	}
}

// Effective returns the console palette a theme gives terminals: its own
// colors, and the default theme's where it sets none.
func (c Console) Effective() Console {
	pick := func(own, def Color) Color {
		if own != "" {
			return own
		}
		return def
	}
	d := defaultConsole
	return Console{
		Background: pick(c.Background, d.Background), Foreground: pick(c.Foreground, d.Foreground),
		Accent: pick(c.Accent, d.Accent), Success: pick(c.Success, d.Success),
		Warning: pick(c.Warning, d.Warning), Danger: pick(c.Danger, d.Danger),
	}
}

// Check returns everything wrong with the theme's tokens: colors that
// don't parse or are translucent where they can't be, contrasts below WCAG
// AA in both palettes and the console palette, status colors that lost
// their meaning, and fonts the panel doesn't bundle that come without
// files. The order is fixed: light, dark, console, then typography.
func (t *Theme) Check() []Problem {
	var out []Problem
	out = append(out, checkColors(ModeLight, t.Tokens.Light.Colors(), paletteChecks(t.Tokens.Light))...)
	out = append(out, checkColors(ModeDark, t.Tokens.Dark.Colors(), paletteChecks(t.Tokens.Dark))...)
	out = append(out, checkColors(ModeConsole, t.Console.Effective().Colors(), ConsolePairs)...)
	out = append(out, t.checkFonts()...)
	return out
}

// paletteChecks are the pairs checked in a palette: the fixed ones and one
// per chart color.
func paletteChecks(p Palette) []Pair {
	pairs := slices.Clone(Pairs)
	for i := range p.Chart {
		pairs = append(pairs, Pair{"chart" + strconv.Itoa(i+1), "surfaceRaised", NonTextContrast, "chart series " + strconv.Itoa(i+1)})
	}
	return pairs
}

func checkColors(mode string, colors map[string]Color, pairs []Pair) []Problem {
	var out []Problem
	names := make([]string, 0, len(colors))
	for name := range colors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := colors[name]
		if c == "" {
			continue
		}
		if _, err := c.Parse(); err != nil {
			out = append(out, Problem{Mode: mode, Kind: ProblemColor, Foreground: name, Purpose: err.Error()})
		} else if c.HasAlpha() && (mode == ModeConsole || !slices.Contains(TranslucentTokens, name)) {
			out = append(out, Problem{Mode: mode, Kind: ProblemAlpha, Foreground: name})
		}
	}
	for _, pr := range pairs {
		fg, bg := colors[pr.Foreground], colors[pr.Background]
		if fg.HasAlpha() || bg.HasAlpha() {
			// Reported above; a contrast of a translucent color means nothing.
			continue
		}
		ratio, err := Contrast(fg, bg)
		if err != nil || ratio+1e-9 < pr.Minimum {
			out = append(out, Problem{
				Mode: mode, Kind: ProblemContrast, Foreground: pr.Foreground, Background: pr.Background,
				Ratio: round2(ratio), Minimum: pr.Minimum, Purpose: pr.Purpose,
			})
		}
	}
	for _, m := range Meanings {
		c, ok := colors[m.Token]
		if !ok {
			continue
		}
		hue, sat, err := Hue(c)
		if err != nil {
			continue
		}
		fits := false
		if sat >= 0.25 {
			for _, r := range m.Hues {
				fits = fits || hue >= r[0] && hue <= r[1]
			}
		}
		if !fits {
			out = append(out, Problem{Mode: mode, Kind: ProblemMeaning, Foreground: m.Token, Purpose: m.Name})
		}
	}
	return out
}

// checkFonts reports families the panel doesn't serve that come without
// files. Whether the files are in the package is checked with the package.
func (t *Theme) checkFonts() []Problem {
	var out []Problem
	for _, f := range []struct {
		name string
		font Font
	}{{"sans", t.Tokens.Typography.Sans}, {"mono", t.Tokens.Typography.Mono}} {
		if f.font.Family == "" || len(f.font.Files) > 0 || IsBundledFont(f.font.Family) {
			continue
		}
		out = append(out, Problem{Mode: ModeTypography, Kind: ProblemFont, Foreground: f.name, Purpose: f.font.Family})
	}
	return out
}

// IsBundledFont reports whether the panel serves a font family itself.
func IsBundledFont(family string) bool {
	return slices.ContainsFunc(BundledFonts, func(b string) bool { return strings.EqualFold(b, family) })
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
