package agent

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Servers prove who they are to each other with certificates from a CA the
// panel keeps. A certificate's identity is a URI in its subject
// alternative names: racksail://panel for the panel and
// racksail://agent/<server> for an agent. Names and addresses play no part,
// so servers can be reached by any address.

// Roles in a certificate's identity.
const (
	RolePanel = "panel"
	RoleAgent = "agent"
)

// Certificate lifetimes.
const (
	CAValidity   = 20 * 365 * 24 * time.Hour
	CertValidity = 365 * 24 * time.Hour
	// RenewBefore is how long before expiry a certificate is renewed.
	RenewBefore = 30 * 24 * time.Hour
)

// Identity is who a certificate belongs to.
type Identity struct {
	Role string `json:"role"`
	// Server names an agent's server; empty for the panel.
	Server string `json:"server,omitempty"`
}

var serverNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Validate checks an identity.
func (id Identity) Validate() error {
	switch id.Role {
	case RolePanel:
		if id.Server != "" {
			return errors.New("the panel's identity names no server")
		}
	case RoleAgent:
		if !serverNamePattern.MatchString(id.Server) {
			return fmt.Errorf("server name %q must be lowercase letters, digits, '-' or '_'", id.Server)
		}
	default:
		return fmt.Errorf("role %q", id.Role)
	}
	return nil
}

// URI renders the identity as it appears in a certificate.
func (id Identity) URI() *url.URL {
	if id.Role == RolePanel {
		return &url.URL{Scheme: "racksail", Host: RolePanel}
	}
	return &url.URL{Scheme: "racksail", Host: RoleAgent, Path: "/" + id.Server}
}

// String renders the identity for logs and the audit trail.
func (id Identity) String() string { return id.URI().String() }

// IdentityOf reads the identity of a certificate.
func IdentityOf(cert *x509.Certificate) (Identity, error) {
	var found []Identity
	for _, u := range cert.URIs {
		if u.Scheme != "racksail" {
			continue
		}
		id := Identity{Role: u.Host, Server: strings.TrimPrefix(u.Path, "/")}
		if err := id.Validate(); err != nil {
			return Identity{}, fmt.Errorf("certificate identity %s: %w", u, err)
		}
		found = append(found, id)
	}
	if len(found) != 1 {
		return Identity{}, fmt.Errorf("the certificate has %d Racksail identities, not one", len(found))
	}
	return found[0], nil
}

// CA issues the certificates servers identify each other with.
type CA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     crypto.Signer
}

// NewCA makes a CA and returns it with its private key in PEM, for the
// panel to keep with its secrets.
func NewCA(name string, now time.Time) (*CA, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name, Organization: []string{"Racksail"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := marshalKey(key)
	if err != nil {
		return nil, nil, err
	}
	ca, err := LoadCA(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM)
	return ca, keyPEM, err
}

// LoadCA reads a CA from PEM.
func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("reading the CA: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	if !cert.IsCA {
		return nil, errors.New("the certificate isn't a CA")
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("the CA's key can't sign")
	}
	return &CA{cert: cert, certPEM: certPEM, key: signer}, nil
}

// CertPEM returns the CA certificate.
func (c *CA) CertPEM() []byte { return c.certPEM }

// Fingerprint returns the SHA-256 of the CA certificate in hex, which a
// joining server pins.
func (c *CA) Fingerprint() string { return Fingerprint(c.cert) }

// Fingerprint returns the SHA-256 of a certificate in hex.
func Fingerprint(cert *x509.Certificate) string {
	s := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(s[:])
}

// Pool returns a pool holding the CA.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// Issue signs a certificate for id over a public key.
func (c *CA) Issue(id Identity, pub crypto.PublicKey, now time.Time) ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	notAfter := now.Add(CertValidity)
	if notAfter.After(c.cert.NotAfter) {
		notAfter = c.cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: id.String(), Organization: []string{"Racksail"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// Either side may open the connection, so every certificate works
		// as client and as server.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		URIs:        []*url.URL{id.URI()},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, pub, c.key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// SignCSR checks a certificate request's signature and issues a certificate
// for id over its key. What the request asks for is ignored: the panel
// decides the identity.
func (c *CA) SignCSR(csrPEM []byte, id Identity, now time.Time) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("no PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("the certificate request isn't signed by its key: %w", err)
	}
	return c.Issue(id, csr.PublicKey, now)
}

// Verify checks that a certificate chains to the CA, is valid now and
// carries an identity, and returns that identity.
func (c *CA) Verify(cert *x509.Certificate, now time.Time) (Identity, error) {
	return verifyIdentity(c.Pool(), cert, nil, now)
}

func verifyIdentity(pool *x509.CertPool, leaf *x509.Certificate, intermediates []*x509.Certificate, now time.Time) (Identity, error) {
	inter := x509.NewCertPool()
	for _, c := range intermediates {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: pool, Intermediates: inter, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return Identity{}, err
	}
	return IdentityOf(leaf)
}

// NewKeyAndCSR makes a key and a certificate request for it, as a joining
// or renewing server sends. The name is informational; the panel decides
// the identity.
func NewKeyAndCSR(name string) (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: name},
	}, key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = marshalKey(key)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

func marshalKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// Credentials are what a server needs to prove itself: its certificate and
// key, and the CA it trusts.
type Credentials struct {
	CertPEM []byte `json:"cert"`
	KeyPEM  []byte `json:"key"`
	CAPEM   []byte `json:"ca"`
}

// TLSCertificate returns the certificate and key for a tls.Config.
func (c Credentials) TLSCertificate() (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	return cert, err
}

// Pool returns a pool holding the trusted CA.
func (c Credentials) Pool() (*x509.CertPool, error) {
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(c.CAPEM) {
		return nil, errors.New("no CA certificate in the credentials")
	}
	return p, nil
}

// Identity returns the identity of the credentials' certificate.
func (c Credentials) Identity() (Identity, error) {
	cert, err := c.TLSCertificate()
	if err != nil {
		return Identity{}, err
	}
	return IdentityOf(cert.Leaf)
}

// NeedsRenewal reports whether the certificate expires within RenewBefore.
func (c Credentials) NeedsRenewal(now time.Time) bool {
	cert, err := c.TLSCertificate()
	return err != nil || now.Add(RenewBefore).After(cert.Leaf.NotAfter)
}

// verifyPeer returns a VerifyConnection function that accepts only a peer
// whose certificate chains to pool and holds the wanted role, and, when
// server isn't empty, names that server.
func verifyPeer(pool *x509.CertPool, role, server string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("the peer presented no certificate")
		}
		id, err := verifyIdentity(pool, cs.PeerCertificates[0], cs.PeerCertificates[1:], time.Now())
		if err != nil {
			return err
		}
		if id.Role != role || (server != "" && id.Server != server) {
			return fmt.Errorf("the peer is %s, not the expected %s", id, Identity{Role: role, Server: server})
		}
		return nil
	}
}

// ServerTLSConfig is the agent's configuration for direct connections: it
// presents its certificate and accepts only the panel's.
func ServerTLSConfig(creds Credentials) (*tls.Config, error) {
	cert, err := creds.TLSCertificate()
	if err != nil {
		return nil, err
	}
	pool, err := creds.Pool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:       tls.VersionTLS13,
		Certificates:     []tls.Certificate{cert},
		ClientAuth:       tls.RequireAnyClientCert,
		VerifyConnection: verifyPeer(pool, RolePanel, ""),
		NextProtos:       []string{"http/1.1"},
	}, nil
}

// ClientTLSConfig is the panel's configuration for calling an agent
// directly: it presents the panel's certificate and accepts only the agent
// of server.
func ClientTLSConfig(creds Credentials, server string) (*tls.Config, error) {
	cert, err := creds.TLSCertificate()
	if err != nil {
		return nil, err
	}
	pool, err := creds.Pool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		// The identity in the certificate is checked instead of a host
		// name, so the agent can be reached at any address.
		InsecureSkipVerify: true,
		VerifyConnection:   verifyPeer(pool, RoleAgent, server),
		NextProtos:         []string{"http/1.1"},
	}, nil
}
