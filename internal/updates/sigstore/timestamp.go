package sigstore

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// RFC 3161 timestamps arrive as CMS signed data, which the standard
// library doesn't read; these are the parts of it a timestamp uses.
var (
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidTSTInfo       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}
)

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,tag:0"`
}

type timeStampResp struct {
	Status         asn1.RawValue
	TimeStampToken contentInfo `asn1:"optional"`
}

type encapsulatedContent struct {
	EContentType asn1.ObjectIdentifier
	EContent     asn1.RawValue `asn1:"explicit,optional,tag:0"`
}

type signerInfo struct {
	Version            int
	SID                asn1.RawValue
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
	UnsignedAttrs      asn1.RawValue `asn1:"optional,tag:1"`
}

type signedData struct {
	Version          int
	DigestAlgorithms asn1.RawValue
	EncapContentInfo encapsulatedContent
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      []signerInfo  `asn1:"set"`
}

type messageImprint struct {
	HashAlgorithm pkix.AlgorithmIdentifier
	HashedMessage []byte
}

type accuracy struct {
	Seconds int `asn1:"optional"`
	Millis  int `asn1:"optional,tag:0"`
	Micros  int `asn1:"optional,tag:1"`
}

type tstInfo struct {
	Version        int
	Policy         asn1.ObjectIdentifier
	MessageImprint messageImprint
	SerialNumber   *big.Int
	GenTime        time.Time     `asn1:"generalized"`
	Accuracy       accuracy      `asn1:"optional"`
	Ordering       bool          `asn1:"optional,default:false"`
	Nonce          *big.Int      `asn1:"optional"`
	TSA            asn1.RawValue `asn1:"optional,explicit,tag:0"`
	Extensions     asn1.RawValue `asn1:"optional,tag:1"`
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

func hashOf(oid asn1.ObjectIdentifier) (crypto.Hash, error) {
	switch {
	case oid.Equal(oidSHA256):
		return crypto.SHA256, nil
	case oid.Equal(oidSHA384):
		return crypto.SHA384, nil
	case oid.Equal(oidSHA512):
		return crypto.SHA512, nil
	}
	return 0, fmt.Errorf("the hash %s isn't supported", oid)
}

func digestWith(h crypto.Hash, data []byte) []byte {
	w := h.New()
	w.Write(data)
	return w.Sum(nil)
}

// parseToken reads a timestamp response, or a bare timestamp token.
func parseToken(der []byte) (*signedData, error) {
	var ci contentInfo
	var resp timeStampResp
	if rest, err := asn1.Unmarshal(der, &resp); err == nil && len(rest) == 0 && resp.TimeStampToken.ContentType != nil {
		ci = resp.TimeStampToken
	} else if rest, err := asn1.Unmarshal(der, &ci); err != nil || len(rest) > 0 {
		return nil, errors.New("the timestamp isn't a timestamp response or token")
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return nil, errors.New("the timestamp isn't signed data")
	}
	var sd signedData
	if rest, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil || len(rest) > 0 {
		return nil, errors.New("reading the timestamp's signed data")
	}
	if !sd.EncapContentInfo.EContentType.Equal(oidTSTInfo) || len(sd.SignerInfos) != 1 {
		return nil, errors.New("the timestamp doesn't hold one signed timestamp")
	}
	return &sd, nil
}

// verifyTimestamp checks an RFC 3161 timestamp over sig against the
// timestamp authorities of the trusted root and returns the time it
// vouches for.
func verifyTimestamp(root *TrustedRoot, der, sig []byte) (time.Time, error) {
	sd, err := parseToken(der)
	if err != nil {
		return time.Time{}, err
	}
	var content []byte
	if _, err := asn1.Unmarshal(sd.EncapContentInfo.EContent.Bytes, &content); err != nil {
		return time.Time{}, errors.New("reading the timestamp's content")
	}
	var info tstInfo
	if rest, err := asn1.Unmarshal(content, &info); err != nil || len(rest) > 0 {
		return time.Time{}, errors.New("reading the timestamp")
	}
	h, err := hashOf(info.MessageImprint.HashAlgorithm.Algorithm)
	if err != nil {
		return time.Time{}, err
	}
	if !bytes.Equal(info.MessageImprint.HashedMessage, digestWith(h, sig)) {
		return time.Time{}, errors.New("the timestamp is for another signature")
	}
	si := sd.SignerInfos[0]
	if len(si.SignedAttrs.FullBytes) == 0 {
		return time.Time{}, errors.New("the timestamp's signature covers no attributes")
	}
	dh, err := hashOf(si.DigestAlgorithm.Algorithm)
	if err != nil {
		return time.Time{}, err
	}
	// The signature is over the attributes as a SET, not as the [0] they
	// are tagged with in place.
	signed := append([]byte{0x31}, si.SignedAttrs.FullBytes[1:]...)
	var attrs []attribute
	if _, err := asn1.UnmarshalWithParams(signed, &attrs, "set"); err != nil {
		return time.Time{}, errors.New("reading the timestamp's signed attributes")
	}
	var typed, digested bool
	for _, a := range attrs {
		switch {
		case a.Type.Equal(oidContentType):
			var ct asn1.ObjectIdentifier
			if _, err := asn1.Unmarshal(a.Values.Bytes, &ct); err == nil && ct.Equal(oidTSTInfo) {
				typed = true
			}
		case a.Type.Equal(oidMessageDigest):
			var d []byte
			if _, err := asn1.Unmarshal(a.Values.Bytes, &d); err == nil && bytes.Equal(d, digestWith(dh, content)) {
				digested = true
			}
		}
	}
	if !typed || !digested {
		return time.Time{}, errors.New("the timestamp's signature doesn't cover its content")
	}
	at := info.GenTime
	for _, tsa := range root.TimestampAuthorities {
		if !validAt(tsa.Start, tsa.End, at) {
			continue
		}
		leaf := tsa.Chain[0]
		if at.Before(leaf.NotBefore) || at.After(leaf.NotAfter) {
			continue
		}
		if !hasUsage(leaf, x509.ExtKeyUsageTimeStamping) {
			continue
		}
		if checkTSASignature(leaf.PublicKey, dh, signed, si.Signature) != nil {
			continue
		}
		if err := verifyChain(tsa, leaf, at, x509.ExtKeyUsageTimeStamping); err != nil {
			continue
		}
		return at, nil
	}
	return time.Time{}, errors.New("no timestamp authority the trusted root lists signed the timestamp")
}

func checkTSASignature(key crypto.PublicKey, h crypto.Hash, msg, sig []byte) error {
	digest := digestWith(h, msg)
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(k, digest, sig) {
			return errors.New("the signature doesn't match")
		}
		return nil
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, h, digest, sig)
	}
	return fmt.Errorf("keys of type %T aren't supported", key)
}

func hasUsage(cert *x509.Certificate, usage x509.ExtKeyUsage) bool {
	for _, u := range cert.ExtKeyUsage {
		if u == usage {
			return true
		}
	}
	return false
}

// verifyChain checks that leaf chains to the authority's root at a time.
func verifyChain(a Authority, leaf *x509.Certificate, at time.Time, usage x509.ExtKeyUsage) error {
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	last := a.Chain[len(a.Chain)-1]
	roots.AddCert(last)
	for _, c := range a.Chain[:len(a.Chain)-1] {
		intermediates.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{usage}})
	return err
}
