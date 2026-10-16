package theme

import (
	"strings"
	"testing"
)

func TestProtectedPalettesAreReadable(t *testing.T) {
	for name, p := range map[string]ProtectedPalette{
		"light": ProtectedLight, "dark": ProtectedDark,
		"high contrast light": ProtectedHighContrastLight, "high contrast dark": ProtectedHighContrastDark,
	} {
		colors := p.Colors()
		for _, pr := range ProtectedPairs {
			ratio, err := Contrast(colors[pr.Foreground], colors[pr.Background])
			if err != nil || ratio < pr.Minimum {
				t.Errorf("%s: %s on %s is %.2f:1, needs %.1f:1", name, pr.Foreground, pr.Background, ratio, pr.Minimum)
			}
		}
		for _, m := range Meanings[:2] {
			hue, _, _ := Hue(colors[m.Token])
			ok := false
			for _, r := range m.Hues {
				ok = ok || hue >= r[0] && hue <= r[1]
			}
			if !ok {
				t.Errorf("%s: protected %s isn't %s", name, m.Token, m.Name)
			}
		}
	}
}

func TestProtectedTokensAreRendered(t *testing.T) {
	d := Default()
	css := d.CSS()
	protected := css[strings.Index(css, "@layer rs-protected {"):]
	for _, tok := range ProtectedTokens {
		if !strings.Contains(protected, "--rs-"+tok+":") {
			t.Errorf("the protected layer lacks --rs-%s", tok)
		}
	}
	// Nothing but the protected layer sets them.
	if strings.Contains(css[:strings.Index(css, "@layer rs-protected {")], "--rs-critical") {
		t.Error("protected tokens appear outside their layer")
	}
}
