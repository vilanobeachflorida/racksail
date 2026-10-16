package ldap

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/internal/ber"
)

// Tags of the filter choices (RFC 4511, section 4.5.1.7).
const (
	FilterAnd            = ber.Context | ber.Constructed // [0]
	FilterOr             = ber.Context | ber.Constructed | 1
	FilterNot            = ber.Context | ber.Constructed | 2
	FilterEquality       = ber.Context | ber.Constructed | 3
	FilterSubstrings     = ber.Context | ber.Constructed | 4
	FilterGreaterOrEqual = ber.Context | ber.Constructed | 5
	FilterLessOrEqual    = ber.Context | ber.Constructed | 6
	FilterPresent        = ber.Context | 7
	FilterApprox         = ber.Context | ber.Constructed | 8

	SubstringInitial = ber.Context // [0]
	SubstringAny     = ber.Context | 1
	SubstringFinal   = ber.Context | 2
)

// maxFilterDepth bounds how deep filters nest.
const maxFilterDepth = 16

// ErrFilter is a search filter that isn't written as RFC 4515 says.
var ErrFilter = errors.New("ldap: the search filter can't be read")

// EscapeFilter escapes a value for a search filter, so what someone types
// as their username is matched as it is and can't change the filter.
func EscapeFilter(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c == '\\' || c == '*' || c == '(' || c == ')' || c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, "\\%02x", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// EncodeFilter turns a filter such as (&(objectClass=person)(uid=ada))
// into its BER form.
func EncodeFilter(filter string) ([]byte, error) {
	p := &filterParser{s: strings.TrimSpace(filter)}
	out, err := p.filter(0)
	if err != nil {
		return nil, err
	}
	if p.i != len(p.s) {
		return nil, fmt.Errorf("%w: there's more after the filter", ErrFilter)
	}
	return out, nil
}

type filterParser struct {
	s string
	i int
}

func (p *filterParser) fail(reason string) error {
	return fmt.Errorf("%w: %s at character %d", ErrFilter, reason, p.i+1)
}

func (p *filterParser) filter(depth int) ([]byte, error) {
	if depth > maxFilterDepth {
		return nil, p.fail("it nests too deeply")
	}
	if p.i >= len(p.s) || p.s[p.i] != '(' {
		return nil, p.fail("a filter starts with (")
	}
	p.i++
	if p.i >= len(p.s) {
		return nil, p.fail("the filter ends early")
	}
	var out []byte
	var err error
	switch p.s[p.i] {
	case '&':
		p.i++
		out, err = p.list(FilterAnd, depth)
	case '|':
		p.i++
		out, err = p.list(FilterOr, depth)
	case '!':
		p.i++
		var inner []byte
		if inner, err = p.filter(depth + 1); err == nil {
			out = ber.Join(FilterNot, inner)
		}
	default:
		out, err = p.item()
	}
	if err != nil {
		return nil, err
	}
	if p.i >= len(p.s) || p.s[p.i] != ')' {
		return nil, p.fail("a filter ends with )")
	}
	p.i++
	return out, nil
}

func (p *filterParser) list(tag byte, depth int) ([]byte, error) {
	var parts [][]byte
	for p.i < len(p.s) && p.s[p.i] == '(' {
		f, err := p.filter(depth + 1)
		if err != nil {
			return nil, err
		}
		parts = append(parts, f)
	}
	if len(parts) == 0 {
		return nil, p.fail("& and | take at least one filter")
	}
	return ber.Join(tag, parts...), nil
}

// item reads attr=value, attr~=value, attr>=value, attr<=value, attr=* and
// substrings such as cn=Ad*Lov*.
func (p *filterParser) item() ([]byte, error) {
	start := p.i
	for p.i < len(p.s) && strings.IndexByte("=~<>()", p.s[p.i]) < 0 {
		p.i++
	}
	attr := p.s[start:p.i]
	if !validAttribute(attr) {
		return nil, p.fail("an attribute's name is letters, digits, hyphens and options, or an OID")
	}
	if p.i >= len(p.s) {
		return nil, p.fail("the filter ends early")
	}
	tag := byte(FilterEquality)
	switch p.s[p.i] {
	case '~':
		tag = FilterApprox
		p.i++
	case '>':
		tag = FilterGreaterOrEqual
		p.i++
	case '<':
		tag = FilterLessOrEqual
		p.i++
	}
	if p.i >= len(p.s) || p.s[p.i] != '=' {
		return nil, p.fail("expected =")
	}
	p.i++
	start = p.i
	for p.i < len(p.s) && p.s[p.i] != ')' {
		if p.s[p.i] == '(' {
			return nil, p.fail("( in a value is written \\28")
		}
		p.i++
	}
	raw := p.s[start:p.i]
	if tag == FilterEquality && raw == "*" {
		return ber.String(FilterPresent, attr), nil
	}
	if tag == FilterEquality && strings.Contains(raw, "*") {
		return p.substrings(attr, raw)
	}
	value, err := unescape(raw)
	if err != nil {
		return nil, p.fail(err.Error())
	}
	return ber.Join(tag, ber.String(ber.OctetString, attr), ber.String(ber.OctetString, value)), nil
}

func (p *filterParser) substrings(attr, raw string) ([]byte, error) {
	pieces := strings.Split(raw, "*")
	var parts [][]byte
	for i, piece := range pieces {
		if piece == "" {
			continue
		}
		value, err := unescape(piece)
		if err != nil {
			return nil, p.fail(err.Error())
		}
		tag := byte(SubstringAny)
		switch i {
		case 0:
			tag = SubstringInitial
		case len(pieces) - 1:
			tag = SubstringFinal
		}
		parts = append(parts, ber.String(tag, value))
	}
	if len(parts) == 0 {
		return nil, p.fail("a substring filter needs some text")
	}
	return ber.Join(FilterSubstrings, ber.String(ber.OctetString, attr), ber.Join(ber.Sequence, parts...)), nil
}

// unescape reads a filter's value, where \XX is the byte XX.
func unescape(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+3 > len(s) {
			return "", errors.New(`\ is followed by two hexadecimal digits`)
		}
		v, err := hex.DecodeString(s[i+1 : i+3])
		if err != nil {
			return "", errors.New(`\ is followed by two hexadecimal digits`)
		}
		b.Write(v)
		i += 2
	}
	return b.String(), nil
}

// validAttribute reports whether s is an attribute description: a name
// such as mail or userPrincipalName with options such as ;binary, or an
// OID such as 2.5.4.3.
func validAttribute(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == ';' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}
