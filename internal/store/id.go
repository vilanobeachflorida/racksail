package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"
)

// ID is a TypeID: a lowercase prefix that names what the ID is for, an
// underscore and a UUIDv7 encoded in base32, for example
// site_01j9x3k4m5n6p7q8r9s0t1u2v3. IDs sort by creation time.
type ID string

const (
	idAlphabet     = "0123456789abcdefghjkmnpqrstvwxyz"
	idSuffixLength = 26
	maxPrefixLen   = 63
)

var idDecode = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xff
	}
	for i := 0; i < len(idAlphabet); i++ {
		t[idAlphabet[i]] = byte(i)
	}
	return t
}()

// ErrInvalidID is returned when a string is not a valid TypeID.
var ErrInvalidID = errors.New("invalid id")

// NewID returns a new ID with the given prefix. The prefix must satisfy
// ValidPrefix; kind prefixes are checked when the kind is defined.
func NewID(prefix string) ID {
	return FromUUID(prefix, uuid.NewV7())
}

// FromUUID builds an ID from a prefix and a UUID.
func FromUUID(prefix string, u uuid.UUID) ID {
	suffix := encodeSuffix(u)
	if prefix == "" {
		return ID(suffix)
	}
	return ID(prefix + "_" + suffix)
}

// ParseID parses and validates s.
func ParseID(s string) (ID, error) {
	if _, _, err := split(s); err != nil {
		return "", err
	}
	return ID(s), nil
}

// Valid reports whether id is a well-formed TypeID.
func (id ID) Valid() bool {
	_, _, err := split(string(id))
	return err == nil
}

// Prefix returns the part before the last underscore.
func (id ID) Prefix() string {
	prefix, _, _ := split(string(id))
	return prefix
}

// UUID returns the UUID encoded in the ID.
func (id ID) UUID() (uuid.UUID, error) {
	_, u, err := split(string(id))
	return u, err
}

// Time returns the creation time recorded in a UUIDv7-based ID, or the zero
// time when the ID is invalid.
func (id ID) Time() time.Time {
	u, err := id.UUID()
	if err != nil {
		return time.Time{}
	}
	ms := int64(u[0])<<40 | int64(u[1])<<32 | int64(u[2])<<24 | int64(u[3])<<16 | int64(u[4])<<8 | int64(u[5])
	return time.UnixMilli(ms).UTC()
}

// String returns the ID as a string.
func (id ID) String() string { return string(id) }

// ValidPrefix reports whether p can be used as a TypeID prefix: lowercase
// ASCII letters and underscores, at most 63 characters, starting and ending
// with a letter.
func ValidPrefix(p string) bool {
	if p == "" || len(p) > maxPrefixLen {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '_' {
			if i == 0 || i == len(p)-1 {
				return false
			}
			continue
		}
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func split(s string) (string, uuid.UUID, error) {
	var zero uuid.UUID
	prefix, suffix := "", s
	if i := strings.LastIndexByte(s, '_'); i >= 0 {
		prefix, suffix = s[:i], s[i+1:]
		if !ValidPrefix(prefix) {
			return "", zero, fmt.Errorf("%w: bad prefix in %q", ErrInvalidID, s)
		}
	}
	u, err := decodeSuffix(suffix)
	if err != nil {
		return "", zero, fmt.Errorf("%w: %q: %v", ErrInvalidID, s, err)
	}
	return prefix, u, nil
}

// encodeSuffix writes the 128 bits of u as 26 base32 characters, most
// significant first. The first character carries only three bits.
func encodeSuffix(u uuid.UUID) string {
	var hi, lo uint64
	for i := 0; i < 8; i++ {
		hi = hi<<8 | uint64(u[i])
		lo = lo<<8 | uint64(u[8+i])
	}
	var out [idSuffixLength]byte
	for i := 0; i < idSuffixLength; i++ {
		shift := uint(5 * (idSuffixLength - 1 - i))
		var v uint64
		switch {
		case shift >= 64:
			v = hi >> (shift - 64)
		case shift > 59:
			v = lo>>shift | hi<<(64-shift)
		default:
			v = lo >> shift
		}
		out[i] = idAlphabet[v&31]
	}
	return string(out[:])
}

func decodeSuffix(s string) (uuid.UUID, error) {
	var u uuid.UUID
	if len(s) != idSuffixLength {
		return u, fmt.Errorf("suffix must be %d characters", idSuffixLength)
	}
	if s[0] > '7' {
		return u, errors.New("suffix encodes more than 128 bits")
	}
	var hi, lo uint64
	for i := 0; i < len(s); i++ {
		v := idDecode[s[i]]
		if v == 0xff {
			return u, fmt.Errorf("character %q is not in the alphabet", s[i])
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(v)
	}
	for i := 7; i >= 0; i-- {
		u[i] = byte(hi)
		u[8+i] = byte(lo)
		hi >>= 8
		lo >>= 8
	}
	return u, nil
}
