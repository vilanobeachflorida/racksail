// Command deb writes what Racksail's Debian package installs besides the
// binary: the systemd units, the sysusers.d and tmpfiles.d entries that
// create its users and directories, the maintainer scripts and the copyright
// file. They come from the plan "racksail install --convert" carries out, so
// a server set up from the package matches a converted one. nfpm then builds
// the package from packaging/nfpm.yaml.
//
//	go run ./packaging/deb -out build/deb -license LICENSE
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "deb:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("deb", flag.ContinueOnError)
	out := flags.String("out", "", "directory to write the package's files into; it must not exist or be empty")
	license := flags.String("license", "LICENSE", "the license text for the copyright file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	text, err := os.ReadFile(*license)
	if err != nil {
		return err
	}
	files, err := Files(text)
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(*out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s isn't empty; write the package's files into a new directory", *out)
	}
	for name, f := range files {
		path := filepath.Join(*out, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, f.data, f.mode); err != nil {
			return err
		}
	}
	return nil
}

type file struct {
	data []byte
	mode os.FileMode
}

// Files lays out everything Render produces under the names
// packaging/nfpm.yaml reads them from.
func Files(license []byte) (map[string]file, error) {
	p, err := Render(Binary)
	if err != nil {
		return nil, err
	}
	scripts, err := p.Scripts()
	if err != nil {
		return nil, err
	}
	out := map[string]file{
		"sysusers/racksail.conf": {p.Sysusers, 0o644},
		"tmpfiles/racksail.conf": {p.Tmpfiles, 0o644},
		"copyright":              {Copyright(license), 0o644},
	}
	for name, data := range p.Units {
		out["units/"+name] = file{data, 0o644}
	}
	for name, data := range scripts {
		out["scripts/"+name] = file{data, 0o755}
	}
	return out, nil
}
