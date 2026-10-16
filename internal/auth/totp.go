package auth

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

// TOTP settings: the ones every authenticator app supports, six digits
// every thirty seconds with SHA-1, and one step of clock drift either way.
const (
	totpPeriod = 30
	totpSkew   = 1
)

var totpOptions = hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// TOTPKey is a new authenticator app secret, with what the app needs to
// set it up.
type TOTPKey struct {
	// Secret is the base32 secret, for typing into the app by hand.
	Secret string
	// URI is the otpauth:// link the QR code holds.
	URI string
	// QR is the QR code as a data: URL of a PNG image.
	QR string
}

// NewTOTPKey makes a secret for an authenticator app. issuer names the
// panel in the app, and account the person signing in.
func NewTOTPKey(issuer, account string) (*TOTPKey, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer: issuer, AccountName: account, Period: totpPeriod, SecretSize: 20,
		Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &TOTPKey{
		Secret: key.Secret(),
		URI:    key.URL(),
		QR:     "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

// CheckTOTP checks a code from an authenticator app at time now. A code
// counts once: it must belong to a later time step than after, the last
// one used, so a code someone saw over a shoulder can't be used again. It
// returns the step the code belongs to, to keep as the new after.
func CheckTOTP(secret, code string, now time.Time, after int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	for step := current - totpSkew; step <= current+totpSkew; step++ {
		if step <= after || step < 0 {
			continue
		}
		ok, err := hotp.ValidateCustom(code, uint64(step), secret, totpOptions)
		if err == nil && ok {
			return step, true
		}
	}
	return 0, false
}

// TOTPCode returns the code for secret at time t, for tests and the
// command line's own checks.
func TOTPCode(secret string, t time.Time) (string, error) {
	code, err := hotp.GenerateCodeCustom(secret, uint64(t.Unix()/totpPeriod), totpOptions)
	if err != nil {
		return "", fmt.Errorf("auth: generating a TOTP code: %w", err)
	}
	return code, nil
}
