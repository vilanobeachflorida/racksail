package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Key files hold one base64 line after any comment lines, so a public key
// can be pasted where a panel asks for a catalog's key, and a private key
// stored as a CI secret.
const (
	privateHeader = "# Racksail catalog signing key (Ed25519 seed, base64). Keep it secret.\n"
	publicHeader  = "# Racksail catalog key (Ed25519 public key, base64).\n"
)

// GenerateKeys writes a new signing key pair. The private key file is only
// readable by its owner, and neither file may exist yet.
func GenerateKeys(privatePath, publicPath string) (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	if err := writeNew(privatePath, privateHeader+seed+"\n", 0o600); err != nil {
		return nil, err
	}
	if err := writeNew(publicPath, publicHeader+base64.StdEncoding.EncodeToString(pub)+"\n", 0o644); err != nil {
		return nil, err
	}
	return pub, nil
}

func writeNew(path, content string, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// keyLine returns the base64 line of a key file.
func keyLine(data []byte) ([]byte, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return base64.StdEncoding.DecodeString(line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("no key found")
}

// ParsePrivateKey reads a signing key: a 32-byte seed or a 64-byte private
// key, in base64.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	raw, err := keyLine(data)
	if err != nil {
		return nil, fmt.Errorf("reading the signing key: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		key := ed25519.PrivateKey(raw)
		// The second half is the public key; a mismatch means a damaged key.
		if !bytes.Equal(ed25519.NewKeyFromSeed(key.Seed()), key) {
			return nil, errors.New("the signing key is damaged")
		}
		return key, nil
	}
	return nil, fmt.Errorf("the signing key is %d bytes; expected an Ed25519 seed of %d bytes", len(raw), ed25519.SeedSize)
}

// ParsePublicKey reads a public key in base64.
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	raw, err := keyLine(data)
	if err != nil {
		return nil, fmt.Errorf("reading the public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("the public key is %d bytes; expected %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}
