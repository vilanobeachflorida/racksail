package theme

import (
	"strings"
	"testing"
)

func TestCheckCustomCSS(t *testing.T) {
	fine := `/* a comment with { braces } and url(https://x) */
.sidebar { --rs-surface: #101010; background: url("data:image/png;base64,AAAA"); }
@media (max-width: 40rem) { .x { content: "}"; } }`
	if err := CheckCustomCSS(fine); err != nil {
		t.Errorf("fine CSS refused: %v", err)
	}
	wrapped, err := WrapCustomCSS(fine)
	if err != nil || !strings.HasPrefix(wrapped, "@layer rs-custom {\n") || !strings.HasSuffix(wrapped, "\n}\n") {
		t.Errorf("wrapped = %q, %v", wrapped, err)
	}
	for name, css := range map[string]string{
		"import":              `@import url("x.css");`,
		"remote image":        `.a { background: url(https://x.example/a.png) }`,
		"remote font":         `@font-face { src: url('//fonts.example/a.woff2') }`,
		"protected token":     `:root { --rs-critical-danger: green }`,
		"protected element":   `[data-critical] { display: none }`,
		"layer":               `@layer rs-protected { :root { --rs-surface: red } }`,
		"closing the wrapper": `} :root { --rs-danger: green } .a {`,
		"open block":          `.a { color: red`,
		"open comment":        `.a { } /* never closed`,
		"open string":         `.a { content: "x }`,
		"escape":              `.a { --rs-crit\69 cal-danger: red }`,
		"html":                `.a{}</style><script>alert(1)</script>`,
		"expression":          `.a { width: expression(alert(1)) }`,
		"binding":             `.a { -moz-binding: url(x.xml) }`,
		"too long":            strings.Repeat("a", MaxCustomCSSBytes+1),
	} {
		if err := CheckCustomCSS(css); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}
