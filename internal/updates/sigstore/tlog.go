package sigstore

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// verifySignature checks sig over msg with a public key, hashing as the
// key's kind does: ECDSA with the hash its curve pairs with, Ed25519 over
// the message itself and RSA with SHA-256.
func verifySignature(key crypto.PublicKey, msg, sig []byte) error {
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		var digest []byte
		switch k.Curve {
		case elliptic.P384():
			d := sha512.Sum384(msg)
			digest = d[:]
		case elliptic.P521():
			d := sha512.Sum512(msg)
			digest = d[:]
		default:
			d := sha256.Sum256(msg)
			digest = d[:]
		}
		if !ecdsa.VerifyASN1(k, digest, sig) {
			return errors.New("the signature doesn't match")
		}
		return nil
	case ed25519.PublicKey:
		if !ed25519.Verify(k, msg, sig) {
			return errors.New("the signature doesn't match")
		}
		return nil
	case *rsa.PublicKey:
		d := sha256.Sum256(msg)
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], sig)
	}
	return fmt.Errorf("keys of type %T aren't supported", key)
}

// checkBody makes sure a log entry records this signature of this file by
// this certificate, and not something else.
func checkBody(e Entry, digest, sig []byte, cert *x509.Certificate) error {
	switch e.Kind + " " + e.Version {
	case "hashedrekord 0.0.1":
		var body struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Spec       struct {
				Data struct {
					Hash struct {
						Algorithm string `json:"algorithm"`
						Value     string `json:"value"`
					} `json:"hash"`
				} `json:"data"`
				Signature struct {
					Content   string `json:"content"`
					PublicKey struct {
						Content string `json:"content"`
					} `json:"publicKey"`
				} `json:"signature"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(e.Body, &body); err != nil {
			return fmt.Errorf("reading the log entry: %w", err)
		}
		if body.Kind != e.Kind || body.APIVersion != e.Version {
			return errors.New("the log entry isn't the kind it says")
		}
		if body.Spec.Data.Hash.Algorithm != "sha256" || body.Spec.Data.Hash.Value != hex.EncodeToString(digest) {
			return errors.New("the log entry records another file")
		}
		logged, err := base64.StdEncoding.DecodeString(body.Spec.Signature.Content)
		if err != nil || !bytes.Equal(logged, sig) {
			return errors.New("the log entry records another signature")
		}
		pemData, err := base64.StdEncoding.DecodeString(body.Spec.Signature.PublicKey.Content)
		if err != nil {
			return errors.New("the log entry's certificate isn't base64")
		}
		block, _ := pem.Decode(pemData)
		if block == nil || !bytes.Equal(block.Bytes, cert.Raw) {
			return errors.New("the log entry records another certificate")
		}
		return nil
	case "hashedrekord 0.0.2":
		var body struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Spec       struct {
				HashedRekordV002 struct {
					Data struct {
						Algorithm string `json:"algorithm"`
						Digest    string `json:"digest"`
					} `json:"data"`
					Signature struct {
						Content  string `json:"content"`
						Verifier struct {
							X509Certificate *struct {
								RawBytes string `json:"rawBytes"`
							} `json:"x509Certificate"`
						} `json:"verifier"`
					} `json:"signature"`
				} `json:"hashedRekordV002"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(e.Body, &body); err != nil {
			return fmt.Errorf("reading the log entry: %w", err)
		}
		if body.Kind != e.Kind || body.APIVersion != e.Version {
			return errors.New("the log entry isn't the kind it says")
		}
		spec := body.Spec.HashedRekordV002
		logDigest, err := base64.StdEncoding.DecodeString(spec.Data.Digest)
		if err != nil || spec.Data.Algorithm != "SHA2_256" || !bytes.Equal(logDigest, digest) {
			return errors.New("the log entry records another file")
		}
		logged, err := base64.StdEncoding.DecodeString(spec.Signature.Content)
		if err != nil || !bytes.Equal(logged, sig) {
			return errors.New("the log entry records another signature")
		}
		if spec.Signature.Verifier.X509Certificate == nil {
			return errors.New("the log entry records a key rather than a certificate")
		}
		der, err := base64.StdEncoding.DecodeString(spec.Signature.Verifier.X509Certificate.RawBytes)
		if err != nil || !bytes.Equal(der, cert.Raw) {
			return errors.New("the log entry records another certificate")
		}
		return nil
	}
	return fmt.Errorf("log entries of kind %s %s aren't supported", e.Kind, e.Version)
}

// verifySET checks a Rekor v1 signed entry timestamp: the log's signature
// over the entry's body, time, log ID and index, as canonical JSON.
func verifySET(e Entry, log Log) error {
	payload := struct {
		Body           string `json:"body"`
		IntegratedTime int64  `json:"integratedTime"`
		LogID          string `json:"logID"`
		LogIndex       int64  `json:"logIndex"`
	}{base64.StdEncoding.EncodeToString(e.Body), e.IntegratedTime, hex.EncodeToString(e.LogID), e.LogIndex}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return err
	}
	if err := verifySignature(log.Key, bytes.TrimSuffix(buf.Bytes(), []byte("\n")), e.SET); err != nil {
		return fmt.Errorf("the log's promise to include the entry: %w", err)
	}
	return nil
}

func leafHash(data []byte) []byte {
	h := sha256.New()
	h.Write([]byte{0})
	h.Write(data)
	return h.Sum(nil)
}

func nodeHash(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte{1})
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// rootFromProof computes a tree's root from a leaf and its audit path, as
// RFC 6962 describes.
func rootFromProof(index, size uint64, leaf []byte, proof [][]byte) ([]byte, error) {
	if index >= size {
		return nil, fmt.Errorf("entry %d is outside a tree of %d", index, size)
	}
	inner := bits.Len64(index ^ (size - 1))
	border := bits.OnesCount64(index >> uint(inner))
	if len(proof) != inner+border {
		return nil, fmt.Errorf("the proof has %d hashes; a tree of %d needs %d for entry %d", len(proof), size, inner+border, index)
	}
	res := leaf
	for i, h := range proof[:inner] {
		if len(h) != sha256.Size {
			return nil, errors.New("a proof hash isn't a SHA-256")
		}
		if (index>>uint(i))&1 == 0 {
			res = nodeHash(res, h)
		} else {
			res = nodeHash(h, res)
		}
	}
	for _, h := range proof[inner:] {
		if len(h) != sha256.Size {
			return nil, errors.New("a proof hash isn't a SHA-256")
		}
		res = nodeHash(h, res)
	}
	return res, nil
}

// verifyProof checks that an entry is in the log: its audit path leads to
// the root hash of a checkpoint the log signed.
func verifyProof(e Entry, log Log) error {
	p := e.Proof
	if p.LogIndex < 0 || p.TreeSize <= 0 {
		return errors.New("the inclusion proof has no position in the log")
	}
	root, err := rootFromProof(uint64(p.LogIndex), uint64(p.TreeSize), leafHash(e.Body), p.Hashes)
	if err != nil {
		return fmt.Errorf("the inclusion proof: %w", err)
	}
	if !bytes.Equal(root, p.RootHash) {
		return errors.New("the inclusion proof doesn't lead to the log's root")
	}
	return verifyCheckpoint(p.Checkpoint, log, p)
}

// verifyCheckpoint checks a checkpoint, a signed note: the log's origin,
// its size and root hash, and the log's signature over them.
func verifyCheckpoint(envelope string, log Log, p *InclusionProof) error {
	text, sigs, ok := strings.Cut(envelope, "\n\n")
	if !ok {
		return errors.New("the checkpoint isn't signed")
	}
	text += "\n"
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 3 {
		return errors.New("the checkpoint is incomplete")
	}
	size, err := strconv.ParseInt(lines[1], 10, 64)
	if err != nil || size != p.TreeSize {
		return errors.New("the checkpoint is for another tree size than the proof")
	}
	root, err := base64.StdEncoding.DecodeString(lines[2])
	if err != nil || !bytes.Equal(root, p.RootHash) {
		return errors.New("the checkpoint has another root than the proof")
	}
	for _, line := range strings.Split(sigs, "\n") {
		rest, ok := strings.CutPrefix(line, "— ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(fields[1])
		if err != nil || len(raw) < 5 {
			continue
		}
		// The first four bytes hint at the key; trying the log's key is
		// what matters.
		if verifySignature(log.Key, []byte(text), raw[4:]) == nil {
			return nil
		}
	}
	return errors.New("the checkpoint isn't signed by the log")
}

// verifyEntry checks one log entry and returns the time it vouches for:
// the time it was logged when the log signed that, or zero.
func verifyEntry(root *TrustedRoot, e Entry, digest, sig []byte, cert *x509.Certificate) (time.Time, error) {
	log, ok := findLog(root.TransparencyLogs, e.LogID)
	if !ok {
		return time.Time{}, errors.New("the entry is in a log the trusted root doesn't list")
	}
	if err := checkBody(e, digest, sig, cert); err != nil {
		return time.Time{}, err
	}
	if e.SET == nil && e.Proof == nil {
		return time.Time{}, errors.New("the entry has neither a promise of inclusion nor a proof of it")
	}
	if e.Proof != nil {
		if err := verifyProof(e, log); err != nil {
			return time.Time{}, err
		}
	}
	if e.SET == nil {
		return time.Time{}, nil
	}
	if err := verifySET(e, log); err != nil {
		return time.Time{}, err
	}
	at := time.Unix(e.IntegratedTime, 0)
	if !validAt(log.Start, log.End, at) {
		return time.Time{}, errors.New("the log's key wasn't trusted when the entry was logged")
	}
	return at, nil
}
