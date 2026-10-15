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
	if strings.Contains(th.LayerCSS(nil), "@font-face") {
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
