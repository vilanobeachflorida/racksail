package theme

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/spec"
)

func TestDefaultThemeIsValid(t *testing.T) {
	d := Default()
	if problems := d.Check(); len(problems) > 0 {
		for _, p := range problems {
			t.Error(p)
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateJSON(spec.Theme, raw); err != nil {
		t.Errorf("the default theme doesn't match its schema: %v", err)
	}
	if d.Tokens.Light.Brand != BrandNavy || d.Tokens.Light.Focus != BrandBlue {
		t.Error("the default theme uses the brand colors")
	}
}

func TestContrast(t *testing.T) {
	tests := []struct {
		a, b Color
		want float64
	}{
		{"#000000", "#FFFFFF", 21},
		{"#FFFFFF", "#FFFFFF", 1},
		{"#777777", "#FFFFFF", 4.48},
		{"#007CFD", "#FFFFFF", 3.96},
	}
	for _, tt := range tests {
		got, err := Contrast(tt.a, tt.b)
		if err != nil || math.Abs(got-tt.want) > 0.02 {
			t.Errorf("Contrast(%s, %s) = %.3f, %v; want %.2f", tt.a, tt.b, got, err, tt.want)
		}
	}
	if _, err := Contrast("blue", "#FFFFFF"); err == nil {
		t.Error("named colors aren't accepted")
	}
}

func TestParseRejectsBadThemes(t *testing.T) {
	d := Default()
	raw, _ := yaml.Marshal(d)
	if _, err := Parse(raw); err != nil {
		t.Fatalf("the default theme should parse: %v", err)
	}
	lowContrast := d
	lowContrast.Tokens.Light.TextMuted = "#C0C8D0"
	raw, _ = yaml.Marshal(lowContrast)
	if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), "textMuted") {
		t.Errorf("low contrast should be refused: %v", err)
	}
	remote := d
	remote.Assets.LogoLight = "https://example.com/logo.png"
	raw, _ = yaml.Marshal(remote)
	if _, err := Parse(raw); err == nil {
		t.Error("themes can't load files from the network")
	}
	if _, err := Parse([]byte("format: 1\nid: x\nname: x\nversion: 1.0.0\nlicense: MIT\ntokens: {}\n")); err == nil {
		t.Error("a theme without palettes should be refused")
	}
}

func TestCSS(t *testing.T) {
	d := Default()
	css := d.CSS()
	for _, want := range []string{
		"--rs-surface-raised: #FFFFFF;",
		"--rs-brand: #01214F;",
		"--rs-chart1: #007CFD;",
		`--rs-font-sans: "Inter Variable", ui-sans-serif`,
		`:root[data-theme="dark"]`,
		"@media (prefers-color-scheme: dark)",
		`:root:not([data-theme="light"])`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS lacks %q", want)
		}
	}
	if cssVar("surfaceRaised") != "--rs-surface-raised" || cssVar("chart1") != "--rs-chart1" {
		t.Error("css variable names are wrong")
	}
}

func TestOverlay(t *testing.T) {
	base := Default()
	layer := Theme{Name: "Reseller", Tokens: Tokens{Light: Palette{Brand: "#5B21B6"}}}
	merged, err := Overlay(base, layer)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Name != "Reseller" || merged.Tokens.Light.Brand != "#5B21B6" || merged.Tokens.Light.Text != base.Tokens.Light.Text || merged.Format != 1 || len(merged.Tokens.Light.Chart) != 8 {
		t.Errorf("merged = %+v", merged)
	}
}
