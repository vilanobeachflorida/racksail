package auth

import (
	"crypto/sha512"
	"crypto/subtle"
	"strconv"
	"strings"
)

// SHA-512 crypt, as in Ulrich Drepper's "Unix crypt using SHA-256 and
// SHA-512": the $6$ hashes of /etc/shadow, which installer answers may
// carry for the first admin. Racksail only ever checks them, and replaces
// them with Argon2id after the first sign-in.

const cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const (
	sha512DefaultRounds = 5000
	sha512MinRounds     = 1000
	sha512MaxSalt       = 16
)

// verifySHA512Crypt checks password against a $6$ hash.
func verifySHA512Crypt(hash, password string) (bool, error) {
	rest := strings.TrimPrefix(hash, "$6$")
	rounds, custom := sha512DefaultRounds, false
	if r, ok := strings.CutPrefix(rest, "rounds="); ok {
		n, after, found := strings.Cut(r, "$")
		if !found {
			return false, ErrHashFormat
		}
		v, err := strconv.Atoi(n)
		if err != nil || v < 1 {
			return false, ErrHashFormat
		}
		rounds, custom, rest = max(v, sha512MinRounds), true, after
		if rounds > maxSHA512Round {
			return false, ErrHashFormat
		}
	}
	salt, _, found := strings.Cut(rest, "$")
	if !found {
		return false, ErrHashFormat
	}
	if len(salt) > sha512MaxSalt {
		salt = salt[:sha512MaxSalt]
	}
	computed := sha512Crypt([]byte(password), []byte(salt), rounds, custom)
	return subtle.ConstantTimeCompare([]byte(computed), []byte(hash)) == 1, nil
}

// sha512Crypt computes the full $6$ string for a password and salt.
func sha512Crypt(password, salt []byte, rounds int, custom bool) string {
	alt := sha512.New()
	alt.Write(password)
	alt.Write(salt)
	alt.Write(password)
	b := alt.Sum(nil)

	a := sha512.New()
	a.Write(password)
	a.Write(salt)
	n := len(password)
	for ; n > 64; n -= 64 {
		a.Write(b)
	}
	a.Write(b[:n])
	for n := len(password); n > 0; n >>= 1 {
		if n&1 != 0 {
			a.Write(b)
		} else {
			a.Write(password)
		}
	}
	digest := a.Sum(nil)

	dp := sha512.New()
	for range len(password) {
		dp.Write(password)
	}
	pBytes := repeatTo(dp.Sum(nil), len(password))

	ds := sha512.New()
	for range 16 + int(digest[0]) {
		ds.Write(salt)
	}
	sBytes := repeatTo(ds.Sum(nil), len(salt))

	for i := range rounds {
		c := sha512.New()
		if i&1 != 0 {
			c.Write(pBytes)
		} else {
			c.Write(digest)
		}
		if i%3 != 0 {
			c.Write(sBytes)
		}
		if i%7 != 0 {
			c.Write(pBytes)
		}
		if i&1 != 0 {
			c.Write(digest)
		} else {
			c.Write(pBytes)
		}
		digest = c.Sum(digest[:0])
	}

	var out strings.Builder
	out.WriteString("$6$")
	if custom {
		out.WriteString("rounds=" + strconv.Itoa(rounds) + "$")
	}
	out.Write(salt)
	out.WriteByte('$')
	// The digest's bytes go out in this order, three at a time.
	order := [][3]int{
		{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13},
		{56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41},
	}
	for _, o := range order {
		encode24(&out, digest[o[0]], digest[o[1]], digest[o[2]], 4)
	}
	encode24(&out, 0, 0, digest[63], 2)
	return out.String()
}

func repeatTo(block []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out)+len(block) <= n {
		out = append(out, block...)
	}
	return append(out, block[:n-len(out)]...)
}

func encode24(out *strings.Builder, b2, b1, b0 byte, n int) {
	w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
	for range n {
		out.WriteByte(cryptAlphabet[w&0x3f])
		w >>= 6
	}
}
