package sigstore

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Bundle is a signature with what proves it: the signing certificate, the
// transparency log entries that recorded it and timestamps.
type Bundle struct {
	MediaType   string
	Certificate *x509.Certificate
	// Signature signs the file; Digest is the file's SHA-256 as the bundle
	// states it, when it does.
	Signature []byte
	Digest    []byte
	Entries   []Entry
	// Timestamps are RFC 3161 timestamp responses over the signature.
	Timestamps [][]byte
}

// Entry is a transparency log entry.
type Entry struct {
	LogIndex       int64
	LogID          []byte
	Kind, Version  string
	IntegratedTime int64
	// SET is the log's signed promise to include the entry, Rekor v1 only.
	SET []byte
	// Body is the entry as the log canonicalized it.
	Body  []byte
	Proof *InclusionProof
}

// InclusionProof proves an entry is in a log's tree of TreeSize entries
// with root RootHash.
type InclusionProof struct {
	LogIndex   int64
	TreeSize   int64
	RootHash   []byte
	Hashes     [][]byte
	Checkpoint string
}

type jsonBundle struct {
	MediaType            string `json:"mediaType"`
	VerificationMaterial struct {
		Certificate *struct {
			RawBytes string `json:"rawBytes"`
		} `json:"certificate"`
		X509CertificateChain *struct {
			Certificates []struct {
				RawBytes string `json:"rawBytes"`
			} `json:"certificates"`
		} `json:"x509CertificateChain"`
		PublicKey *struct {
			Hint string `json:"hint"`
		} `json:"publicKey"`
		TlogEntries []struct {
			LogIndex jsonInt `json:"logIndex"`
			LogID    struct {
				KeyID string `json:"keyId"`
			} `json:"logId"`
			KindVersion struct {
				Kind    string `json:"kind"`
				Version string `json:"version"`
			} `json:"kindVersion"`
			IntegratedTime   jsonInt `json:"integratedTime"`
			InclusionPromise *struct {
				SignedEntryTimestamp string `json:"signedEntryTimestamp"`
			} `json:"inclusionPromise"`
			InclusionProof *struct {
				LogIndex   jsonInt  `json:"logIndex"`
				RootHash   string   `json:"rootHash"`
				TreeSize   jsonInt  `json:"treeSize"`
				Hashes     []string `json:"hashes"`
				Checkpoint struct {
					Envelope string `json:"envelope"`
				} `json:"checkpoint"`
			} `json:"inclusionProof"`
			CanonicalizedBody string `json:"canonicalizedBody"`
		} `json:"tlogEntries"`
		TimestampVerificationData *struct {
			RFC3161Timestamps []struct {
				SignedTimestamp string `json:"signedTimestamp"`
			} `json:"rfc3161Timestamps"`
		} `json:"timestampVerificationData"`
	} `json:"verificationMaterial"`
	MessageSignature *struct {
		MessageDigest *struct {
			Algorithm string `json:"algorithm"`
			Digest    string `json:"digest"`
		} `json:"messageDigest"`
		Signature string `json:"signature"`
	} `json:"messageSignature"`
	DSSEEnvelope json.RawMessage `json:"dsseEnvelope"`
}

// jsonInt reads the 64-bit integers protobuf's JSON writes as strings.
type jsonInt int64

func (n *jsonInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("%s isn't a whole number", data)
	}
	*n = jsonInt(v)
	return nil
}

func b64(field, s string) ([]byte, error) {
	v, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s isn't base64", field)
	}
	return v, nil
}

// ParseBundle reads a bundle that signs a file.
func ParseBundle(data []byte) (*Bundle, error) {
	var raw jsonBundle
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("reading the signature bundle: %w", err)
	}
	if !strings.HasPrefix(raw.MediaType, "application/vnd.dev.sigstore.bundle") {
		return nil, fmt.Errorf("%q isn't a Sigstore bundle", raw.MediaType)
	}
	if raw.MessageSignature == nil {
		if len(raw.DSSEEnvelope) > 0 {
			return nil, errors.New("the bundle signs an attestation, not a file")
		}
		return nil, errors.New("the bundle has no signature")
	}
	b := &Bundle{MediaType: raw.MediaType}
	var err error
	if b.Signature, err = b64("the signature", raw.MessageSignature.Signature); err != nil {
		return nil, err
	}
	if len(b.Signature) == 0 {
		return nil, errors.New("the bundle's signature is empty")
	}
	if d := raw.MessageSignature.MessageDigest; d != nil {
		if d.Algorithm != "SHA2_256" {
			return nil, fmt.Errorf("the bundle's digest is %s; only SHA2_256 is supported", d.Algorithm)
		}
		if b.Digest, err = b64("the digest", d.Digest); err != nil {
			return nil, err
		}
	}
	vm := raw.VerificationMaterial
	var certDER string
	switch {
	case vm.Certificate != nil:
		certDER = vm.Certificate.RawBytes
	case vm.X509CertificateChain != nil && len(vm.X509CertificateChain.Certificates) > 0:
		certDER = vm.X509CertificateChain.Certificates[0].RawBytes
	case vm.PublicKey != nil:
		return nil, errors.New("the bundle is signed with a key rather than a certificate, which identities can't be checked against")
	default:
		return nil, errors.New("the bundle has no signing certificate")
	}
	der, err := b64("the certificate", certDER)
	if err != nil {
		return nil, err
	}
	if b.Certificate, err = x509.ParseCertificate(der); err != nil {
		return nil, fmt.Errorf("reading the signing certificate: %w", err)
	}
	for i, e := range vm.TlogEntries {
		entry := Entry{
			LogIndex: int64(e.LogIndex), Kind: e.KindVersion.Kind, Version: e.KindVersion.Version, IntegratedTime: int64(e.IntegratedTime),
		}
		if entry.LogID, err = b64("a log ID", e.LogID.KeyID); err != nil {
			return nil, err
		}
		if entry.Body, err = b64("a log entry", e.CanonicalizedBody); err != nil {
			return nil, err
		}
		if len(entry.Body) == 0 {
			return nil, fmt.Errorf("log entry %d has no body", i+1)
		}
		if p := e.InclusionPromise; p != nil && p.SignedEntryTimestamp != "" {
			if entry.SET, err = b64("a signed entry timestamp", p.SignedEntryTimestamp); err != nil {
				return nil, err
			}
		}
		if p := e.InclusionProof; p != nil {
			proof := &InclusionProof{LogIndex: int64(p.LogIndex), TreeSize: int64(p.TreeSize), Checkpoint: p.Checkpoint.Envelope}
			if proof.RootHash, err = b64("a root hash", p.RootHash); err != nil {
				return nil, err
			}
			for _, h := range p.Hashes {
				v, err := b64("a proof hash", h)
				if err != nil {
					return nil, err
				}
				proof.Hashes = append(proof.Hashes, v)
			}
			entry.Proof = proof
		}
		b.Entries = append(b.Entries, entry)
	}
	if tv := vm.TimestampVerificationData; tv != nil {
		for _, ts := range tv.RFC3161Timestamps {
			v, err := b64("a timestamp", ts.SignedTimestamp)
			if err != nil {
				return nil, err
			}
			b.Timestamps = append(b.Timestamps, v)
		}
	}
	return b, nil
}
