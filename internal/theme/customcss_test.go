package theme

import (
	"strings"
	"testing"
)

func TestCheckCustomCSS(t *testing.T) {
	fine := `/* a comment with { braces } and url(https://x) */
.sidebar { --rs-surface: #101010; background: url("data:image/png;base64,AAAA"); }
.logo { background-image: image-set("data:image/png;base64,AAAA" 1x, url(data:image/png;base64,BBBB) 2x); }
.hover\:bg-muted:hover { color: var(--rs-text); }
@media (max-width: 40rem) { .x { content: "}"; } }
@font-face { font-family: "Acme"; src: url(data:font/woff2;base64,AAAA) format("woff2"); }
@keyframes spin { from { rotate: 0deg } to { rotate: 360deg } }`
	if err := CheckCustomCSS(fine); err != nil {
		t.Errorf("fine CSS refused: %v", err)
	}
	for name, css := range map[string]string{
		// Browsers read the brace inside the unquoted url as part of the
		// URL; reading it as a block would let the next } close the layer.
		"brace in a url":       `.a{background:url(data:image/png{)}} :root{--rs-surface:#000000} [role=alertdialog]{display:none}`,
		"important":            `.critical-panel{background-color:#00ff00 !important}`,
		"important on a token": `:root{--rs-text:#777 ! important}`,
		"image-set":            `.a{background:image-set("https://evil.example/a.png" 1x)}`,
		"webkit image-set":     `.a{background:-webkit-image-set("//evil.example/a.png" 1x)}`,
		"src function":         `.a{background:src("https://evil.example/a.png")}`,
		"image function":       `.a{background:image("https://evil.example/a.png")}`,
		"cross-fade":           `.a{background:cross-fade(url(data:image/png;base64,AA), "https://evil.example/b.png", 50%)}`,
		"url from a variable":  `.a{--u:"https://evil.example/a.png";background:image-set(var(--u) 1x)}`,
		"import":               `@import url("x.css");`,
		"escaped import":       `@\69mport "https://evil.example/x.css";`,
		"remote image":         `.a { background: url(https://x.example/a.png) }`,
		"escaped url":          `.a { background: u\72l(https://x.example/a.png) }`,
		"spaced remote url":    `.a { background: url(" https://x.example/a.png") }`,
		"remote font":          `@font-face { src: url('//fonts.example/a.woff2') }`,
		"protected token":      `:root { --rs-critical-danger: green }`,
		"protected reference":  `.a { color: var(--rs-critical-danger) }`,
		"escaped protection":   `.a { --rs-crit\69 cal-danger: red }`,
		"protected element":    `[data-critical] { display: none }`,
		"escaped element":      `[data\-critical] { display: none }`,
		"protected class":      `.critical-area { display: none }`,
		"registered token":     `@property --rs-text { syntax: "*"; inherits: false }`,
		"layer":                `@layer rs-protected { :root { --rs-surface: red } }`,
		"layer statement":      `@layer rs-custom;`,
		"namespace":            `@namespace svg url(http://www.w3.org/2000/svg);`,
		"scope":                `@scope (.a) { .b { color: red } }`,
		"nested font":          `@media screen { @font-face { font-family: x } }`,
		"closing the wrapper":  `} :root { --rs-danger: green } .a {`,
		"brace in parens":      `.a { width: calc(1px } ) }`,
		"open block":           `.a { color: red`,
		"open comment":         `.a { } /* never closed`,
		"open string":          `.a { content: "x }`,
		"broken string":        ".a { content: \"x\n}\" }",
		"bad url":              `.a { background: url(data:image/png"x) }`,
		"stray backslash":      ".a { color: red }\\\n.b{}",
		"declaration outside":  `color: red; .a { }`,
		"semicolon outside":    `.a { }; .b { }`,
		"unfinished rule":      `.a { } .b`,
		"html":                 `.a{}</style><script>alert(1)</script>`,
		"html comment marker":  `<!-- .a{} -->`,
		"expression":           `.a { width: expression(alert(1)) }`,
		"binding":              `.a { -moz-binding: url(data:image/png;base64,AA) }`,
		"behavior":             `.a { behavior: url(data:image/png;base64,AA) }`,
		"too long":             strings.Repeat("a", MaxCustomCSSBytes+1),
	} {
		if err := CheckCustomCSS(css); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	err := CheckCustomCSS(`.a{color:red !important}`)
	if err == nil || !strings.Contains(err.Error(), "!important") {
		t.Errorf("the reason names !important: %v", err)
	}
	err = CheckCustomCSS(`.a{background:image-set("https://evil.example/a.png" 1x)}`)
	if err == nil || !strings.Contains(err.Error(), "https://evil.example/a.png") {
		t.Errorf("the reason names the URL: %v", err)
	}
}

func TestWrapCustomCSS(t *testing.T) {
	css := ".a { color: red }\n/* between */\n@font-face { font-family: \"Acme\" }\n@media (min-width: 40rem) { .b { color: blue } }\n@keyframes k { to { opacity: 0 } }"
	wrapped, err := WrapCustomCSS(css)
	if err != nil {
		t.Fatal(err)
	}
	want := "@layer rs-custom {\n" +
		"@font-face { font-family: \"Acme\" }\n" +
		"@keyframes k { to { opacity: 0 } }\n" +
		"@scope (:root) to ([data-critical]) {\n" +
		".a { color: red }\n" +
		"@media (min-width: 40rem) { .b { color: blue } }\n" +
		"}\n" +
		"}\n"
	if wrapped != want {
		t.Errorf("wrapped =\n%s\nwant\n%s", wrapped, want)
	}
	if empty, err := WrapCustomCSS("/* nothing yet */\n"); err != nil || empty != "@layer rs-custom {\n}\n" {
		t.Errorf("empty CSS = %q, %v", empty, err)
	}
	if _, err := WrapCustomCSS(`.a{background:url(data:image/png{)} :root{}`); err != nil {
		t.Errorf("a brace inside a url is fine once read as part of it: %v", err)
	}
}
