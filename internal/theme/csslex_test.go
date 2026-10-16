package theme

import (
	"strings"
	"testing"
)

func cssKinds(css string) []cssToken {
	var out []cssToken
	for _, t := range tokenizeCSS(preprocessCSS(css)) {
		if t.kind != cssWhitespace && t.kind != cssEOF {
			out = append(out, t)
		}
	}
	return out
}

func TestTokenizeCSS(t *testing.T) {
	type tok struct {
		kind  cssKind
		value string
	}
	tests := []struct {
		css  string
		want []tok
	}{
		// A brace in an unquoted url is part of the URL, not a block.
		{`url(data:image/png{)}`, []tok{{cssURL, "data:image/png{"}, {cssCloseCurly, ""}}},
		{`URL( a\)b )`, []tok{{cssURL, "a)b"}}},
		{`url( "x.png" )`, []tok{{cssFunction, "url"}, {cssString, "x.png"}, {cssCloseParen, ""}}},
		{`url(a b)`, []tok{{cssBadURL, ""}}},
		{`url(a"b) x`, []tok{{cssBadURL, ""}, {cssIdent, "x"}}},
		{`url(a(b) x`, []tok{{cssBadURL, ""}, {cssIdent, "x"}}},
		// Escapes are resolved in names, so they can't disguise one.
		{`@\69mport`, []tok{{cssAtKeyword, "import"}}},
		{`u\72l(x)`, []tok{{cssURL, "x"}}},
		{`\75 rl(x)`, []tok{{cssURL, "x"}}},
		{`--rs-crit\69 cal`, []tok{{cssIdent, "--rs-critical"}}},
		{`.a\:hover`, []tok{{cssDelim, "."}, {cssIdent, "a:hover"}}},
		{`#\31 23`, []tok{{cssHash, "123"}}},
		{`\0`, []tok{{cssIdent, "�"}}},
		{"\\\n", []tok{{cssDelim, "\\"}}},
		// Strings end at their quote or the end of the line.
		{`"a\"b" 'c'`, []tok{{cssString, `a"b`}, {cssString, "c"}}},
		{"\"a\\\nb\"", []tok{{cssString, "ab"}}},
		{"\"a\nb\"", []tok{{cssBadString, ""}, {cssIdent, "b"}, {cssString, ""}}},
		{`"\26 x"`, []tok{{cssString, "&x"}}},
		{`/* { */ a`, []tok{{cssComment, ""}, {cssIdent, "a"}}},
		{`1.5e3em -2% +.5 10`, []tok{{cssDimension, "em"}, {cssPercentage, ""}, {cssNumber, ""}, {cssNumber, ""}}},
		{`<!-- -->`, []tok{{cssCDO, ""}, {cssCDC, ""}}},
		{`!important`, []tok{{cssDelim, "!"}, {cssIdent, "important"}}},
		{`image-set("a.png" 1x)`, []tok{{cssFunction, "image-set"}, {cssString, "a.png"}, {cssDimension, "x"}, {cssCloseParen, ""}}},
		{"a\r\nb\x00", []tok{{cssIdent, "a"}, {cssIdent, "b�"}}},
	}
	for _, tt := range tests {
		got := cssKinds(tt.css)
		ok := len(got) == len(tt.want)
		for i := 0; ok && i < len(got); i++ {
			ok = got[i].kind == tt.want[i].kind && got[i].value == tt.want[i].value
		}
		if !ok {
			t.Errorf("%q: got %+v, want %+v", tt.css, got, tt.want)
		}
	}

	for _, unclosed := range []string{`/* x`, `"abc`, `url(abc`} {
		got := cssKinds(unclosed)
		if len(got) != 1 || !got[0].unclosed {
			t.Errorf("%q: %+v should be one unclosed token", unclosed, got)
		}
	}
}

func TestTokensCoverTheInput(t *testing.T) {
	src := preprocessCSS(`.a{background:url(data:image/png{)}} /* c */ @media (x){.b{c:"d\"e"}}`)
	pos := 0
	for _, tok := range tokenizeCSS(src) {
		if tok.start != pos {
			t.Fatalf("token %+v starts at %d, want %d", tok, tok.start, pos)
		}
		pos = tok.end
	}
	if pos != len(src) {
		t.Errorf("tokens end at %d of %d", pos, len(src))
	}
	if !strings.Contains(string(src), "url(data:image/png{)") {
		t.Error("preprocessing changed the text")
	}
}
