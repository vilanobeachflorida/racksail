package sigstore

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// oidSCTList is the extension holding the signed certificate timestamps a
// certificate authority embeds once certificate transparency logs promised
// to publish the certificate.
var oidSCTList = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 4, 2}

// sct is a signed certificate timestamp, as RFC 6962 encodes it.
type sct struct {
	LogID      []byte
	Timestamp  uint64
	Extensions []byte
	HashAlg    byte
	SigAlg     byte
	Signature  []byte
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || n > len(r.b) {
		r.err = errors.New("the timestamps are cut short")
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *reader) uint(n int) uint64 {
	var v uint64
	for _, c := range r.take(n) {
		v = v<<8 | uint64(c)
	}
	return v
}

func (r *reader) vector(lenBytes int) []byte { return r.take(int(r.uint(lenBytes))) }

// parseSCTs reads the timestamps embedded in a certificate.
func parseSCTs(cert *x509.Certificate) ([]sct, error) {
	var list []byte
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oidSCTList) {
			if _, err := asn1.Unmarshal(ext.Value, &list); err != nil {
				return nil, fmt.Errorf("reading the certificate's timestamps: %w", err)
			}
		}
	}
	if list == nil {
		return nil, errors.New("the certificate carries no certificate transparency timestamp")
	}
	r := &reader{b: list}
	all := &reader{b: r.vector(2)}
	var out []sct
	for len(all.b) > 0 && all.err == nil {
		one := &reader{b: all.vector(2)}
		if v := one.uint(1); v != 0 {
			continue
		}
		s := sct{LogID: one.take(32), Timestamp: one.uint(8), Extensions: one.vector(2)}
		s.HashAlg, s.SigAlg = byte(one.uint(1)), byte(one.uint(1))
		s.Signature = one.vector(2)
		if one.err != nil {
			return nil, one.err
		}
		out = append(out, s)
	}
	if r.err != nil || all.err != nil {
		return nil, errors.New("the certificate's timestamps are malformed")
	}
	return out, nil
}

type tbsCertificate struct {
	Raw                asn1.RawContent
	Version            int `asn1:"optional,explicit,default:0,tag:0"`
	SerialNumber       *big.Int
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Issuer             asn1.RawValue
	Validity           asn1.RawValue
	Subject            asn1.RawValue
	PublicKey          asn1.RawValue
	IssuerUniqueID     asn1.BitString   `asn1:"optional,tag:1"`
	SubjectUniqueID    asn1.BitString   `asn1:"optional,tag:2"`
	Extensions         []pkix.Extension `asn1:"optional,explicit,tag:3"`
}

// precertTBS is the certificate as the logs saw it: without the timestamps
// they signed.
func precertTBS(cert *x509.Certificate) ([]byte, error) {
	var tbs tbsCertificate
	if rest, err := asn1.Unmarshal(cert.RawTBSCertificate, &tbs); err != nil || len(rest) > 0 {
		return nil, errors.New("reading the certificate for its timestamps")
	}
	var kept []pkix.Extension
	for _, ext := range tbs.Extensions {
		if !ext.Id.Equal(oidSCTList) {
			kept = append(kept, ext)
		}
	}
	tbs.Extensions = kept
	tbs.Raw = nil
	return asn1.Marshal(tbs)
}

// signedData is what a log signed for a precertificate's timestamp.
func (s sct) signedData(issuerKeyHash [32]byte, tbs []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(0) // version 1
	b.WriteByte(0) // certificate_timestamp
	binary.Write(&b, binary.BigEndian, s.Timestamp)
	b.Write([]byte{0, 1}) // precert_entry
	b.Write(issuerKeyHash[:])
	b.Write([]byte{byte(len(tbs) >> 16), byte(len(tbs) >> 8), byte(len(tbs))})
	b.Write(tbs)
	binary.Write(&b, binary.BigEndian, uint16(len(s.Extensions)))
	b.Write(s.Extensions)
	return b.Bytes()
}

// verifySCTs checks that a certificate transparency log the trusted root
// lists promised to publish the certificate, issued by issuer.
func verifySCTs(root *TrustedRoot, cert, issuer *x509.Certificate) error {
	scts, err := parseSCTs(cert)
	if err != nil {
		return err
	}
	tbs, err := precertTBS(cert)
	if err != nil {
		return err
	}
	keyHash := sha256.Sum256(issuer.RawSubjectPublicKeyInfo)
	for _, s := range scts {
		log, ok := findLog(root.CTLogs, s.LogID)
		if !ok {
			continue
		}
		at := time.UnixMilli(int64(s.Timestamp))
		if !validAt(log.Start, log.End, at) {
			continue
		}
		// SHA-256 with ECDSA or RSA are what logs use.
		if s.HashAlg != 4 || (s.SigAlg != 3 && s.SigAlg != 1) {
			continue
		}
		if verifySignature(log.Key, s.signedData(keyHash, tbs), s.Signature) == nil {
			return nil
		}
	}
	return errors.New("no certificate transparency log the trusted root lists vouches for the certificate")
}
