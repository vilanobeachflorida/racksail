package theme

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/vilanobeachflorida/racksail/spec"
)

// Cascade layers, lowest first. Themes sit at the bottom, an admin's custom
// CSS above them, personal preferences above both, and the protected
// palette on top, so nothing a theme or custom CSS does can override
// preferences or the colors of safety-critical UI.
const (
	LayerTheme       = "rs-theme"
	LayerCustom      = "rs-custom"
	LayerPreferences = "rs-preferences"
	LayerProtected   = "rs-protected"
)

// cssVar turns a token name such as surfaceRaised into --rs-surface-raised.
func cssVar(name string) string {
	var b strings.Builder
	b.WriteString("--rs-")
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('-')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// paletteVars lists a palette's custom properties in a fixed order: the
// tokens themes set, the chart colors, then the derived tokens. Every chart
// slot is written: the ones a palette has no color for become initial, so a
// layer with fewer chart colors than the one below it doesn't inherit
// colors that were never checked against its surfaces.
func paletteVars(p Palette) [][2]string {
	vars := [][2]string{
		{"surface", string(p.Surface)}, {"surfaceRaised", string(p.SurfaceRaised)}, {"surfaceSunken", string(p.SurfaceSunken)},
		{"text", string(p.Text)}, {"textMuted", string(p.TextMuted)}, {"border", string(p.Border)}, {"borderStrong", string(p.BorderStrong)},
		{"brand", string(p.Brand)}, {"brandText", string(p.BrandText)}, {"accent", string(p.Accent)}, {"accentText", string(p.AccentText)},
		{"success", string(p.Success)}, {"successText", string(p.SuccessText)}, {"warning", string(p.Warning)}, {"warningText", string(p.WarningText)},
		{"danger", string(p.Danger)}, {"dangerText", string(p.DangerText)}, {"info", string(p.Info)}, {"infoText", string(p.InfoText)},
		{"focus", string(p.Focus)}, {"overlay", string(p.Overlay)},
	}
	for i := range MaxChartColors {
		value := "initial"
		if i < len(p.Chart) {
			value = string(p.Chart[i])
		}
		vars = append(vars, [2]string{"chart" + strconv.Itoa(i+1), value})
	}
	colors := p.Colors()
	for _, d := range Derivations {
		vars = append(vars, [2]string{d.Name, string(colors[d.Name])})
	}
	return vars
}

func trimFloat(v float64, decimals int) string {
	return strconv.FormatFloat(math.Round(v*math.Pow10(decimals))/math.Pow10(decimals), 'f', -1, 64)
}

// TypeSteps are the steps of the type scale and their exponent: each step
// is the scale ratio times the one below, with base the base size.
var TypeSteps = []struct {
	Name     string
	Exponent int
}{{"xs", -2}, {"sm", -1}, {"base", 0}, {"lg", 1}, {"xl", 2}, {"2xl", 3}, {"3xl", 4}, {"4xl", 5}}

// typeScale lists the type scale in rem, so it follows the text-size
// preference, with a line height for each step that tightens as text grows.
func typeScale(ratio float64) [][2]string {
	if ratio <= 0 {
		ratio = Default().Tokens.Typography.Scale
	}
	var vars [][2]string
	for _, s := range TypeSteps {
		size := math.Pow(ratio, float64(s.Exponent))
		leading := math.Max(1.2, 1.5-0.06*float64(max(s.Exponent, 0)))
		vars = append(vars,
			[2]string{"text-" + s.Name, trimFloat(size, 4) + "rem"},
			[2]string{"leading-" + s.Name, trimFloat(leading, 2)})
	}
	return vars
}

// densityVars are the sizes density sets: control heights, table rows,
// cell padding and gaps, in rem so they grow with the text size.
var densityVars = map[string][][2]string{
	DensityComfortable: {
		{"control-sm", "2rem"}, {"control-md", "2.25rem"}, {"control-lg", "2.5rem"}, {"row", "2.75rem"},
		{"cell-x", "0.75rem"}, {"cell-y", "0.625rem"}, {"gap", "1rem"}, {"card-pad", "1.25rem"},
	},
	DensityCompact: {
		{"control-sm", "1.75rem"}, {"control-md", "2rem"}, {"control-lg", "2.25rem"}, {"row", "2.25rem"},
		{"cell-x", "0.5rem"}, {"cell-y", "0.375rem"}, {"gap", "0.75rem"}, {"card-pad", "1rem"},
	},
}

// TextSizes are the text-size preference's steps and how much each scales
// all text.
var TextSizes = []struct {
	Name string
	Zoom string
}{{"small", "0.875"}, {"large", "1.125"}, {"larger", "1.25"}, {"largest", "1.5"}}

// shadowPattern is the theme schema's pattern for shadows, whose color
// functions take numbers only. Parse refuses shadows that don't match it,
// and the renderer leaves them out, so not even a theme built in code can
// leave a parenthesis open and swallow the rest of its layer.
var shadowPattern = regexp.MustCompile(schemaPattern("shadow"))

// schemaPattern returns the pattern of a definition in the theme schema.
func schemaPattern(def string) string {
	raw, err := spec.SchemaJSON(spec.Theme)
	if err != nil {
		panic(err)
	}
	var schema struct {
		Defs map[string]struct {
			Pattern string `json:"pattern"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		panic(fmt.Sprintf("reading the theme schema: %v", err))
	}
	pattern := schema.Defs[def].Pattern
	if pattern == "" {
		panic("the theme schema has no pattern for " + def)
	}
	return pattern
}

// cssShadow is a shadow as the stylesheet gets it: as it is when the schema
// allows it, and left out otherwise.
func cssShadow(s string) string {
	if shadowPattern.MatchString(s) {
		return s
	}
	return ""
}

// sharedVars are the tokens both modes share.
func sharedVars(tok Tokens) [][2]string {
	vars := [][2]string{
		{"fontSans", fontStack(tok.Typography.Sans.Family, "ui-sans-serif, system-ui, sans-serif")},
		{"fontMono", fontStack(tok.Typography.Mono.Family, "ui-monospace, monospace")},
		{"fontSizeBase", tok.Typography.BaseSize},
	}
	if w := tok.Typography.Weights; w.Regular != 0 {
		vars = append(vars,
			[2]string{"weightRegular", strconv.Itoa(w.Regular)}, [2]string{"weightMedium", strconv.Itoa(w.Medium)},
			[2]string{"weightSemibold", strconv.Itoa(w.Semibold)}, [2]string{"weightBold", strconv.Itoa(w.Bold)})
	}
	if tok.Typography.Scale != 0 {
		vars = append(vars, typeScale(tok.Typography.Scale)...)
	}
	vars = append(vars,
		[2]string{"radiusSm", tok.Radius.SM}, [2]string{"radiusMd", tok.Radius.MD}, [2]string{"radiusLg", tok.Radius.LG}, [2]string{"radiusXl", tok.Radius.XL},
		[2]string{"shadowSm", cssShadow(tok.Shadows.SM)}, [2]string{"shadowMd", cssShadow(tok.Shadows.MD)}, [2]string{"shadowLg", cssShadow(tok.Shadows.LG)},
		[2]string{"motionFast", tok.Motion.Fast}, [2]string{"motionNormal", tok.Motion.Normal}, [2]string{"motionSlow", tok.Motion.Slow},
		[2]string{"focusWidth", tok.FocusRing.Width}, [2]string{"focusOffset", tok.FocusRing.Offset},
	)
	if d, ok := densityVars[tok.Density]; ok {
		vars = append(vars, d...)
	}
	return vars
}

// property turns a token name into its custom property; names that already
// contain a dash, such as text-sm, only get the prefix.
func property(token string) string {
	if strings.Contains(token, "-") {
		return "--rs-" + token
	}
	return cssVar(token)
}

type cssWriter struct {
	b      strings.Builder
	indent string
}

func (w *cssWriter) line(format string, args ...any) {
	if format == "" {
		w.b.WriteByte('\n')
		return
	}
	w.b.WriteString(w.indent)
	fmt.Fprintf(&w.b, format, args...)
	w.b.WriteByte('\n')
}

func (w *cssWriter) open(format string, args ...any) {
	w.line(format+" {", args...)
	w.indent += "  "
}

func (w *cssWriter) close() {
	w.indent = w.indent[:len(w.indent)-2]
	w.line("}")
}

func (w *cssWriter) block(selector string, vars ...[][2]string) {
	w.open("%s", selector)
	for _, group := range vars {
		for _, v := range group {
			if v[1] != "" {
				w.line("%s: %s;", property(v[0]), v[1])
			}
		}
	}
	w.close()
}

// modeBlocks writes a pair of palettes: light on base, dark when the page
// says data-theme="dark", or when it doesn't pick a mode and the system is
// dark. sel(extra) builds a selector with an extra condition.
func (w *cssWriter) modeBlocks(base string, light, dark [][2]string, extra ...[][2]string) {
	w.block(base, append([][][2]string{light}, extra...)...)
	w.line("")
	w.block(base+`[data-theme="dark"]`, dark)
	w.line("")
	w.open("@media (prefers-color-scheme: dark)")
	w.block(base+`:not([data-theme="light"])`, dark)
	w.close()
}

func (w *cssWriter) themeLayer(t *Theme) {
	tok := t.Tokens
	w.open("@layer %s", LayerTheme)
	w.modeBlocks(":root", paletteVars(tok.Light), paletteVars(tok.Dark), sharedVars(tok))
	w.close()
}

// LayerCSS renders the theme alone, inside the theme layer, as the panel
// applies server and reseller themes on top of the default theme's
// stylesheet. fontURL turns a font file in the theme package into the URL
// the panel serves it from, or "" for one it doesn't serve; fonts are left
// out when it is nil.
func (t *Theme) LayerCSS(fontURL func(path string) string) string {
	var w cssWriter
	if fontURL != nil {
		for _, f := range []Font{t.Tokens.Typography.Sans, t.Tokens.Typography.Mono} {
			for _, file := range f.Files {
				url := fontURL(file)
				if url == "" {
					continue
				}
				w.open("@font-face")
				w.line("font-family: %q;", f.Family)
				w.line("src: url(%q) format(%q);", url, fontFormat(file))
				w.line("font-weight: 100 900;")
				w.line("font-display: swap;")
				w.close()
			}
		}
	}
	w.themeLayer(t)
	return w.b.String()
}

func fontFormat(file string) string {
	if strings.HasSuffix(strings.ToLower(file), ".woff") {
		return "woff"
	}
	return "woff2"
}

// CSS renders the theme as the panel's whole token stylesheet: the theme
// itself, the blocks that apply personal preferences (density, text size,
// high contrast and reduced motion) and the protected palette. Preferences
// are attributes on the root element; without one, the system setting
// decides where there is one.
func (t *Theme) CSS() string {
	var w cssWriter
	w.line("/* Generated from the %s theme by `racksail theme css`. Don't edit by hand. */", t.Name)
	w.line("@layer %s, %s, %s, %s;", LayerTheme, LayerCustom, LayerPreferences, LayerProtected)
	w.line("")
	w.themeLayer(t)
	w.line("")

	hc := HighContrast().Tokens
	hcShared := [][2]string{
		{"focusWidth", hc.FocusRing.Width},
		{"shadowSm", hc.Shadows.SM}, {"shadowMd", hc.Shadows.MD}, {"shadowLg", hc.Shadows.LG},
	}
	reduced := [][2]string{{"motionFast", "0ms"}, {"motionNormal", "0ms"}, {"motionSlow", "0ms"}}
	w.open("@layer %s", LayerPreferences)
	for _, d := range []string{DensityComfortable, DensityCompact} {
		w.block(fmt.Sprintf(`:root[data-density=%q]`, d), densityVars[d])
		w.line("")
	}
	for _, s := range TextSizes {
		w.block(fmt.Sprintf(`:root[data-text-size=%q]`, s.Name), [][2]string{{"text-zoom", s.Zoom}})
		w.line("")
	}
	w.modeBlocks(`:root[data-contrast="high"]`, paletteVars(hc.Light), paletteVars(hc.Dark), hcShared)
	w.line("")
	w.open("@media (prefers-contrast: more)")
	w.modeBlocks(":root:not([data-contrast])", paletteVars(hc.Light), paletteVars(hc.Dark), hcShared)
	w.close()
	w.line("")
	w.block(`:root[data-motion="reduce"]`, reduced)
	w.line("")
	w.open("@media (prefers-reduced-motion: reduce)")
	w.block(":root:not([data-motion])", reduced)
	w.close()
	w.close()
	w.line("")

	w.open("@layer %s", LayerProtected)
	w.modeBlocks(":root", ProtectedLight.vars(), ProtectedDark.vars())
	w.line("")
	w.modeBlocks(`:root[data-contrast="high"]`, ProtectedHighContrastLight.vars(), ProtectedHighContrastDark.vars())
	w.line("")
	w.open("@media (prefers-contrast: more)")
	w.modeBlocks(":root:not([data-contrast])", ProtectedHighContrastLight.vars(), ProtectedHighContrastDark.vars())
	w.close()
	w.close()
	return w.b.String()
}

func fontStack(family, fallback string) string {
	if family == "" {
		return fallback
	}
	return fmt.Sprintf("%q, %s", family, fallback)
}
