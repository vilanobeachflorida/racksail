package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPack(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "built")
	if err := os.WriteFile(program, []byte("\x7fELF the program"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(dir, "a.tar.gz"), filepath.Join(dir, "b.tar.gz")
	sum, err := pack(program, first)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := pack(program, second); err != nil || again != sum {
		t.Errorf("packing twice gave %s and %s, %v", sum, again, err)
	}
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != sum {
		t.Errorf("the checksum printed isn't the archive's")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(tr)
	if hdr.Name != "addon" || hdr.Mode != 0o755 || string(body) != "\x7fELF the program" {
		t.Errorf("the archive holds %s (%o): %q", hdr.Name, hdr.Mode, body)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Errorf("more than the program in the archive: %v", err)
	}
}
