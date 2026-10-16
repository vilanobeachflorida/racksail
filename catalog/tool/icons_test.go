package main

import (
	"bytes"
	"encoding/xml"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSVG(t *testing.T) {
	good := []string{
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>`,
		`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="g"/></defs><rect fill="url(#g)" width="1" height="1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="#a"/><style>.a{fill:url( '#g' )}</style></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"><animate attributeName="opacity" values="0;1" dur="1s" repeatCount="indefinite"/></rect></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>/* brand */ .a{fill:var(--brand, #007cfd)} .b{font-family:"Inter", sans-serif}</style><title>Mark</title><text class="b" transform="rotate(45)">i</text></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style><![CDATA[.a{fill:url(#g)}]]></style><rect style="fill:URL(\23 g)" filter="url(#f)"/></svg>`,
	}
	for _, s := range good {
		if err := checkSVG([]byte(s)); err != nil {
			t.Errorf("checkSVG(%s) = %v", s, err)
		}
	}
	bad := map[string]string{
		`<svg><script>alert(1)</script></svg>`:                                 "script",
		`<svg onload="alert(1)"/>`:                                             "event handler",
		`<svg><image href="https://tracker.example/x.png"/></svg>`:             "refers to",
		`<svg><rect style="fill:url(https://example.com/a.svg#b)"/></svg>`:     "url()",
		`<svg><style>@import url(https://example.com/a.css);</style></svg>`:    "imports",
		`<svg><foreignObject><div/></foreignObject></svg>`:                     "foreignObject",
		`<!DOCTYPE svg [<!ENTITY x "y">]><svg/>`:                               "declaration",
		`<html><body/></html>`:                                                 "first element must be <svg>",
		``:                                                                     "empty",
		`<svg><rect></svg>`:                                                    "isn't valid SVG",
		`<?xml-stylesheet href="https://example.com/a.css"?><svg/>`:            "processing instruction",
		`<svg><handler type="application/ecmascript">alert(1)</handler></svg>`: "handler",
		`<svg><a><set attributeName="href" to="javascript:alert(1)"/><text>x</text></a></svg>`:                  "animates href",
		`<svg><a href="#x"><animate attributeName="xlink:href" values="https://elsewhere.example/"/></a></svg>`: "animates xlink:href",
		`<svg><use href="#a"><set attributeName=" HREF " to="https://elsewhere.example/a.svg#b"/></use></svg>`:  "animates",
		`<svg><rect><set attributeName="onclick" to="alert(1)"/></rect></svg>`:                                  "animates onclick",
		// CSS escapes and functions other than url() that load images.
		`<svg><style>.a{fill:\75 rl(https://tracker.example/x.svg#a)}</style></svg>`:          "url()",
		`<svg><rect style="background:u\72 l(https://tracker.example/x.png)"/></svg>`:         "url()",
		`<svg><style>@\69 mport "https://tracker.example/a.css";</style></svg>`:               "imports",
		`<svg><rect style='background:image-set("https://tracker.example/x.png" 1x)'/></svg>`: "image-set()",
		// A style sheet is the element's whole text, however XML splits it.
		`<svg><style>.a{fill:u<!-- -->rl(https://tracker.example/x)}</style></svg>`:                   "url()",
		`<svg><style>.a{fill:u<![CDATA[rl(https://tracker.example/x)]]>}</style></svg>`:               "url()",
		`<svg><style>.a{fill:u<x>junk</x>rl(https://tracker.example/x)}</style></svg>`:                "inside a <style>",
		`<svg><style>.a{content:"/*"; background:url(https://tracker.example/x) /*"*/}</style></svg>`: "url()",
		// Inline in a page, these leave the SVG and become HTML elements.
		`<svg><img src="https://tracker.example/x.png"/></svg>`:                         "HTML element, <img>",
		`<svg xmlns:h="http://www.w3.org/1999/xhtml"><h:object/><h:a href="#x"/></svg>`: "<object>",
		`<svg xmlns:h="http://www.w3.org/1999/xhtml"><h:a href="#x"/></svg>`:            "HTML element, <a>",
		`<svg xml:base="https://elsewhere.example/"><use href="#a"/></svg>`:             "xml:base",
		`<svg><a :href="https://elsewhere.example/"/></svg>`:                            "refers to",
	}
	for s, want := range bad {
		err := checkSVG([]byte(s))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("checkSVG(%s) = %v, want %q", s, err, want)
		}
	}
}

func TestCheckCSS(t *testing.T) {
	harmless := []string{
		`fill:url(#g)`,
		`fill: URL( '#g' )`,
		`fill:url( \23 g)`,
		`fill:var(--brand, #007cfd); transform: rotate(45deg) translate(1px, 2px)`,
		`font-family: "Inter", sans-serif; src: local(Inter)`,
		`fill: 1url(https://elsewhere.example/)`,
		`a { fill: myurl(https://elsewhere.example/) }`,
	}
	for _, css := range harmless {
		if err := checkCSS(css); err != nil {
			t.Errorf("checkCSS(%s) = %v", css, err)
		}
		if cssLoads(css) {
			t.Errorf("the model of CSS finds that %s loads something", css)
		}
	}
	loading := map[string]string{
		`fill:\75 rl(https://tracker.example/x.svg#a)`:                    "url()",
		`background:u\72 l(https://tracker.example/x.png)`:                "url()",
		`background:ur\l(https://tracker.example/x.png)`:                  "url()",
		`background:u\000072l(https://tracker.example/x.png)`:             "url()",
		`@\69 mport "https://tracker.example/a.css";`:                     "imports",
		`@IMPORT "https://tracker.example/a.css";`:                        "imports",
		`background:image-set("https://tracker.example/x.png" 1x)`:        "image-set()",
		`background:-webkit-image-set(url(#a) 1x)`:                        "-webkit-image-set()",
		`mask:src("https://tracker.example/m.svg")`:                       "src()",
		`background:image("https://tracker.example/x.png")`:               "image()",
		`background:element(#panel)`:                                      "element()",
		`background:cross-fade(url(#a), url(#b), 50%)`:                    "cross-fade()",
		`content:"/*"; background:url(https://tracker.example/x) /*"*/`:   "url()",
		`content:'\'/*'; background:url(https://tracker.example/x) /*'*/`: "url()",
		`background:url(  "https://tracker.example/x"  )`:                 "url()",
		// A backslash before a CRLF continues a string as one before a line
		// feed does, so the comment after it isn't one.
		"content:\"a\\\r\n/*\"; background:url(https://tracker.example/x) /*\"*/": "url()",
		"content:\"a\\\f/*\"; background:url(https://tracker.example/x) /*\"*/":   "url()",
	}
	for css, want := range loading {
		err := checkCSS(css)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("checkCSS(%s) = %v, want %q", css, err, want)
		}
		if !cssLoads(css) {
			t.Errorf("the model of CSS doesn't find that %s loads something", css)
		}
	}
	// Comments never join two names into one, so this loads nothing, but
	// checkCSS errs on the side of refusing it.
	if css := `background:u/**/rl(https://tracker.example/x)`; checkCSS(css) == nil || cssLoads(css) {
		t.Errorf("checkCSS(%s) = %v, and the model says it loads: %v", css, checkCSS(css), cssLoads(css))
	}
}

func FuzzCheckCSS(f *testing.F) {
	for _, seed := range []string{
		`fill:url(#g)`, `fill:\75 rl(https://x/#a)`, `background:u\72 l(https://x/)`, `@\69 mport "https://x/"`,
		`background:image-set("https://x/" 1x)`, `content:"/*"; background:url(https://x/) /*"*/`,
		`a{fill:var(--b)}`, `x:"\"/*"; y:url(https://x/)`, `-\75 rl(https://x/)`, `\55 RL("https://x/")`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, css string) {
		if checkCSS(css) == nil && cssLoads(css) {
			t.Fatalf("checkCSS accepted %q, which a browser reads as loading something", css)
		}
	})
}

func FuzzCheckSVG(f *testing.F) {
	f.Add([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`))
	f.Add([]byte(`<svg><style>.a{fill:url(#g)}</style></svg>`))
	f.Add([]byte(`<svg onload="x"/>`))
	f.Add([]byte(`<svg><a><set attributeName="href" to="javascript:alert(1)"/></a></svg>`))
	f.Add([]byte(`<svg><a><animate attributeName="xlink:href" values="https://elsewhere.example/"/></a></svg>`))
	f.Add([]byte(`<svg><rect><animate attributeName="opacity" values="0;1" dur="1s"/></rect></svg>`))
	f.Add([]byte(`<svg><text>attributeName="href"</text></svg>`))
	f.Add([]byte(`<svg><style>.a{fill:u<!-- -->rl(https://x/)}</style></svg>`))
	f.Add([]byte(`<svg><rect style="fill:\75 rl(https://x/)"/></svg>`))
	// An attribute in a tag; text that reads like one is harmless.
	animatesLink := regexp.MustCompile(`<[^<>]*[\s:]attributename\s*=\s*["']\s*([^"':]*:)?href\s*["']`)
	f.Fuzz(func(t *testing.T, data []byte) {
		if checkSVG(data) != nil {
			return
		}
		// Whatever passes has style a browser reads as loading nothing.
		for _, css := range svgStyle(data) {
			if cssLoads(css) {
				t.Fatalf("checkSVG accepted %q, whose style %q loads something", data, css)
			}
		}
		// And no script element, and no animated link.
		lower := strings.ToLower(string(data))
		if strings.Contains(lower, "<![cdata[") || strings.Contains(lower, "<!--") {
			return
		}
		for _, banned := range []string{"<script", "<handler", "<foreignobject", "@import"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("checkSVG accepted %q", data)
			}
		}
		if animatesLink.MatchString(lower) {
			t.Fatalf("checkSVG accepted an animated link: %q", data)
		}
	})
}

// svgStyle returns every attribute's value and the text of every style
// element, which is where an SVG has style.
func svgStyle(data []byte) []string {
	var out []string
	dec := xml.NewDecoder(bytes.NewReader(data))
	var sheet *strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			for _, a := range t.Attr {
				out = append(out, a.Value)
			}
			if strings.EqualFold(t.Name.Local, "style") {
				sheet = &strings.Builder{}
			}
		case xml.CharData:
			if sheet != nil {
				sheet.Write(t)
			}
		case xml.EndElement:
			if sheet != nil && strings.EqualFold(t.Name.Local, "style") {
				out = append(out, sheet.String())
				sheet = nil
			}
		}
	}
}

// cssLoads is a model of how a browser reads CSS, after the tokenizer in
// CSS Syntax Module Level 3, section 4.3, kept apart from checkCSS: it
// reports whether style imports a style sheet, has a url() to anything but
// an element of its own document, or calls a function that loads images or
// shows other elements. checkCSS reads style more cautiously than this, so
// whatever it accepts, this must find harmless.
func cssLoads(s string) bool {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\f", "\n", "\x00", "�").Replace(s)
	tz := &cssTokenizer{in: []rune(s)}
	urlFunction := false
	for {
		kind, value := tz.next()
		switch kind {
		case tokenEOF:
			return false
		case tokenWhitespace:
			continue
		case tokenAtKeyword:
			if asciiLower(value) == "import" {
				return true
			}
		case tokenURL:
			if !sameDocument(value) {
				return true
			}
		case tokenString:
			if urlFunction && !sameDocument(value) {
				return true
			}
		case tokenFunction:
			switch asciiLower(value) {
			case "url":
				urlFunction = true
				continue
			case "image", "image-set", "-webkit-image-set", "src", "cross-fade", "-webkit-cross-fade", "element", "-moz-element":
				return true
			}
		}
		urlFunction = false
	}
}

// sameDocument reports whether a URL refers to an element of the document
// it is in: only a fragment, once URL parsing has dropped the spaces and
// control characters around it.
func sameDocument(url string) bool {
	return strings.HasPrefix(strings.TrimLeft(url, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\x0b\x0c\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f "), "#")
}

// asciiLower lowercases ASCII letters only, as CSS compares names.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

type tokenKind int

const (
	tokenEOF tokenKind = iota
	tokenWhitespace
	tokenString
	tokenBadString
	tokenURL
	tokenBadURL
	tokenFunction
	tokenAtKeyword
	tokenIdent
	tokenHash
	tokenNumber
	tokenDelim
)

type cssTokenizer struct {
	in  []rune
	pos int
}

const eof = -1

func (z *cssTokenizer) peek(n int) rune {
	if z.pos+n < len(z.in) {
		return z.in[z.pos+n]
	}
	return eof
}

func isNewline(r rune) bool    { return r == '\n' }
func isWhitespace(r rune) bool { return r == '\n' || r == '\t' || r == ' ' }
func isDigit(r rune) bool      { return r >= '0' && r <= '9' }
func isHexRune(r rune) bool {
	return isDigit(r) || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}
func isIdentStart(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 0x80 || r == '_'
}
func isIdentRune(r rune) bool { return isIdentStart(r) || isDigit(r) || r == '-' }

func validEscape(a, b rune) bool { return a == '\\' && !isNewline(b) }

func startsIdent(a, b, c rune) bool {
	switch {
	case a == '-':
		return isIdentStart(b) || b == '-' || validEscape(b, c)
	case isIdentStart(a):
		return true
	case a == '\\':
		return validEscape(a, b)
	}
	return false
}

func startsNumber(a, b, c rune) bool {
	switch {
	case a == '+' || a == '-':
		return isDigit(b) || b == '.' && isDigit(c)
	case a == '.':
		return isDigit(b)
	}
	return isDigit(a)
}

func (z *cssTokenizer) next() (tokenKind, string) {
	for z.peek(0) == '/' && z.peek(1) == '*' {
		z.pos += 2
		for z.peek(0) != eof && !(z.peek(0) == '*' && z.peek(1) == '/') {
			z.pos++
		}
		if z.peek(0) != eof {
			z.pos += 2
		}
	}
	c := z.peek(0)
	switch {
	case c == eof:
		return tokenEOF, ""
	case isWhitespace(c):
		for isWhitespace(z.peek(0)) {
			z.pos++
		}
		return tokenWhitespace, ""
	case c == '"' || c == '\'':
		z.pos++
		return z.string(c)
	case c == '#':
		z.pos++
		if isIdentRune(z.peek(0)) || validEscape(z.peek(0), z.peek(1)) {
			return tokenHash, z.name()
		}
		return tokenDelim, "#"
	case c == '+' || c == '.':
		if startsNumber(c, z.peek(1), z.peek(2)) {
			return z.numeric()
		}
	case c == '-':
		if startsNumber(c, z.peek(1), z.peek(2)) {
			return z.numeric()
		}
		if z.peek(1) == '-' && z.peek(2) == '>' {
			z.pos += 3
			return tokenDelim, "-->"
		}
		if startsIdent(c, z.peek(1), z.peek(2)) {
			return z.identLike()
		}
	case c == '@':
		z.pos++
		if startsIdent(z.peek(0), z.peek(1), z.peek(2)) {
			return tokenAtKeyword, z.name()
		}
		return tokenDelim, "@"
	case c == '\\':
		if validEscape(c, z.peek(1)) {
			return z.identLike()
		}
	case isDigit(c):
		return z.numeric()
	case isIdentStart(c):
		return z.identLike()
	}
	z.pos++
	return tokenDelim, string(c)
}

// escape reads the escaped code point after a backslash.
func (z *cssTokenizer) escape() rune {
	c := z.peek(0)
	if c == eof {
		return utf8.RuneError
	}
	z.pos++
	if !isHexRune(c) {
		return c
	}
	digits := string(c)
	for len(digits) < 6 && isHexRune(z.peek(0)) {
		digits += string(z.peek(0))
		z.pos++
	}
	if isWhitespace(z.peek(0)) {
		z.pos++
	}
	var v rune
	for _, d := range digits {
		v *= 16
		switch {
		case isDigit(d):
			v += d - '0'
		case d >= 'a':
			v += d - 'a' + 10
		default:
			v += d - 'A' + 10
		}
	}
	if v == 0 || v > utf8.MaxRune || v >= 0xD800 && v <= 0xDFFF {
		return utf8.RuneError
	}
	return v
}

func (z *cssTokenizer) name() string {
	var b strings.Builder
	for {
		c := z.peek(0)
		switch {
		case isIdentRune(c):
			b.WriteRune(c)
			z.pos++
		case validEscape(c, z.peek(1)):
			z.pos++
			b.WriteRune(z.escape())
		default:
			return b.String()
		}
	}
}

func (z *cssTokenizer) string(quote rune) (tokenKind, string) {
	var b strings.Builder
	for {
		c := z.peek(0)
		switch {
		case c == eof:
			return tokenString, b.String()
		case c == quote:
			z.pos++
			return tokenString, b.String()
		case isNewline(c):
			return tokenBadString, b.String()
		case c == '\\':
			z.pos++
			switch {
			case z.peek(0) == eof:
			case isNewline(z.peek(0)):
				z.pos++
			default:
				b.WriteRune(z.escape())
			}
		default:
			b.WriteRune(c)
			z.pos++
		}
	}
}

func (z *cssTokenizer) numeric() (tokenKind, string) {
	start := z.pos
	if c := z.peek(0); c == '+' || c == '-' {
		z.pos++
	}
	for isDigit(z.peek(0)) {
		z.pos++
	}
	if z.peek(0) == '.' && isDigit(z.peek(1)) {
		z.pos++
		for isDigit(z.peek(0)) {
			z.pos++
		}
	}
	if c := z.peek(0); c == 'e' || c == 'E' {
		if isDigit(z.peek(1)) || (z.peek(1) == '+' || z.peek(1) == '-') && isDigit(z.peek(2)) {
			z.pos += 2
			for isDigit(z.peek(0)) {
				z.pos++
			}
		}
	}
	number := string(z.in[start:z.pos])
	if startsIdent(z.peek(0), z.peek(1), z.peek(2)) {
		return tokenNumber, number + z.name()
	}
	if z.peek(0) == '%' {
		z.pos++
	}
	return tokenNumber, number
}

func (z *cssTokenizer) identLike() (tokenKind, string) {
	name := z.name()
	if z.peek(0) != '(' {
		return tokenIdent, name
	}
	z.pos++
	if asciiLower(name) != "url" {
		return tokenFunction, name
	}
	for isWhitespace(z.peek(0)) && isWhitespace(z.peek(1)) {
		z.pos++
	}
	c := z.peek(0)
	if c == '"' || c == '\'' || isWhitespace(c) && (z.peek(1) == '"' || z.peek(1) == '\'') {
		return tokenFunction, name
	}
	return z.url()
}

func (z *cssTokenizer) url() (tokenKind, string) {
	var b strings.Builder
	for isWhitespace(z.peek(0)) {
		z.pos++
	}
	for {
		c := z.peek(0)
		switch {
		case c == ')':
			z.pos++
			return tokenURL, b.String()
		case c == eof:
			return tokenURL, b.String()
		case isWhitespace(c):
			for isWhitespace(z.peek(0)) {
				z.pos++
			}
			if z.peek(0) == ')' || z.peek(0) == eof {
				if z.peek(0) == ')' {
					z.pos++
				}
				return tokenURL, b.String()
			}
			z.badURL()
			return tokenBadURL, ""
		case c == '"' || c == '\'' || c == '(' || c < 0x20 && c != '\t' || c == 0x7f:
			z.badURL()
			return tokenBadURL, ""
		case c == '\\':
			if !validEscape(c, z.peek(1)) {
				z.badURL()
				return tokenBadURL, ""
			}
			z.pos++
			b.WriteRune(z.escape())
		default:
			b.WriteRune(c)
			z.pos++
		}
	}
}

// badURL reads what is left of a url() that can't be one.
func (z *cssTokenizer) badURL() {
	for {
		c := z.peek(0)
		switch {
		case c == eof:
			return
		case c == ')':
			z.pos++
			return
		case validEscape(c, z.peek(1)):
			z.pos++
			z.escape()
		default:
			z.pos++
		}
	}
}

// The model agrees with what browsers do on the cases checkCSS is about.
func TestCSSModel(t *testing.T) {
	cases := map[string]bool{
		`fill:url(#g)`:                          false,
		`fill:url(https://x/)`:                  true,
		`fill:URL( "https://x/" )`:              true,
		`fill:url("#g")`:                        false,
		`fill:\75 rl(https://x/)`:               true,
		`fill:u\72 l(https://x/)`:               true,
		`fill:u/**/rl(https://x/)`:              false,
		`fill:url\28 https://x/)`:               false,
		`fill:1url(https://x/)`:                 false,
		`fill:-url(https://x/)`:                 false,
		`content:"url(https://x/)"`:             false,
		`content:"/*"; b:url(https://x/) /*"*/`: true,
		`/* url(https://x/) */ fill:red`:        false,
		`@\69 mport "x.css"`:                    true,
		`@media print { a { b: c } }`:           false,
		`b:image-set("x.png" 1x)`:               true,
		`b:var(--x)`:                            false,
		`b:url(https://x/ y)`:                   false,
		`b:url("https://x/" y)`:                 true,
	}
	for css, want := range cases {
		if got := cssLoads(css); got != want {
			t.Errorf("cssLoads(%s) = %v, want %v", css, got, want)
		}
	}
}
