package theme

import (
	"bytes"
	"encoding/json"
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/spec"
)

// YAML writes a theme as a theme.yaml file.
func (t *Theme) YAML() ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(t); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// JSON writes a theme as JSON, which Parse reads like YAML.
func (t *Theme) JSON() ([]byte, error) {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Spec is what the web UI needs to know about themes: the built-in themes,
// the rules every theme is checked against and the protected palettes. The
// panel's theme editor checks themes as people edit them with the same
// rules the server applies when they are saved.
type Spec struct {
	Themes      []Theme      `json:"themes"`
	Pairs       []Pair       `json:"pairs"`
	Derivations []Derivation `json:"derivations"`
	Meanings    []Meaning    `json:"meanings"`
	// ConsolePairs are the contrasts of the console palette; colors a theme
	// leaves out of it are the default theme's.
	ConsolePairs []Pair `json:"consolePairs"`
	// TranslucentTokens may have an alpha channel; no other color may.
	TranslucentTokens []string `json:"translucentTokens"`
	// BundledFonts are the families the panel serves; any other comes with
	// its files.
	BundledFonts []string                    `json:"bundledFonts"`
	Protected    map[string]ProtectedPalette `json:"protected"`
	// LayerCSS is each built-in theme rendered by LayerCSS, so the UI's own
	// renderer can be checked against it.
	LayerCSS map[string]string `json:"layerCss"`
	// Schema is spec/schemas/theme.schema.json, which the editor checks
	// imported themes against.
	Schema json.RawMessage `json:"schema"`
}

// UISpec builds the spec of the built-in themes.
func UISpec() Spec {
	s := Spec{
		Themes: Builtins(), Pairs: Pairs, Derivations: Derivations, Meanings: Meanings,
		ConsolePairs: ConsolePairs, TranslucentTokens: TranslucentTokens, BundledFonts: BundledFonts,
		Protected: map[string]ProtectedPalette{
			"light": ProtectedLight, "dark": ProtectedDark,
			"highContrastLight": ProtectedHighContrastLight, "highContrastDark": ProtectedHighContrastDark,
		},
		LayerCSS: map[string]string{},
	}
	for _, t := range s.Themes {
		s.LayerCSS[t.ID] = t.LayerCSS(nil)
	}
	schema, err := spec.SchemaJSON(spec.Theme)
	if err != nil {
		panic(err)
	}
	s.Schema = schema
	return s
}

// JSON writes the spec as the UI's themes.json.
func (s Spec) JSON() ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the theme spec: %w", err)
	}
	return append(data, '\n'), nil
}
