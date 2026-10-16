package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCSS(t *testing.T) {
	d := Default()
	css := d.CSS()
	for _, want := range []string{
		"@layer rs-theme, rs-custom, rs-preferences, rs-protected;",
		"--rs-surface-raised: #FFFFFF;",
		"--rs-brand: #01214F;",
		"--rs-chart1: #007CFD;",
		"--rs-accent-subtle: #E0ECF9;",
		"--rs-overlay: #01214F66;",
		`--rs-font-sans: "Inter Variable", ui-sans-serif`,
		"--rs-text-sm: 0.8889rem;",
		"--rs-leading-4xl: 1.2;",
		"--rs-control-md: 2.25rem;",
		"--rs-focus-width: 2px;",
		`:root[data-theme="dark"]`,
		"@media (prefers-color-scheme: dark)",
		`:root:not([data-theme="light"])`,
		`:root[data-density="compact"]`,
		`:root[data-text-size="largest"]`,
		"--rs-text-zoom: 1.5;",
		`:root[data-contrast="high"]`,
		"@media (prefers-contrast: more)",
		`:root:not([data-contrast])`,
		`:root[data-motion="reduce"]`,
		"@media (prefers-reduced-motion: reduce)",
		"--rs-motion-normal: 0ms;",
		"--rs-critical-danger: #B3261E;",
		"--rs-critical-danger: #FF7A7A;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS lacks %q", want)
		}
	}
	// Preferences and the protected palette come after the theme, in
	// layers that rank above it.
	theme, prefs, protected := strings.Index(css, "@layer rs-theme {"), strings.Index(css, "@layer rs-preferences {"), strings.Index(css, "@layer rs-protected {")
	if !(theme > 0 && prefs > theme && protected > prefs) {
		t.Errorf("layers out of order: %d %d %d", theme, prefs, protected)
	}
	if strings.Count(css, "{") != strings.Count(css, "}") {
		t.Error("unbalanced braces")
	}
	if cssVar("surfaceRaised") != "--rs-surface-raised" || cssVar("chart1") != "--rs-chart1" || property("text-2xl") != "--rs-text-2xl" {
		t.Error("custom property names are wrong")
	}
}

func TestLayerCSS(t *testing.T) {
	th := Compact()
	th.Tokens.Typography.Sans = Font{Family: "Acme Sans", Files: []string{"fonts/acme.woff2", "fonts/acme.woff"}}
	css := th.LayerCSS(func(p string) string { return "/api/v1/themes/acme/files/" + p })
	for _, want := range []string{
		`font-family: "Acme Sans";`,
		`src: url("/api/v1/themes/acme/files/fonts/acme.woff2") format("woff2");`,
		`format("woff");`,
		"@layer rs-theme {",
		"--rs-control-md: 2rem;",
		"--rs-radius-md: 4px;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("layer CSS lacks %q", want)
		}
	}
	for _, unwanted := range []string{"rs-preferences", "rs-protected", "critical"} {
		if strings.Contains(css, unwanted) {
			t.Errorf("a theme's layer can't contain %q", unwanted)
		}
	}
	if strings.Contains(th.LayerCSS(nil), "@font-face") || strings.Contains(th.LayerCSS(func(string) string { return "" }), "@font-face") {
		t.Error("fonts need a URL")
	}
}

func TestTypeScale(t *testing.T) {
	vars := map[string]string{}
	for _, v := range typeScale(1.25) {
		vars[v[0]] = v[1]
	}
	if vars["text-base"] != "1rem" || vars["text-lg"] != "1.25rem" || vars["text-sm"] != "0.8rem" || vars["text-xs"] != "0.64rem" {
		t.Errorf("scale = %v", vars)
	}
	if vars["leading-base"] != "1.5" || vars["leading-xl"] != "1.38" {
		t.Errorf("leading = %v", vars)
	}
	if len(typeScale(0)) != 2*len(TypeSteps) {
		t.Error("a missing ratio falls back to the default")
	}
}

// The UI's stylesheet is generated from the default theme; cmd/racksail
// checks it's current, and this checks the copy the docs describe.
func TestStylesheetIsTheDefaultTheme(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("..", "..", "ui", "src", "design", "theme.css"))
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if string(committed) != d.CSS() {
		t.Error("ui/src/design/theme.css is out of date; run make gen")
	}
}

// A layer with fewer chart colors than the one below it clears the rest,
// so charts never use a color that wasn't checked against its surfaces.
func TestLayerCSSClearsUnusedChartSlots(t *testing.T) {
	th := Default()
	th.Tokens.Light.Chart = th.Tokens.Light.Chart[:6]
	th.Tokens.Dark.Chart = append(append([]Color{}, th.Tokens.Dark.Chart...), "#7D8CFF", "#D07CE0", "#4FB8B0", "#C9A227")
	css := th.LayerCSS(nil)
	light := css[:strings.Index(css, `[data-theme="dark"]`)]
	for _, want := range []string{"--rs-chart6: #008300;", "--rs-chart7: initial;", "--rs-chart12: initial;"} {
		if !strings.Contains(light, want) {
			t.Errorf("light block lacks %q", want)
		}
	}
	dark := css[strings.Index(css, `[data-theme="dark"]`):]
	if !strings.Contains(dark, "--rs-chart12: #C9A227;") {
		t.Error("the dark block has all twelve colors")
	}
	if strings.Count(css, "--rs-chart13") != 0 {
		t.Error("there are at most twelve chart colors")
	}
	// High contrast has eight, so a theme's ninth and later don't show
	// with it either.
	d := Default()
	full := d.CSS()
	hc := full[strings.Index(full, `:root[data-contrast="high"]`):]
	if !strings.Contains(hc, "--rs-chart9: initial;") {
		t.Error("high contrast clears chart colors past its own")
	}
}

func TestShadows(t *testing.T) {
	d := Default()
	for _, ok := range []string{
		d.Tokens.Shadows.SM, HighContrast().Tokens.Shadows.LG, "none", "inset 0 0 0 1px #FFFFFF",
		"0 1px 2px rgba(1, 33, 79, 0.08), 0 0 0 1px #0000001A", "0 0 .5rem -1px hsl(210 50% 40% / 0.2)",
		"0 1px 2px color(display-p3 0 0 0 / 0.1)", "0 2px 4px transparent",
	} {
		if !shadowPattern.MatchString(ok) {
			t.Errorf("%q should be a shadow", ok)
		}
	}
	for _, bad := range []string{
		"", "0 1px 2px rgba(0, 0, 0, 0.1", "0 1px 2px rgba(0, 0, 0, (0.1))", "0 1px url(x)", "0 1px var(--rs-brand)",
		"0 1px 2px #000;}", "1px2px", "0 1px 2px #000,", ", 0 1px #000", "0 1px 2px rgba(0, 0, 0, 0.1))",
	} {
		if shadowPattern.MatchString(bad) {
			t.Errorf("%q shouldn't be a shadow", bad)
		}
	}

	// A theme built in code can't get an unclosed function into the
	// stylesheet, where it would swallow the rest of the layer.
	d.Tokens.Shadows.SM = "0 1px 2px rgba(0, 0, 0, 0.1"
	d.Tokens.Shadows.LG = "0 12px 32px rgba(1, 33, 79, 0.14), inset 0 0 0 1px #FFFFFF"
	css := d.LayerCSS(nil)
	if strings.Contains(css, "--rs-shadow-sm") || !strings.Contains(css, "--rs-shadow-lg: "+d.Tokens.Shadows.LG+";") {
		t.Errorf("shadows in the layer:\n%s", css)
	}
	if strings.Count(css, "(") != strings.Count(css, ")") || strings.Count(css, "{") != strings.Count(css, "}") {
		t.Errorf("the layer doesn't balance:\n%s", css)
	}
	if _, err := Parse(encode(t, d)); err == nil || !strings.Contains(err.Error(), "shadows/sm") {
		t.Errorf("an unclosed shadow: %v", err)
	}
}
