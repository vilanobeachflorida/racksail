package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

func TestPasswordHashes(t *testing.T) {
	ctx := context.Background()
	hash, err := HashPassword(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("hash = %s", hash)
	}
	if again, _ := HashPassword(ctx, "correct horse battery staple"); again == hash {
		t.Error("every hash gets its own salt")
	}
	ok, rehash, err := VerifyPassword(ctx, hash, "correct horse battery staple")
	if err != nil || !ok || rehash {
		t.Errorf("right password = %v, %v, %v", ok, rehash, err)
	}
	if ok, _, _ := VerifyPassword(ctx, hash, "correct horse battery stapler"); ok {
		t.Error("a wrong password passed")
	}
	// A hash made with other parameters still checks, and asks to be
	// replaced.
	weaker := "$argon2id$v=19$m=8192,t=1,p=1$c29tZXNhbHRzb21lc2FsdA$" +
		"+fmTfVv8KoNjVCKPsc8Mbv+WTDRYXpvfC6szowtd8wY"
	if _, rehash, err := VerifyPassword(ctx, weaker, "x"); err != nil || !rehash {
		t.Errorf("weaker parameters = %v, %v", rehash, err)
	}
	for _, bad := range []string{
		"", "plain", "$argon2id$v=19$m=999999999,t=1,p=1$c29tZXNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=16$m=19456,t=2,p=1$c29tZXNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "$2y$10$abc", "$6$rounds=9999999$salt$x",
	} {
		if _, _, err := VerifyPassword(ctx, bad, "x"); !errors.Is(err, ErrHashFormat) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

// The installer's answers carry crypt(3) hashes, which are checked with the
// vectors their specifications publish.
func TestCryptHashes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ hash, password string }{
		{"$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1", "Hello world!"},
		{"$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v.", "Hello world!"},
		{"$6$rounds=5000$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQzQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0", "This is just a test"},
		{"$y$j9T$AAt9R641xPvCI9nXw1HHW/$cuQRBMN3N/f8IcmVN.4YrZ1bHMOiLOoz9/XQMKV/v0A", "openwall"},
		{"$y$j9T$e8R9q85ZuzUkArEUurdtS.$esON.7y6H.u3UCPVCpbRFueRpAut2n2cMf1EhpjbuiC", "pleaseletmein"},
	} {
		ok, rehash, err := VerifyPassword(ctx, tc.hash, tc.password)
		if err != nil || !ok || !rehash {
			t.Errorf("%s = %v, %v, %v", tc.hash[:10], ok, rehash, err)
		}
		if ok, _, _ := VerifyPassword(ctx, tc.hash, tc.password+"!"); ok {
			t.Errorf("%s accepted a wrong password", tc.hash[:10])
		}
		if !KnownHash(tc.hash) {
			t.Errorf("%s isn't known", tc.hash[:10])
		}
	}
}

func TestPasswordStrength(t *testing.T) {
	for _, tc := range []struct {
		password string
		ok       bool
	}{
		{"short", false},
		{"aaaaaaaaaaaaaaa", false},
		{"Password123", false},
		{"alice-is-great-2024", false},
		{"tangerine kettle orbit", true},
		{"my password is long enough to pass", true},
		{strings.Repeat("ab12", 70), false},
	} {
		err := CheckPasswordStrength(tc.password, "alice")
		if (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.password, err)
		}
		if err != nil && !ErrWeakPassword.Is(err) {
			t.Errorf("%q: %v isn't auth.weak_password", tc.password, err)
		}
	}
}

func TestTOTP(t *testing.T) {
	key, err := NewTOTPKey("Racksail", "alice@panel.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key.URI, "otpauth://totp/Racksail:alice@panel.example?") || !strings.HasPrefix(key.QR, "data:image/png;base64,") || len(key.Secret) < 32 {
		t.Errorf("key = %+v", key)
	}
	now := time.Unix(1_800_000_015, 0)
	code, err := TOTPCode(key.Secret, now)
	if err != nil {
		t.Fatal(err)
	}
	step, ok := CheckTOTP(key.Secret, code, now, 0)
	if !ok || step != now.Unix()/30 {
		t.Fatalf("current code = %d, %v", step, ok)
	}
	if _, ok := CheckTOTP(key.Secret, code, now, step); ok {
		t.Error("a code counts once")
	}
	if _, ok := CheckTOTP(key.Secret, code, now.Add(25*time.Second), 0); !ok {
		t.Error("one step of drift is allowed")
	}
	if _, ok := CheckTOTP(key.Secret, code, now.Add(2*time.Minute), 0); ok {
		t.Error("an old code passed")
	}
	if _, ok := CheckTOTP(key.Secret, "12345", now, 0); ok {
		t.Error("a short code passed")
	}
	spaced := code[:3] + " " + code[3:]
	if _, ok := CheckTOTP(key.Secret, spaced, now, 0); !ok {
		t.Error("spaces in a code don't matter")
	}
}

func TestSecretsAndCodes(t *testing.T) {
	a, b := NewSecret(TokenPrefix), NewSecret(TokenPrefix)
	if a == b || !strings.HasPrefix(a, "rsk_") || len(a) != 4+43 {
		t.Errorf("secrets = %s, %s", a, b)
	}
	if HashSecret(a) == HashSecret(b) || len(HashSecret(a)) != 64 || !SameHash(HashSecret(a), HashSecret(a)) {
		t.Error("hashes")
	}
	code := NewCode(4)
	if len(code) != 23 || strings.Count(code, "-") != 3 || strings.ContainsAny(code, "01OILU") {
		t.Errorf("code = %s", code)
	}
	if HashCode(strings.ToLower(strings.ReplaceAll(code, "-", " "))) != HashCode(code) {
		t.Error("codes match whatever their case and separators")
	}
	codes := NewRecoveryCodes()
	seen := map[string]bool{}
	for _, c := range codes {
		seen[c] = true
	}
	if len(codes) != RecoveryCodeCount || len(seen) != RecoveryCodeCount {
		t.Errorf("recovery codes = %v", codes)
	}
}

func TestErrorsAreDefined(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatal(err)
	}
	if err := i18n.Err(); err != nil {
		t.Fatal(err)
	}
}
