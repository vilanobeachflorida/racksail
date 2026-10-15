package theme

import (
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"mime"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Template kinds: notification emails, and the pages visitors of hosted
// sites see when something is wrong, a site is in maintenance or an account
// is suspended.
const (
	TemplateEmail       = "email"
	TemplateError       = "error"
	TemplateMaintenance = "maintenance"
	TemplateSuspended   = "suspended"
)

// TemplateVars are the variables each kind of template can use. Templates
// are logic-less: {{name}} is replaced by the variable's value, escaped,
// and that is all they can do. Two variables are written by the renderer
// rather than escaped: style, the theme's colors as CSS, and logo, an image
// element showing the theme's logo, or nothing.
var TemplateVars = map[string][]string{
	TemplateEmail:       {"style", "logo", "lang", "dir", "brandName", "title", "preheader", "body", "actionLabel", "actionUrl", "footer"},
	TemplateError:       {"style", "logo", "lang", "dir", "brandName", "siteName", "status", "title", "message", "requestId"},
	TemplateMaintenance: {"style", "logo", "lang", "dir", "brandName", "siteName", "title", "message", "until"},
	TemplateSuspended:   {"style", "logo", "lang", "dir", "brandName", "siteName", "title", "message", "contact"},
}

//go:embed templates/*.html
var defaultTemplates embed.FS

// DefaultTemplate returns the template Racksail uses when a theme has none
// of that kind.
func DefaultTemplate(kind string) (string, error) {
	if _, ok := TemplateVars[kind]; !ok {
		return "", fmt.Errorf("there is no %q template", kind)
	}
	data, err := defaultTemplates.ReadFile("templates/" + kind + ".html")
	return string(data), err
}

var (
	placeholder   = regexp.MustCompile(`\{\{\s*([A-Za-z]+)\s*\}\}`)
	forbiddenTags = regexp.MustCompile(`(?i)<\s*(script|iframe|frame|frameset|object|embed|applet|link|base|form|input|button|textarea|select|meta\s+http-equiv)\b`)
	eventAttr     = regexp.MustCompile(`(?i)[\s"'/]on[a-z]+\s*=`)
	srcAttr       = regexp.MustCompile(`(?i)\s(src|srcset|background|poster|data)\s*=\s*["']?\s*([^"'\s>]*)`)
)

// CheckTemplate makes sure a theme's template only uses the variables its
// kind has and loads nothing: no scripts, no forms, no event handlers, and
// images only from variables or data URLs. Links may point anywhere, since
// following one is the reader's choice.
func CheckTemplate(kind, src string) error {
	allowed, ok := TemplateVars[kind]
	if !ok {
		return fmt.Errorf("there is no %q template", kind)
	}
	for _, m := range placeholder.FindAllStringSubmatch(src, -1) {
		if !slices.Contains(allowed, m[1]) {
			return fmt.Errorf("%s templates have no {{%s}} variable; they can use %s", kind, m[1], strings.Join(allowed, ", "))
		}
	}
	rest := placeholder.ReplaceAllString(src, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		return errors.New("templates can only use {{variable}} placeholders; they have no logic")
	}
	if m := forbiddenTags.FindString(src); m != "" {
		return fmt.Errorf("templates can't contain %s>", strings.TrimSpace(m))
	}
	if eventAttr.MatchString(src) {
		return errors.New("templates can't have event handlers")
	}
	if strings.Contains(strings.ToLower(src), "javascript:") {
		return errors.New("templates can't contain javascript: links")
	}
	for _, m := range srcAttr.FindAllStringSubmatch(src, -1) {
		if !strings.HasPrefix(m[2], "{{") && !strings.HasPrefix(strings.ToLower(m[2]), "data:image/") {
			return fmt.Errorf("templates can't load %q; use a variable or a data URL", m[2])
		}
	}
	for _, m := range cssURL.FindAllStringSubmatch(src, -1) {
		v := strings.ToLower(m[1])
		if !strings.HasPrefix(v, "{{") && !strings.HasPrefix(v, "data:image/") && !strings.HasPrefix(v, "#") {
			return fmt.Errorf("templates can't load %q; use a variable or a data URL", m[1])
		}
	}
	if strings.Contains(strings.ToLower(src), "@import") {
		return errors.New("templates can't import style sheets")
	}
	return nil
}

// RenderTemplate fills in a template for this theme. vars holds the values
// of the variables, which are escaped; style and logo are written by the
// renderer from the theme and logo, a file of the package or nil.
func (t *Theme) RenderTemplate(kind, src string, vars map[string]string, logo *Asset) (string, error) {
	if err := CheckTemplate(kind, src); err != nil {
		return "", err
	}
	values := map[string]string{}
	for k, v := range vars {
		if urlVars[k] && !safeURL(v) {
			v = ""
		}
		values[k] = html.EscapeString(v)
	}
	if values["lang"] == "" {
		values["lang"] = "en"
	}
	if values["dir"] != "rtl" {
		values["dir"] = "ltr"
	}
	values["style"] = t.templateCSS()
	values["logo"] = ""
	if logo != nil {
		alt := values["brandName"]
		values["logo"] = fmt.Sprintf(`<img class="logo" src="%s" alt="%s">`, logo.DataURL(), alt)
	}
	return placeholder.ReplaceAllStringFunc(src, func(m string) string {
		return values[placeholder.FindStringSubmatch(m)[1]]
	}), nil
}

// urlVars are the variables templates put in links.
var urlVars = map[string]bool{"actionUrl": true}

// safeURL allows web and mail links only, so a variable can't turn a link
// into a script.
func safeURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:")
}

// Asset is a file of a theme package.
type Asset struct {
	Path string
	Data []byte
}

// DataURL encodes the asset to embed it in a page or an email.
func (a Asset) DataURL() string {
	typ := mime.TypeByExtension(path.Ext(a.Path))
	if typ == "" {
		typ = "application/octet-stream"
	}
	if i := strings.IndexByte(typ, ';'); i >= 0 {
		typ = typ[:i]
	}
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
}

// Logo returns the package's logo for light backgrounds, which templates
// show, or nil when it has none.
func (p *Package) Logo() *Asset {
	for _, name := range []string{p.Theme.Assets.LogoLight, p.Theme.Assets.Mark} {
		if data, ok := p.Files[name]; ok && name != "" {
			return &Asset{Path: name, Data: data}
		}
	}
	return nil
}

// Template returns the package's template of a kind, or the default one.
func (p *Package) Template(kind string) (string, error) {
	file := map[string]string{
		TemplateEmail: p.Theme.Templates.Email, TemplateError: p.Theme.Templates.Error,
		TemplateMaintenance: p.Theme.Templates.Maintenance, TemplateSuspended: p.Theme.Templates.Suspended,
	}[kind]
	if data, ok := p.Files[file]; ok && file != "" {
		return string(data), nil
	}
	return DefaultTemplate(kind)
}

// Render fills in the package's template of a kind, or the default one,
// with the theme's colors and logo.
func (p *Package) Render(kind string, vars map[string]string) (string, error) {
	src, err := p.Template(kind)
	if err != nil {
		return "", err
	}
	return p.Theme.RenderTemplate(kind, src, vars, p.Logo())
}

// templateCSS is the theme's colors and fonts for templates: custom
// properties for light mode, and for dark mode where the reader prefers it.
func (t *Theme) templateCSS() string {
	pick := func(p Palette) string {
		var b strings.Builder
		for _, v := range [][2]string{
			{"surface", string(p.Surface)}, {"surfaceRaised", string(p.SurfaceRaised)}, {"text", string(p.Text)},
			{"textMuted", string(p.TextMuted)}, {"border", string(p.Border)}, {"brand", string(p.Brand)},
			{"brandText", string(p.BrandText)}, {"accent", string(p.Accent)}, {"accentText", string(p.AccentText)},
			{"danger", string(p.Danger)}, {"warning", string(p.Warning)}, {"success", string(p.Success)},
		} {
			fmt.Fprintf(&b, "%s:%s;", cssVar(v[0]), v[1])
		}
		return b.String()
	}
	typo := t.Tokens.Typography
	return fmt.Sprintf(":root{%s%s:%s;%s:%s;color-scheme:light dark}@media (prefers-color-scheme:dark){:root{%s}}",
		pick(t.Tokens.Light),
		cssVar("fontSans"), fontStack(typo.Sans.Family, "ui-sans-serif, system-ui, sans-serif"),
		cssVar("fontMono"), fontStack(typo.Mono.Family, "ui-monospace, monospace"),
		pick(t.Tokens.Dark))
}
