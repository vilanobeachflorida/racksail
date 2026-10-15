package theme

import (
	"fmt"
	"slices"
	"strings"
)

// cssRules is what CSS in one place may contain. Custom CSS, styles in
// templates and styles in SVG files each have their own.
type cssRules struct {
	// what names the CSS in messages.
	what string
	// urls are the URL prefixes it may refer to, lowercase; everything else
	// would load from somewhere.
	urls []string
	// allowed explains the URLs it may use, for messages.
	allowed string
	// important allows !important.
	important bool
	// protected refuses the protected palette and the markers of
	// safety-critical areas.
	protected bool
	// strict requires a sheet to be only complete rules, so it can be
	// split into rules and wrapped.
	strict bool
	// noLineComments refuses //, which CSS reads as two slashes but the
	// template renderer reads as a comment to the end of the line.
	noLineComments bool
}

var (
	customCSSRules = cssRules{
		what: "custom CSS", urls: []string{"data:image/", "data:font/", "#"},
		allowed: "only data: URLs of images and fonts", protected: true, strict: true,
	}
	templateCSSRules = cssRules{
		what: "styles in templates", urls: []string{"data:image/", "data:font/", "#"},
		allowed: "only data: URLs of images and fonts", important: true, noLineComments: true,
	}
	svgCSSRules = cssRules{
		what: "styles in theme files", urls: []string{"#"},
		allowed: "only parts of the same file", important: true,
	}
)

// cssMode says what a piece of CSS is.
type cssMode uint8

const (
	// cssSheet is a style sheet: rules and at-rules.
	cssSheet cssMode = iota
	// cssDeclarations is a style attribute.
	cssDeclarations
	// cssValue is any other attribute a browser may read as CSS, such as
	// an SVG presentation attribute. Only what it refers to matters.
	cssValue
)

// cssAtRules are the at-rules a style sheet may use. None of them loads
// anything or changes where rules sit in the cascade.
var cssAtRules = map[string]bool{
	"media": true, "supports": true, "container": true, "starting-style": true,
	"font-face": true, "keyframes": true, "-webkit-keyframes": true, "property": true,
	"counter-style": true, "font-feature-values": true, "font-palette-values": true, "page": true,
	// Inside @font-feature-values and @page.
	"swash": true, "annotation": true, "ornaments": true, "stylistic": true, "styleset": true, "character-variant": true,
	"top-left-corner": true, "top-left": true, "top-center": true, "top-right": true, "top-right-corner": true,
	"bottom-left-corner": true, "bottom-left": true, "bottom-center": true, "bottom-right": true, "bottom-right-corner": true,
	"left-top": true, "left-middle": true, "left-bottom": true, "right-top": true, "right-middle": true, "right-bottom": true,
}

// cssGlobalAtRules define things for the whole document rather than style
// elements, so they stay at the top level of a sheet.
var cssGlobalAtRules = []string{
	"font-face", "keyframes", "-webkit-keyframes", "property", "counter-style",
	"font-feature-values", "font-palette-values", "page",
}

// cssURLFunctions take URLs as strings: url("…") and src("…"), and the
// image functions, whose strings are images to load.
var cssURLFunctions = []string{
	"url", "src", "image-set", "-webkit-image-set", "image", "-webkit-image", "cross-fade", "-webkit-cross-fade",
}

// cssRule is a top-level rule of a sheet: its code points and, for an
// at-rule, its name.
type cssRule struct {
	text   string
	atRule string
}

func (r cssRules) refuse(format string, args ...any) error {
	return fmt.Errorf(r.what+" "+format, args...)
}

// allowsURL reports whether a URL may be referred to.
func (r cssRules) allowsURL(u string) bool {
	return hasURLPrefix(u, r.urls...)
}

// hasURLPrefix reports whether a URL starts with one of the prefixes,
// which are lowercase. Browsers ignore spaces and control characters
// around a URL and tabs and newlines inside it, so they're ignored here
// too.
func hasURLPrefix(u string, prefixes ...string) bool {
	u = strings.TrimFunc(u, func(c rune) bool { return c <= ' ' })
	u = strings.Map(func(c rune) rune {
		if c == '\t' || c == '\n' || c == '\r' {
			return -1
		}
		return c
	}, u)
	u = strings.ToLower(u)
	for _, prefix := range prefixes {
		if strings.HasPrefix(u, prefix) {
			return true
		}
	}
	return false
}

// checkCSS checks CSS against the rules and, for a strict sheet, returns
// its top-level rules.
func checkCSS(css string, r cssRules, mode cssMode) ([]cssRule, error) {
	src := preprocessCSS(css)
	tokens := tokenizeCSS(src)
	strict := mode != cssValue

	type open struct {
		closer cssKind
		// urls is set inside a function whose strings are URLs.
		urls bool
	}
	var stack []open
	inURLs := func() bool {
		return slices.ContainsFunc(stack, func(o open) bool { return o.urls })
	}
	depth := func() int {
		n := 0
		for _, o := range stack {
			if o.closer == cssCloseCurly {
				n++
			}
		}
		return n
	}
	push := func(closer cssKind, urls bool) { stack = append(stack, open{closer, urls}) }

	for i, t := range tokens {
		switch t.kind {
		case cssComment:
			if t.unclosed && strict {
				return nil, r.refuse("has a comment that isn't closed; end it with */")
			}
		case cssString:
			if t.unclosed && strict {
				return nil, r.refuse("has a string that isn't closed")
			}
			if inURLs() && !r.allowsURL(t.value) {
				return nil, r.refuse("can't load %q; %s are allowed, so nothing loads from elsewhere", t.value, r.allowed)
			}
		case cssBadString:
			if strict {
				return nil, r.refuse("has a string that runs past the end of its line; close it, or end the line with a backslash to continue it")
			}
		case cssURL:
			if !r.allowsURL(t.value) {
				return nil, r.refuse("can't load %q; %s are allowed, so nothing loads from elsewhere", t.value, r.allowed)
			}
		case cssBadURL:
			if strict {
				return nil, r.refuse("has a url() that browsers can't read; put the URL in quotes")
			}
		case cssCDO, cssCDC:
			if strict {
				return nil, r.refuse("can't contain HTML comment markers")
			}
		case cssDelim:
			if !strict {
				break
			}
			switch t.value {
			case "\\":
				return nil, r.refuse("has a backslash that doesn't escape anything")
			case "/":
				// Text after // would vanish from the rendered page, changing
				// what the rest of the sheet means.
				if next := tokens[i+1]; r.noLineComments && next.start == t.end &&
					(next.kind == cssComment || next.kind == cssDelim && next.value == "/") {
					return nil, r.refuse("can't contain //; CSS has no line comments, so use /* */")
				}
			case "!":
				if !r.important {
					return nil, r.refuse("can't use !important; it would outrank personal preferences and the protected colors, which always win")
				}
			}
		case cssAtKeyword:
			name := asciiFold(t.value)
			switch {
			case name == "import":
				return nil, r.refuse("can't use @import; copy the rules in instead, since nothing may load from elsewhere")
			case name == "layer":
				if r.protected {
					return nil, r.refuse("can't use @layer; the panel puts it in a cascade layer of its own")
				}
				return nil, r.refuse("can't use @layer")
			case mode == cssSheet && !cssAtRules[name]:
				return nil, r.refuse("can't use @%s", t.value)
			case mode == cssSheet && slices.Contains(cssGlobalAtRules, name) && depth() > 0:
				return nil, r.refuse("has @%s inside another rule; put it at the top level", t.value)
			case name == "property" && r.protected:
				if next := nextCSSToken(tokens, i); next.kind == cssIdent && strings.HasPrefix(asciiFold(next.value), "--rs-") {
					return nil, r.refuse("can't register %s; the panel's own custom properties keep their definitions", next.value)
				}
			}
		case cssFunction:
			name := asciiFold(t.value)
			switch {
			case name == "expression":
				return nil, r.refuse("can't contain expressions")
			case (name == "var" || name == "attr" || name == "env") && inURLs():
				return nil, r.refuse("can't build a URL from %s(); write the data: URL out instead", name)
			}
			if r.protected && strings.HasPrefix(name, "--rs-critical") {
				return nil, r.refuse("can't change the protected colors of safety-critical screens")
			}
			push(cssCloseParen, slices.Contains(cssURLFunctions, name))
		case cssIdent:
			name := asciiFold(t.value)
			switch {
			case name == "behavior" || name == "-moz-binding":
				return nil, r.refuse("can't attach behaviors or bindings")
			case r.protected && strings.HasPrefix(name, "--rs-critical"):
				return nil, r.refuse("can't change the protected colors of safety-critical screens")
			case r.protected && (name == "data-critical" || name == "critical-area"):
				return nil, r.refuse("can't restyle safety-critical screens")
			}
		case cssOpenParen:
			push(cssCloseParen, false)
		case cssOpenSquare:
			push(cssCloseSquare, false)
		case cssOpenCurly:
			push(cssCloseCurly, false)
		case cssCloseParen, cssCloseSquare, cssCloseCurly:
			if len(stack) > 0 && stack[len(stack)-1].closer == t.kind {
				stack = stack[:len(stack)-1]
				break
			}
			if strict {
				return nil, r.refuse("closes a %s it didn't open", closerName(t.kind))
			}
			// Elsewhere only what's inside url functions matters; drop what
			// this closes.
			for len(stack) > 0 && stack[len(stack)-1].closer != t.kind {
				stack = stack[:len(stack)-1]
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if strict && len(stack) > 0 {
		return nil, r.refuse("leaves a %s open; close it with %s", closerName(stack[len(stack)-1].closer), closerText(stack[len(stack)-1].closer))
	}
	if mode != cssSheet || !r.strict {
		return nil, nil
	}
	return splitCSSRules(src, tokens, r)
}

func nextCSSToken(tokens []cssToken, i int) cssToken {
	for _, t := range tokens[i+1:] {
		if t.kind != cssWhitespace && t.kind != cssComment {
			return t
		}
	}
	return cssToken{kind: cssEOF}
}

func closerName(k cssKind) string {
	switch k {
	case cssCloseCurly:
		return "block"
	case cssCloseSquare:
		return "bracket"
	}
	return "parenthesis"
}

func closerText(k cssKind) string {
	switch k {
	case cssCloseCurly:
		return "}"
	case cssCloseSquare:
		return "]"
	}
	return ")"
}

// splitCSSRules splits a checked sheet into its top-level rules. Every
// top-level rule has a block: statement at-rules (@import, @layer a;) are
// refused already, and anything else outside a rule would change meaning
// once the sheet is wrapped in another rule.
func splitCSSRules(src []rune, tokens []cssToken, r cssRules) ([]cssRule, error) {
	var rules []cssRule
	start := -1
	atRule := ""
	depth := 0
	for _, t := range tokens {
		if start < 0 {
			switch t.kind {
			case cssWhitespace, cssComment, cssEOF:
				continue
			case cssSemicolon:
				return nil, r.refuse("has a semicolon outside a rule; remove it")
			case cssAtKeyword:
				atRule = asciiFold(t.value)
			default:
				atRule = ""
			}
			start = t.start
		}
		switch t.kind {
		case cssOpenCurly:
			depth++
		case cssCloseCurly:
			depth--
			if depth == 0 {
				rules = append(rules, cssRule{text: string(src[start:t.end]), atRule: atRule})
				start = -1
			}
		case cssSemicolon:
			if depth == 0 {
				return nil, r.refuse("has %q outside a rule; every declaration goes inside a rule's braces", strings.TrimSpace(string(src[start:t.end])))
			}
		case cssEOF:
			return nil, r.refuse("ends with %q, which isn't a complete rule", strings.TrimSpace(string(src[start:t.start])))
		}
	}
	return rules, nil
}

// checkStyle checks a style attribute.
func (r cssRules) checkStyle(declarations string) error {
	_, err := checkCSS(declarations, r, cssDeclarations)
	return err
}

// checkSheet checks a style sheet.
func (r cssRules) checkSheet(sheet string) error {
	_, err := checkCSS(sheet, r, cssSheet)
	return err
}

// checkValue checks a value a browser may read as CSS for references.
func (r cssRules) checkValue(value string) error {
	_, err := checkCSS(value, r, cssValue)
	return err
}
