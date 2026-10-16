package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	_ "crypto/sha256" // SHA-256 for RS256, PS256 and ES256
	_ "crypto/sha512" // SHA-384 and SHA-512 for the rest
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
)

// Algorithms are the signature algorithms ID tokens are accepted with. They
// are all asymmetric: a token signed with the client's own secret, or not
// signed at all, is never accepted.
var Algorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}

// minRSABits is the smallest RSA key accepted.
const minRSABits = 2048

// ErrUnknownKey is a token signed with a key the key set doesn't have,
// which is what happens just after a provider rotates its keys.
var ErrUnknownKey = errors.New("oidc: the token was signed with a key the provider's key set doesn't have")

// Key is one public key of a provider's key set (RFC 7517).
type Key struct {
	ID        string
	Algorithm string
	key       crypto.PublicKey
}

type jwk struct {
	Kty    string   `json:"kty"`
	Kid    string   `json:"kid"`
	Use    string   `json:"use"`
	Alg    string   `json:"alg"`
	KeyOps []string `json:"key_ops"`
	N      string   `json:"n"`
	E      string   `json:"e"`
	Crv    string   `json:"crv"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
}

// KeySet is a provider's published keys.
type KeySet struct {
	Keys []Key
}

// FetchKeys fetches the key set at uri.
func FetchKeys(ctx context.Context, hc *http.Client, uri string) (*KeySet, error) {
	var doc json.RawMessage
	if err := getJSON(ctx, hc, uri, "", &doc); err != nil {
		return nil, fmt.Errorf("oidc: fetching the provider's keys: %w", err)
	}
	return ParseKeySet(doc)
}

// ParseKeySet reads a JWK set. Keys that aren't for signatures, or of a
// type it doesn't know, are left out, as RFC 7517 says to.
func ParseKeySet(data []byte) (*KeySet, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("oidc: the provider's key set can't be read: %w", err)
	}
	ks := &KeySet{}
	for _, k := range doc.Keys {
		if (k.Use != "" && k.Use != "sig") || (len(k.KeyOps) > 0 && !slices.Contains(k.KeyOps, "verify")) {
			continue
		}
		pub, err := k.public()
		if err != nil {
			return nil, fmt.Errorf("oidc: key %q of the provider's key set: %w", k.Kid, err)
		}
		if pub == nil {
			continue
		}
		ks.Keys = append(ks.Keys, Key{ID: k.Kid, Algorithm: k.Alg, key: pub})
	}
	return ks, nil
}

func decodeInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, errors.New("a number that isn't base64url")
	}
	return new(big.Int).SetBytes(b), nil
}

// public returns the key, or nil for a type of key it doesn't use.
func (k jwk) public() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := decodeInt(k.N)
		if err != nil {
			return nil, err
		}
		e, err := decodeInt(k.E)
		if err != nil || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31 {
			return nil, errors.New("an RSA exponent that can't be used")
		}
		if n.BitLen() < minRSABits {
			return nil, fmt.Errorf("an RSA key of %d bits, fewer than %d", n.BitLen(), minRSABits)
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, nil
		}
		size := (curve.Params().BitSize + 7) / 8
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) != size || len(y) != size {
			return nil, errors.New("an EC point that can't be read")
		}
		point := append(append([]byte{4}, x...), y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
		if err != nil {
			return nil, fmt.Errorf("an EC point that isn't on its curve: %w", err)
		}
		return pub, nil
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, nil
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, errors.New("an Ed25519 key that can't be read")
		}
		return ed25519.PublicKey(x), nil
	}
	return nil, nil
}

// Header is a JWS header.
type Header struct {
	Alg  string   `json:"alg"`
	Kid  string   `json:"kid,omitempty"`
	Typ  string   `json:"typ,omitempty"`
	Crit []string `json:"crit,omitempty"`
}

// Verify checks a compact JWS with the key set and returns its payload. The
// algorithm must be one of algs, and the key the one its header names, or
// the only one that fits when it names none.
func (ks *KeySet) Verify(token string, algs []string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("oidc: the ID token isn't a signed JWT")
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("oidc: the ID token's header isn't base64url")
	}
	var h Header
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return nil, errors.New("oidc: the ID token's header isn't JSON")
	}
	if len(h.Crit) > 0 {
		return nil, fmt.Errorf("oidc: the ID token needs extensions %v, which aren't supported", h.Crit)
	}
	if !slices.Contains(algs, h.Alg) || !slices.Contains(Algorithms, h.Alg) {
		return nil, fmt.Errorf("oidc: the ID token is signed with %q, which isn't accepted", h.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("oidc: the ID token's signature isn't base64url")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("oidc: the ID token's claims aren't base64url")
	}
	signed := []byte(parts[0] + "." + parts[1])
	var candidates []Key
	for _, k := range ks.Keys {
		if (h.Kid == "" || k.ID == h.Kid) && (k.Algorithm == "" || k.Algorithm == h.Alg) && fits(k.key, h.Alg) {
			candidates = append(candidates, k)
		}
	}
	if len(candidates) == 0 || (h.Kid == "" && len(candidates) > 1) {
		return nil, ErrUnknownKey
	}
	if err := verify(candidates[0].key, h.Alg, signed, sig); err != nil {
		return nil, err
	}
	return payload, nil
}

// fits reports whether a key can make signatures of alg.
func fits(key crypto.PublicKey, alg string) bool {
	switch k := key.(type) {
	case *rsa.PublicKey:
		return strings.HasPrefix(alg, "RS") || strings.HasPrefix(alg, "PS")
	case *ecdsa.PublicKey:
		return map[string]elliptic.Curve{"ES256": elliptic.P256(), "ES384": elliptic.P384(), "ES512": elliptic.P521()}[alg] == k.Curve
	case ed25519.PublicKey:
		return alg == "EdDSA"
	}
	return false
}

var hashes = map[string]crypto.Hash{"256": crypto.SHA256, "384": crypto.SHA384, "512": crypto.SHA512}

var errSignature = errors.New("oidc: the ID token's signature doesn't match")

func verify(key crypto.PublicKey, alg string, signed, sig []byte) error {
	if alg == "EdDSA" {
		if !ed25519.Verify(key.(ed25519.PublicKey), signed, sig) {
			return errSignature
		}
		return nil
	}
	hash := hashes[alg[2:]]
	h := hash.New()
	h.Write(signed)
	digest := h.Sum(nil)
	switch k := key.(type) {
	case *rsa.PublicKey:
		var err error
		if strings.HasPrefix(alg, "PS") {
			err = rsa.VerifyPSS(k, hash, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		} else {
			err = rsa.VerifyPKCS1v15(k, hash, digest, sig)
		}
		if err != nil {
			return errSignature
		}
	case *ecdsa.PublicKey:
		size := (k.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*size {
			return errSignature
		}
		r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
		if !ecdsa.Verify(k, digest, r, s) {
			return errSignature
		}
	default:
		return errSignature
	}
	return nil
}
