// Package ber encodes and decodes the Basic Encoding Rules as LDAP
// restricts them (RFC 4511, section 5.1): definite lengths only, and tags
// that fit in one byte.
package ber

import (
	"errors"
	"fmt"
	"io"
)

// Universal tags, and the class and form bits of the others.
const (
	Boolean     = 0x01
	Integer     = 0x02
	OctetString = 0x04
	Null        = 0x05
	Enumerated  = 0x0a
	Sequence    = 0x30
	Set         = 0x31

	Application = 0x40
	Context     = 0x80
	Constructed = 0x20
)

// MaxElement bounds the length of one element. A directory entry is far
// smaller, and anything larger is a broken or hostile peer.
const MaxElement = 4 << 20

// ErrMalformed is input that isn't BER as LDAP writes it.
var ErrMalformed = errors.New("ber: malformed element")

// Element encodes one element.
func Element(tag byte, content []byte) []byte {
	out := make([]byte, 0, 6+len(content))
	out = append(out, tag)
	out = append(out, length(len(content))...)
	return append(out, content...)
}

func length(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var digits []byte
	for v := n; v > 0; v >>= 8 {
		digits = append([]byte{byte(v)}, digits...)
	}
	return append([]byte{0x80 | byte(len(digits))}, digits...)
}

// Join encodes a constructed element from its parts.
func Join(tag byte, parts ...[]byte) []byte {
	var content []byte
	for _, p := range parts {
		content = append(content, p...)
	}
	return Element(tag, content)
}

// String encodes an octet string, or anything else made of bytes, with
// the given tag.
func String(tag byte, s string) []byte { return Element(tag, []byte(s)) }

// Int encodes n in the fewest bytes of two's complement, as an INTEGER or
// ENUMERATED as tag says.
func Int(tag byte, n int64) []byte {
	var content []byte
	for {
		content = append([]byte{byte(n)}, content...)
		if (n >= -128 && n <= 127) || len(content) == 8 {
			break
		}
		n >>= 8
	}
	return Element(tag, content)
}

// Bool encodes a BOOLEAN.
func Bool(b bool) []byte {
	if b {
		return []byte{Boolean, 1, 0xff}
	}
	return []byte{Boolean, 1, 0}
}

// Next reads one element from b: its tag, its content and what follows.
func Next(b []byte) (tag byte, content, rest []byte, err error) {
	if len(b) < 2 {
		return 0, nil, nil, fmt.Errorf("%w: it ends early", ErrMalformed)
	}
	tag = b[0]
	if tag&0x1f == 0x1f {
		return 0, nil, nil, fmt.Errorf("%w: a tag of more than one byte", ErrMalformed)
	}
	n, size, err := decodeLength(b[1:])
	if err != nil {
		return 0, nil, nil, err
	}
	start := 1 + size
	if n > len(b)-start {
		return 0, nil, nil, fmt.Errorf("%w: it ends early", ErrMalformed)
	}
	return tag, b[start : start+n], b[start+n:], nil
}

// decodeLength reads a definite length, which is all LDAP uses.
func decodeLength(b []byte) (n, size int, err error) {
	if len(b) == 0 {
		return 0, 0, fmt.Errorf("%w: it ends early", ErrMalformed)
	}
	if b[0] < 0x80 {
		return int(b[0]), 1, nil
	}
	digits := int(b[0] & 0x7f)
	if digits == 0 {
		return 0, 0, fmt.Errorf("%w: an indefinite length", ErrMalformed)
	}
	if digits > 4 || len(b) < 1+digits {
		return 0, 0, fmt.Errorf("%w: a length that can't be read", ErrMalformed)
	}
	for _, d := range b[1 : 1+digits] {
		n = n<<8 | int(d)
	}
	if n > MaxElement {
		return 0, 0, fmt.Errorf("%w: %d bytes in one element, more than %d", ErrMalformed, n, MaxElement)
	}
	return n, 1 + digits, nil
}

// Expect reads one element of a given tag.
func Expect(b []byte, tag byte) (content, rest []byte, err error) {
	got, content, rest, err := Next(b)
	if err != nil {
		return nil, nil, err
	}
	if got != tag {
		return nil, nil, fmt.Errorf("%w: expected tag %#x, got %#x", ErrMalformed, tag, got)
	}
	return content, rest, nil
}

// DecodeInt reads the content of an INTEGER or ENUMERATED.
func DecodeInt(content []byte) (int64, error) {
	if len(content) == 0 || len(content) > 8 {
		return 0, fmt.Errorf("%w: an integer of %d bytes", ErrMalformed, len(content))
	}
	n := int64(int8(content[0]))
	for _, b := range content[1:] {
		n = n<<8 | int64(b)
	}
	return n, nil
}

// DecodeBool reads the content of a BOOLEAN.
func DecodeBool(content []byte) (bool, error) {
	if len(content) != 1 {
		return false, fmt.Errorf("%w: a boolean of %d bytes", ErrMalformed, len(content))
	}
	return content[0] != 0, nil
}

// ReadMessage reads one whole LDAPMessage, a SEQUENCE, from r and returns
// its content.
func ReadMessage(r io.Reader) ([]byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, err
	}
	if head[0] != Sequence {
		return nil, fmt.Errorf("%w: a message starts with tag %#x", ErrMalformed, head[0])
	}
	lengthBytes := []byte{head[1]}
	if head[1] >= 0x80 {
		digits := int(head[1] & 0x7f)
		if digits == 0 || digits > 4 {
			return nil, fmt.Errorf("%w: a length that can't be read", ErrMalformed)
		}
		more := make([]byte, digits)
		if _, err := io.ReadFull(r, more); err != nil {
			return nil, err
		}
		lengthBytes = append(lengthBytes, more...)
	}
	n, _, err := decodeLength(lengthBytes)
	if err != nil {
		return nil, err
	}
	content := make([]byte, n)
	if _, err := io.ReadFull(r, content); err != nil {
		return nil, err
	}
	return content, nil
}
