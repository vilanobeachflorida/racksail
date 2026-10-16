package recipes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"golang.org/x/crypto/blake2b"

	"github.com/vilanobeachflorida/racksail/internal/updates"
	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore"
)

// Upstream files a recipe downloads are pinned by checksum and size, and
// may also carry the upstream's signature: a minisign or OpenPGP signature
// by a key the recipe or the panel names, or a Sigstore bundle by an
// identity.

// Signature types.
const (
	SignatureMinisign = "minisign"
	SignatureGPG      = "gpg"
	SignatureSigstore = "sigstore"
)

var vendorKeys = struct {
	sync.RWMutex
	m map[string]openpgp.EntityList
}{m: map[string]openpgp.EntityList{}}

// RegisterSigningKey adds a vendor's armored OpenPGP public key that gpg
// signatures of downloads are checked against, by name. Recipes name keys
// and never carry them. Call it from an init function.
func RegisterSigningKey(name string, armored []byte) error {
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return fmt.Errorf("recipes: signing key %s: %w", name, err)
	}
	vendorKeys.Lock()
	defer vendorKeys.Unlock()
	vendorKeys.m[name] = keys
	return nil
}

func vendorKey(name string) (openpgp.EntityList, bool) {
	vendorKeys.RLock()
	defer vendorKeys.RUnlock()
	k, ok := vendorKeys.m[name]
	return k, ok
}

// VerifiedDownload records that an upstream file matched its recipe.
type VerifiedDownload struct {
	URL        string    `json:"url" doc:"Where the file was downloaded from"`
	SHA256     string    `json:"sha256" doc:"The SHA-256 the file matched, as the recipe pins it"`
	Size       int64     `json:"size" doc:"The file's size in bytes"`
	Signature  string    `json:"signature,omitempty" doc:"The kind of signature that was checked too, if any"`
	VerifiedAt time.Time `json:"verifiedAt" doc:"When the file was checked"`
}

// maxSignature bounds signature files and bundles.
const maxSignature = 1 << 20

// Verifier checks upstream downloads.
type Verifier struct {
	HTTP *http.Client
	// Dir holds downloads while they are checked.
	Dir string
	// TrustedRoot returns Sigstore's trusted root as Racksail pins it, for
	// sigstore signatures.
	TrustedRoot func(ctx context.Context) (*sigstore.TrustedRoot, error)
	Now         func() time.Time
}

func (v *Verifier) client() *http.Client {
	if v.HTTP != nil {
		return v.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

// Verify downloads a recipe's upstream file and checks its size, checksum
// and signature. Nothing is kept.
func (v *Verifier) Verify(ctx context.Context, d Download) (VerifiedDownload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		return VerifiedDownload{}, err
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return VerifiedDownload{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return VerifiedDownload{}, fmt.Errorf("%s answered %s", d.URL, resp.Status)
	}
	if resp.ContentLength > d.Size {
		return VerifiedDownload{}, fmt.Errorf("%s is %d bytes, not %d", d.URL, resp.ContentLength, d.Size)
	}
	f, err := os.CreateTemp(v.Dir, "download-*")
	if err != nil {
		return VerifiedDownload{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	sum := sha256.New()
	pre, _ := blake2b.New512(nil)
	n, err := io.Copy(io.MultiWriter(f, sum, pre), io.LimitReader(resp.Body, d.Size+1))
	if err != nil {
		return VerifiedDownload{}, err
	}
	if n != d.Size || hex.EncodeToString(sum.Sum(nil)) != d.SHA256 {
		return VerifiedDownload{}, fmt.Errorf("%s doesn't match the checksum the recipe pins: upstream changed the file", d.URL)
	}
	out := VerifiedDownload{URL: d.URL, SHA256: d.SHA256, Size: d.Size, VerifiedAt: time.Now().UTC()}
	if v.Now != nil {
		out.VerifiedAt = v.Now().UTC()
	}
	if d.Signature == nil {
		return out, nil
	}
	sig, err := v.fetch(ctx, d.Signature.URL)
	if err != nil {
		return out, fmt.Errorf("fetching the signature of %s: %w", d.URL, err)
	}
	switch d.Signature.Type {
	case SignatureMinisign:
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return out, err
		}
		err = verifyMinisign(d.Signature.Key, sig, f, n, pre)
	case SignatureGPG:
		keys, ok := vendorKey(d.Signature.Key)
		if !ok {
			return out, fmt.Errorf("the panel knows no signing key called %q", d.Signature.Key)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return out, err
		}
		err = verifyGPG(keys, f, sig)
	case SignatureSigstore:
		err = v.verifySigstore(ctx, d.Signature.Key, sig, sum.Sum(nil))
	default:
		err = fmt.Errorf("signatures of type %q aren't supported", d.Signature.Type)
	}
	if err != nil {
		return out, fmt.Errorf("%s isn't signed as its recipe says: %w", d.URL, err)
	}
	out.Signature = d.Signature.Type
	return out, nil
}

func (v *Verifier) fetch(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", target, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSignature+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSignature {
		return nil, fmt.Errorf("%s is too large to be a signature", target)
	}
	return data, nil
}

// verifyMinisign checks a minisign signature: the signature over the
// file, or over its BLAKE2b-512 hash when prehashed, and the global
// signature over it and its trusted comment.
func verifyMinisign(keyText string, sigFile []byte, content io.Reader, size int64, prehash hash.Hash) error {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lastLine(keyText)))
	if err != nil || len(key) != 42 || string(key[:2]) != "Ed" {
		return errors.New("the recipe's minisign key isn't a minisign public key")
	}
	keyID, pub := key[2:10], ed25519.PublicKey(key[10:])
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(sigFile))
	for sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	if len(lines) < 4 || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return errors.New("the minisign signature is malformed")
	}
	sig, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(sig) != 74 {
		return errors.New("the minisign signature is malformed")
	}
	alg, sigID, signature := string(sig[:2]), sig[2:10], sig[10:]
	if !bytes.Equal(sigID, keyID) {
		return errors.New("the file is signed by another minisign key")
	}
	var msg []byte
	switch alg {
	case "ED":
		msg = prehash.Sum(nil)
	case "Ed":
		if size > 256<<20 {
			return errors.New("a file this large must be signed prehashed")
		}
		if msg, err = io.ReadAll(content); err != nil {
			return err
		}
	default:
		return fmt.Errorf("minisign algorithm %q isn't supported", alg)
	}
	if !ed25519.Verify(pub, msg, signature) {
		return errors.New("the minisign signature doesn't match")
	}
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || !ed25519.Verify(pub, append(append([]byte{}, signature...), []byte(strings.TrimPrefix(lines[2], "trusted comment: "))...), global) {
		return errors.New("the minisign trusted comment isn't signed")
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func verifyGPG(keys openpgp.EntityList, content io.Reader, sig []byte) error {
	var err error
	if bytes.Contains(sig, []byte("-----BEGIN PGP SIGNATURE-----")) {
		_, err = openpgp.CheckArmoredDetachedSignature(keys, content, bytes.NewReader(sig), nil)
	} else {
		_, err = openpgp.CheckDetachedSignature(keys, content, bytes.NewReader(sig), nil)
	}
	return err
}

// verifySigstore checks a Sigstore bundle against the trusted root Racksail
// pins. The recipe's key names the signer's identity and, after a space,
// the issuer; GitHub Actions when it names none.
func (v *Verifier) verifySigstore(ctx context.Context, key string, bundle, digest []byte) error {
	subject, issuer, _ := strings.Cut(strings.TrimSpace(key), " ")
	if subject == "" {
		return errors.New("a sigstore signature needs the signer's identity as its key")
	}
	if issuer == "" {
		issuer = updates.Issuer
	}
	if v.TrustedRoot == nil {
		return errors.New("the panel can't fetch Sigstore's trusted root")
	}
	root, err := v.TrustedRoot(ctx)
	if err != nil {
		return err
	}
	_, err = sigstore.Verify(root, bundle, digest, sigstore.Identity{Subject: subject, Issuer: strings.TrimSpace(issuer)})
	return err
}
