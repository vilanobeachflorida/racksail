package theme

import (
	"maps"
	"strings"
	"testing"
)

func TestPreferences(t *testing.T) {
	if err := (Preferences{}).Validate(); err != nil {
		t.Errorf("defaults: %v", err)
	}
	if got := (Preferences{}).Attributes(); len(got) != 0 {
		t.Errorf("defaults set %v", got)
	}
	p := Preferences{Mode: "dark", Contrast: "high", Density: DensityCompact, TextSize: "larger", Motion: "reduce"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"data-theme": "dark", "data-contrast": "high", "data-density": "compact",
		"data-text-size": "larger", "data-motion": "reduce",
	}
	if got := p.Attributes(); !maps.Equal(got, want) {
		t.Errorf("attributes = %v", got)
	}
	explicitDefaults := Preferences{Mode: "system", Contrast: "system", Density: "theme", TextSize: "default", Motion: "system"}
	if got := explicitDefaults.Attributes(); len(got) != 0 {
		t.Errorf("explicit defaults set %v", got)
	}
	for _, bad := range []Preferences{{Mode: "sepia"}, {Contrast: "low"}, {Density: "airy"}, {TextSize: "huge"}, {Motion: "none"}} {
		if bad.Validate() == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
}

func TestEveryPreferenceHasCSS(t *testing.T) {
	d := Default()
	css := d.CSS()
	for _, attr := range []struct {
		name   string
		values []string
	}{
		{"data-theme", Modes[1:]}, {"data-contrast", []string{"high"}}, {"data-density", Densities[1:]},
		{"data-text-size", TextSizeNames[1:]}, {"data-motion", []string{"reduce"}},
	} {
		for _, v := range attr.values {
			sel := "[" + attr.name + "=\"" + v + "\"]"
			if !strings.Contains(css, sel) {
				t.Errorf("no CSS for %s", sel)
			}
		}
	}
}
