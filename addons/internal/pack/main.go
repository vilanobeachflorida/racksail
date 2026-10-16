// Command pack writes an add-on's release archive: a gzipped tar with the
// add-on's program at its top, called addon, as the panel unpacks it. The
// archive's bytes depend only on the program's, so the same build gives
// the same checksum wherever it runs.
//
//	go run ./internal/pack -program bin/addon -out ldap-sign-in-1.0.0-linux-amd64.tar.gz
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	program := flag.String("program", "", "the add-on's executable")
	out := flag.String("out", "", "the archive to write")
	flag.Parse()
	if *program == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "pack needs -program and -out")
		os.Exit(2)
	}
	sum, err := pack(*program, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
		os.Exit(1)
	}
	fmt.Printf("%s  %s\n", sum, *out)
}

func pack(program, out string) (string, error) {
	data, err := os.ReadFile(program)
	if err != nil {
		return "", err
	}
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	gz, err := gzip.NewWriterLevel(io.MultiWriter(f, h), gzip.BestCompression)
	if err != nil {
		return "", err
	}
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name: "addon", Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0), Format: tar.FormatPAX, Uname: "root", Gname: "root",
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return "", err
	}
	if _, err := tw.Write(data); err != nil {
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), f.Close()
}
