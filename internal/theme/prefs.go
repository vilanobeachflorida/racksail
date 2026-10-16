package theme

import (
	"fmt"
	"slices"
)

// Preferences are one person's display settings, the top layer above every
// theme. Empty fields mean the default: follow the system for mode,
// contrast and motion, the theme's density and the normal text size.
type Preferences struct {
	// Mode is system, light or dark.
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Contrast is system, standard or high.
	Contrast string `json:"contrast,omitempty" yaml:"contrast,omitempty"`
	// Density is theme, comfortable or compact.
	Density string `json:"density,omitempty" yaml:"density,omitempty"`
	// TextSize is default, small, large, larger or largest.
	TextSize string `json:"textSize,omitempty" yaml:"textSize,omitempty"`
	// Motion is system, reduce or full.
	Motion string `json:"motion,omitempty" yaml:"motion,omitempty"`
}

// The values each preference takes; the first is the default.
var (
	Modes         = []string{"system", "light", "dark"}
	Contrasts     = []string{"system", "standard", "high"}
	Densities     = []string{"theme", DensityComfortable, DensityCompact}
	TextSizeNames = []string{"default", "small", "large", "larger", "largest"}
	Motions       = []string{"system", "reduce", "full"}
)

// Validate reports a preference set to a value it doesn't take.
func (p Preferences) Validate() error {
	for _, f := range []struct {
		name, value string
		allowed     []string
	}{
		{"mode", p.Mode, Modes}, {"contrast", p.Contrast, Contrasts}, {"density", p.Density, Densities},
		{"textSize", p.TextSize, TextSizeNames}, {"motion", p.Motion, Motions},
	} {
		if f.value != "" && !slices.Contains(f.allowed, f.value) {
			return fmt.Errorf("%s can't be %q; use one of %v", f.name, f.value, f.allowed)
		}
	}
	return nil
}

// Attributes returns the attributes of the root element that apply the
// preferences through the stylesheet CSS renders. Defaults set nothing, so
// the system settings and the theme decide.
func (p Preferences) Attributes() map[string]string {
	attrs := map[string]string{}
	set := func(attr, value string, defaults []string) {
		if value != "" && value != defaults[0] {
			attrs[attr] = value
		}
	}
	set("data-theme", p.Mode, Modes)
	set("data-contrast", p.Contrast, Contrasts)
	set("data-density", p.Density, Densities)
	set("data-text-size", p.TextSize, TextSizeNames)
	set("data-motion", p.Motion, Motions)
	return attrs
}
