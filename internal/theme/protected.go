package theme

// ProtectedPalette colors safety-critical UI: destructive confirmations,
// warnings in the change preview, security prompts, license labels and
// permission screens. Themes can't set these colors, so no theme can make a
// destructive action look harmless or hide a warning. They follow only the
// light or dark mode and the high-contrast preference.
type ProtectedPalette struct {
	Surface     Color `json:"surface"`
	Text        Color `json:"text"`
	Border      Color `json:"border"`
	Danger      Color `json:"danger"`
	DangerText  Color `json:"dangerText"`
	Warning     Color `json:"warning"`
	WarningText Color `json:"warningText"`
	Focus       Color `json:"focus"`
}

// The protected palettes for each mode, standard and high contrast.
var (
	ProtectedLight = ProtectedPalette{
		Surface: "#FFFFFF", Text: "#0B1B33", Border: "#7A889C",
		Danger: "#B3261E", DangerText: "#FFFFFF", Warning: "#8A5A00", WarningText: "#FFFFFF", Focus: "#0062CC",
	}
	ProtectedDark = ProtectedPalette{
		Surface: "#0F2036", Text: "#E6EDF5", Border: "#5B6F88",
		Danger: "#FF6B6B", DangerText: "#00142F", Warning: "#E3A33A", WarningText: "#00142F", Focus: "#3D9BFF",
	}
	ProtectedHighContrastLight = ProtectedPalette{
		Surface: "#FFFFFF", Text: "#000000", Border: "#000000",
		Danger: "#A3001B", DangerText: "#FFFFFF", Warning: "#7A4F00", WarningText: "#FFFFFF", Focus: "#0049B8",
	}
	ProtectedHighContrastDark = ProtectedPalette{
		Surface: "#000000", Text: "#FFFFFF", Border: "#FFFFFF",
		Danger: "#FF7A7A", DangerText: "#000000", Warning: "#FFC94D", WarningText: "#000000", Focus: "#FFD60A",
	}
)

// ProtectedTokens are the custom properties of the protected palette. No
// theme sets them, and custom CSS may not mention them.
var ProtectedTokens = []string{
	"critical-surface", "critical-text", "critical-border", "critical-danger",
	"critical-danger-text", "critical-warning", "critical-warning-text", "critical-focus",
}

func (p ProtectedPalette) vars() [][2]string {
	return [][2]string{
		{"criticalSurface", string(p.Surface)}, {"criticalText", string(p.Text)}, {"criticalBorder", string(p.Border)},
		{"criticalDanger", string(p.Danger)}, {"criticalDangerText", string(p.DangerText)},
		{"criticalWarning", string(p.Warning)}, {"criticalWarningText", string(p.WarningText)},
		{"criticalFocus", string(p.Focus)},
	}
}

// ProtectedPairs are the contrasts the protected palettes reach, which the
// tests hold them to.
var ProtectedPairs = []Pair{
	{"text", "surface", TextContrast, "text in protected areas"},
	{"danger", "surface", TextContrast, "danger text in protected areas"},
	{"warning", "surface", TextContrast, "warning text in protected areas"},
	{"dangerText", "danger", TextContrast, "labels of destructive confirmations"},
	{"warningText", "warning", TextContrast, "labels on warnings"},
	{"border", "surface", NonTextContrast, "outlines in protected areas"},
	{"focus", "surface", NonTextContrast, "the focus ring in protected areas"},
}

// Colors returns the protected palette by token name, for checks.
func (p ProtectedPalette) Colors() map[string]Color {
	return map[string]Color{
		"surface": p.Surface, "text": p.Text, "border": p.Border, "danger": p.Danger,
		"dangerText": p.DangerText, "warning": p.Warning, "warningText": p.WarningText, "focus": p.Focus,
	}
}
