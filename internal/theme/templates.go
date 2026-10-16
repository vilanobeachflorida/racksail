package theme

import (
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"mime"
	"path"
	"regexp"
	"slices"
	"strings"

	nethtml "golang.org/x/net/html"
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
// are logic-less: {{name}} is replaced by the variable's value, escaped for
// where it stands, and that is all they can do. Two variables are written
// by the renderer rather than escaped: style, the theme's colors as CSS for
// a style element, and logo, an image element showing the theme's logo, or
// nothing.
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

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z]+)\s*\}\}`)

// The elements templates may use: text, structure, tables, links and
// images. Nothing that runs code, embeds a document, takes input or brings
// in markup of another language.
var templateElements = map[string]bool{
	"html": true, "head": true, "body": true, "title": true, "meta": true, "style": true,
	"main": true, "header": true, "footer": true, "nav": true, "section": true, "article": true, "aside": true,
	"div": true, "span": true, "p": true, "br": true, "hr": true, "wbr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"a": true, "img": true,
	"table": true, "caption": true, "colgroup": true, "col": true, "thead": true, "tbody": true, "tfoot": true,
	"tr": true, "th": true, "td": true,
	"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"strong": true, "b": true, "em": true, "i": true, "u": true, "s": true, "small": true, "sub": true, "sup": true,
	"mark": true, "code": true, "pre": true, "kbd": true, "samp": true, "blockquote": true, "q": true, "abbr": true,
	"time": true, "address": true, "figure": true, "figcaption": true, "center": true, "font": true,
	"details": true, "summary": true,
}

// The attributes templates may use on any element. None of them loads
// anything or runs code.
var templateAttributes = map[string]bool{
	"class": true, "id": true, "lang": true, "dir": true, "title": true, "style": true, "role": true,
	"hidden": true, "translate": true, "tabindex": true, "width": true, "height": true, "align": true, "valign": true,
	"bgcolor": true, "border": true, "cellpadding": true, "cellspacing": true, "colspan": true, "rowspan": true,
	"scope": true, "headers": true, "nowrap": true, "color": true, "face": true, "size": true, "start": true,
	"reversed": true, "type": true, "datetime": true, "abbr": true, "span": true, "open": true, "summary": true,
	"xmlns": true, "media": true,
}

// Attributes only some elements may have.
var templateElementAttributes = map[string][]string{
	"a":    {"href", "target", "rel", "name"},
	"img":  {"src", "alt", "loading", "decoding"},
	"meta": {"charset", "name", "content"},
}

// The meta names templates may set: page settings, never anything that
// refers to another document.
var templateMetaNames = []string{
	"viewport", "color-scheme", "supported-color-schemes", "robots", "description", "format-detection",
	"x-apple-disable-message-reformatting", "referrer", "theme-color", "author", "generator",
}

// The schemes links may use. Following a link is the reader's choice, so
// any web or mail address is fine; scripts and documents built from data
// aren't.
var templateLinkSchemes = []string{"http", "https", "mailto", "tel"}

// CheckTemplate makes sure a theme's template only uses the variables its
// kind has and loads nothing: only the elements and attributes of plain
// pages and emails, no scripts, forms, event handlers, embedded documents
// or conditional comments, images only from the logo variable or data:
// URLs, styles that refer to nothing outside the page, and links only to
// web and mail addresses. The template is read as browsers read HTML, so
// entities and unusual attribute orders can't hide anything.
func CheckTemplate(kind, src string) error {
	_, err := parseTemplate(kind, src)
	return err
}

func parseTemplate(kind, src string) (*template.Template, error) {
	allowed, ok := TemplateVars[kind]
	if !ok {
		return nil, fmt.Errorf("there is no %q template", kind)
	}
	for _, m := range placeholder.FindAllStringSubmatch(src, -1) {
		if !slices.Contains(allowed, m[1]) {
			return nil, fmt.Errorf("%s templates have no {{%s}} variable; they can use %s", kind, m[1], strings.Join(allowed, ", "))
		}
	}
	rest := placeholder.ReplaceAllString(src, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		return nil, errors.New("templates can only use {{variable}} placeholders; they have no logic")
	}
	if err := checkTemplateMarkup(src, true); err != nil {
		return nil, err
	}
	t, err := template.New(kind).Parse(placeholder.ReplaceAllString(src, "{{.$1}}"))
	if err != nil {
		return nil, fmt.Errorf("the template can't be read: %w", err)
	}
	// The escaper works out where each variable stands the first time the
	// template runs; doing it now refuses a template it can't make sense of.
	if err := t.Execute(io.Discard, templateData(kind, nil, "", "")); err != nil {
		return nil, fmt.Errorf("the template can't be filled in: %w", err)
	}
	return t, nil
}

// checkTemplateMarkup checks a template's HTML. With variables, it also
// checks where the {{variables}} stand; without, as for a filled-in
// template, braces are only text.
func checkTemplateMarkup(src string, variables bool) error {
	z := nethtml.NewTokenizer(strings.NewReader(src))
	inside := ""
	for {
		switch z.Next() {
		case nethtml.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return nil
			}
			return fmt.Errorf("the template can't be read: %w", z.Err())
		case nethtml.TextToken:
			text := string(z.Text())
			if err := checkTemplateText(inside, text, variables); err != nil {
				return err
			}
		case nethtml.StartTagToken, nethtml.SelfClosingTagToken:
			tok := z.Token()
			if err := checkTemplateTag(tok, variables); err != nil {
				return err
			}
			if tok.Data == "style" || tok.Data == "title" {
				inside = tok.Data
			}
		case nethtml.EndTagToken:
			if name, _ := z.TagName(); string(name) == inside {
				inside = ""
			}
		case nethtml.CommentToken:
			text := strings.ToLower(string(z.Text()))
			if strings.Contains(text, "[if") || strings.Contains(text, "[endif") || strings.ContainsAny(text, "<>") {
				return errors.New("templates can't contain conditional comments or markup inside comments; some mail programs read them as HTML")
			}
			if variables && placeholder.MatchString(text) {
				return errors.New("templates can't put variables in comments")
			}
		case nethtml.DoctypeToken:
			if variables && placeholder.MatchString(string(z.Text())) {
				return errors.New("templates can't put variables in the doctype")
			}
		}
	}
}

func checkTemplateText(inside, text string, variables bool) error {
	names := []string{}
	if variables {
		for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
			names = append(names, m[1])
		}
	}
	switch inside {
	case "style":
		for _, name := range names {
			if name != "style" {
				return fmt.Errorf("templates can't put {{%s}} in a style element; only {{style}} goes there", name)
			}
		}
		css := text
		if variables {
			css = placeholder.ReplaceAllString(text, "")
		}
		return templateCSSRules.checkSheet(css)
	case "title":
		for _, name := range names {
			if name == "style" || name == "logo" {
				return fmt.Errorf("templates can't put {{%s}} in the title", name)
			}
		}
	default:
		if slices.Contains(names, "style") {
			return errors.New("templates can only put {{style}} in a style element")
		}
	}
	return nil
}

func checkTemplateTag(tok nethtml.Token, variables bool) error {
	if !templateElements[tok.Data] {
		return fmt.Errorf("templates can't contain <%s> elements", tok.Data)
	}
	for _, a := range tok.Attr {
		key := a.Key
		if a.Namespace != "" {
			key = a.Namespace + ":" + a.Key
		}
		allowed := templateAttributes[key] || slices.Contains(templateElementAttributes[tok.Data], key) ||
			strings.HasPrefix(key, "aria-") || strings.HasPrefix(key, "data-") || strings.HasPrefix(key, "xmlns:")
		switch {
		case strings.HasPrefix(key, "on"):
			return errors.New("templates can't have event handlers")
		case !allowed:
			return fmt.Errorf("templates can't use the %s attribute on <%s>", key, tok.Data)
		}
		var names []string
		if variables {
			for _, m := range placeholder.FindAllStringSubmatch(a.Val, -1) {
				names = append(names, m[1])
			}
		}
		if slices.Contains(names, "style") || slices.Contains(names, "logo") {
			return errors.New("templates can only put {{style}} in a style element and {{logo}} between elements")
		}
		switch key {
		case "style":
			if len(names) > 0 {
				return errors.New("templates can't put variables in style attributes")
			}
			if err := templateCSSRules.checkStyle(a.Val); err != nil {
				return err
			}
		case "src":
			if len(names) > 0 || !hasURLPrefix(a.Val, "data:image/") {
				return fmt.Errorf("templates can't load %q; images come from {{logo}} or data: URLs", a.Val)
			}
		case "href":
			if err := checkTemplateLink(a.Val, len(names) > 0); err != nil {
				return err
			}
		case "name":
			if tok.Data == "meta" && !slices.Contains(templateMetaNames, strings.ToLower(strings.TrimSpace(a.Val))) {
				return fmt.Errorf("templates can't set the %q meta name; they can set %s", a.Val, strings.Join(templateMetaNames, ", "))
			}
		}
	}
	return nil
}

// checkTemplateLink allows links to web and mail addresses. A variable can
// be the whole address, which the renderer checks when it fills it in, or
// come after a scheme or a path, where it can't change what kind of
// address it is.
func checkTemplateLink(href string, variables bool) error {
	trimmed := strings.TrimFunc(href, func(c rune) bool { return c <= ' ' })
	if variables && placeholder.FindString(trimmed) == trimmed {
		return nil
	}
	prefix := trimmed
	if variables {
		prefix = trimmed[:placeholder.FindStringIndex(trimmed)[0]]
	}
	prefix = strings.Map(func(c rune) rune {
		if c == '\t' || c == '\n' || c == '\r' {
			return -1
		}
		return c
	}, prefix)
	if scheme, ok := urlScheme(prefix); ok {
		if !slices.Contains(templateLinkSchemes, strings.ToLower(scheme)) {
			return fmt.Errorf("templates can't link to %q; links go to web and mail addresses", href)
		}
		return nil
	}
	if variables && !strings.HasPrefix(prefix, "/") && !strings.HasPrefix(prefix, "#") &&
		!strings.HasPrefix(prefix, "?") && !strings.HasPrefix(prefix, ".") {
		return fmt.Errorf("templates can't build the link %q; a variable is the whole address, or follows https://, mailto: or a path", href)
	}
	return nil
}

// urlScheme returns the scheme a URL starts with, if it has one.
func urlScheme(u string) (string, bool) {
	for i, c := range u {
		switch {
		case c == ':':
			return u[:i], i > 0
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return "", false
		}
	}
	return "", false
}

// RenderTemplate fills in a template for this theme. vars holds the values
// of the variables, which are escaped for where they stand; style and logo
// are written by the renderer from the theme and logo, a file of the
// package or nil.
func (t *Theme) RenderTemplate(kind, src string, vars map[string]string, logo *Asset) (string, error) {
	tmpl, err := parseTemplate(kind, src)
	if err != nil {
		return "", err
	}
	logoHTML := ""
	if logo != nil {
		logoHTML = fmt.Sprintf(`<img class="logo" src="%s" alt="%s">`, logo.DataURL(), html.EscapeString(vars["brandName"]))
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, templateData(kind, vars, t.templateCSS(), logoHTML)); err != nil {
		return "", fmt.Errorf("filling in the %s template: %w", kind, err)
	}
	return b.String(), nil
}

// templateData is what a template is filled in with: every variable of its
// kind, empty unless vars has it.
func templateData(kind string, vars map[string]string, css, logo string) map[string]any {
	data := map[string]any{}
	for _, name := range TemplateVars[kind] {
		v := vars[name]
		if urlVars[name] && !safeURL(v) {
			v = ""
		}
		data[name] = v
	}
	if data["lang"] == "" {
		data["lang"] = "en"
	}
	if data["dir"] != "rtl" {
		data["dir"] = "ltr"
	}
	data["style"] = template.CSS(css)
	data["logo"] = template.HTML(logo)
	return data
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
