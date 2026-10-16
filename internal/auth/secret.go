package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// Prefixes of the secrets Racksail hands out, so they're recognizable in a
// leaked log or a code scanner, and so a token pasted where a session is
// expected fails clearly.
const (
	TokenPrefix   = "rsk_"
	SessionPrefix = "rss_"
	DevicePrefix  = "rsd_"
	InvitePrefix  = "rsi_"
)

// NewSecret returns a random secret with the given prefix: 32 random bytes,
// base64url-encoded, so it can't be guessed and survives URLs and headers.
func NewSecret(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on the platforms Racksail runs on; going
		// on with a weak secret would be worse than stopping.
		panic("auth: no randomness: " + err.Error())
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// HashSecret returns the hex SHA-256 of a secret, which is what gets
// stored: secrets carry 256 bits of randomness, so a fast hash is enough,
// and a stolen database holds nothing that signs anyone in.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// codeAlphabet leaves out letters and digits people confuse: 0 and O, 1, I
// and L, and U, which turns codes into words.
const codeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewCode returns a code people type, such as a recovery code or the setup
// code: groups of five characters joined by dashes.
func NewCode(groups int) string {
	b := make([]byte, groups*5)
	max := byte(256 - 256%len(codeAlphabet))
	out := make([]byte, 0, groups*6)
	for i := 0; i < len(b); {
		var r [1]byte
		if _, err := rand.Read(r[:]); err != nil {
			panic("auth: no randomness: " + err.Error())
		}
		// Rejecting the top of the range keeps every character equally
		// likely.
		if r[0] >= max {
			continue
		}
		if i > 0 && i%5 == 0 {
			out = append(out, '-')
		}
		out = append(out, codeAlphabet[int(r[0])%len(codeAlphabet)])
		i++
	}
	return string(out)
}

// NormalizeCode reads a code the way people type it: any case, with or
// without the dashes and spaces.
func NormalizeCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r != '-' && r != ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HashCode hashes a code as typed, after NormalizeCode.
func HashCode(code string) string { return HashSecret(NormalizeCode(code)) }

// SameHash compares two hashes in constant time.
func SameHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// RecoveryCodeCount is how many recovery codes a person gets at a time.
const RecoveryCodeCount = 10

// NewRecoveryCodes returns a fresh set of recovery codes.
func NewRecoveryCodes() []string {
	codes := make([]string, RecoveryCodeCount)
	for i := range codes {
		codes[i] = NewCode(2)
	}
	return codes
}

// Cookies the panel sets. Both are HTTP-only, Secure and SameSite=Strict.
const (
	// SessionCookie holds the secret of the browser's session.
	SessionCookie = "racksail_session"
	// DeviceCookie holds the secret of a device someone chose to trust, so
	// it skips the second sign-in step.
	DeviceCookie = "racksail_device"
)
