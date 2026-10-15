package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Module is a Go module a build uses, with the directory its source is in.
type Module struct {
	Path    string
	Version string
	Dir     string
}

// Platforms are the systems Racksail is built for: Linux for servers, and
// macOS for development.
var Platforms = []string{"linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64"}

// GoModules lists the modules the packages matching patterns use on every
// platform, with tests when tests is set, leaving out the main module and
// the standard library. It runs in dir, the root of a Go module.
func GoModules(ctx context.Context, dir string, patterns []string, platforms []string, tests bool) ([]Module, error) {
	const format = `{{with .Module}}{{if not .Main}}{{with .Replace}}{{.Path}} {{.Version}} {{.Dir}}{{else}}{{$.Module.Path}} {{$.Module.Version}} {{$.Module.Dir}}{{end}}{{end}}{{end}}`
	seen := map[string]Module{}
	for _, p := range platforms {
		goos, goarch, ok := strings.Cut(p, "/")
		if !ok {
			return nil, fmt.Errorf("platform %q must look like linux/amd64", p)
		}
		args := []string{"list", "-deps", "-f", format}
		if tests {
			args = append(args, "-test")
		}
		args = append(args, patterns...)
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOFLAGS=-mod=readonly")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list for %s in %s: %w\n%s", p, dir, err, stderr.String())
		}
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) == 0 {
				continue
			}
			m := Module{Path: fields[0]}
			if len(fields) > 1 {
				m.Version = fields[1]
			}
			if len(fields) > 2 {
				m.Dir = strings.Join(fields[2:], " ")
			}
			seen[m.Path+"@"+m.Version] = m
		}
	}
	mods := make([]Module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// licenseFile matches the names modules keep their licenses under.
var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|unlicense)([.-][a-z0-9.-]*)?$`)

// LicenseFiles returns the license files at the top of a module's source.
func LicenseFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && licenseFile.MatchString(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// ModuleLicense finds a module's license: every license named by its license
// files, all of which apply.
func ModuleLicense(m Module) (string, []string, error) {
	if m.Dir == "" {
		return "", nil, fmt.Errorf("%s %s isn't downloaded; run go mod download", m.Path, m.Version)
	}
	files, err := LicenseFiles(m.Dir)
	if err != nil {
		return "", nil, err
	}
	var ids []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", nil, err
		}
		for _, id := range Classify(string(data)) {
			if !contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return strings.Join(ids, " AND "), files, nil
}
