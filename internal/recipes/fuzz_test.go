package recipes

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"
)

// Recipes come from catalogs anyone can publish, so whatever a file holds,
// reading it neither panics nor lets through a recipe the panel can't plan.
func FuzzParse(f *testing.F) {
	f.Add([]byte(wordpress))
	f.Add([]byte(wordpress + "health:\n  - {type: resource, target: site}\nupgrade: {strategy: in-place}\n"))
	f.Add([]byte("format: 1\nkind: theme\nid: x\nname: X\nversion: \"1\"\n"))
	f.Add([]byte("resources: [{name: a, kind: web.site, dependsOn: [a]}]"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Parse(data)
		if err != nil {
			return
		}
		// What parses can be ordered, and its inputs checked.
		if _, err := r.Order(); err != nil {
			return
		}
		_, _ = r.Values(map[string]any{})
		_, _ = r.Values(r.Tests.Inputs)
	})
}

// fuzzKey signs indexes in the fuzz targets; it is the same every run, so
// what the fuzzer finds can be run again.
var fuzzKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))

// A catalog's index is only read once its signature checks out, but a
// catalog's own key can sign anything: what it signs never panics the
// panel, and never names a file outside the catalog.
func FuzzVerifyIndex(f *testing.F) {
	good, _ := json.Marshal(Index{Format: 1, Catalog: "test", Built: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Items: []IndexItem{{
		ID: "wordpress", Kind: KindApp, Name: "WordPress", Version: "6.8.3", LicenseClass: OpenSource, Path: "apps/wordpress/recipe.yaml",
		Files: []IndexFile{{Path: "apps/wordpress/recipe.yaml", SHA256: strings.Repeat("a", 64), Size: 10}},
	}}})
	f.Add(good)
	f.Add([]byte(`{"format":1,"items":[{"id":"x","path":"../../etc/passwd"}]}`))
	f.Add([]byte(`{"format":2}`))
	pub := fuzzKey.Public().(ed25519.PublicKey)
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := VerifyIndex(data, make([]byte, ed25519.SignatureSize), pub); err == nil {
			t.Fatal("an index without its signature was taken")
		}
		idx, err := VerifyIndex(data, ed25519.Sign(fuzzKey, data), pub)
		if err != nil {
			return
		}
		for _, it := range idx.Items {
			for _, p := range append([]string{it.Path, it.Icon}, filePaths(it.Files)...) {
				if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
					t.Fatalf("the index names %q", p)
				}
			}
		}
	})
}

func filePaths(files []IndexFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// Keys come from what admins paste when they add a catalog.
func FuzzParseKey(f *testing.F) {
	f.Add("# Racksail catalog key\n" + base64.StdEncoding.EncodeToString(fuzzKey.Public().(ed25519.PublicKey)) + "\n")
	f.Add("untrusted comment: x\n\n")
	f.Fuzz(func(t *testing.T, text string) {
		key, err := ParseKey(text)
		if err == nil && len(key) != ed25519.PublicKeySize {
			t.Fatalf("a key of %d bytes", len(key))
		}
	})
}

// Minisign signatures and keys come from upstream projects and recipes.
// Whatever they hold, checking them never panics, and only a signature by
// the key named passes.
func FuzzMinisign(f *testing.F) {
	content := []byte("upstream release tarball")
	keyID := []byte("8bytesid")
	pub := fuzzKey.Public().(ed25519.PublicKey)
	key := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...))
	sign := func(alg string, msg []byte) []byte {
		sig := ed25519.Sign(fuzzKey, msg)
		global := ed25519.Sign(fuzzKey, append(append([]byte{}, sig...), []byte("timestamp:1")...))
		return []byte("untrusted comment: signature\n" + base64.StdEncoding.EncodeToString(append(append([]byte(alg), keyID...), sig...)) +
			"\ntrusted comment: timestamp:1\n" + base64.StdEncoding.EncodeToString(global) + "\n")
	}
	pre := blake2b.Sum512(content)
	f.Add(key, sign("ED", pre[:]), content)
	f.Add(key, sign("Ed", content), content)
	f.Add("untrusted comment: key\n"+key, []byte("untrusted comment: x\nRUQ=\n"), content)
	f.Fuzz(func(t *testing.T, keyText string, sig, data []byte) {
		hash, _ := blake2b.New512(nil)
		hash.Write(data)
		if verifyMinisign(keyText, sig, bytes.NewReader(data), int64(len(data)), hash) != nil {
			return
		}
		named, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lastLine(keyText)))
		if err != nil || len(named) != 42 || !bytes.Equal(named[10:], pub) {
			t.Fatalf("a signature passed with another key: %q", keyText)
		}
	})
}
