package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/spec"
)

// Brand colors from the brand guidelines.
const (
	BrandNavy = "#01214F"
	BrandBlue = "#007CFD"
)

// Color is a hex color, #RRGGBB or #RRGGBBAA.
type Color string

// Palette is the color tokens of one mode.
type Palette struct {
	Surface       Color   `yaml:"surface" json:"surface"`
	SurfaceRaised Color   `yaml:"surfaceRaised" json:"surfaceRaised"`
	SurfaceSunken Color   `yaml:"surfaceSunken" json:"surfaceSunken"`
	Text          Color   `yaml:"text" json:"text"`
	TextMuted     Color   `yaml:"textMuted" json:"textMuted"`
	Border        Color   `yaml:"border" json:"border"`
	BorderStrong  Color   `yaml:"borderStrong" json:"borderStrong"`
	Brand         Color   `yaml:"brand" json:"brand"`
	BrandText     Color   `yaml:"brandText" json:"brandText"`
	Accent        Color   `yaml:"accent" json:"accent"`
	AccentText    Color   `yaml:"accentText" json:"accentText"`
	Success       Color   `yaml:"success" json:"success"`
	SuccessText   Color   `yaml:"successText" json:"successText"`
	Warning       Color   `yaml:"warning" json:"warning"`
	WarningText   Color   `yaml:"warningText" json:"warningText"`
	Danger        Color   `yaml:"danger" json:"danger"`
	DangerText    Color   `yaml:"dangerText" json:"dangerText"`
	Info          Color   `yaml:"info" json:"info"`
	InfoText      Color   `yaml:"infoText" json:"infoText"`
	Focus         Color   `yaml:"focus" json:"focus"`
	Chart         []Color `yaml:"chart" json:"chart"`
}

// Font is a bundled font family.
type Font struct {
	Family string   `yaml:"family" json:"family"`
	Files  []string `yaml:"files,omitempty" json:"files,omitempty"`
}

// Typography is the type tokens.
type Typography struct {
	Sans     Font    `yaml:"sans,omitempty" json:"sans,omitzero"`
	Mono     Font    `yaml:"mono,omitempty" json:"mono,omitzero"`
	BaseSize string  `yaml:"baseSize,omitempty" json:"baseSize,omitempty"`
	Scale    float64 `yaml:"scale,omitempty" json:"scale,omitempty"`
	Weights  Weights `yaml:"weights,omitempty" json:"weights,omitzero"`
}

// Weights are font weights.
type Weights struct {
	Regular  int `yaml:"regular,omitempty" json:"regular,omitempty"`
	Medium   int `yaml:"medium,omitempty" json:"medium,omitempty"`
	Semibold int `yaml:"semibold,omitempty" json:"semibold,omitempty"`
	Bold     int `yaml:"bold,omitempty" json:"bold,omitempty"`
}

// Sizes holds small to large values, such as radii or shadows.
type Sizes struct {
	SM string `yaml:"sm,omitempty" json:"sm,omitempty"`
	MD string `yaml:"md,omitempty" json:"md,omitempty"`
	LG string `yaml:"lg,omitempty" json:"lg,omitempty"`
	XL string `yaml:"xl,omitempty" json:"xl,omitempty"`
}

// Motion holds animation durations.
type Motion struct {
	Fast   string `yaml:"fast,omitempty" json:"fast,omitempty"`
	Normal string `yaml:"normal,omitempty" json:"normal,omitempty"`
	Slow   string `yaml:"slow,omitempty" json:"slow,omitempty"`
}

// Tokens are a theme's design tokens.
type Tokens struct {
	Light      Palette    `yaml:"light" json:"light"`
	Dark       Palette    `yaml:"dark" json:"dark"`
	Typography Typography `yaml:"typography,omitempty" json:"typography,omitzero"`
	Radius     Sizes      `yaml:"radius,omitempty" json:"radius,omitzero"`
	Density    string     `yaml:"density,omitempty" json:"density,omitempty"`
	Shadows    Sizes      `yaml:"shadows,omitempty" json:"shadows,omitzero"`
	Motion     Motion     `yaml:"motion,omitempty" json:"motion,omitzero"`
}

// Assets are files in the theme package.
type Assets struct {
	LogoLight       string            `yaml:"logoLight,omitempty" json:"logoLight,omitempty"`
	LogoDark        string            `yaml:"logoDark,omitempty" json:"logoDark,omitempty"`
	Mark            string            `yaml:"mark,omitempty" json:"mark,omitempty"`
	Favicon         string            `yaml:"favicon,omitempty" json:"favicon,omitempty"`
	LoginBackground string            `yaml:"loginBackground,omitempty" json:"loginBackground,omitempty"`
	Illustrations   map[string]string `yaml:"illustrations,omitempty" json:"illustrations,omitempty"`
}

// Templates are logic-less templates for emails and visitor pages.
type Templates struct {
	Email       string `yaml:"email,omitempty" json:"email,omitempty"`
	Error       string `yaml:"error,omitempty" json:"error,omitempty"`
	Maintenance string `yaml:"maintenance,omitempty" json:"maintenance,omitempty"`
	Suspended   string `yaml:"suspended,omitempty" json:"suspended,omitempty"`
}

// Console is the terminal palette.
type Console struct {
	Background Color `yaml:"background,omitempty" json:"background,omitempty"`
	Foreground Color `yaml:"foreground,omitempty" json:"foreground,omitempty"`
	Accent     Color `yaml:"accent,omitempty" json:"accent,omitempty"`
	Success    Color `yaml:"success,omitempty" json:"success,omitempty"`
	Warning    Color `yaml:"warning,omitempty" json:"warning,omitempty"`
	Danger     Color `yaml:"danger,omitempty" json:"danger,omitempty"`
}

// Theme is a complete theme.
type Theme struct {
	Format      int       `yaml:"format" json:"format"`
	ID          string    `yaml:"id" json:"id"`
	Name        string    `yaml:"name" json:"name"`
	Version     string    `yaml:"version" json:"version"`
	License     string    `yaml:"license" json:"license"`
	Author      string    `yaml:"author,omitempty" json:"author,omitempty"`
	Description string    `yaml:"description,omitempty" json:"description,omitempty"`
	Tokens      Tokens    `yaml:"tokens" json:"tokens"`
	Assets      Assets    `yaml:"assets,omitempty" json:"assets,omitzero"`
	Templates   Templates `yaml:"templates,omitempty" json:"templates,omitzero"`
	Console     Console   `yaml:"console,omitempty" json:"console,omitzero"`
}

// Default is the Racksail theme. Brand blue is too light for white text, so
// buttons and links use a darker shade of it in light mode and brand blue
// itself is the focus ring and first chart color.
func Default() Theme {
	return Theme{
		Format: 1, ID: "racksail", Name: "Racksail", Version: "1.0.0", License: "CC-BY-4.0", Author: "Racksail",
		Description: "The default Racksail theme.",
		Tokens: Tokens{
			Light: Palette{
				Surface: "#F7F9FC", SurfaceRaised: "#FFFFFF", SurfaceSunken: "#EEF2F7",
				Text: "#0B1B33", TextMuted: "#4A5A70", Border: "#D8E0EA", BorderStrong: "#7A889C",
				Brand: BrandNavy, BrandText: "#FFFFFF", Accent: "#0062CC", AccentText: "#FFFFFF",
				Success: "#1A7F37", SuccessText: "#FFFFFF", Warning: "#9A6700", WarningText: "#FFFFFF",
				Danger: "#CF222E", DangerText: "#FFFFFF", Info: "#0062CC", InfoText: "#FFFFFF",
				Focus: BrandBlue,
				Chart: []Color{BrandBlue, "#E8590C", "#2F9E44", "#AE3EC9", "#F59F00", "#0C8599", "#E64980", "#5C7CFA"},
			},
			Dark: Palette{
				Surface: "#0A1626", SurfaceRaised: "#0F2036", SurfaceSunken: "#06101C",
				Text: "#E6EDF5", TextMuted: "#9FB0C4", Border: "#22344C", BorderStrong: "#5B6F88",
				Brand: BrandBlue, BrandText: "#00142F", Accent: "#3D9BFF", AccentText: "#00142F",
				Success: "#3FB950", SuccessText: "#00142F", Warning: "#D29922", WarningText: "#00142F",
				Danger: "#F85149", DangerText: "#00142F", Info: "#3D9BFF", InfoText: "#00142F",
				Focus: "#3D9BFF",
				Chart: []Color{"#3D9BFF", "#FF922B", "#51CF66", "#CC5DE8", "#FCC419", "#22B8CF", "#F06595", "#748FFC"},
			},
			Typography: Typography{
				Sans: Font{Family: "Inter Variable"}, Mono: Font{Family: "JetBrains Mono Variable"}, BaseSize: "16px", Scale: 1.2,
				Weights: Weights{Regular: 400, Medium: 500, Semibold: 600, Bold: 700},
			},
			Radius:  Sizes{SM: "4px", MD: "6px", LG: "10px", XL: "16px"},
			Density: "comfortable",
			Shadows: Sizes{
				SM: "0 1px 2px rgba(1, 33, 79, 0.08)",
				MD: "0 4px 12px rgba(1, 33, 79, 0.10)",
				LG: "0 12px 32px rgba(1, 33, 79, 0.14)",
			},
			Motion: Motion{Fast: "120ms", Normal: "200ms", Slow: "320ms"},
		},
		Console: Console{Background: BrandNavy, Foreground: "#E6EDF5", Accent: "#3D9BFF", Success: "#3FB950", Warning: "#D29922", Danger: "#F85149"},
	}
}

// Parse reads a theme from YAML or JSON, checks it against the schema and
// for contrast, and returns it.
func Parse(data []byte) (*Theme, error) {
	if err := spec.ValidateYAML(spec.Theme, data); err != nil {
		return nil, err
	}
	var t Theme
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("reading the theme: %w", err)
	}
	if problems := t.Check(); len(problems) > 0 {
		errs := make([]error, len(problems))
		for i, p := range problems {
			errs[i] = p
		}
		return nil, errors.Join(errs...)
	}
	return &t, nil
}

// Problem is a contrast check a theme fails.
type Problem struct {
	Mode       string
	Foreground string
	Background string
	Ratio      float64
	Minimum    float64
}

func (p Problem) Error() string {
	return fmt.Sprintf("%s mode: %s on %s has contrast %.2f:1, needs at least %.1f:1", p.Mode, p.Foreground, p.Background, p.Ratio, p.Minimum)
}

type pair struct {
	fg, bg string
	min    float64
}

// The pairs every theme must pass: 4.5:1 for text (WCAG 2.2 AA) and 3:1 for
// the focus ring and input borders, which identify interactive elements.
var pairs = []pair{
	{"text", "surface", 4.5}, {"text", "surfaceRaised", 4.5}, {"text", "surfaceSunken", 4.5},
	{"textMuted", "surface", 4.5}, {"textMuted", "surfaceRaised", 4.5},
	{"brandText", "brand", 4.5}, {"accentText", "accent", 4.5},
	{"successText", "success", 4.5}, {"warningText", "warning", 4.5},
	{"dangerText", "danger", 4.5}, {"infoText", "info", 4.5},
	{"accent", "surfaceRaised", 3}, {"focus", "surface", 3}, {"focus", "surfaceRaised", 3},
	{"borderStrong", "surface", 3}, {"borderStrong", "surfaceRaised", 3},
}

func (p Palette) token(name string) Color {
	switch name {
	case "surface":
		return p.Surface
	case "surfaceRaised":
		return p.SurfaceRaised
	case "surfaceSunken":
		return p.SurfaceSunken
	case "text":
		return p.Text
	case "textMuted":
		return p.TextMuted
	case "borderStrong":
		return p.BorderStrong
	case "brand":
		return p.Brand
	case "brandText":
		return p.BrandText
	case "accent":
		return p.Accent
	case "accentText":
		return p.AccentText
	case "success":
		return p.Success
	case "successText":
		return p.SuccessText
	case "warning":
		return p.Warning
	case "warningText":
		return p.WarningText
	case "danger":
		return p.Danger
	case "dangerText":
		return p.DangerText
	case "info":
		return p.Info
	case "infoText":
		return p.InfoText
	case "focus":
		return p.Focus
	}
	return ""
}

// Check returns the contrast checks the theme fails.
func (t *Theme) Check() []Problem {
	var out []Problem
	for _, mode := range []struct {
		name string
		p    Palette
	}{{"light", t.Tokens.Light}, {"dark", t.Tokens.Dark}} {
		for _, pr := range pairs {
			ratio, err := Contrast(mode.p.token(pr.fg), mode.p.token(pr.bg))
			if err != nil || ratio+1e-9 < pr.min {
				out = append(out, Problem{Mode: mode.name, Foreground: pr.fg, Background: pr.bg, Ratio: ratio, Minimum: pr.min})
			}
		}
	}
	return out
}

// Contrast returns the WCAG contrast ratio of two colors, from 1 to 21.
// Alpha is ignored.
func Contrast(a, b Color) (float64, error) {
	la, err := luminance(a)
	if err != nil {
		return 0, err
	}
	lb, err := luminance(b)
	if err != nil {
		return 0, err
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), nil
}

func luminance(c Color) (float64, error) {
	s := strings.TrimPrefix(string(c), "#")
	if len(s) != 6 && len(s) != 8 {
		return 0, fmt.Errorf("color %q must be #RRGGBB", c)
	}
	var ch [3]float64
	for i := range ch {
		v, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return 0, fmt.Errorf("color %q: %w", c, err)
		}
		x := float64(v) / 255
		if x <= 0.04045 {
			ch[i] = x / 12.92
		} else {
			ch[i] = math.Pow((x+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2], nil
}

// Overlay returns base with every token the layer sets replacing base's,
// as the server, reseller and personal layers do.
func Overlay(base, layer Theme) (Theme, error) {
	// A round trip through JSON maps merges every field without listing
	// them, and omitted fields in the layer keep the base value.
	var merged map[string]any
	for _, t := range []Theme{base, layer} {
		raw, err := json.Marshal(t)
		if err != nil {
			return Theme{}, err
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return Theme{}, err
		}
		if merged == nil {
			merged = m
		} else {
			mergeInto(merged, m)
		}
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return Theme{}, err
	}
	var out Theme
	err = json.Unmarshal(raw, &out)
	return out, err
}

func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				mergeInto(dm, sm)
				continue
			}
		}
		switch x := v.(type) {
		case nil:
			continue
		case string:
			if x == "" {
				continue
			}
		case float64:
			if x == 0 {
				continue
			}
		case []any:
			if len(x) == 0 {
				continue
			}
		}
		dst[k] = v
	}
}
