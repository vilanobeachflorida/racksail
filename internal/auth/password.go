package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/openwall/yescrypt-go"
	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for new password hashes: 19 MiB, two passes, one
// lane, the minimum OWASP recommends. They keep a sign-in under a tenth of
// a second on a small server while every guess costs an attacker the same
// memory.
const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Bounds on the parameters a stored hash may ask for, so a tampered record
// can't make one check take the server's memory or minutes of CPU.
const (
	maxArgonMemory = 256 * 1024
	maxArgonTime   = 10
	maxSHA512Round = 1_000_000
)

// MinPasswordLength and MaxPasswordLength bound what people choose. The
// upper bound keeps hashing cheap to start; passphrases fit easily.
const (
	MinPasswordLength = 10
	MaxPasswordLength = 256
)

// ErrHashFormat is returned for a stored hash in a format Racksail doesn't
// read.
var ErrHashFormat = errors.New("auth: unknown password hash format")

// hashing limits how many hashes are computed at once, so a burst of
// sign-ins can't take more than a bounded amount of memory.
var hashing = make(chan struct{}, 2)

func acquire(ctx context.Context) error {
	select {
	case hashing <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release() { <-hashing }

// HashPassword returns an Argon2id hash of password in the PHC string
// format, $argon2id$v=19$m=...,t=...,p=...$salt$hash.
func HashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := acquire(ctx); err != nil {
		return "", err
	}
	defer release()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks password against a stored hash. Argon2id hashes are
// what Racksail writes; SHA-512 crypt ($6$) and yescrypt ($y$) hashes are
// read too, as the installer's answers carry them for the first admin.
// rehash says the hash should be replaced with a new one of the current
// kind and strength once the password checked out.
func VerifyPassword(ctx context.Context, hash, password string) (ok, rehash bool, err error) {
	if err := acquire(ctx); err != nil {
		return false, false, err
	}
	defer release()
	switch {
	case strings.HasPrefix(hash, "$argon2id$"):
		return verifyArgon2id(hash, password)
	case strings.HasPrefix(hash, "$6$"):
		ok, err := verifySHA512Crypt(hash, password)
		return ok, true, err
	case strings.HasPrefix(hash, "$y$"):
		computed, err := yescrypt.Hash([]byte(password), []byte(hash))
		if err != nil {
			return false, false, fmt.Errorf("%w: %v", ErrHashFormat, err)
		}
		return subtle.ConstantTimeCompare(computed, []byte(hash)) == 1, true, nil
	}
	return false, false, ErrHashFormat
}

// KnownHash reports whether hash is in a format VerifyPassword reads.
func KnownHash(hash string) bool {
	for _, prefix := range []string{"$argon2id$", "$6$", "$y$j"} {
		if strings.HasPrefix(hash, prefix) {
			return true
		}
	}
	return false
}

func verifyArgon2id(hash, password string) (bool, bool, error) {
	parts := strings.Split(hash, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 {
		return false, false, ErrHashFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, ErrHashFormat
	}
	var memory, passes uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil {
		return false, false, ErrHashFormat
	}
	if memory == 0 || memory > maxArgonMemory || passes == 0 || passes > maxArgonTime || threads == 0 {
		return false, false, ErrHashFormat
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, false, ErrHashFormat
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, false, ErrHashFormat
	}
	got := argon2.IDKey([]byte(password), salt, passes, memory, threads, uint32(len(want)))
	ok := subtle.ConstantTimeCompare(got, want) == 1
	rehash := memory != argonMemory || passes != argonTime || threads != argonThreads || len(want) != argonKeyLen
	return ok, rehash, nil
}

// dummyHash is checked when nobody has the name someone signs in with, so
// the answer takes as long as for a real user. It is made the first time
// it's needed rather than whenever the binary starts.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword(context.Background(), "racksail does not use this password")
	if err != nil {
		// Only a broken random source fails here; a hash nobody can match
		// still costs the same to check.
		return "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}
	return h
})

// WasteTime spends as long as checking a password does, for sign-ins with
// a name nobody has, so timing doesn't tell which names exist.
func WasteTime(ctx context.Context, password string) {
	_, _, _ = VerifyPassword(ctx, dummyHash(), password)
}

// CheckPasswordStrength refuses passwords that are too short, too long or
// obviously weak: the same character over and over, or the username.
func CheckPasswordStrength(password, username string) error {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinPasswordLength:
		return ErrWeakPassword.New("reason", "use at least "+strconv.Itoa(MinPasswordLength)+" characters")
	case len(password) > MaxPasswordLength:
		return ErrWeakPassword.New("reason", "use at most "+strconv.Itoa(MaxPasswordLength)+" bytes")
	case !utf8.ValidString(password):
		return ErrWeakPassword.New("reason", "it isn't valid text")
	}
	distinct := map[rune]bool{}
	for _, r := range password {
		distinct[r] = true
	}
	if len(distinct) < 4 {
		return ErrWeakPassword.New("reason", "use more than a few different characters")
	}
	lower := strings.ToLower(password)
	if username != "" && strings.Contains(lower, strings.ToLower(username)) {
		return ErrWeakPassword.New("reason", "leave your username out of it")
	}
	for _, common := range commonPasswords {
		if strings.Contains(lower, common) && n < len(common)+6 {
			return ErrWeakPassword.New("reason", "it is too close to a password everyone tries")
		}
	}
	return nil
}

// commonPasswords are the stems of the passwords guessed first. A password
// built mostly from one is refused; a long passphrase that contains one is
// fine.
var commonPasswords = []string{
	"password", "passw0rd", "123456", "qwerty", "letmein", "welcome", "admin", "racksail", "iloveyou",
	"abc123", "monkey", "dragon", "football", "baseball", "sunshine", "princess", "trustno1", "changeme",
}
