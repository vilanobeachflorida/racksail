package system

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// ArchiveSignature verifies a vendor archive with the vendor's OpenPGP key:
// either a detached signature of the archive itself, or a signed list of
// checksums that includes the archive, as many vendors publish
// (SHASUMS256.txt and SHASUMS256.txt.sig).
type ArchiveSignature struct {
	// Key names a signing key registered in the binary. The agent resolves
	// it into PublicKey; the panel can only name keys, never supply them.
	Key string `json:"key" doc:"Name of a vendor signing key built into Racksail"`
	// URL is the detached signature, armored or binary.
	URL string `json:"url" doc:"Detached OpenPGP signature"`
	// Checksums is the signed list of SHA-256 checksums. When it is empty,
	// the signature covers the archive itself.
	Checksums string `json:"checksums,omitempty" doc:"Signed list of SHA-256 checksums that includes the archive"`
	// Name is the archive's name in the checksums list; the last element of
	// the archive's URL when empty.
	Name string `json:"name,omitempty" doc:"The archive's name in the checksums list"`
	// PublicKey is the armored public key, filled in by the agent.
	PublicKey []byte `json:"-"`
}

// Size limits for signature downloads.
const (
	MaxSignatureBytes = 64 << 10
	MaxChecksumsBytes = 4 << 20
)

// VerifySignature checks a detached OpenPGP signature, armored or binary,
// of signed against an armored public key ring.
func VerifySignature(publicKey []byte, signed io.Reader, signature []byte) error {
	keyring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(publicKey))
	if err != nil {
		return fmt.Errorf("reading the signing key: %v: %w", err, ErrInvalid)
	}
	if bytes.Contains(signature, []byte("-----BEGIN PGP SIGNATURE-----")) {
		_, err = openpgp.CheckArmoredDetachedSignature(keyring, signed, bytes.NewReader(signature), nil)
	} else {
		_, err = openpgp.CheckDetachedSignature(keyring, signed, bytes.NewReader(signature), nil)
	}
	if err != nil {
		return fmt.Errorf("the signature doesn't verify: %v: %w", err, ErrInvalid)
	}
	return nil
}

var (
	gnuSumLine = regexp.MustCompile(`^([0-9a-fA-F]{64})\s+\*?(.+)$`)
	bsdSumLine = regexp.MustCompile(`^SHA256 \((.+)\) = ([0-9a-fA-F]{64})$`)
)

// ChecksumFor finds the SHA-256 of name in a checksums list in the GNU
// (sha256sum) or BSD format. Names are compared by their last element, so
// lists that give paths still match.
func ChecksumFor(list []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(list))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	found := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		var sum, file string
		if m := gnuSumLine.FindStringSubmatch(line); m != nil {
			sum, file = m[1], m[2]
		} else if m := bsdSumLine.FindStringSubmatch(line); m != nil {
			file, sum = m[1], m[2]
		} else {
			continue
		}
		file = strings.TrimPrefix(strings.TrimSpace(file), "./")
		if file != name && !strings.HasSuffix(file, "/"+name) {
			continue
		}
		sum = strings.ToLower(sum)
		if found != "" && found != sum {
			return "", fmt.Errorf("the checksums list gives %s two different checksums: %w", name, ErrInvalid)
		}
		found = sum
	}
	if found == "" {
		return "", fmt.Errorf("the checksums list doesn't include %s: %w", name, ErrNotFound)
	}
	return found, nil
}
