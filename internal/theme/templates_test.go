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

func TestCheckTemplateRefusals(t *testing.T) {
	for name, src := range map[string]string{
		"unknown variable": "<p>{{password}}</p>",
		"logic":            "{{#if title}}<p>{{title}}</p>{{/if}}",
		"script":           "<script>alert(1)</script>",
		"handler":          `<img src="{{logo}}" onerror="x()">`,
		"remote image":     `<img src="https://tracker.example/pixel.gif">`,
		"remote style":     `<div style="background:url(https://x.example/a.png)"></div>`,
		"import":           `<style>@import "x.css";</style>`,
		"stylesheet":       `<link rel="stylesheet" href="x.css">`,
		"form":             `<form action="https://x.example"></form>`,
		"script link":      `<a href="javascript:alert(1)">x</a>`,
		"refresh":          `<meta http-equiv="refresh" content="0;url=https://x.example">`,
	} {
		if err := CheckTemplate(TemplateError, src); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	if err := CheckTemplate("invoice", "x"); err == nil {
		t.Error("unknown kinds are refused")
	}
	ok := `<a href="https://example.com">{{siteName}}</a><img src="data:image/png;base64,AAAA" alt=""><div style="background:url({{logo}})"></div>`
	if err := CheckTemplate(TemplateError, ok); err != nil {
		t.Errorf("links, data URLs and variables are fine: %v", err)
	}
}
