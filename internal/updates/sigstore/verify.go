package sigstore

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"time"
)

// Identity is who must have signed: the identity in the signing
// certificate, such as a workflow URI or an email address, and the OpenID
// Connect issuer that vouched for it.
type Identity struct {
	Subject string
	Issuer  string
}

// Result is what a good signature tells.
type Result struct {
	// SignedAt is the earliest time a log or timestamp authority vouches
	// for.
	SignedAt    time.Time
	Certificate *x509.Certificate
	// LogIndex is the signature's index in the transparency log.
	LogIndex int64
}

// The issuer extensions Fulcio writes: the first one raw, its successor as
// a DER string.
var (
	oidIssuerV1 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	oidIssuerV2 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
)

// Verify checks that a bundle signs the file whose SHA-256 is digest, as
// identity, against the trusted root.
func Verify(root *TrustedRoot, bundle, digest []byte, id Identity) (*Result, error) {
	if root == nil {
		return nil, errors.New("there is no trusted root to check the signature against")
	}
	if len(digest) != 32 {
		return nil, errors.New("the file's digest isn't a SHA-256")
	}
	b, err := ParseBundle(bundle)
	if err != nil {
		return nil, err
	}
	if b.Digest != nil && !bytes.Equal(b.Digest, digest) {
		return nil, errors.New("the bundle signs another file")
	}
	cert := b.Certificate
	if err := verifyFileSignature(cert.PublicKey, digest, b.Signature); err != nil {
		return nil, fmt.Errorf("the signature doesn't match the file: %w", err)
	}
	var times []time.Time
	res := &Result{Certificate: cert, LogIndex: -1}
	for _, e := range b.Entries {
		at, err := verifyEntry(root, e, digest, b.Signature, cert)
		if err != nil {
			return nil, fmt.Errorf("the transparency log entry: %w", err)
		}
		if res.LogIndex < 0 {
			res.LogIndex = e.LogIndex
		}
		if !at.IsZero() {
			times = append(times, at)
		}
	}
	if len(b.Entries) == 0 {
		return nil, errors.New("the signature was never logged in a transparency log")
	}
	for _, ts := range b.Timestamps {
		at, err := verifyTimestamp(root, ts, b.Signature)
		if err != nil {
			return nil, fmt.Errorf("the timestamp: %w", err)
		}
		times = append(times, at)
	}
	if len(times) == 0 {
		return nil, errors.New("neither a log nor a timestamp authority vouches for when the file was signed")
	}
	var issuer *x509.Certificate
	for _, at := range times {
		chain, err := chainAt(root, cert, at)
		if err != nil {
			return nil, err
		}
		issuer = chain[1]
		if res.SignedAt.IsZero() || at.Before(res.SignedAt) {
			res.SignedAt = at
		}
	}
	if err := verifySCTs(root, cert, issuer); err != nil {
		return nil, err
	}
	if err := checkIdentity(cert, id); err != nil {
		return nil, err
	}
	return res, nil
}

// chainAt finds the certificate authority the certificate chains to at a
// time.
func chainAt(root *TrustedRoot, cert *x509.Certificate, at time.Time) ([]*x509.Certificate, error) {
	for _, ca := range root.CertificateAuthorities {
		if !validAt(ca.Start, ca.End, at) {
			continue
		}
		roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
		roots.AddCert(ca.Chain[len(ca.Chain)-1])
		for _, c := range ca.Chain[:len(ca.Chain)-1] {
			intermediates.AddCert(c)
		}
		chains, err := cert.Verify(x509.VerifyOptions{
			Roots: roots, Intermediates: intermediates, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		})
		if err == nil && len(chains[0]) > 1 {
			return chains[0], nil
		}
	}
	return nil, fmt.Errorf("the signing certificate wasn't issued by a certificate authority the trusted root lists, or wasn't valid at %s", at.UTC().Format(time.RFC3339))
}

// checkIdentity compares the certificate's identity and issuer with what
// is expected.
func checkIdentity(cert *x509.Certificate, id Identity) error {
	var names []string
	for _, u := range cert.URIs {
		names = append(names, u.String())
	}
	names = append(names, cert.EmailAddresses...)
	found := false
	for _, n := range names {
		found = found || n == id.Subject
	}
	if !found {
		return fmt.Errorf("the file was signed by %v, not %s", names, id.Subject)
	}
	var issuer string
	for _, ext := range cert.Extensions {
		switch {
		case ext.Id.Equal(oidIssuerV2):
			var s string
			if _, err := asn1.UnmarshalWithParams(ext.Value, &s, "utf8"); err == nil {
				issuer = s
			}
		case ext.Id.Equal(oidIssuerV1) && issuer == "":
			issuer = string(ext.Value)
		}
	}
	if issuer != id.Issuer {
		return fmt.Errorf("the signer's identity was vouched for by %q, not %s", issuer, id.Issuer)
	}
	return nil
}

func verifyFileSignature(key crypto.PublicKey, digest, sig []byte) error {
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(k, digest, sig) {
			return errors.New("the signature doesn't match")
		}
		return nil
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest, sig)
	}
	return fmt.Errorf("signatures by %T keys aren't supported", key)
}
