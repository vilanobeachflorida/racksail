// Package sigstore checks Sigstore signatures of files the way cosign
// verify-blob does, with nothing but the standard library: the signing
// certificate must chain to a certificate authority of a pinned trusted
// root, carry a certificate transparency timestamp, name the expected
// identity and issuer, and have been logged in a transparency log, with a
// time a log or timestamp authority vouches for, at which the certificate
// was valid.
//
// Bundles are the JSON form cosign sign-blob --bundle writes, versions
// 0.1 to 0.3, logged in Rekor v1 or v2.
package sigstore

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TrustedRoot is what signatures are checked against: Sigstore's
// certificate authorities, transparency logs, certificate transparency logs
// and timestamp authorities, each with the time it was trusted for.
type TrustedRoot struct {
	CertificateAuthorities []Authority
	TimestampAuthorities   []Authority
	TransparencyLogs       []Log
	CTLogs                 []Log
}

// Authority is a certificate authority or a timestamp authority: its chain,
// the issuing certificate first and the root last.
type Authority struct {
	URI        string
	Chain      []*x509.Certificate
	Start, End time.Time
}

// Log is a transparency log or a certificate transparency log.
type Log struct {
	BaseURL string
	// KeyID is the log's ID: the SHA-256 of its public key.
	KeyID      []byte
	Key        crypto.PublicKey
	Start, End time.Time
}

// validAt reports whether t falls in [start, end); a zero end is open.
func validAt(start, end, t time.Time) bool {
	return !t.Before(start) && (end.IsZero() || t.Before(end))
}

type jsonTime struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type jsonKey struct {
	RawBytes   string   `json:"rawBytes"`
	KeyDetails string   `json:"keyDetails"`
	ValidFor   jsonTime `json:"validFor"`
}

type jsonLog struct {
	BaseURL       string  `json:"baseUrl"`
	HashAlgorithm string  `json:"hashAlgorithm"`
	PublicKey     jsonKey `json:"publicKey"`
	LogID         struct {
		KeyID string `json:"keyId"`
	} `json:"logId"`
}

type jsonAuthority struct {
	URI       string `json:"uri"`
	CertChain struct {
		Certificates []struct {
			RawBytes string `json:"rawBytes"`
		} `json:"certificates"`
	} `json:"certChain"`
	ValidFor jsonTime `json:"validFor"`
}

type jsonRoot struct {
	MediaType              string          `json:"mediaType"`
	Tlogs                  []jsonLog       `json:"tlogs"`
	CertificateAuthorities []jsonAuthority `json:"certificateAuthorities"`
	Ctlogs                 []jsonLog       `json:"ctlogs"`
	TimestampAuthorities   []jsonAuthority `json:"timestampAuthorities"`
}

// ParseTrustedRoot reads a trusted root in the JSON form Sigstore's TUF
// repository publishes as trusted_root.json.
func ParseTrustedRoot(data []byte) (*TrustedRoot, error) {
	var raw jsonRoot
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("reading the trusted root: %w", err)
	}
	if !bytes.HasPrefix([]byte(raw.MediaType), []byte("application/vnd.dev.sigstore.trustedroot")) {
		return nil, fmt.Errorf("%q isn't a Sigstore trusted root", raw.MediaType)
	}
	root := &TrustedRoot{}
	var err error
	if root.TransparencyLogs, err = parseLogs(raw.Tlogs); err != nil {
		return nil, fmt.Errorf("transparency logs: %w", err)
	}
	if root.CTLogs, err = parseLogs(raw.Ctlogs); err != nil {
		return nil, fmt.Errorf("certificate transparency logs: %w", err)
	}
	if root.CertificateAuthorities, err = parseAuthorities(raw.CertificateAuthorities); err != nil {
		return nil, fmt.Errorf("certificate authorities: %w", err)
	}
	if root.TimestampAuthorities, err = parseAuthorities(raw.TimestampAuthorities); err != nil {
		return nil, fmt.Errorf("timestamp authorities: %w", err)
	}
	if len(root.CertificateAuthorities) == 0 || len(root.TransparencyLogs) == 0 {
		return nil, errors.New("the trusted root has no certificate authority or no transparency log")
	}
	return root, nil
}

func parseLogs(in []jsonLog) ([]Log, error) {
	var out []Log
	for _, l := range in {
		der, err := base64.StdEncoding.DecodeString(l.PublicKey.RawBytes)
		if err != nil {
			return nil, fmt.Errorf("the key of %s isn't base64", l.BaseURL)
		}
		key, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return nil, fmt.Errorf("the key of %s: %w", l.BaseURL, err)
		}
		id, err := base64.StdEncoding.DecodeString(l.LogID.KeyID)
		if err != nil || len(id) == 0 {
			return nil, fmt.Errorf("the log ID of %s isn't base64", l.BaseURL)
		}
		out = append(out, Log{BaseURL: l.BaseURL, KeyID: id, Key: key, Start: l.PublicKey.ValidFor.Start, End: l.PublicKey.ValidFor.End})
	}
	return out, nil
}

func parseAuthorities(in []jsonAuthority) ([]Authority, error) {
	var out []Authority
	for _, a := range in {
		auth := Authority{URI: a.URI, Start: a.ValidFor.Start, End: a.ValidFor.End}
		for _, c := range a.CertChain.Certificates {
			der, err := base64.StdEncoding.DecodeString(c.RawBytes)
			if err != nil {
				return nil, fmt.Errorf("a certificate of %s isn't base64", a.URI)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				return nil, fmt.Errorf("a certificate of %s: %w", a.URI, err)
			}
			auth.Chain = append(auth.Chain, cert)
		}
		if len(auth.Chain) == 0 {
			return nil, fmt.Errorf("%s has no certificates", a.URI)
		}
		out = append(out, auth)
	}
	return out, nil
}

// log returns the log with the given ID.
func findLog(logs []Log, id []byte) (Log, bool) {
	for _, l := range logs {
		if bytes.Equal(l.KeyID, id) {
			return l, true
		}
	}
	return Log{}, false
}
