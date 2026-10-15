package theme

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	nethtml "golang.org/x/net/html"
)

// Themes, packages, SVG files, templates and custom CSS all come from
// outside, so their readers must never panic, whatever they're given, and
// what they accept must keep the promises they make.

func FuzzParse(f *testing.F) {
	d := Default()
	data, _ := d.YAML()
	f.Add(data)
	f.Add([]byte("format: 1\nid: x1\nname: x\nversion: 1.0.0\nlicense: MIT\ntokens: {}\n"))
	f.Add([]byte(`{"format":1,"tokens":{"light":{"criticalDanger":"#000000"}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		th, err := Parse(data)
		if err == nil && len(th.Check()) > 0 {
			t.Error("a theme with problems was accepted")
		}
	})
}

func FuzzReadArchive(f *testing.F) {
	f.Add([]byte("PK\x05\x06" + string(make([]byte, 18))))
	f.Add([]byte("not a zip"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadArchive(bytes.NewReader(data), int64(len(data)))
	})
}

func FuzzCheckSVG(f *testing.F) {
	f.Add([]byte(logoSVG))
	f.Add([]byte(`<svg onload="x()"/>`))
	f.Add([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><style>@\69mport "x.css";</style></svg>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = CheckSVG(data)
	})
}

func FuzzCheckTemplate(f *testing.F) {
	for kind := range TemplateVars {
		src, _ := DefaultTemplate(kind)
		f.Add(src, "<script>alert(1)</script>")
	}
	f.Add(`<div title={{title}}>x</div>`, "x onfocus=alert(1) autofocus")
	f.Add(`<a href="{{actionUrl}}">{{actionLabel}}</a>`, "javascript:alert(1)")
	f.Add("{{#each}}", "")
	f.Fuzz(func(t *testing.T, src, value string) {
		if CheckTemplate(TemplateEmail, src) != nil {
			return
		}
		vars := map[string]string{}
		for _, name := range TemplateVars[TemplateEmail] {
			vars[name] = value
		}
		d := Default()
		out, err := d.RenderTemplate(TemplateEmail, src, vars, nil)
		if err != nil {
			t.Fatalf("an accepted template didn't render: %v", err)
		}
		// Whatever the values, the filled-in page is as safe as the
		// template: nothing they add loads, runs or links to a script.
		if err := checkTemplateMarkup(out, false); err != nil {
			t.Errorf("the filled-in template breaks the rules: %v\n%s", err, out)
		}
		if strings.Contains(value, "<") && strings.Contains(out, value) && !strings.Contains(src, value) {
			t.Error("a variable's value got in unescaped")
		}
		z := nethtml.NewTokenizer(strings.NewReader(out))
		for tt := z.Next(); tt != nethtml.ErrorToken; tt = z.Next() {
			if tt != nethtml.StartTagToken && tt != nethtml.SelfClosingTagToken {
				continue
			}
			for _, a := range z.Token().Attr {
				if a.Key == "href" {
					if scheme, ok := urlScheme(strings.TrimSpace(a.Val)); ok && !strings.EqualFold(scheme, "https") &&
						!strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "mailto") && !strings.EqualFold(scheme, "tel") {
						t.Errorf("a link to %q came out", a.Val)
					}
				}
			}
		}
	})
}

func FuzzTokenizeCSS(f *testing.F) {
	f.Add(`.a{background:url(data:image/png{)}} @\69mport "x"; /* open`)
	f.Add("\"a\\\n\x00\r\n\f url( b c) \\")
	f.Fuzz(func(t *testing.T, css string) {
		src := preprocessCSS(css)
		pos := 0
		for _, tok := range tokenizeCSS(src) {
			if tok.start != pos || tok.end < tok.start {
				t.Fatalf("token %+v doesn't follow the one before, at %d", tok, pos)
			}
			pos = tok.end
		}
		if pos != len(src) {
			t.Fatalf("the tokens cover %d of %d code points", pos, len(src))
		}
	})
}

func FuzzCheckCustomCSS(f *testing.F) {
	f.Add(".a { color: red }")
	f.Add("} :root { --rs-critical-danger: green }")
	f.Add(`.a{background:url(data:image/png{)}} :root{--rs-surface:#000000} [role=alertdialog]{display:none}`)
	f.Add(`.a{background:url(data:image/png{)} :root{--rs-surface:#000} @font-face{src:url(data:font/woff2;base64,AA)}`)
	f.Add(`.critical-panel{background-color:#00ff00 !important}`)
	f.Add(`.a{background:image-set("https://evil.example/a.png" 1x)}`)
	f.Add(`.a{background:\75 rl(data:image/png{)}`)
	f.Fuzz(func(t *testing.T, css string) {
		if CheckCustomCSS(css) != nil {
			return
		}
		wrapped, err := WrapCustomCSS(css)
		if err != nil {
			t.Fatal(err)
		}
		if problem := checkWrappedCSS(wrapped); problem != "" {
			t.Errorf("accepted custom CSS breaks out:\n%s\n\nwrapped:\n%s\n\n%s", css, wrapped, problem)
		}
	})
}

// The fuzz test reads wrapped custom CSS with a second, separate reading of
// CSS Syntax Level 3, so a mistake in the tokenizer the checks use can't
// hide itself. It is written the way the specification reads, one step
// after another.

type refKind int

const (
	refString refKind = iota
	refBadString
	refURL
	refBadURL
	refFunction
	refIdent
	refAtKeyword
	refDelim
	refOpen
	refClose
	refOther
)

type refToken struct {
	kind refKind
	text string
}

func referenceCSS(input string) []refToken {
	input = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\f", "\n", "\x00", "\uFFFD").Replace(input)
	if !utf8.ValidString(input) {
		input = strings.ToValidUTF8(input, "\uFFFD")
	}
	cps := []rune(input)
	n := len(cps)
	peek := func(i int) rune {
		if i < n {
			return cps[i]
		}
		return -1
	}
	space := func(c rune) bool { return c == ' ' || c == '\t' || c == '\n' }
	hex := func(c rune) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
	nameStart := func(c rune) bool { return c == '_' || c >= 0x80 || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
	nameChar := func(c rune) bool { return nameStart(c) || c == '-' || c >= '0' && c <= '9' }
	escapeStarts := func(i int) bool { return peek(i) == '\\' && peek(i+1) != '\n' }
	identStarts := func(i int) bool {
		if peek(i) == '-' {
			return nameStart(peek(i+1)) || peek(i+1) == '-' || escapeStarts(i+1)
		}
		return nameStart(peek(i)) || escapeStarts(i)
	}
	// readEscape reads the code points after a backslash at i-1.
	readEscape := func(i int) (rune, int) {
		if i >= n {
			return 0xFFFD, i
		}
		if !hex(cps[i]) {
			return cps[i], i + 1
		}
		j := i
		for j < n && j-i < 6 && hex(cps[j]) {
			j++
		}
		var v rune
		for _, c := range cps[i:j] {
			v = v*16 + rune(strings.IndexRune("0123456789abcdef", c|0x20))
		}
		if j < n && space(cps[j]) {
			j++
		}
		if v == 0 || v > 0x10FFFF || v >= 0xD800 && v <= 0xDFFF {
			v = 0xFFFD
		}
		return v, j
	}
	readName := func(i int) (string, int) {
		var name []rune
		for {
			switch {
			case i < n && nameChar(cps[i]):
				name = append(name, cps[i])
				i++
			case escapeStarts(i):
				var c rune
				c, i = readEscape(i + 1)
				name = append(name, c)
			default:
				return string(name), i
			}
		}
	}

	var out []refToken
	i := 0
	for i < n {
		c := cps[i]
		switch {
		case c == '/' && peek(i+1) == '*':
			end := strings.Index(string(cps[i+2:]), "*/")
			if end < 0 {
				i = n
			} else {
				i += 2 + len([]rune(string(cps[i+2:])[:end])) + 2
			}
		case space(c):
			i++
		case c == '"' || c == '\'':
			i++
			var s []rune
			kind := refString
		str:
			for i < n {
				switch d := cps[i]; {
				case d == c:
					i++
					break str
				case d == '\n':
					kind = refBadString
					break str
				case d == '\\' && i+1 >= n:
					i++
				case d == '\\' && cps[i+1] == '\n':
					i += 2
				case d == '\\':
					var e rune
					e, i = readEscape(i + 1)
					s = append(s, e)
				default:
					s = append(s, d)
					i++
				}
			}
			out = append(out, refToken{kind, string(s)})
		case c == '(' || c == '[' || c == '{':
			out = append(out, refToken{refOpen, string(c)})
			i++
		case c == ')' || c == ']' || c == '}':
			out = append(out, refToken{refClose, string(c)})
			i++
		case c == '@' && identStarts(i+1):
			var name string
			name, i = readName(i + 1)
			out = append(out, refToken{refAtKeyword, name})
		case c >= '0' && c <= '9' || (c == '+' || c == '-' || c == '.') && (peek(i+1) >= '0' && peek(i+1) <= '9' || c != '.' && peek(i+1) == '.' && peek(i+2) >= '0' && peek(i+2) <= '9'):
			i++
			for i < n && (cps[i] >= '0' && cps[i] <= '9' || cps[i] == '.' && peek(i+1) >= '0' && peek(i+1) <= '9') {
				i++
			}
			if (peek(i) == 'e' || peek(i) == 'E') && (peek(i+1) >= '0' && peek(i+1) <= '9' || (peek(i+1) == '+' || peek(i+1) == '-') && peek(i+2) >= '0' && peek(i+2) <= '9') {
				i += 2
				for i < n && cps[i] >= '0' && cps[i] <= '9' {
					i++
				}
			}
			if identStarts(i) {
				_, i = readName(i)
			} else if peek(i) == '%' {
				i++
			}
			out = append(out, refToken{kind: refOther})
		case c == '-' && peek(i+1) == '-' && peek(i+2) == '>':
			out = append(out, refToken{refOther, "-->"})
			i += 3
		case c == '<' && peek(i+1) == '!' && peek(i+2) == '-' && peek(i+3) == '-':
			out = append(out, refToken{refOther, "<!--"})
			i += 4
		case c == '#' && (peek(i+1) >= 0 && nameChar(peek(i+1)) || escapeStarts(i+1)):
			_, i = readName(i + 1)
			out = append(out, refToken{kind: refOther})
		case identStarts(i):
			var name string
			name, i = readName(i)
			if peek(i) != '(' {
				out = append(out, refToken{refIdent, name})
				break
			}
			i++
			if strings.ToLower(name) != "url" {
				out = append(out, refToken{refFunction, name})
				break
			}
			for space(peek(i)) && space(peek(i+1)) {
				i++
			}
			if q := peek(i); q == '"' || q == '\'' || space(q) && (peek(i+1) == '"' || peek(i+1) == '\'') {
				out = append(out, refToken{refFunction, name})
				break
			}
			for space(peek(i)) {
				i++
			}
			var u []rune
			kind := refURL
		url:
			for i < n {
				switch d := cps[i]; {
				case d == ')':
					i++
					break url
				case space(d):
					for space(peek(i)) {
						i++
					}
					if peek(i) == ')' || i >= n {
						i++
						break url
					}
					kind = refBadURL
				case d == '"' || d == '\'' || d == '(' || d <= 8 || d == 0x0B || d >= 0x0E && d <= 0x1F || d == 0x7F:
					kind = refBadURL
				case d == '\\' && peek(i+1) != '\n':
					var e rune
					e, i = readEscape(i + 1)
					u = append(u, e)
					continue
				case d == '\\':
					kind = refBadURL
				default:
					u = append(u, d)
					i++
					continue
				}
				if kind == refBadURL {
					// The rest up to ) belongs to the broken url.
					for i < n && cps[i] != ')' {
						if escapeStarts(i) {
							_, i = readEscape(i + 1)
							continue
						}
						i++
					}
					i++
					break url
				}
			}
			out = append(out, refToken{kind, string(u)})
		default:
			out = append(out, refToken{refDelim, string(c)})
			i++
		}
	}
	return out
}

// checkWrappedCSS reads wrapped custom CSS on its own and says what's
// wrong with it, or "" when it keeps every promise: one layer holds
// everything, and nothing in it loads, outranks the layers above or names
// another layer.
func checkWrappedCSS(css string) string {
	tokens := referenceCSS(css)
	if len(tokens) < 3 || tokens[0].kind != refAtKeyword || tokens[0].text != "layer" || tokens[1].text != "rs-custom" || tokens[2].text != "{" {
		return "it doesn't start with the custom layer"
	}
	type frame struct {
		closer string
		urls   bool
	}
	closers := map[string]string{"(": ")", "[": "]", "{": "}"}
	urlFunctions := map[string]bool{"url": true, "src": true, "image-set": true, "-webkit-image-set": true, "image": true, "-webkit-image": true, "cross-fade": true, "-webkit-cross-fade": true}
	allowedURL := func(u string) bool {
		u = strings.ToLower(strings.NewReplacer("\t", "", "\n", "").Replace(strings.TrimFunc(u, func(r rune) bool { return r <= ' ' })))
		return strings.HasPrefix(u, "data:image/") || strings.HasPrefix(u, "data:font/") || strings.HasPrefix(u, "#")
	}
	atRules := map[string]bool{"media": true, "supports": true, "container": true, "starting-style": true, "font-face": true,
		"keyframes": true, "-webkit-keyframes": true, "property": true, "counter-style": true, "font-feature-values": true,
		"font-palette-values": true, "page": true}
	var stack []frame
	scoped := false
	for i, tok := range tokens[3:] {
		urls := false
		for _, f := range stack {
			urls = urls || f.urls
		}
		switch tok.kind {
		case refBadString, refBadURL:
			return "a string or url is broken"
		case refURL:
			if !allowedURL(tok.text) {
				return "it loads " + tok.text
			}
		case refString:
			if urls && !allowedURL(tok.text) {
				return "it loads " + tok.text
			}
		case refDelim:
			if tok.text == "!" {
				return "it uses !important"
			}
		case refAtKeyword:
			name := strings.ToLower(tok.text)
			if name == "scope" && len(stack) == 0 && !scoped {
				scoped = true
				continue
			}
			if !atRules[name] && !strings.HasPrefix(name, "top-") && !strings.HasPrefix(name, "bottom-") &&
				!strings.HasPrefix(name, "left-") && !strings.HasPrefix(name, "right-") {
				return "it uses @" + tok.text
			}
		case refFunction:
			stack = append(stack, frame{")", urlFunctions[strings.ToLower(tok.text)]})
		case refOpen:
			stack = append(stack, frame{closers[tok.text], false})
		case refClose:
			if len(stack) == 0 {
				if tok.text == "}" && i == len(tokens)-4 {
					return ""
				}
				return "it closes the custom layer early"
			}
			if stack[len(stack)-1].closer != tok.text {
				return "its brackets don't match"
			}
			stack = stack[:len(stack)-1]
		}
	}
	return "the custom layer isn't closed"
}

func TestReferenceReading(t *testing.T) {
	for css, want := range map[string]string{
		"@layer rs-custom {\n.a{background:url(data:image/png{)}\n}\n":  "",
		"@layer rs-custom {\n.a{}}\n.b{}\n}\n":                          "it closes the custom layer early",
		"@layer rs-custom {\n.a{color:red !important}\n}\n":             "it uses !important",
		"@layer rs-custom {\n.a{background:image-set(\"//x\" 1x)}\n}\n": "it loads //x",
		"@layer rs-custom {\n@\\69mport \"x\";\n}\n":                    "it uses @import",
		"@layer rs-custom {\n.a{b:\\75 rl(https://x)}\n}\n":             "it loads https://x",
		"@layer rs-custom {\n.a{}\n":                                    "the custom layer isn't closed",
	} {
		if got := checkWrappedCSS(css); got != want {
			t.Errorf("%q: %q, want %q", css, got, want)
		}
	}
}
