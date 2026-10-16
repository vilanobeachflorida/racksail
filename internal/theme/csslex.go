package theme

// A tokenizer for CSS as CSS Syntax Level 3 defines it
// (https://www.w3.org/TR/css-syntax-3/#tokenization). The checks of custom
// CSS, templates and SVG files read style sheets with it, so text splits
// into tokens exactly where a browser splits it: a brace inside an unquoted
// url() or an escaped letter in @\69mport is what the browser sees too.

type cssKind uint8

const (
	cssEOF cssKind = iota
	cssWhitespace
	// Comments aren't tokens in the specification; they're kept here so a
	// comment left open can be refused and rules keep their text.
	cssComment
	cssIdent
	cssFunction
	cssAtKeyword
	cssHash
	cssString
	cssBadString
	cssURL
	cssBadURL
	cssDelim
	cssNumber
	cssPercentage
	cssDimension
	cssCDO
	cssCDC
	cssColon
	cssSemicolon
	cssComma
	cssOpenSquare
	cssCloseSquare
	cssOpenParen
	cssCloseParen
	cssOpenCurly
	cssCloseCurly
)

// cssToken is one token. Positions index the preprocessed code points.
type cssToken struct {
	kind cssKind
	// value is the decoded name of an ident, function, at-keyword or hash
	// (escapes resolved), the contents of a string or url, the code point
	// of a delim, or the unit of a dimension.
	value string
	// unclosed marks a comment, string or url cut off by the end of the
	// input.
	unclosed   bool
	start, end int
}

const cssEOFRune = -1

// preprocessCSS normalizes newlines and replaces NUL and invalid UTF-8 with
// U+FFFD, as browsers do before tokenizing.
func preprocessCSS(s string) []rune {
	in := []rune(s)
	out := make([]rune, 0, len(in))
	for i := 0; i < len(in); i++ {
		switch r := in[i]; r {
		case '\r':
			if i+1 < len(in) && in[i+1] == '\n' {
				i++
			}
			out = append(out, '\n')
		case '\f':
			out = append(out, '\n')
		case 0:
			out = append(out, '�')
		default:
			out = append(out, r)
		}
	}
	return out
}

type cssLexer struct {
	src []rune
	pos int
}

// tokenizeCSS splits preprocessed CSS into tokens, ending with cssEOF.
func tokenizeCSS(src []rune) []cssToken {
	l := &cssLexer{src: src}
	var out []cssToken
	for {
		t := l.next()
		out = append(out, t)
		if t.kind == cssEOF {
			return out
		}
	}
}

func (l *cssLexer) at(i int) rune {
	if l.pos+i < len(l.src) {
		return l.src[l.pos+i]
	}
	return cssEOFRune
}

func (l *cssLexer) token(kind cssKind, start int, value string) cssToken {
	return cssToken{kind: kind, value: value, start: start, end: l.pos}
}

func isCSSWhitespace(c rune) bool { return c == '\n' || c == '\t' || c == ' ' }
func isCSSDigit(c rune) bool      { return c >= '0' && c <= '9' }
func isCSSHex(c rune) bool {
	return isCSSDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
func isCSSIdentStart(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 0x80
}
func isCSSIdentChar(c rune) bool { return isCSSIdentStart(c) || isCSSDigit(c) || c == '-' }
func isCSSNonPrintable(c rune) bool {
	return c >= 0 && c <= 8 || c == 0x0B || c >= 0x0E && c <= 0x1F || c == 0x7F
}

// cssValidEscape reports whether two code points start an escape.
func cssValidEscape(a, b rune) bool { return a == '\\' && b != '\n' }

// cssStartsIdent reports whether three code points start an ident
// sequence.
func cssStartsIdent(a, b, c rune) bool {
	switch {
	case a == '-':
		return isCSSIdentStart(b) || b == '-' || cssValidEscape(b, c)
	case isCSSIdentStart(a):
		return true
	case a == '\\':
		return cssValidEscape(a, b)
	}
	return false
}

// cssStartsNumber reports whether three code points start a number.
func cssStartsNumber(a, b, c rune) bool {
	switch {
	case a == '+' || a == '-':
		return isCSSDigit(b) || b == '.' && isCSSDigit(c)
	case a == '.':
		return isCSSDigit(b)
	}
	return isCSSDigit(a)
}

func (l *cssLexer) next() cssToken {
	start := l.pos
	c := l.at(0)
	switch {
	case c == cssEOFRune:
		return l.token(cssEOF, start, "")
	case c == '/' && l.at(1) == '*':
		l.pos += 2
		for {
			if l.at(0) == cssEOFRune {
				t := l.token(cssComment, start, "")
				t.unclosed = true
				return t
			}
			if l.at(0) == '*' && l.at(1) == '/' {
				l.pos += 2
				return l.token(cssComment, start, "")
			}
			l.pos++
		}
	case isCSSWhitespace(c):
		for isCSSWhitespace(l.at(0)) {
			l.pos++
		}
		return l.token(cssWhitespace, start, "")
	case c == '"' || c == '\'':
		return l.string(c)
	case c == '#':
		if isCSSIdentChar(l.at(1)) || cssValidEscape(l.at(1), l.at(2)) {
			l.pos++
			return l.token(cssHash, start, l.identSequence())
		}
	case c == '(':
		l.pos++
		return l.token(cssOpenParen, start, "")
	case c == ')':
		l.pos++
		return l.token(cssCloseParen, start, "")
	case c == '+' || c == '.':
		if cssStartsNumber(c, l.at(1), l.at(2)) {
			return l.numeric()
		}
	case c == ',':
		l.pos++
		return l.token(cssComma, start, "")
	case c == '-':
		if cssStartsNumber(c, l.at(1), l.at(2)) {
			return l.numeric()
		}
		if l.at(1) == '-' && l.at(2) == '>' {
			l.pos += 3
			return l.token(cssCDC, start, "")
		}
		if cssStartsIdent(c, l.at(1), l.at(2)) {
			return l.identLike()
		}
	case c == ':':
		l.pos++
		return l.token(cssColon, start, "")
	case c == ';':
		l.pos++
		return l.token(cssSemicolon, start, "")
	case c == '<':
		if l.at(1) == '!' && l.at(2) == '-' && l.at(3) == '-' {
			l.pos += 4
			return l.token(cssCDO, start, "")
		}
	case c == '@':
		if cssStartsIdent(l.at(1), l.at(2), l.at(3)) {
			l.pos++
			return l.token(cssAtKeyword, start, l.identSequence())
		}
	case c == '[':
		l.pos++
		return l.token(cssOpenSquare, start, "")
	case c == ']':
		l.pos++
		return l.token(cssCloseSquare, start, "")
	case c == '{':
		l.pos++
		return l.token(cssOpenCurly, start, "")
	case c == '}':
		l.pos++
		return l.token(cssCloseCurly, start, "")
	case c == '\\':
		if cssValidEscape(c, l.at(1)) {
			return l.identLike()
		}
	case isCSSDigit(c):
		return l.numeric()
	case isCSSIdentStart(c):
		return l.identLike()
	}
	l.pos++
	return l.token(cssDelim, start, string(c))
}

// escape consumes an escaped code point; the backslash is already
// consumed.
func (l *cssLexer) escape() rune {
	c := l.at(0)
	if c == cssEOFRune {
		return '�'
	}
	if !isCSSHex(c) {
		l.pos++
		return c
	}
	v := 0
	for n := 0; n < 6 && isCSSHex(l.at(0)); n++ {
		d := l.at(0)
		switch {
		case isCSSDigit(d):
			v = v*16 + int(d-'0')
		case d >= 'a':
			v = v*16 + int(d-'a'+10)
		default:
			v = v*16 + int(d-'A'+10)
		}
		l.pos++
	}
	if isCSSWhitespace(l.at(0)) {
		l.pos++
	}
	if v == 0 || v >= 0xD800 && v <= 0xDFFF || v > 0x10FFFF {
		return '�'
	}
	return rune(v)
}

func (l *cssLexer) identSequence() string {
	var b []rune
	for {
		c := l.at(0)
		switch {
		case isCSSIdentChar(c):
			b = append(b, c)
			l.pos++
		case cssValidEscape(c, l.at(1)):
			l.pos++
			b = append(b, l.escape())
		default:
			return string(b)
		}
	}
}

// asciiFold lowercases ASCII letters only, as CSS compares names.
func asciiFold(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func (l *cssLexer) identLike() cssToken {
	start := l.pos
	name := l.identSequence()
	if asciiFold(name) == "url" && l.at(0) == '(' {
		l.pos++
		for isCSSWhitespace(l.at(0)) && isCSSWhitespace(l.at(1)) {
			l.pos++
		}
		a, b := l.at(0), l.at(1)
		if a == '"' || a == '\'' || isCSSWhitespace(a) && (b == '"' || b == '\'') {
			return l.token(cssFunction, start, name)
		}
		return l.url(start)
	}
	if l.at(0) == '(' {
		l.pos++
		return l.token(cssFunction, start, name)
	}
	return l.token(cssIdent, start, name)
}

func (l *cssLexer) url(start int) cssToken {
	var b []rune
	for isCSSWhitespace(l.at(0)) {
		l.pos++
	}
	for {
		c := l.at(0)
		switch {
		case c == ')':
			l.pos++
			return l.token(cssURL, start, string(b))
		case c == cssEOFRune:
			t := l.token(cssURL, start, string(b))
			t.unclosed = true
			return t
		case isCSSWhitespace(c):
			for isCSSWhitespace(l.at(0)) {
				l.pos++
			}
			if l.at(0) == ')' {
				l.pos++
				return l.token(cssURL, start, string(b))
			}
			if l.at(0) == cssEOFRune {
				t := l.token(cssURL, start, string(b))
				t.unclosed = true
				return t
			}
			l.badURLRemnants()
			return l.token(cssBadURL, start, "")
		case c == '"' || c == '\'' || c == '(' || isCSSNonPrintable(c):
			l.badURLRemnants()
			return l.token(cssBadURL, start, "")
		case c == '\\':
			if !cssValidEscape(c, l.at(1)) {
				l.badURLRemnants()
				return l.token(cssBadURL, start, "")
			}
			l.pos++
			b = append(b, l.escape())
		default:
			b = append(b, c)
			l.pos++
		}
	}
}

func (l *cssLexer) badURLRemnants() {
	for {
		c := l.at(0)
		switch {
		case c == cssEOFRune:
			return
		case c == ')':
			l.pos++
			return
		case cssValidEscape(c, l.at(1)):
			l.pos++
			l.escape()
		default:
			l.pos++
		}
	}
}

func (l *cssLexer) string(quote rune) cssToken {
	start := l.pos
	l.pos++
	var b []rune
	for {
		c := l.at(0)
		switch {
		case c == quote:
			l.pos++
			return l.token(cssString, start, string(b))
		case c == cssEOFRune:
			t := l.token(cssString, start, string(b))
			t.unclosed = true
			return t
		case c == '\n':
			// The newline isn't part of the string; it's the next token.
			return l.token(cssBadString, start, "")
		case c == '\\':
			switch l.at(1) {
			case cssEOFRune:
				l.pos++
			case '\n':
				l.pos += 2
			default:
				l.pos++
				b = append(b, l.escape())
			}
		default:
			b = append(b, c)
			l.pos++
		}
	}
}

func (l *cssLexer) numeric() cssToken {
	start := l.pos
	if l.at(0) == '+' || l.at(0) == '-' {
		l.pos++
	}
	for isCSSDigit(l.at(0)) {
		l.pos++
	}
	if l.at(0) == '.' && isCSSDigit(l.at(1)) {
		l.pos++
		for isCSSDigit(l.at(0)) {
			l.pos++
		}
	}
	if e := l.at(0); e == 'e' || e == 'E' {
		if isCSSDigit(l.at(1)) {
			l.pos++
		} else if (l.at(1) == '+' || l.at(1) == '-') && isCSSDigit(l.at(2)) {
			l.pos += 2
		}
		for isCSSDigit(l.at(0)) {
			l.pos++
		}
	}
	if cssStartsIdent(l.at(0), l.at(1), l.at(2)) {
		return l.token(cssDimension, start, l.identSequence())
	}
	if l.at(0) == '%' {
		l.pos++
		return l.token(cssPercentage, start, "")
	}
	return l.token(cssNumber, start, "")
}
