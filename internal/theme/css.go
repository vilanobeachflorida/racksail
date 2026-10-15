package theme

import (
	"fmt"
	"strings"
	"unicode"
)

// cssVar turns a token name such as surfaceRaised into --rs-surface-raised.
func cssVar(name string) string {
	var b strings.Builder
	b.WriteString("--rs-")
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('-')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func paletteVars(p Palette) [][2]string {
	vars := [][2]string{
		{"surface", string(p.Surface)}, {"surfaceRaised", string(p.SurfaceRaised)}, {"surfaceSunken", string(p.SurfaceSunken)},
		{"text", string(p.Text)}, {"textMuted", string(p.TextMuted)}, {"border", string(p.Border)}, {"borderStrong", string(p.BorderStrong)},
		{"brand", string(p.Brand)}, {"brandText", string(p.BrandText)}, {"accent", string(p.Accent)}, {"accentText", string(p.AccentText)},
		{"success", string(p.Success)}, {"successText", string(p.SuccessText)}, {"warning", string(p.Warning)}, {"warningText", string(p.WarningText)},
		{"danger", string(p.Danger)}, {"dangerText", string(p.DangerText)}, {"info", string(p.Info)}, {"infoText", string(p.InfoText)},
		{"focus", string(p.Focus)},
	}
	for i, c := range p.Chart {
		vars = append(vars, [2]string{fmt.Sprintf("chart%d", i+1), string(c)})
	}
	return vars
}

func writeBlock(b *strings.Builder, selector, indent string, vars [][2]string) {
	fmt.Fprintf(b, "%s%s {\n", indent, selector)
	for _, v := range vars {
		if v[1] == "" {
			continue
		}
		fmt.Fprintf(b, "%s  %s: %s;\n", indent, cssVar(v[0]), v[1])
	}
	fmt.Fprintf(b, "%s}\n", indent)
}

// CSS renders the theme as CSS custom properties. The light palette and the
// shared tokens go on :root; the dark palette applies when the page says
// data-theme="dark", or follows the system when it says neither.
func (t *Theme) CSS() string {
	tok := t.Tokens
	shared := [][2]string{
		{"fontSans", fontStack(tok.Typography.Sans.Family, "ui-sans-serif, system-ui, sans-serif")},
		{"fontMono", fontStack(tok.Typography.Mono.Family, "ui-monospace, monospace")},
		{"fontSizeBase", tok.Typography.BaseSize},
		{"radiusSm", tok.Radius.SM}, {"radiusMd", tok.Radius.MD}, {"radiusLg", tok.Radius.LG}, {"radiusXl", tok.Radius.XL},
		{"shadowSm", tok.Shadows.SM}, {"shadowMd", tok.Shadows.MD}, {"shadowLg", tok.Shadows.LG},
		{"motionFast", tok.Motion.Fast}, {"motionNormal", tok.Motion.Normal}, {"motionSlow", tok.Motion.Slow},
	}
	if w := tok.Typography.Weights; w.Regular != 0 {
		shared = append(shared,
			[2]string{"weightRegular", fmt.Sprint(w.Regular)}, [2]string{"weightMedium", fmt.Sprint(w.Medium)},
			[2]string{"weightSemibold", fmt.Sprint(w.Semibold)}, [2]string{"weightBold", fmt.Sprint(w.Bold)})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "/* Generated from the %s theme by `racksail theme css`. Don't edit by hand. */\n", t.Name)
	writeBlock(&b, ":root", "", append(append([][2]string{}, shared...), paletteVars(tok.Light)...))
	b.WriteString("\n")
	writeBlock(&b, `:root[data-theme="dark"]`, "", paletteVars(tok.Dark))
	b.WriteString("\n@media (prefers-color-scheme: dark) {\n")
	writeBlock(&b, `:root:not([data-theme="light"])`, "  ", paletteVars(tok.Dark))
	b.WriteString("}\n")
	return b.String()
}

func fontStack(family, fallback string) string {
	if family == "" {
		return fallback
	}
	return fmt.Sprintf("%q, %s", family, fallback)
}
