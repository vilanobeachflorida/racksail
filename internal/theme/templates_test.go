package theme

import (
	"strings"
	"testing"
)

func TestDefaultTemplates(t *testing.T) {
	for kind, vars := range TemplateVars {
		src, err := DefaultTemplate(kind)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckTemplate(kind, src); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
		// Every default template uses every variable its kind offers.
		for _, v := range vars {
			if !strings.Contains(src, "{{"+v+"}}") {
				t.Errorf("%s doesn't use {{%s}}", kind, v)
			}
		}
	}
	if _, err := DefaultTemplate("invoice"); err == nil {
		t.Error("there is no invoice template")
	}
}

func TestRenderTemplate(t *testing.T) {
	th := Default()
	pkg := &Package{Theme: th, Files: map[string][]byte{}}
	out, err := pkg.Render(TemplateError, map[string]string{
		"title": "This site can't be reached", "message": "<b>Bad gateway</b> & more",
		"status": "502", "siteName": "example.com", "requestId": "req_1", "lang": "de",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<html lang="de" dir="ltr">`,
		"&lt;b&gt;Bad gateway&lt;/b&gt; &amp; more",
		"--rs-surface:#F7F9FC;",
		"@media (prefers-color-scheme:dark)",
		"<title>This site can&#39;t be reached</title>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page lacks %q", want)
		}
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "<img") {
		t.Error("placeholders remain, or a logo appeared without one")
	}

	pkg.Theme.Assets.LogoLight = "logo.svg"
	pkg.Files["logo.svg"] = []byte(logoSVG)
	out, err = pkg.Render(TemplateEmail, map[string]string{
		"brandName": `Acme "Hosting"`, "actionUrl": "javascript:alert(1)", "actionLabel": "Open", "dir": "rtl",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `<img class="logo" src="data:image/svg+xml;base64,`) || !strings.Contains(out, `alt="Acme &#34;Hosting&#34;"`) {
		t.Errorf("logo missing or unescaped: %s", out)
	}
	if strings.Contains(out, "javascript:") || !strings.Contains(out, `href=""`) || !strings.Contains(out, `dir="rtl"`) {
		t.Error("a script URL got into a link")
	}
	out, _ = pkg.Render(TemplateEmail, map[string]string{"actionUrl": "https://panel.example.com/sites?a=1&b=2"})
	if !strings.Contains(out, `href="https://panel.example.com/sites?a=1&amp;b=2"`) {
		t.Error("web links are kept, escaped")
	}
}

// Values are escaped for where they stand, so one in an unquoted attribute
// can't add attributes of its own, and one at the start of a link can't
// make it a script.
func TestRenderEscapesForTheContext(t *testing.T) {
	th := Default()
	src := `<div title={{message}}>{{title}}</div><a href="{{siteName}}">x</a><a href="mailto:{{contact}}">y</a>`
	out, err := th.RenderTemplate(TemplateSuspended, src, map[string]string{
		"message": "x onfocus=alert(1) autofocus", "title": "<script>", "siteName": "javascript:alert(1)", "contact": "a@example.com",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, " onfocus=") || strings.Contains(out, "<script>") || strings.Contains(out, "javascript:") {
		t.Errorf("a value escaped its place: %s", out)
	}
	if !strings.Contains(out, `href="mailto:a@example.com"`) {
		t.Errorf("a mail link lost its address: %s", out)
	}
	if err := checkTemplateMarkup(out, false); err != nil {
		t.Errorf("the filled-in page breaks the rules: %v", err)
	}
}

func TestCheckTemplateRefusals(t *testing.T) {
	for name, src := range map[string]string{
		"unknown variable": "<p>{{password}}</p>",
		"logic":            "{{#if title}}<p>{{title}}</p>{{/if}}",
		"script":           "<script>alert(1)</script>",
		"handler":          `<img src="data:image/png;base64,AA" onerror="x()">`,
		"remote image":     `<img src="https://tracker.example/pixel.gif">`,
		"image variable":   `<img src="{{siteName}}">`,
		"srcset":           `<img src="data:image/png;base64,AA" srcset="https://tracker.example/p.gif 2x">`,
		"remote style":     `<div style="background:url(https://x.example/a.png)"></div>`,
		"style variable":   `<div style="color: {{title}}"></div>`,
		"logo in a style":  `<div style="background:url({{logo}})"></div>`,
		"import":           `<style>@import "x.css";</style>`,
		// The review's bypasses: an escaped @import, image-set, a refresh
		// whose attributes come in another order, an image inside SVG and an
		// entity-encoded script link.
		"escaped import":       `<style>@\69mport "https://evil.example/x.css";</style>`,
		"image-set":            `<style>.a{background:image-set("https://evil.example/a.png" 1x)}</style>`,
		"refresh":              `<meta content="0;url=https://evil.example/" http-equiv="refresh">`,
		"svg image":            `<svg><image href="https://track.example/p.png"/></svg>`,
		"encoded script link":  `<a href="java&#115;cript:alert(1)">x</a>`,
		"tabbed script link":   "<a href=\"java\tscript:alert(1)\">x</a>",
		"data link":            `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
		"scheme from variable": `<a href="java{{siteName}}">x</a>`,
		"variable then text":   `<a href="{{siteName}}:alert(1)">x</a>`,
		"variable in style":    `<style>{{style}} .a { content: "{{title}}" }</style>`,
		"style outside":        `<p>{{style}}</p>`,
		"logo in the title":    `<title>{{logo}}</title>`,
		"variable in comment":  `<!-- {{title}} -->`,
		"conditional comment":  `<!--[if mso]><v:image src="https://evil.example/a.png"/><![endif]-->`,
		"revealed comment":     `<![if !mso]><p>x</p><![endif]>`,
		"stylesheet":           `<link rel="stylesheet" href="x.css">`,
		"base":                 `<base href="https://evil.example/">`,
		"form":                 `<form action="https://x.example"></form>`,
		"iframe":               `<iframe srcdoc="x"></iframe>`,
		"table background":     `<table background="https://evil.example/a.png"></table>`,
		"meta name":            `<meta name="msapplication-config" content="https://evil.example/c.xml">`,
		"attribute variable":   `<div {{title}}>x</div>`,
		"unknown attribute":    `<a ping="https://evil.example/">x</a>`,
	} {
		if err := CheckTemplate(TemplateSuspended, src); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	if err := CheckTemplate("invoice", "x"); err == nil {
		t.Error("unknown kinds are refused")
	}
	ok := `<!doctype html><html lang="{{lang}}" dir="{{dir}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">` +
		`<title>{{title}}</title><style>{{style}} .a { background: url(data:image/png;base64,AAAA) } @media (max-width: 30rem) { .b { color: var(--rs-text) !important } }</style></head>` +
		`<body><!-- the page -->{{logo}}<a href="https://example.com/?site={{siteName}}" target="_blank" rel="noopener">{{siteName}}</a>` +
		`<a href="{{contact}}">{{contact}}</a><a href="mailto:{{contact}}">mail</a><a href="/help#{{siteName}}">help</a>` +
		`<img src="data:image/png;base64,AAAA" alt="" width="10"><table role="presentation" cellpadding="0"><tr><td align="center" style="padding: 8px">{{message}}</td></tr></table>` +
		`<div title={{message}} data-x="1" aria-label="{{title}}"></div></body></html>`
	if err := CheckTemplate(TemplateSuspended, ok); err != nil {
		t.Errorf("plain markup, links, data URLs and variables are fine: %v", err)
	}
}
