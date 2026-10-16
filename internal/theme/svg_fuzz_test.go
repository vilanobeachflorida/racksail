package theme

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// The SVG fuzz test reads every file CheckSVG accepts a second time, the
// way XML 1.0, Namespaces in XML and browsers read it, with a reader of its
// own, and its styles with the reference CSS reader. Whatever that reading
// finds must keep CheckSVG's promise: only SVG drawing elements, no
// handlers, links only to the file's own parts, and nothing that loads.

func FuzzCheckSVG(f *testing.F) {
	for _, drawing := range svgDrawings {
		f.Add([]byte(drawing))
	}
	for _, refusal := range svgRefusals {
		f.Add([]byte(refusal.file))
	}
	for _, seed := range []string{
		svg(`<style>.a{fill:url(#g)}</style><rect class="a" style="stroke:url(#g)" fill="url(#g)"/>`),
		`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "x.dtd">` + svg(`<use xmlns:xlink="http://www.w3.org/1999/xlink" xlink:href="#a"/>`),
		`<s:svg xmlns:s="http://www.w3.org/2000/svg"><s:style><![CDATA[a{}]]><!-- b --></s:style></s:svg>`,
		svg(`<style>@im<!-- -->port "https://x.example/a.css";</style>`),
		svg(`<style>rect{fill:url(<![CDATA[https://x.example/a.svg]]>)}</style>`),
		svg(`<img xmlns="http://www.w3.org/1999/xhtml" src="https://x.example/a.png"/>`),
		svg("<rect fill='\"\n\" url(https://x.example/a.svg#p)'/>"),
		svg("<rect fill=\"url(https://x.example/a\\\nb.svg#p)\"/>"),
		svg(`<rect fill="image-set( ] 'https://x.example/a.png' 1x)"/>`),
		svg(`<use href="&#xA0;#a"/><rect fill="&#x75;rl(https://x.example/a.svg#p)"/>`),
		`<!DOCTYPE svg [<!ATTLIST svg onload CDATA "alert(1)">]>` + svg(``),
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if CheckSVG(data) != nil {
			return
		}
		events, _ := referenceXML(data)
		if problem := checkSVGReading(events); problem != "" {
			t.Errorf("an accepted SVG %s:\n%q", problem, data)
		}
	})
}

const xmlnsNamespace = "http://www.w3.org/2000/xmlns/"

type xmlEventKind int

const (
	xmlStart xmlEventKind = iota
	xmlEnd
	xmlText
	xmlInstruction
	xmlDoctype
)

type xmlAttr struct{ space, local, value string }

type xmlEvent struct {
	kind xmlEventKind
	// space and local name an element; local is an instruction's target.
	space, local string
	attrs        []xmlAttr
	text         string
	// subset marks a document type declaration with declarations of its
	// own, which this reading doesn't apply.
	subset bool
}

// xmlScope maps namespace prefixes to namespaces; "" is the default one.
type xmlScope map[string]string

type xmlReader struct {
	in     []rune
	pos    int
	events []xmlEvent
}

// notWellFormed stops a reading at the first error.
type notWellFormed struct{}

// referenceXML reads a document and returns what it read before the first
// error, as a browser that shows a broken document up to its error does,
// and whether it read the whole document.
func referenceXML(data []byte) (events []xmlEvent, ok bool) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	whole := true
	for i := 0; i < len(data); {
		c, size := utf8.DecodeRune(data[i:])
		if c == utf8.RuneError && size <= 1 {
			data, whole = data[:i], false
			break
		}
		i += size
	}
	text := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(data))
	r := &xmlReader{in: []rune(text)}
	defer func() {
		if v := recover(); v != nil {
			if _, stopped := v.(notWellFormed); !stopped {
				panic(v)
			}
			events, ok = r.events, false
		}
	}()
	r.document()
	return r.events, whole
}

func xmlChar(c rune) bool {
	return c == 0x9 || c == 0xA || c == 0xD || c >= 0x20 && c <= 0xD7FF || c >= 0xE000 && c <= 0xFFFD || c >= 0x10000 && c <= 0x10FFFF
}

func xmlSpace(c rune) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func xmlNameStart(c rune) bool {
	return c == ':' || c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= 0xC0 && c <= 0xD6 ||
		c >= 0xD8 && c <= 0xF6 || c >= 0xF8 && c <= 0x2FF || c >= 0x370 && c <= 0x37D || c >= 0x37F && c <= 0x1FFF ||
		c >= 0x200C && c <= 0x200D || c >= 0x2070 && c <= 0x218F || c >= 0x2C00 && c <= 0x2FEF ||
		c >= 0x3001 && c <= 0xD7FF || c >= 0xF900 && c <= 0xFDCF || c >= 0xFDF0 && c <= 0xFFFD || c >= 0x10000 && c <= 0xEFFFF
}

func xmlNameChar(c rune) bool {
	return xmlNameStart(c) || c == '-' || c == '.' || c >= '0' && c <= '9' || c == 0xB7 ||
		c >= 0x300 && c <= 0x36F || c >= 0x203F && c <= 0x2040
}

// ncName reports whether a name has no colon, as namespace prefixes and
// local names may not.
func ncName(s string) bool {
	for i, c := range s {
		if c == ':' || i == 0 && !xmlNameStart(c) || !xmlNameChar(c) {
			return false
		}
	}
	return s != ""
}

func (r *xmlReader) fail() { panic(notWellFormed{}) }

func (r *xmlReader) peek(i int) rune {
	if r.pos+i < len(r.in) {
		return r.in[r.pos+i]
	}
	return -1
}

func (r *xmlReader) at(s string) bool {
	i := r.pos
	for _, c := range s {
		if i >= len(r.in) || r.in[i] != c {
			return false
		}
		i++
	}
	return true
}

func (r *xmlReader) expect(s string) {
	if !r.at(s) {
		r.fail()
	}
	r.pos += utf8.RuneCountInString(s)
}

func (r *xmlReader) spaces() bool {
	start := r.pos
	for r.pos < len(r.in) && xmlSpace(r.in[r.pos]) {
		r.pos++
	}
	return r.pos > start
}

func (r *xmlReader) name() string {
	if r.pos >= len(r.in) || !xmlNameStart(r.in[r.pos]) {
		r.fail()
	}
	start := r.pos
	for r.pos < len(r.in) && xmlNameChar(r.in[r.pos]) {
		r.pos++
	}
	return string(r.in[start:r.pos])
}

// until reads characters up to end, and end itself.
func (r *xmlReader) until(end string) string {
	start := r.pos
	for !r.at(end) {
		if r.pos >= len(r.in) || !xmlChar(r.in[r.pos]) {
			r.fail()
		}
		r.pos++
	}
	s := string(r.in[start:r.pos])
	r.expect(end)
	return s
}

var xmlDeclaration = regexp.MustCompile(`^[ \t\n]+version[ \t\n]*=[ \t\n]*("1\.[0-9]+"|'1\.[0-9]+')` +
	`([ \t\n]+encoding[ \t\n]*=[ \t\n]*("[A-Za-z][A-Za-z0-9._-]*"|'[A-Za-z][A-Za-z0-9._-]*'))?` +
	`([ \t\n]+standalone[ \t\n]*=[ \t\n]*("(yes|no)"|'(yes|no)'))?[ \t\n]*$`)

func (r *xmlReader) document() {
	if r.at("<?xml") && xmlSpace(r.peek(5)) {
		r.pos += len("<?xml")
		m := xmlDeclaration.FindStringSubmatch(r.until("?>"))
		// This reading only knows UTF-8; a browser would decode another
		// encoding differently.
		if m == nil || m[3] != "" && !strings.EqualFold(strings.Trim(m[3], `"'`), "utf-8") {
			r.fail()
		}
	}
	doctype := false
	for root := false; !root; {
		r.spaces()
		switch {
		case r.at("<!--"):
			r.comment()
		case r.at("<?"):
			r.instruction()
		case r.at("<!DOCTYPE") && !doctype:
			doctype = true
			r.doctype()
		case r.at("<") && !r.at("<!"):
			r.element()
			root = true
		default:
			r.fail()
		}
	}
	for {
		r.spaces()
		switch {
		case r.pos >= len(r.in):
			return
		case r.at("<!--"):
			r.comment()
		case r.at("<?"):
			r.instruction()
		default:
			r.fail()
		}
	}
}

func (r *xmlReader) comment() {
	r.expect("<!--")
	for !r.at("--") {
		if r.pos >= len(r.in) || !xmlChar(r.in[r.pos]) {
			r.fail()
		}
		r.pos++
	}
	// -- may only end a comment.
	r.expect("-->")
}

func (r *xmlReader) instruction() {
	r.expect("<?")
	target := r.name()
	if strings.Contains(target, ":") || strings.EqualFold(target, "xml") {
		r.fail()
	}
	if !r.at("?>") && !r.spaces() {
		r.fail()
	}
	r.events = append(r.events, xmlEvent{kind: xmlInstruction, local: target, text: r.until("?>")})
}

func (r *xmlReader) literal() {
	q := r.peek(0)
	if q != '"' && q != '\'' {
		r.fail()
	}
	r.pos++
	r.until(string(q))
}

func (r *xmlReader) doctype() {
	r.expect("<!DOCTYPE")
	if !r.spaces() {
		r.fail()
	}
	r.name()
	if r.spaces() {
		switch {
		case r.at("SYSTEM"):
			r.pos += len("SYSTEM")
			if !r.spaces() {
				r.fail()
			}
			r.literal()
		case r.at("PUBLIC"):
			r.pos += len("PUBLIC")
			if !r.spaces() {
				r.fail()
			}
			r.literal()
			if !r.spaces() {
				r.fail()
			}
			r.literal()
		}
		r.spaces()
	}
	if r.at("[") {
		r.events = append(r.events, xmlEvent{kind: xmlDoctype, subset: true})
		r.fail()
	}
	r.expect(">")
	r.events = append(r.events, xmlEvent{kind: xmlDoctype})
}

// reference reads a character or entity reference. Without a DTD of its
// own, a document can only use the predefined entities.
func (r *xmlReader) reference() rune {
	r.expect("&")
	if r.at("#") {
		r.pos++
		base := 10
		if r.at("x") {
			r.pos++
			base = 16
		}
		start := r.pos
		v := 0
		for r.pos < len(r.in) && r.in[r.pos] != ';' {
			d := strings.IndexRune("0123456789abcdef"[:base], r.in[r.pos]|0x20)
			if d < 0 || r.in[r.pos] >= 0x80 {
				r.fail()
			}
			if v = v*base + d; v > 0x10FFFF {
				r.fail()
			}
			r.pos++
		}
		if r.pos == start {
			r.fail()
		}
		r.expect(";")
		if !xmlChar(rune(v)) {
			r.fail()
		}
		return rune(v)
	}
	name := r.name()
	r.expect(";")
	c, ok := map[string]rune{"lt": '<', "gt": '>', "amp": '&', "apos": '\'', "quot": '"'}[name]
	if !ok {
		r.fail()
	}
	return c
}

// attrValue reads an attribute value and normalizes it as XML does: each
// literal space, tab or line break becomes a space, while one written as a
// character reference stays as it is.
func (r *xmlReader) attrValue() string {
	q := r.peek(0)
	if q != '"' && q != '\'' {
		r.fail()
	}
	r.pos++
	var b strings.Builder
	for {
		c := r.peek(0)
		switch {
		case c == q:
			r.pos++
			return b.String()
		case c == '&':
			b.WriteRune(r.reference())
		case xmlSpace(c):
			b.WriteByte(' ')
			r.pos++
		case c == '<' || !xmlChar(c):
			r.fail()
		default:
			b.WriteRune(c)
			r.pos++
		}
	}
}

// resolve finds the namespace of an element's or attribute's name.
// Attributes without a prefix are in no namespace.
func (r *xmlReader) resolve(qname string, scope xmlScope, element bool) (string, string) {
	prefix, local, prefixed := strings.Cut(qname, ":")
	if !prefixed {
		if element {
			return scope[""], qname
		}
		return "", qname
	}
	if !ncName(prefix) || !ncName(local) || prefix == "xmlns" {
		r.fail()
	}
	if prefix == "xml" {
		return xmlNamespace, local
	}
	space, ok := scope[prefix]
	if !ok {
		r.fail()
	}
	return space, local
}

// startTag reads a start tag, given the namespaces in scope around it, and
// returns its name, the namespaces in scope inside it and whether it's an
// empty element.
func (r *xmlReader) startTag(parent xmlScope) (string, xmlScope, bool) {
	r.expect("<")
	qname := r.name()
	type rawAttr struct{ name, value string }
	var raw []rawAttr
	empty := false
	for {
		spaced := r.spaces()
		if r.at("/>") {
			r.pos += 2
			empty = true
			break
		}
		if r.at(">") {
			r.pos++
			break
		}
		if !spaced {
			r.fail()
		}
		name := r.name()
		r.spaces()
		r.expect("=")
		r.spaces()
		value := r.attrValue()
		for _, a := range raw {
			if a.name == name {
				r.fail()
			}
		}
		raw = append(raw, rawAttr{name, value})
	}
	scope := xmlScope{}
	for prefix, space := range parent {
		scope[prefix] = space
	}
	for _, a := range raw {
		if a.name == "xmlns" {
			if a.value == xmlNamespace || a.value == xmlnsNamespace {
				r.fail()
			}
			scope[""] = a.value
		} else if prefix, ok := strings.CutPrefix(a.name, "xmlns:"); ok {
			if !ncName(prefix) || prefix == "xmlns" || a.value == "" || a.value == xmlnsNamespace ||
				(prefix == "xml") != (a.value == xmlNamespace) {
				r.fail()
			}
			scope[prefix] = a.value
		}
	}
	space, local := r.resolve(qname, scope, true)
	event := xmlEvent{kind: xmlStart, space: space, local: local}
	seen := map[[2]string]bool{}
	for _, a := range raw {
		var name [2]string
		if prefix, ok := strings.CutPrefix(a.name, "xmlns:"); ok {
			name = [2]string{xmlnsNamespace, prefix}
		} else if a.name == "xmlns" {
			name = [2]string{xmlnsNamespace, "xmlns"}
		} else {
			name[0], name[1] = r.resolve(a.name, scope, false)
		}
		if seen[name] {
			r.fail()
		}
		seen[name] = true
		event.attrs = append(event.attrs, xmlAttr{name[0], name[1], a.value})
	}
	r.events = append(r.events, event)
	return qname, scope, empty
}

func (r *xmlReader) text(s string) {
	r.events = append(r.events, xmlEvent{kind: xmlText, text: s})
}

func (r *xmlReader) element() {
	type open struct {
		name  string
		scope xmlScope
	}
	name, scope, empty := r.startTag(xmlScope{})
	if empty {
		r.events = append(r.events, xmlEvent{kind: xmlEnd})
		return
	}
	stack := []open{{name, scope}}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		switch {
		case r.at("</"):
			r.pos += 2
			if r.name() != top.name {
				r.fail()
			}
			r.spaces()
			r.expect(">")
			stack = stack[:len(stack)-1]
			r.events = append(r.events, xmlEvent{kind: xmlEnd})
		case r.at("<!--"):
			r.comment()
		case r.at("<![CDATA["):
			r.pos += len("<![CDATA[")
			r.text(r.until("]]>"))
		case r.at("<?"):
			r.instruction()
		case r.at("<!") || r.pos >= len(r.in):
			r.fail()
		case r.at("<"):
			name, scope, empty := r.startTag(top.scope)
			if empty {
				r.events = append(r.events, xmlEvent{kind: xmlEnd})
			} else {
				stack = append(stack, open{name, scope})
			}
		case r.at("&"):
			r.text(string(r.reference()))
		default:
			start := r.pos
			for r.pos < len(r.in) && r.in[r.pos] != '<' && r.in[r.pos] != '&' {
				if r.at("]]>") || !xmlChar(r.in[r.pos]) {
					r.fail()
				}
				r.pos++
			}
			r.text(string(r.in[start:r.pos]))
		}
	}
}

// Elements that run code, link somewhere, embed a document or an image,
// animate or bring a font. Whatever the allowed elements are, none of
// these is one.
var svgNever = map[string]bool{
	"script": true, "foreignObject": true, "image": true, "feImage": true, "a": true, "iframe": true,
	"video": true, "audio": true, "canvas": true, "handler": true, "listener": true, "animate": true,
	"animateMotion": true, "animateTransform": true, "animateColor": true, "set": true, "mpath": true,
	"discard": true, "cursor": true, "font": true, "font-face": true, "font-face-uri": true, "glyph": true,
	"altGlyph": true, "tref": true, "color-profile": true,
}

// checkSVGReading says what's wrong with a reading of an SVG file, or ""
// when it keeps the promise.
func checkSVGReading(events []xmlEvent) string {
	type open struct {
		style bool
		// text is the element's own text, without that of elements inside
		// it: a style element's sheet.
		text strings.Builder
	}
	var stack []*open
	for _, e := range events {
		switch e.kind {
		case xmlDoctype:
			if e.subset {
				return "declares entities or attribute defaults, which browsers apply"
			}
		case xmlInstruction:
			if strings.EqualFold(e.local, "xml-stylesheet") {
				return "loads a style sheet"
			}
		case xmlStart:
			if problem := checkReadElement(e); problem != "" {
				return problem
			}
			stack = append(stack, &open{style: e.local == "style"})
		case xmlText:
			if len(stack) > 0 {
				stack[len(stack)-1].text.WriteString(e.text)
			}
		case xmlEnd:
			stack = stack[:len(stack)-1]
			continue
		}
		// A style element's sheet is checked as it grows: a browser that
		// shows a broken document up to its error keeps a style element
		// that was cut short.
		for _, o := range stack {
			if o.style {
				if problem := referenceLoads(o.text.String()); problem != "" {
					return problem
				}
			}
		}
	}
	return ""
}

func checkReadElement(e xmlEvent) string {
	if e.space != svgNamespace {
		return fmt.Sprintf("has <%s> in the namespace %q", e.local, e.space)
	}
	if svgNever[e.local] || !svgElements[e.local] {
		return fmt.Sprintf("has <%s>", e.local)
	}
	for _, a := range e.attrs {
		switch {
		case a.space == xmlnsNamespace:
			// Declares a namespace; nothing reads it as CSS or a link.
			continue
		case a.space == "" && strings.HasPrefix(referenceFold(a.local), "on"):
			return "has the event handler " + a.local
		case a.local == "href" && (a.space == "" || a.space == xlinkNamespace):
			if !referenceSameFile(a.value) {
				return "links to " + a.value
			}
			continue
		case a.space == "" && (svgAttributes[a.local] || strings.HasPrefix(a.local, "aria-") || strings.HasPrefix(a.local, "data-")):
		case a.space == xmlNamespace && (a.local == "lang" || a.local == "space"):
		default:
			return fmt.Sprintf("has the attribute %s in the namespace %q", a.local, a.space)
		}
		if problem := referenceLoads(a.value); problem != "" {
			return problem
		}
	}
	return ""
}

// referenceFold lowercases ASCII letters only, as CSS and HTML compare
// names.
func referenceFold(s string) string {
	return strings.Map(func(c rune) rune {
		if c >= 'A' && c <= 'Z' {
			return c + 'a' - 'A'
		}
		return c
	}, s)
}

// referenceSameFile reports whether a URL points into the same file, as the
// URL parser reads it: without the spaces and control characters around
// it, and without tabs and line breaks anywhere.
func referenceSameFile(u string) bool {
	u = strings.TrimFunc(u, func(c rune) bool { return c <= 0x20 })
	u = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(u)
	return strings.HasPrefix(u, "#")
}

var referenceURLFunctions = map[string]bool{
	"url": true, "src": true, "image-set": true, "-webkit-image-set": true, "image": true, "-webkit-image": true,
	"cross-fade": true, "-webkit-cross-fade": true,
}

// referenceLoads reads CSS and says what it loads from outside the file,
// or "".
func referenceLoads(css string) string {
	type frame struct {
		closer string
		urls   bool
	}
	closers := map[string]string{"(": ")", "[": "]", "{": "}"}
	var stack []frame
	for _, tok := range referenceCSS(css) {
		urls := false
		for _, f := range stack {
			urls = urls || f.urls
		}
		switch tok.kind {
		case refURL:
			if !referenceSameFile(tok.text) {
				return "loads " + tok.text
			}
		case refString:
			if urls && !referenceSameFile(tok.text) {
				return "loads " + tok.text
			}
		case refAtKeyword:
			if referenceFold(tok.text) == "import" {
				return "imports a style sheet"
			}
		case refFunction:
			stack = append(stack, frame{")", referenceURLFunctions[referenceFold(tok.text)]})
		case refOpen:
			stack = append(stack, frame{closers[tok.text], false})
		case refClose:
			// Only the innermost opener's own closer closes it.
			if len(stack) > 0 && stack[len(stack)-1].closer == tok.text {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return ""
}

func TestReferenceXMLReading(t *testing.T) {
	events, ok := referenceXML([]byte("\xef\xbb\xbf<?xml version=\"1.0\"?>\r\n<!-- a -->" +
		`<s:svg xmlns:s="http://www.w3.org/2000/svg" xmlns:x="http://www.w3.org/1999/xlink" a="1&#10;2` + "\t3\n" + `&lt;"><s:use x:href="#a"/>t&amp;<![CDATA[<c>]]></s:svg>`))
	if !ok || len(events) != 7 {
		t.Fatalf("events = %+v, ok = %v", events, ok)
	}
	root := events[0]
	if root.space != svgNamespace || root.local != "svg" || root.attrs[2] != (xmlAttr{"", "a", "1\n2 3 <"}) {
		t.Errorf("root = %+v", root)
	}
	if use := events[1]; use.local != "use" || use.attrs[0] != (xmlAttr{xlinkNamespace, "href", "#a"}) {
		t.Errorf("use = %+v", use)
	}
	if text := events[3].text + events[4].text + events[5].text; text != "t&<c>" {
		t.Errorf("text = %q", text)
	}
	for name, doc := range map[string]string{
		"undeclared prefix":   svg(`<p:rect/>`),
		"duplicate":           svg(`<rect a="1" a="2"/>`),
		"duplicate expanded":  `<svg xmlns="http://www.w3.org/2000/svg" xmlns:a="u" xmlns:b="u" a:x="1" b:x="2"/>`,
		"dashes in a comment": svg(`<!-- a -- b -->`),
		"two roots":           svg(``) + svg(``),
		"late declaration":    svg(``) + `<?xml version="1.0"?>`,
		"text outside":        `x` + svg(``),
		"cdata end":           svg(`]]>`),
		"control character":   svg("\x01"),
		"unknown entity":      svg(`&nbsp;`),
		"other encoding":      `<?xml version="1.0" encoding="ISO-8859-1"?>` + svg(``),
		"mismatched end":      `<svg xmlns="http://www.w3.org/2000/svg"></SVG>`,
		"empty prefix":        `<svg xmlns="http://www.w3.org/2000/svg" xmlns:p=""/>`,
		"no space":            `<svg xmlns="http://www.w3.org/2000/svg"a="1"/>`,
		"cut short":           svg(`<g>`),
		"invalid UTF-8":       svg("\xff"),
	} {
		if _, ok := referenceXML([]byte(doc)); ok {
			t.Errorf("%s: should not be well-formed", name)
		}
	}
	// What comes before an error is still read.
	if events, _ := referenceXML([]byte(svg(`<rect/><g>`))); len(events) != 4 {
		t.Errorf("events before the error = %+v", events)
	}
}

// The reading finds what CheckSVG refuses: elements of other namespaces,
// and references hidden by line breaks and split style sheets.
func TestSVGReadingFindsWhatLoads(t *testing.T) {
	for name, tt := range map[string]struct{ file, want string }{
		"plain":            {logoSVG, ""},
		"xhtml image":      {svg(`<img xmlns="http://www.w3.org/1999/xhtml" src="https://x.example/a.png"/>`), "namespace"},
		"split import":     {svg(`<style>@im<!-- -->port "https://x.example/a.css";</style>`), "imports"},
		"split url":        {svg(`<style><![CDATA[rect{fill:ur]]>l(https://x.example/a.svg#p)}</style>`), "loads https://x.example/a.svg#p"},
		"element in style": {svg(`<style>rect{fill:u<g/>rl(https://x.example/a.svg#p)}</style>`), "loads https://x.example/a.svg#p"},
		"cut short":        {`<svg xmlns="http://www.w3.org/2000/svg"><style>@import "x.css";`, "imports"},
		"newline":          {svg("<rect fill='\"\n\" url(https://x.example/a.svg#p)'/>"), "loads https://x.example/a.svg#p"},
		"escaped newline":  {svg("<rect fill=\"url(https://x.example/a\\\nb.svg#p)\"/>"), "loads https://x.example/a b.svg#p"},
		"stray closer":     {svg(`<rect fill="image-set( ] 'https://x.example/a.png' 1x)"/>`), "loads https://x.example/a.png"},
		"spaced link":      {svg(`<use href="&#xA0;#a"/>`), "links to"},
		"attribute list":   {`<!DOCTYPE svg [<!ATTLIST svg onload CDATA "alert(1)">]>` + svg(``), "declares"},
		"kelvin sign":      {svg("<rect fill=\"-web\u212Ait-image-set('https://x.example/a.png' 1x)\"/>"), ""},
	} {
		events, _ := referenceXML([]byte(tt.file))
		got := checkSVGReading(events)
		if tt.want == "" && got != "" || tt.want != "" && !strings.Contains(got, tt.want) {
			t.Errorf("%s: %q, want %q", name, got, tt.want)
		}
	}
}
