package theme

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/spec"
)

func TestBuiltinThemesAreValid(t *testing.T) {
	for _, th := range Builtins() {
		t.Run(th.ID, func(t *testing.T) {
			for _, p := range th.Check() {
				t.Error(p)
			}
			raw, err := json.Marshal(th)
			if err != nil {
				t.Fatal(err)
			}
			if err := spec.ValidateJSON(spec.Theme, raw); err != nil {
				t.Errorf("doesn't match its schema: %v", err)
			}
			// What the editor exports parses back to the same theme.
			for _, encode := range []func() ([]byte, error){th.YAML, th.JSON} {
				data, err := encode()
				if err != nil {
					t.Fatal(err)
				}
				back, err := Parse(data)
				if err != nil {
					t.Fatalf("exported theme doesn't parse: %v", err)
				}
				a, _ := json.Marshal(th)
				b, _ := json.Marshal(back)
				if string(a) != string(b) {
					t.Errorf("round trip changed the theme:\n%s\n%s", a, b)
				}
			}
		})
	}
	d := Default()
	if d.Tokens.Light.Brand != BrandNavy || d.Tokens.Light.Focus != BrandBlue || d.Tokens.Light.Chart[0] != BrandBlue {
		t.Error("the default theme uses the brand colors")
	}
	if _, ok := Builtin("nope"); ok {
		t.Error("there is no built-in theme called nope")
	}
	if c, _ := Builtin(CompactID); c.Tokens.Density != DensityCompact {
		t.Error("the compact theme is compact")
	}
}

func TestBuiltinsDontShareSlices(t *testing.T) {
	a := Default()
	a.Tokens.Light.Chart[0] = "#000000"
	if Default().Tokens.Light.Chart[0] != BrandBlue {
		t.Error("changing one theme's chart colors changed the default")
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
		{"#007CFDFF", "#FFFFFF", 3.96},
	}
	for _, tt := range tests {
		got, err := Contrast(tt.a, tt.b)
		if err != nil || math.Abs(got-tt.want) > 0.02 {
			t.Errorf("Contrast(%s, %s) = %.3f, %v; want %.2f", tt.a, tt.b, got, err, tt.want)
		}
	}
	// What shows through a translucent color decides its contrast, so it
	// has none to check.
	for _, pair := range [][2]Color{{"#0B1B3300", "#FFFFFF"}, {"#000000", "#FFFFFF80"}} {
		if _, err := Contrast(pair[0], pair[1]); !errors.Is(err, ErrTranslucent) {
			t.Errorf("Contrast(%s, %s): %v, want ErrTranslucent", pair[0], pair[1], err)
		}
	}
	for _, bad := range []Color{"blue", "#12345", "#GGGGGG", "007CFD", ""} {
		if _, err := Contrast(bad, "#FFFFFF"); err == nil {
			t.Errorf("%q isn't a color", bad)
		}
	}
}

func TestMixAndHue(t *testing.T) {
	if m, _ := Mix("#0062CC", "#FFFFFF", 0.12); m != "#E0ECF9" {
		t.Errorf("mix = %s", m)
	}
	if m, _ := Mix("#000000", "#FFFFFF", 0.5); m != "#808080" {
		t.Errorf("half black over white = %s", m)
	}
	for _, tt := range []struct {
		c   Color
		hue float64
	}{{"#FF0000", 0}, {"#00FF00", 120}, {"#0000FF", 240}, {"#CF222E", 355.8}, {"#9A6700", 40.1}} {
		h, s, err := Hue(tt.c)
		if err != nil || math.Abs(h-tt.hue) > 0.1 || s == 0 {
			t.Errorf("Hue(%s) = %.1f, %.2f, %v", tt.c, h, s, err)
		}
	}
	if _, s, _ := Hue("#777777"); s != 0 {
		t.Error("gray has no saturation")
	}
}

func encode(t *testing.T, th Theme) []byte {
	t.Helper()
	raw, err := th.YAML()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseRejectsBadThemes(t *testing.T) {
	d := Default()
	if _, err := Parse(encode(t, d)); err != nil {
		t.Fatalf("the default theme should parse: %v", err)
	}
	tests := []struct {
		name   string
		change func(*Theme)
		want   string
	}{
		{"low contrast", func(th *Theme) { th.Tokens.Light.TextMuted = "#C0C8D0" }, "textMuted on surface"},
		{"invisible focus", func(th *Theme) { th.Tokens.Dark.Focus = "#0F2036" }, "focus on surfaceRaised"},
		{"faint chart color", func(th *Theme) { th.Tokens.Light.Chart[2] = "#E8F0E8" }, "chart3 on surfaceRaised"},
		{"subtle background", func(th *Theme) { th.Tokens.Light.Warning = "#B08A3C"; th.Tokens.Light.WarningText = "#000000" }, "warning on surfaceRaised"},
		{"green danger", func(th *Theme) { th.Tokens.Light.Danger = "#1A7F37" }, "danger must be red"},
		{"gray warning", func(th *Theme) { th.Tokens.Dark.Warning = "#AAAAAA" }, "warning must be amber"},
		{"remote logo", func(th *Theme) { th.Assets.LogoLight = "https://example.com/logo.png" }, "logoLight"},
		{"escape in a path", func(th *Theme) { th.Assets.Favicon = "../favicon.svg" }, "favicon"},
		{"font name that breaks out of CSS", func(th *Theme) { th.Tokens.Typography.Sans.Family = `X"; } body { display: none` }, "family"},
		{"unknown density", func(th *Theme) { th.Tokens.Density = "airy" }, "density"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := Default()
			th.Tokens.Light.Chart = append([]Color{}, th.Tokens.Light.Chart...)
			tt.change(&th)
			_, err := Parse(encode(t, th))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
	for name, doc := range map[string]string{
		"no palettes":       "format: 1\nid: x1\nname: x\nversion: 1.0.0\nlicense: MIT\ntokens: {}\n",
		"protected token":   strings.Replace(string(encode(t, d)), "  light:\n", "  light:\n    criticalDanger: '#00FF00'\n", 1),
		"not YAML":          "format: [1\n",
		"unknown field":     strings.Replace(string(encode(t, d)), "format: 1\n", "format: 1\nscript: alert(1)\n", 1),
		"too large":         "description: " + strings.Repeat("a", MaxThemeBytes) + "\n",
		"wrong format":      strings.Replace(string(encode(t, d)), "format: 1\n", "format: 2\n", 1),
		"chart too short":   "format: 1\nid: x1\nname: x\nversion: 1.0.0\nlicense: MIT\ntokens: {light: {chart: []}, dark: {chart: []}}\n",
		"protected at root": "criticalSurface: '#000000'\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: should be refused", name)
		} else if name == "protected token" && !strings.Contains(err.Error(), "protected") {
			t.Errorf("protected token: %v", err)
		}
	}
}

func TestProtectedTokensAreRefusedWhereTokensGo(t *testing.T) {
	d := Default()
	base := string(encode(t, d))
	for name, tt := range map[string]struct{ doc, want string }{
		"palette": {strings.Replace(base, "  light:\n", "  light:\n    criticalDanger: '#00FF00'\n", 1), "tokens.light.criticalDanger"},
		"root":    {"criticalSurface: '#000000'\n" + base, "(criticalSurface)"},
		"tokens":  {strings.Replace(base, "tokens:\n", "tokens:\n  critical:\n    danger: '#00FF00'\n", 1), "tokens.critical"},
		"console": {strings.Replace(base, "console:\n", "console:\n  criticalText: '#FFFFFF'\n", 1), "console.criticalText"},
		"several": {strings.Replace(base, "  dark:\n", "  dark:\n    CriticalFocus: '#00FF00'\n    criticalText: '#00FF00'\n", 1), "(tokens.dark.CriticalFocus, tokens.dark.criticalText)"},
	} {
		_, err := Parse([]byte(tt.doc))
		if err == nil || !strings.Contains(err.Error(), "protected tokens") || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want it to name %s", name, err, tt.want)
		}
	}
	// Names people choose are only names: an illustration may be called
	// anything.
	d.Assets.Illustrations = map[string]string{"criticalError": "art/error.svg"}
	if _, err := Parse(encode(t, d)); err != nil {
		t.Errorf("an illustration called criticalError: %v", err)
	}
}

func TestCheckReportsEveryProblem(t *testing.T) {
	th := Default()
	th.Tokens.Light.Text = "#C0C0C0"
	th.Tokens.Dark.Text = "#1A1A1A"
	problems := th.Check()
	modes := map[string]bool{}
	for _, p := range problems {
		modes[p.Mode] = true
		if p.Kind != "contrast" || p.Ratio == 0 || p.Minimum != TextContrast || p.Purpose == "" {
			t.Errorf("problem %+v", p)
		}
	}
	if !modes["light"] || !modes["dark"] || len(problems) < 6 {
		t.Errorf("problems = %v", problems)
	}
	if !strings.Contains(problems[0].Error(), "needs at least 4.5:1") {
		t.Errorf("message: %s", problems[0].Error())
	}
	th = Default()
	th.Tokens.Light.Surface = "nope"
	found := false
	for _, p := range th.Check() {
		if p.Kind == "color" && p.Foreground == "surface" {
			found = true
		}
	}
	if !found {
		t.Error("a malformed color is reported")
	}
}

func TestOverlayAndLayers(t *testing.T) {
	base := Default()
	layer := Theme{Name: "Reseller", Tokens: Tokens{Light: Palette{Brand: "#5B21B6"}}}
	merged, err := Overlay(base, layer)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Name != "Reseller" || merged.Tokens.Light.Brand != "#5B21B6" || merged.Tokens.Light.Text != base.Tokens.Light.Text || merged.Format != 1 || len(merged.Tokens.Light.Chart) != 8 {
		t.Errorf("merged = %+v", merged)
	}

	server := Compact()
	server.Assets.LogoLight = "logo.svg"
	reseller := Default()
	reseller.ID, reseller.Name = "acme", "Acme"
	reseller.Tokens.Density = ""
	reseller.Tokens.Light.Accent = "#5B21B6"
	got, err := Layers{Server: &server, Reseller: &reseller}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Acme" || got.Tokens.Light.Accent != "#5B21B6" || got.Assets.LogoLight != "logo.svg" || got.Tokens.Density != DensityCompact {
		t.Errorf("resolved = %+v", got)
	}
	if def, err := (Layers{}).Resolve(); err != nil || def.ID != DefaultID {
		t.Errorf("no layers = %v, %v", def.ID, err)
	}
	bad := Default()
	bad.Tokens.Light.Text = "#FFFFFF"
	if _, err := (Layers{Reseller: &bad}).Resolve(); err == nil {
		t.Error("layers that produce unreadable text are refused")
	}
}

func TestOverlayKeepsFontsAndBrandFilesTogether(t *testing.T) {
	server := Default()
	server.Tokens.Typography.Sans = Font{Family: "Acme Sans", Files: []string{"fonts/acme.woff2"}}
	server.Tokens.Typography.Mono = Font{Family: "Acme Mono", Files: []string{"fonts/acme-mono.woff2"}}
	server.Assets = Assets{LogoLight: "server-logo.svg", LogoDark: "server-logo-dark.svg", Favicon: "server.ico", LoginBackground: "sea.jpg"}

	reseller := Default()
	reseller.Tokens.Typography.Sans = Font{Family: "Inter Variable"}
	reseller.Tokens.Typography.Mono = Font{}
	reseller.Assets = Assets{LogoLight: "reseller-logo.svg"}

	got, err := Layers{Server: &server, Reseller: &reseller}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if sans := got.Tokens.Typography.Sans; sans.Family != "Inter Variable" || len(sans.Files) != 0 {
		t.Errorf("a family named by the reseller took the server's files: %+v", sans)
	}
	if mono := got.Tokens.Typography.Mono; mono.Family != "Acme Mono" || len(mono.Files) != 1 {
		t.Errorf("a font the reseller doesn't name lost the server's files: %+v", mono)
	}
	want := Assets{LogoLight: "reseller-logo.svg", LoginBackground: "sea.jpg"}
	if got.Assets.LogoLight != want.LogoLight || got.Assets.LogoDark != "" || got.Assets.Favicon != "" || got.Assets.LoginBackground != want.LoginBackground {
		t.Errorf("assets = %+v, want the reseller's brand files only, and the server's login background", got.Assets)
	}

	// A layer without brand files or fonts keeps the ones below.
	plain := Default()
	plain.Tokens.Typography = Typography{}
	plain.Assets = Assets{}
	got, err = Overlay(server, plain)
	if err != nil {
		t.Fatal(err)
	}
	if got.Assets.LogoDark != "server-logo-dark.svg" || got.Tokens.Typography.Sans.Files[0] != "fonts/acme.woff2" {
		t.Errorf("overlay without fonts or brand files = %+v", got)
	}
}

func TestPaletteColors(t *testing.T) {
	c := Default().Tokens.Light.Colors()
	if c["chart8"] != "#E34948" || c["accentSubtle"] != "#E0ECF9" || c["overlay"] != "#01214F66" {
		t.Errorf("colors = %v", c)
	}
	for _, d := range Derivations {
		if c[d.Name] == "" {
			t.Errorf("%s isn't derived", d.Name)
		}
	}
	for _, pr := range Pairs {
		if c[pr.Foreground] == "" || c[pr.Background] == "" || pr.Purpose == "" {
			t.Errorf("pair %+v names a token that doesn't exist", pr)
		}
	}
}

// Only the overlay may be translucent: a transparent text or danger color
// would pass any contrast check that ignores alpha.
func TestTranslucentColorsAreRefused(t *testing.T) {
	for name, change := range map[string]func(*Theme){
		"text":         func(th *Theme) { th.Tokens.Light.Text = "#0B1B3300" },
		"danger":       func(th *Theme) { th.Tokens.Dark.Danger = "#F8514980" },
		"chart":        func(th *Theme) { th.Tokens.Light.Chart[1] = "#E2622C10" },
		"opaque alpha": func(th *Theme) { th.Tokens.Light.Focus = "#007CFDFF" },
		"console":      func(th *Theme) { th.Console.Foreground = "#E6EDF500" },
	} {
		t.Run(name, func(t *testing.T) {
			th := Default()
			change(&th)
			problems := th.Check()
			found := false
			for _, p := range problems {
				found = found || p.Kind == ProblemAlpha
			}
			if !found {
				t.Errorf("problems = %v, want a translucent color reported", problems)
			}
			if _, err := Parse(encode(t, th)); err == nil {
				t.Error("the schema and the checks should refuse it")
			}
			raw, _ := json.Marshal(th)
			if err := spec.ValidateJSON(spec.Theme, raw); err == nil {
				t.Error("the schema accepts an alpha channel outside the overlay")
			}
		})
	}
	th := Default()
	th.Tokens.Light.Overlay = "#01214FCC"
	if problems := th.Check(); len(problems) != 0 {
		t.Errorf("a translucent overlay is fine: %v", problems)
	}
	err := Problem{Mode: ModeLight, Kind: ProblemAlpha, Foreground: "text"}.Error()
	if !strings.Contains(err, "only overlay") {
		t.Errorf("message: %s", err)
	}
}

func TestConsolePaletteIsChecked(t *testing.T) {
	th := Default()
	th.Console = Console{Background: "#FFFFFF"}
	problems := th.Check()
	var contrast []string
	for _, p := range problems {
		if p.Mode != ModeConsole {
			t.Errorf("unexpected %v", p)
		}
		if p.Kind == ProblemContrast {
			contrast = append(contrast, p.Foreground)
		}
	}
	// The default's light colors don't show on a white terminal.
	if len(contrast) < 4 {
		t.Errorf("console contrast problems = %v", contrast)
	}
	th.Console = Console{Background: "#FFFFFF", Foreground: "#0B1B33", Accent: "#0049B8", Success: "#0A6B26", Warning: "#7A4F00", Danger: "#A3001B"}
	if problems := th.Check(); len(problems) != 0 {
		t.Errorf("a dark-on-light console palette is fine: %v", problems)
	}
	th.Console.Danger = "#0A6B26"
	problems = th.Check()
	if len(problems) != 1 || problems[0].Kind != ProblemMeaning || problems[0].Foreground != "danger" || problems[0].Mode != ModeConsole {
		t.Errorf("a green danger color in the console: %v", problems)
	}
	if !strings.Contains(problems[0].Error(), "console palette: danger must be red") {
		t.Errorf("message: %s", problems[0].Error())
	}
	if _, err := (Layers{Server: &th}).Resolve(); err == nil {
		t.Error("a layer with an unreadable console palette is refused")
	}
}

func TestFontsComeWithFilesUnlessBundled(t *testing.T) {
	th := Default()
	th.Tokens.Typography.Sans = Font{Family: "Acme Sans"}
	problems := th.Check()
	if len(problems) != 1 || problems[0].Kind != ProblemFont || problems[0].Foreground != "sans" || problems[0].Purpose != "Acme Sans" {
		t.Fatalf("problems = %v", problems)
	}
	if !strings.Contains(problems[0].Error(), `doesn't bundle "Acme Sans"`) {
		t.Errorf("message: %s", problems[0].Error())
	}
	if _, err := Parse(encode(t, th)); err == nil {
		t.Error("a theme naming a font it doesn't bring is refused")
	}
	th.Tokens.Typography.Sans.Files = []string{"fonts/acme.woff2"}
	th.Tokens.Typography.Mono = Font{Family: "jetbrains mono variable"}
	if problems := th.Check(); len(problems) != 0 {
		t.Errorf("a font with files, and a bundled one in any case, are fine: %v", problems)
	}
}

// Problems come in the same order every time, so messages and the joined
// error don't change between runs.
func TestCheckOrderIsStable(t *testing.T) {
	th := Default()
	th.Tokens.Light.Surface, th.Tokens.Light.Text, th.Tokens.Light.Brand = "nope", "#12", "blue"
	th.Tokens.Dark.Focus = "#0F2036"
	th.Console.Accent = "#01214F"
	first := th.Validate().Error()
	for range 20 {
		if again := th.Validate().Error(); again != first {
			t.Fatalf("problems changed order:\n%s\n---\n%s", first, again)
		}
	}
	modes := []string{}
	for _, p := range th.Check() {
		if len(modes) == 0 || modes[len(modes)-1] != p.Mode {
			modes = append(modes, p.Mode)
		}
	}
	if strings.Join(modes, ",") != "light,dark,console" {
		t.Errorf("modes in order: %v", modes)
	}
}
