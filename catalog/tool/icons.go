package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxIconPixels bounds a PNG icon's sides; icons are shown small.
const maxIconPixels = 1024

// checkSVG refuses icons that could run code or load anything when a panel
// shows them: scripts, event handlers, embedded documents, declarations,
// references to anything outside the file, and animations that would set a
// link or a handler, which an icon shown inline would follow.
func checkSVG(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	seenRoot := false
	// A style element's style sheet is all of its text, which comments and
	// CDATA sections split into several tokens, so it is checked whole.
	inStyle := false
	var sheet strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("isn't valid SVG: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if !seenRoot {
				if name != "svg" {
					return errors.New("isn't an SVG file: the first element must be <svg>")
				}
				seenRoot = true
			}
			if inStyle {
				return fmt.Errorf("has a <%s> inside a <style>, which icons can't have", t.Name.Local)
			}
			switch {
			case name == "script", name == "handler", name == "foreignobject", name == "iframe", name == "embed",
				name == "object", name == "audio", name == "video":
				return fmt.Errorf("contains a <%s> element, which icons can't use", t.Name.Local)
			case t.Name.Space == xhtml || htmlBreakout[name]:
				// A page that shows the icon inline makes these its own
				// elements, outside the SVG, where an <img> loads its src.
				return fmt.Errorf("contains an HTML element, <%s>, which icons can't use", t.Name.Local)
			case name == "style":
				inStyle = true
				sheet.Reset()
			}
			for _, a := range t.Attr {
				// Judged by the name after any prefix, however a parser
				// splits it.
				attr := strings.ToLower(a.Name.Local)
				attr = attr[strings.LastIndex(attr, ":")+1:]
				switch {
				case strings.HasPrefix(attr, "on"):
					return fmt.Errorf("has an event handler (%s), which icons can't use", a.Name.Local)
				case attr == "href" && !strings.HasPrefix(strings.TrimSpace(a.Value), "#"):
					return fmt.Errorf("refers to %q; icons can only refer to their own elements", a.Value)
				case attr == "base":
					// xml:base would resolve even #id references elsewhere.
					return errors.New("has an xml:base attribute, which icons can't use")
				case attr == "attributename" && animatesLinkOrHandler(a.Value):
					// The values an animation sets never pass the href check.
					return fmt.Errorf("has a <%s> that animates %s, which icons can't do", t.Name.Local, a.Value)
				}
				if err := checkCSS(a.Value); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if inStyle && strings.EqualFold(t.Name.Local, "style") {
				inStyle = false
				if err := checkCSS(sheet.String()); err != nil {
					return err
				}
			}
		case xml.CharData:
			if inStyle {
				sheet.Write(t)
			}
		case xml.Directive:
			return errors.New("has a declaration such as <!DOCTYPE>, which icons can't use")
		case xml.ProcInst:
			if t.Target != "xml" {
				return fmt.Errorf("has a <?%s?> processing instruction, which icons can't use", t.Target)
			}
		}
	}
	if !seenRoot {
		return errors.New("is empty")
	}
	return nil
}

const xhtml = "http://www.w3.org/1999/xhtml"

// htmlBreakout lists the elements that end an SVG inside an HTML page, which
// then become HTML elements, as the HTML parser has it.
var htmlBreakout = map[string]bool{
	"b": true, "big": true, "blockquote": true, "body": true, "br": true, "center": true, "code": true, "dd": true,
	"div": true, "dl": true, "dt": true, "em": true, "embed": true, "font": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "head": true, "hr": true, "i": true, "img": true, "li": true, "listing": true,
	"menu": true, "meta": true, "nobr": true, "ol": true, "p": true, "pre": true, "ruby": true, "s": true, "small": true,
	"span": true, "strike": true, "strong": true, "sub": true, "sup": true, "table": true, "tt": true, "u": true,
	"ul": true, "var": true,
}

// animatesLinkOrHandler reports whether an animation's attributeName is a
// link, with or without a namespace prefix, or an event handler.
func animatesLinkOrHandler(name string) bool {
	target := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(target, ":"); i >= 0 {
		target = target[i+1:]
	}
	return target == "href" || strings.HasPrefix(target, "on")
}

// loadingFunctions are the CSS functions besides url() that load an image
// or a resource from an address, or show another element of the page.
var loadingFunctions = map[string]bool{
	"image": true, "image-set": true, "-webkit-image-set": true, "src": true,
	"cross-fade": true, "-webkit-cross-fade": true, "element": true, "-moz-element": true,
}

// checkCSS refuses style that loads anything: @import, url() other than to
// an element of the same file, and the functions that load images or show
// other elements. It reads the style as a browser does, with escapes
// decoded and comments gone, so neither can hide a function's name.
func checkCSS(s string) error {
	css := strings.ToLower(readCSS(s))
	if strings.Contains(css, "@import") {
		return errors.New("imports a style sheet, which icons can't do")
	}
	for i := strings.IndexByte(css, '('); i >= 0; i = nextParen(css, i) {
		start := i
		for start > 0 && isNameByte(css[start-1]) {
			start--
		}
		switch name := css[start:i]; {
		case name == "url":
			if !strings.HasPrefix(urlArgument(css[i+1:]), "#") {
				return errors.New("loads something with url(); icons can only refer to their own elements")
			}
		case loadingFunctions[name]:
			return fmt.Errorf("uses %s(), which can show images from elsewhere; icons can only refer to their own elements", name)
		}
	}
	return nil
}

// urlArgument returns the address a url() gives, from the text after its
// parenthesis: a string, up to its closing quote, or else the text itself,
// of which only the start matters. Like URL parsing, it drops the spaces and
// control characters the address starts with.
func urlArgument(s string) string {
	s = strings.TrimLeft(s, " \t\n")
	if s != "" && (s[0] == '"' || s[0] == '\'') {
		if end := strings.IndexByte(s[1:], s[0]); end >= 0 {
			s = s[1 : 1+end]
		} else {
			s = s[1:]
		}
	}
	return strings.TrimLeftFunc(s, func(r rune) bool { return r <= ' ' })
}

func nextParen(s string, i int) int {
	j := strings.IndexByte(s[i+1:], '(')
	if j < 0 {
		return -1
	}
	return i + 1 + j
}

// isNameByte reports whether b can be part of a CSS name: letters, digits,
// hyphens, underscores and anything outside ASCII.
func isNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b >= 0x80
}

// cssNewlines turns every newline into one line feed, and NUL into U+FFFD,
// before CSS is read, as browsers do.
var cssNewlines = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\f", "\n", "\x00", "\uFFFD")

// readCSS returns style the way CSS's tokenizer reads it: comments gone and
// escapes decoded. A backslash and up to six hex digits, with one
// whitespace character after them, stand for that code point, and a
// backslash before anything else but a newline stands for that character.
// Inside a string, which ends at its closing quote or a newline, a
// backslash before a newline continues the line, and /* doesn't start a
// comment.
func readCSS(s string) string {
	s = cssNewlines.Replace(s)
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case quote == 0 && strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += 2 + end + 2
		case c == '\\' && i+1 < len(s) && s[i+1] == '\n':
			// Inside a string this continues the line; outside one it is
			// no escape, and the newline after it ends any name before it.
			if quote == 0 {
				b.WriteByte(c)
				i++
			} else {
				i += 2
			}
		case c == '\\' && i+1 < len(s):
			i = readEscape(&b, s, i)
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
			b.WriteByte(c)
			i++
		case quote != 0 && (c == quote || c == '\n'):
			quote = 0
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// readEscape decodes the escape at s[i], a backslash before anything but a
// newline, into b and returns where the text after it starts.
func readEscape(b *strings.Builder, s string, i int) int {
	j := i + 1
	for j < len(s) && j-i <= 6 && isHex(s[j]) {
		j++
	}
	var r rune
	if j == i+1 {
		var size int
		r, size = utf8.DecodeRuneInString(s[j:])
		j += size
	} else {
		code, _ := strconv.ParseUint(s[i+1:j], 16, 32)
		r = rune(code)
		if code == 0 || code > utf8.MaxRune || code >= 0xD800 && code <= 0xDFFF {
			r = utf8.RuneError
		}
		if j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n') {
			j++
		}
	}
	// An escaped quote is part of a name or an address, never the start or
	// end of a string, so it goes on as a character that can't be read as one.
	if r == '"' || r == '\'' {
		r = utf8.RuneError
	}
	b.WriteRune(r)
	return j
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func checkPNG(data []byte) error {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("isn't a valid PNG file: %w", err)
	}
	if cfg.Width > maxIconPixels || cfg.Height > maxIconPixels {
		return fmt.Errorf("is %dx%d pixels; icons are at most %dx%d", cfg.Width, cfg.Height, maxIconPixels, maxIconPixels)
	}
	return nil
}
