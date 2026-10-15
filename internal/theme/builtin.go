package theme

import "slices"

// Built-in theme ids.
const (
	DefaultID      = "racksail"
	HighContrastID = "high-contrast"
	CompactID      = "compact"
)

// The default palettes. Brand blue is too light for white text, so buttons
// and links use a darker shade of it in light mode, while brand blue itself
// is the focus ring and the first chart color. Chart colors were chosen so
// neighbors stay apart for people with color vision deficiencies and every
// series reaches 3:1 on cards.
var (
	defaultLight = Palette{
		Surface: "#F7F9FC", SurfaceRaised: "#FFFFFF", SurfaceSunken: "#EEF2F7",
		Text: "#0B1B33", TextMuted: "#4A5A70", Border: "#D8E0EA", BorderStrong: "#7A889C",
		Brand: BrandNavy, BrandText: "#FFFFFF", Accent: "#0062CC", AccentText: "#FFFFFF",
		Success: "#1A7F37", SuccessText: "#FFFFFF", Warning: "#9A6700", WarningText: "#FFFFFF",
		Danger: "#CF222E", DangerText: "#FFFFFF", Info: "#0062CC", InfoText: "#FFFFFF",
		Focus: BrandBlue, Overlay: "#01214F66",
		Chart: []Color{BrandBlue, "#E2622C", "#139A6B", "#B88200", "#D9558B", "#008300", "#4A3AA7", "#E34948"},
	}
	defaultDark = Palette{
		Surface: "#0A1626", SurfaceRaised: "#0F2036", SurfaceSunken: "#06101C",
		Text: "#E6EDF5", TextMuted: "#9FB0C4", Border: "#22344C", BorderStrong: "#5B6F88",
		Brand: BrandBlue, BrandText: "#00142F", Accent: "#3D9BFF", AccentText: "#00142F",
		Success: "#3FB950", SuccessText: "#00142F", Warning: "#D29922", WarningText: "#00142F",
		Danger: "#F85149", DangerText: "#00142F", Info: "#3D9BFF", InfoText: "#00142F",
		Focus: "#3D9BFF", Overlay: "#00000099",
		Chart: []Color{"#3390F7", "#D95926", "#199E70", "#C98500", "#D55181", "#1F9A1F", "#9085E9", "#E66767"},
	}
	highContrastLight = Palette{
		Surface: "#FFFFFF", SurfaceRaised: "#FFFFFF", SurfaceSunken: "#EBEBEB",
		Text: "#000000", TextMuted: "#3D3D3D", Border: "#6B6B6B", BorderStrong: "#000000",
		Brand: BrandNavy, BrandText: "#FFFFFF", Accent: "#0049B8", AccentText: "#FFFFFF",
		Success: "#0A6B26", SuccessText: "#FFFFFF", Warning: "#7A4F00", WarningText: "#FFFFFF",
		Danger: "#A3001B", DangerText: "#FFFFFF", Info: "#0049B8", InfoText: "#FFFFFF",
		Focus: "#0049B8", Overlay: "#000000B3",
		Chart: []Color{"#0049B8", "#B33F0A", "#00806A", "#946800", "#B4316F", "#2D6E12", "#4A3AA7", "#C0262B"},
	}
	highContrastDark = Palette{
		Surface: "#000000", SurfaceRaised: "#0B0B0B", SurfaceSunken: "#1C1C1C",
		Text: "#FFFFFF", TextMuted: "#D4D4D4", Border: "#8C8C8C", BorderStrong: "#FFFFFF",
		Brand: "#7EB8FF", BrandText: "#000000", Accent: "#8CC8FF", AccentText: "#000000",
		Success: "#6EE58A", SuccessText: "#000000", Warning: "#FFC94D", WarningText: "#000000",
		Danger: "#FF7A7A", DangerText: "#000000", Info: "#8CC8FF", InfoText: "#000000",
		Focus: "#FFD60A", Overlay: "#000000CC",
		Chart: []Color{"#3390F7", "#D95926", "#199E70", "#C98500", "#D55181", "#1F9A1F", "#9085E9", "#E66767"},
	}
)

func defaultTokens() Tokens {
	return Tokens{
		Light: clonePalette(defaultLight),
		Dark:  clonePalette(defaultDark),
		Typography: Typography{
			Sans: Font{Family: "Inter Variable"}, Mono: Font{Family: "JetBrains Mono Variable"},
			BaseSize: "16px", Scale: 1.125,
			Weights: Weights{Regular: 400, Medium: 500, Semibold: 600, Bold: 700},
		},
		Radius:  Sizes{SM: "4px", MD: "6px", LG: "10px", XL: "16px"},
		Density: DensityComfortable,
		Shadows: Sizes{
			SM: "0 1px 2px rgba(1, 33, 79, 0.08)",
			MD: "0 4px 12px rgba(1, 33, 79, 0.10)",
			LG: "0 12px 32px rgba(1, 33, 79, 0.14)",
		},
		Motion:    Motion{Fast: "120ms", Normal: "200ms", Slow: "320ms"},
		FocusRing: FocusRing{Width: "2px", Offset: "2px"},
	}
}

func clonePalette(p Palette) Palette {
	p.Chart = slices.Clone(p.Chart)
	return p
}

// defaultConsole is the terminal palette: light text and brand blues on navy.
var defaultConsole = Console{Background: BrandNavy, Foreground: "#E6EDF5", Accent: "#3D9BFF", Success: "#3FB950", Warning: "#D29922", Danger: "#F85149"}

// Default is the Racksail theme, the bottom layer of every panel.
func Default() Theme {
	return Theme{
		Format: 1, ID: DefaultID, Name: "Racksail", Version: "1.0.0", License: "MIT", Author: "Racksail",
		Description: "The default Racksail theme: navy and blue on calm surfaces.",
		Tokens:      defaultTokens(),
		Console:     defaultConsole,
	}
}

// HighContrast is the built-in high-contrast theme. Its palettes are also
// what the high-contrast preference applies on top of any theme.
func HighContrast() Theme {
	t := Default()
	t.ID, t.Name = HighContrastID, "High contrast"
	t.Description = "Black and white surfaces with strong borders, darker accents and a thicker focus ring."
	t.Tokens.Light = clonePalette(highContrastLight)
	t.Tokens.Dark = clonePalette(highContrastDark)
	t.Tokens.FocusRing = FocusRing{Width: "3px", Offset: "2px"}
	t.Tokens.Shadows = Sizes{SM: "0 0 0 1px rgba(0, 0, 0, 0.6)", MD: "0 0 0 2px rgba(0, 0, 0, 0.6)", LG: "0 0 0 2px rgba(0, 0, 0, 0.8)"}
	return t
}

// Compact is the built-in compact theme: the default colors with shorter
// controls, tighter tables and smaller corners, for large screens and
// people who want more on them.
func Compact() Theme {
	t := Default()
	t.ID, t.Name = CompactID, "Compact"
	t.Description = "The Racksail colors with shorter controls, tighter tables and smaller corners."
	t.Tokens.Density = DensityCompact
	t.Tokens.Radius = Sizes{SM: "3px", MD: "4px", LG: "6px", XL: "10px"}
	return t
}

// Builtins are the themes every panel has, the default first.
func Builtins() []Theme {
	return []Theme{Default(), HighContrast(), Compact()}
}

// Builtin returns the built-in theme with the given id.
func Builtin(id string) (Theme, bool) {
	for _, t := range Builtins() {
		if t.ID == id {
			return t, true
		}
	}
	return Theme{}, false
}
