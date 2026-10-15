package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/spec"
)

// Brand colors from the brand guidelines.
const (
	BrandNavy = "#01214F"
	BrandBlue = "#007CFD"
)

// Palette is the color tokens of one mode.
type Palette struct {
	// Surface is the page background, SurfaceRaised cards, menus and
	// dialogs, and SurfaceSunken wells, table headers and hover states.
	Surface       Color `yaml:"surface" json:"surface"`
	SurfaceRaised Color `yaml:"surfaceRaised" json:"surfaceRaised"`
	SurfaceSunken Color `yaml:"surfaceSunken" json:"surfaceSunken"`
	Text          Color `yaml:"text" json:"text"`
	TextMuted     Color `yaml:"textMuted" json:"textMuted"`
	// Border is for hairlines that separate; BorderStrong outlines controls
	// and has to stand out from every surface.
	Border       Color `yaml:"border" json:"border"`
	BorderStrong Color `yaml:"borderStrong" json:"borderStrong"`
	Brand        Color `yaml:"brand" json:"brand"`
	BrandText    Color `yaml:"brandText" json:"brandText"`
	// Accent is the color of primary actions, links and selection.
	Accent      Color `yaml:"accent" json:"accent"`
	AccentText  Color `yaml:"accentText" json:"accentText"`
	Success     Color `yaml:"success" json:"success"`
	SuccessText Color `yaml:"successText" json:"successText"`
	Warning     Color `yaml:"warning" json:"warning"`
	WarningText Color `yaml:"warningText" json:"warningText"`
	Danger      Color `yaml:"danger" json:"danger"`
	DangerText  Color `yaml:"dangerText" json:"dangerText"`
	Info        Color `yaml:"info" json:"info"`
	InfoText    Color `yaml:"infoText" json:"infoText"`
	Focus       Color `yaml:"focus" json:"focus"`
	// Overlay dims the page behind dialogs and sheets; it may be
	// translucent.
	Overlay Color `yaml:"overlay,omitempty" json:"overlay,omitempty"`
	// Chart colors identify series, in order.
	Chart []Color `yaml:"chart" json:"chart"`
}

// Font is a bundled font family.
type Font struct {
	Family string   `yaml:"family" json:"family"`
	Files  []string `yaml:"files,omitempty" json:"files,omitempty"`
}

// Typography is the type tokens.
type Typography struct {
	Sans     Font   `yaml:"sans,omitempty" json:"sans,omitzero"`
	Mono     Font   `yaml:"mono,omitempty" json:"mono,omitzero"`
	BaseSize string `yaml:"baseSize,omitempty" json:"baseSize,omitempty"`
	// Scale is the ratio between steps of the type scale.
	Scale   float64 `yaml:"scale,omitempty" json:"scale,omitempty"`
	Weights Weights `yaml:"weights,omitempty" json:"weights,omitzero"`
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

// FocusRing is the outline that shows which element has keyboard focus.
type FocusRing struct {
	Width  string `yaml:"width,omitempty" json:"width,omitempty"`
	Offset string `yaml:"offset,omitempty" json:"offset,omitempty"`
}

// Densities a theme can have.
const (
	DensityComfortable = "comfortable"
	DensityCompact     = "compact"
)

// Tokens are a theme's design tokens.
type Tokens struct {
	Light      Palette    `yaml:"light" json:"light"`
	Dark       Palette    `yaml:"dark" json:"dark"`
	Typography Typography `yaml:"typography,omitempty" json:"typography,omitzero"`
	Radius     Sizes      `yaml:"radius,omitempty" json:"radius,omitzero"`
	// Density is comfortable or compact: how tall controls and table rows
	// are and how much space they leave.
	Density   string    `yaml:"density,omitempty" json:"density,omitempty"`
	Shadows   Sizes     `yaml:"shadows,omitempty" json:"shadows,omitzero"`
	Motion    Motion    `yaml:"motion,omitempty" json:"motion,omitzero"`
	FocusRing FocusRing `yaml:"focusRing,omitempty" json:"focusRing,omitzero"`
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

// Console is the terminal palette of the installer, the console status
// screen and the CLI.
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

// MaxThemeBytes bounds the theme file people upload or import.
const MaxThemeBytes = 256 << 10

// Parse reads a theme from YAML or JSON, checks it against the schema and
// its tokens with Check, and returns it. Every problem is reported, not
// only the first.
func Parse(data []byte) (*Theme, error) {
	if len(data) > MaxThemeBytes {
		return nil, fmt.Errorf("the theme file is larger than %d KiB", MaxThemeBytes>>10)
	}
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("reading the theme: %w", err)
	}
	if keys := protectedKeys(raw); len(keys) > 0 {
		return nil, fmt.Errorf("themes can't set protected tokens (%s); the colors of destructive confirmations, "+
			"change-preview warnings, security prompts, license labels and permission screens are fixed", strings.Join(keys, ", "))
	}
	if err := spec.Validate(spec.Theme, raw); err != nil {
		return nil, err
	}
	var t Theme
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("reading the theme: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Validate returns every problem the theme's tokens have, joined, or nil.
func (t *Theme) Validate() error {
	problems := t.Check()
	if len(problems) == 0 {
		return nil
	}
	errs := make([]error, len(problems))
	for i, p := range problems {
		errs[i] = p
	}
	return errors.Join(errs...)
}

// protectedKeys finds keys anywhere in a raw document that name protected
// tokens, so a theme that tries to change them gets a clear answer rather
// than a schema error about an unknown field.
func protectedKeys(doc any) []string {
	var found []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if strings.HasPrefix(strings.ToLower(k), "critical") {
					found = append(found, k)
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(doc)
	return found
}

// Overlay returns base with every token the layer sets replacing base's,
// as the server, reseller and personal layers do. Two groups go as one: a
// font, so a family never gets another layer's font files, and the brand
// files (logos, mark and favicon), so a reseller's customers never see the
// server's logo beside the reseller's.
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
	if err := json.Unmarshal(raw, &out); err != nil {
		return Theme{}, err
	}
	for _, f := range []struct{ dst, src *Font }{
		{&out.Tokens.Typography.Sans, &layer.Tokens.Typography.Sans},
		{&out.Tokens.Typography.Mono, &layer.Tokens.Typography.Mono},
	} {
		if f.src.Family != "" {
			*f.dst = Font{Family: f.src.Family, Files: slices.Clone(f.src.Files)}
		}
	}
	if a := layer.Assets; a.HasBrand() {
		out.Assets.LogoLight, out.Assets.LogoDark, out.Assets.Mark, out.Assets.Favicon = a.LogoLight, a.LogoDark, a.Mark, a.Favicon
	}
	return out, nil
}

// HasBrand reports whether the assets include any of the brand files: a
// logo, the mark or the favicon.
func (a Assets) HasBrand() bool {
	return a.LogoLight != "" || a.LogoDark != "" || a.Mark != "" || a.Favicon != ""
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

// Layers are the themes someone sees on top of the default theme: the
// server theme an admin set, and the theme of the reseller whose customer
// they are. Either may be missing.
type Layers struct {
	Server   *Theme `json:"server,omitempty"`
	Reseller *Theme `json:"reseller,omitempty"`
}

// Resolve layers the server and reseller themes over the default theme and
// returns the theme in effect. Personal preferences go on top in the
// browser; see Preferences. The result is checked like any theme, so a
// combination of layers can't produce unreadable text either.
func (l Layers) Resolve() (Theme, error) {
	t := Default()
	for _, layer := range []*Theme{l.Server, l.Reseller} {
		if layer == nil {
			continue
		}
		var err error
		if t, err = Overlay(t, *layer); err != nil {
			return Theme{}, err
		}
	}
	if err := t.Validate(); err != nil {
		return Theme{}, err
	}
	return t, nil
}
