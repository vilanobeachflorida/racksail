package system

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"time"
)

// Certificates installs certificates for services that read them from disk,
// such as mail and database servers. The edge keeps its own certificates.
type Certificates interface {
	// Install validates that the key matches the chain and writes both.
	Install(ctx context.Context, name string, chainPEM, keyPEM []byte, owner Owner) error
	Remove(ctx context.Context, name string) error
	Get(ctx context.Context, name string) (CertInfo, error)
	List(ctx context.Context) ([]CertInfo, error)
}

// CertInfo describes an installed certificate.
type CertInfo struct {
	Name              string    `json:"name"`
	Subject           string    `json:"subject"`
	Issuer            string    `json:"issuer"`
	DNSNames          []string  `json:"dnsNames,omitempty"`
	IPAddresses       []string  `json:"ipAddresses,omitempty"`
	NotBefore         time.Time `json:"notBefore"`
	NotAfter          time.Time `json:"notAfter"`
	SerialNumber      string    `json:"serialNumber"`
	FingerprintSHA256 string    `json:"fingerprintSha256"`
}

var certNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// ValidCertName reports whether name can name a certificate directory.
func ValidCertName(name string) bool {
	return certNamePattern.MatchString(name) && name != "." && name != ".."
}

// CertStore implements Certificates on top of any Files adapter, keeping
// each certificate in <dir>/<name>/fullchain.pem and privkey.pem.
type CertStore struct {
	files Files
	dir   string
}

// NewCertStore returns certificates stored under dir through files.
func NewCertStore(files Files, dir string) *CertStore {
	return &CertStore{files: files, dir: dir}
}

// Install implements Certificates.
func (c *CertStore) Install(ctx context.Context, name string, chainPEM, keyPEM []byte, owner Owner) error {
	if !ValidCertName(name) {
		return fmt.Errorf("certificate name %q: %w", name, ErrInvalid)
	}
	if _, err := tls.X509KeyPair(chainPEM, keyPEM); err != nil {
		return fmt.Errorf("certificate %s: key and chain don't match: %v: %w", name, err, ErrInvalid)
	}
	if err := c.ensureDir(ctx, c.dir, 0o755, Owner{}); err != nil {
		return err
	}
	dir := path.Join(c.dir, name)
	if err := c.ensureDir(ctx, dir, 0o750, Owner{Group: owner.Group}); err != nil {
		return err
	}
	if err := c.files.WriteFile(ctx, path.Join(dir, "fullchain.pem"), chainPEM, 0o644, Owner{}); err != nil {
		return err
	}
	return c.files.WriteFile(ctx, path.Join(dir, "privkey.pem"), keyPEM, 0o640, Owner{Group: owner.Group})
}

func (c *CertStore) ensureDir(ctx context.Context, dir string, perm fs.FileMode, owner Owner) error {
	err := c.files.Mkdir(ctx, dir, perm, owner)
	if !errors.Is(err, ErrExists) {
		return err
	}
	if err := c.files.Chown(ctx, dir, owner); err != nil {
		return err
	}
	return c.files.Chmod(ctx, dir, perm)
}

// Remove implements Certificates.
func (c *CertStore) Remove(ctx context.Context, name string) error {
	if !ValidCertName(name) {
		return fmt.Errorf("certificate name %q: %w", name, ErrInvalid)
	}
	return c.files.RemoveAll(ctx, path.Join(c.dir, name))
}

// Get implements Certificates.
func (c *CertStore) Get(ctx context.Context, name string) (CertInfo, error) {
	if !ValidCertName(name) {
		return CertInfo{}, fmt.Errorf("certificate name %q: %w", name, ErrInvalid)
	}
	data, err := c.files.ReadFile(ctx, path.Join(c.dir, name, "fullchain.pem"), 1<<20)
	if err != nil {
		return CertInfo{}, err
	}
	return ParseCertInfo(name, data)
}

// List implements Certificates.
func (c *CertStore) List(ctx context.Context) ([]CertInfo, error) {
	entries, err := c.files.ReadDir(ctx, c.dir)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []CertInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := c.Get(ctx, path.Base(e.Path))
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ParseCertInfo describes the first certificate of a PEM chain.
func ParseCertInfo(name string, chainPEM []byte) (CertInfo, error) {
	block, _ := pem.Decode(chainPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return CertInfo{}, fmt.Errorf("certificate %s: no PEM certificate: %w", name, ErrInvalid)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return CertInfo{}, fmt.Errorf("certificate %s: %v: %w", name, err, ErrInvalid)
	}
	sum := sha256.Sum256(cert.Raw)
	info := CertInfo{
		Name:              name,
		Subject:           cert.Subject.String(),
		Issuer:            cert.Issuer.String(),
		DNSNames:          cert.DNSNames,
		NotBefore:         cert.NotBefore.UTC(),
		NotAfter:          cert.NotAfter.UTC(),
		SerialNumber:      cert.SerialNumber.String(),
		FingerprintSHA256: hex.EncodeToString(sum[:]),
	}
	for _, ip := range cert.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	return info, nil
}
