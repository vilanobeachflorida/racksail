// Command licenses keeps Racksail's dependencies to licenses that are
// approved by the Open Source Initiative and compatible with
// AGPL-3.0-or-later, and writes the license notices releases ship with.
//
//	go run ./packaging/licenses check
//	go run ./packaging/licenses notices -out dist/third-party-licenses.txt
//
// check covers every Go module the repository's code uses on every platform
// it builds for, tests included, and every package in every npm lockfile
// git tracks. notices covers what the release binary contains: the Go
// modules built into it and the npm packages bundled into its web UI.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/packaging/spdx"
)

//go:embed exceptions.yaml
var exceptionsYAML []byte

// Exception tells the check about one dependency.
type Exception struct {
	Module  string `yaml:"module"`
	Package string `yaml:"package"`
	License string `yaml:"license"`
	Allow   bool   `yaml:"allow"`
	Reason  string `yaml:"reason"`
}

// Exceptions are the entries of exceptions.yaml.
type Exceptions struct {
	Go  []Exception `yaml:"go"`
	Npm []Exception `yaml:"npm"`
}

func parseExceptions(data []byte) (Exceptions, error) {
	var ex Exceptions
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&ex); err != nil {
		return ex, fmt.Errorf("exceptions.yaml: %w", err)
	}
	for _, e := range append(append([]Exception{}, ex.Go...), ex.Npm...) {
		name := e.Module + e.Package
		if name == "" || e.Reason == "" || e.License == "" {
			return ex, fmt.Errorf("exceptions.yaml: every entry needs a name, a license and a reason: %+v", e)
		}
		if _, err := spdx.Parse(e.License); err != nil {
			return ex, fmt.Errorf("exceptions.yaml: %s: %w", name, err)
		}
	}
	return ex, nil
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: licenses check | notices -out FILE")
	}
	ex, err := parseExceptions(exceptionsYAML)
	if err != nil {
		return err
	}
	switch args[0] {
	case "check":
		flags := flag.NewFlagSet("check", flag.ContinueOnError)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return check(ctx, ex, stdout)
	case "notices":
		flags := flag.NewFlagSet("notices", flag.ContinueOnError)
		out := flags.String("out", "", "file to write the notices to")
		ui := flags.String("ui", "ui", "the web UI's directory, with its packages installed")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *out == "" {
			return errors.New("notices needs -out")
		}
		return writeNotices(ctx, *ui, *out)
	}
	return fmt.Errorf("unknown command %q; use check or notices", args[0])
}

// trackedFiles lists files git tracks that match a pattern, outside hidden
// directories, where other checkouts may live.
func trackedFiles(ctx context.Context, pattern string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "git", "ls-files", "--", pattern).Output()
	if err != nil {
		return nil, fmt.Errorf("listing %s with git: %w", pattern, err)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f == "" || strings.HasPrefix(f, ".") || strings.Contains(f, "/.") {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

func check(ctx context.Context, ex Exceptions, stdout io.Writer) error {
	var problems []string
	used := map[string]bool{}

	modFiles, err := trackedFiles(ctx, "*go.mod")
	if err != nil {
		return err
	}
	goCount := 0
	for _, mod := range modFiles {
		dir := path.Dir(mod)
		mods, err := GoModules(ctx, dir, []string{"./..."}, Platforms, true)
		if err != nil {
			return err
		}
		for _, m := range mods {
			goCount++
			if p := checkModule(m, ex, used); p != "" {
				problems = append(problems, p)
			}
		}
	}

	locks, err := trackedFiles(ctx, "*package-lock.json")
	if err != nil {
		return err
	}
	npmCount := 0
	for _, lock := range locks {
		pkgs, err := NpmPackages(lock)
		if err != nil {
			return err
		}
		for _, p := range pkgs {
			npmCount++
			if msg := checkPackage(p, ex, used); msg != "" {
				problems = append(problems, lock+": "+msg)
			}
		}
	}

	for _, e := range append(append([]Exception{}, ex.Go...), ex.Npm...) {
		if name := e.Module + e.Package; !used[name] {
			problems = append(problems, fmt.Sprintf("exceptions.yaml: nothing needs the entry for %s any more; remove it", name))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%d dependencies need attention:\n  %s\n\nRacksail only uses dependencies under licenses approved by the Open Source Initiative\nthat are compatible with AGPL-3.0-or-later. See packaging/licenses/exceptions.yaml",
			len(problems), strings.Join(problems, "\n  "))
	}
	fmt.Fprintf(stdout, "licenses: %d Go modules and %d npm packages, all under allowed licenses\n", goCount, npmCount)
	return nil
}

func findException(list []Exception, name string) (Exception, bool) {
	for _, e := range list {
		if e.Module == name || e.Package == name {
			return e, true
		}
	}
	return Exception{}, false
}

func checkModule(m Module, ex Exceptions, used map[string]bool) string {
	license, _, err := ModuleLicense(m)
	if err != nil {
		return err.Error()
	}
	if e, ok := findException(ex.Go, m.Path); ok {
		used[m.Path] = true
		if e.Allow {
			return ""
		}
		license = e.License
	}
	if license == "" {
		return fmt.Sprintf("%s %s: no license file the check recognizes; read it and add the module to exceptions.yaml", m.Path, m.Version)
	}
	return allowed(m.Path+" "+m.Version, license)
}

func checkPackage(p Package, ex Exceptions, used map[string]bool) string {
	if e, ok := findException(ex.Npm, p.Name); ok {
		used[p.Name] = true
		if e.License != p.License && p.License != "" {
			return fmt.Sprintf("%s %s is %q now, but exceptions.yaml covers it as %q", p.Name, p.Version, p.License, e.License)
		}
		if e.Allow {
			return ""
		}
		return allowed(p.Name+" "+p.Version, e.License)
	}
	if p.License == "" {
		return fmt.Sprintf("%s %s doesn't say its license; read it and add the package to exceptions.yaml", p.Name, p.Version)
	}
	return allowed(p.Name+" "+p.Version, p.License)
}

func allowed(what, license string) string {
	expr, err := spdx.Parse(license)
	if err != nil {
		return fmt.Sprintf("%s: %v", what, err)
	}
	if !spdx.Satisfied(expr, spdx.Allowed) {
		return fmt.Sprintf("%s is under %s, which isn't on the allowlist", what, license)
	}
	return ""
}

// writeNotices collects the license texts of everything the release binary
// contains.
func writeNotices(ctx context.Context, uiDir, out string) error {
	var b bytes.Buffer
	b.WriteString("Racksail is licensed under the GNU Affero General Public License v3.0 or\n")
	b.WriteString("later. It contains the following software, under these licenses.\n")

	mods, err := GoModules(ctx, ".", []string{"./cmd/racksail"}, []string{"linux/amd64", "linux/arm64"}, false)
	if err != nil {
		return err
	}
	var missing []string
	for _, m := range mods {
		license, files, err := ModuleLicense(m)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			missing = append(missing, m.Path)
			continue
		}
		if err := notice(&b, m.Path+" "+m.Version, license, files); err != nil {
			return err
		}
	}

	pkgs, err := NpmPackages(filepath.Join(uiDir, "package-lock.json"))
	if err != nil {
		return err
	}
	for _, p := range pkgs {
		if p.Dev {
			continue
		}
		dir := filepath.Join(uiDir, p.Path)
		files, err := LicenseFiles(dir)
		if err != nil {
			return fmt.Errorf("%s isn't installed in %s; run npm ci there first: %w", p.Name, uiDir, err)
		}
		if len(files) > 0 {
			if err := notice(&b, p.Name+" "+p.Version, p.License, files); err != nil {
				return err
			}
			continue
		}
		// Some packages state a short license in package.json without
		// shipping its text; the standard text with their author stands in.
		text, ok := standardText(p.License, packageAuthor(dir))
		if !ok {
			missing = append(missing, p.Name)
			continue
		}
		fmt.Fprintf(&b, "\n%s\n%s %s\nLicense: %s\n\n%s\n", strings.Repeat("=", 78), p.Name, p.Version, p.License, text)
	}
	if len(missing) > 0 {
		return fmt.Errorf("these ship without a license file to copy into the notices: %s", strings.Join(missing, ", "))
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, b.Bytes(), 0o644)
}

// packageAuthor reads the author of an installed npm package.
func packageAuthor(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Author json.RawMessage `json:"author"`
	}
	if json.Unmarshal(data, &pkg) != nil || len(pkg.Author) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(pkg.Author, &s) == nil {
		// "Name <email> (url)" names the author first.
		name, _, _ := strings.Cut(s, " <")
		name, _, _ = strings.Cut(name, " (")
		return strings.TrimSpace(name)
	}
	var obj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(pkg.Author, &obj) == nil {
		return obj.Name
	}
	return ""
}

// standardText returns the text of a short permissive license, for packages
// that only name it.
func standardText(license, holder string) (string, bool) {
	if holder == "" {
		holder = "the authors"
	}
	switch license {
	case "MIT":
		return "Copyright (c) " + holder + "\n\n" + mitText, true
	case "ISC":
		return "Copyright (c) " + holder + "\n\n" + iscText, true
	}
	return "", false
}

const mitText = `Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.`

const iscText = `Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.`

func notice(b *bytes.Buffer, name, license string, files []string) error {
	fmt.Fprintf(b, "\n%s\n%s\nLicense: %s\n", strings.Repeat("=", 78), name, license)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		fmt.Fprintf(b, "\n%s:\n\n%s\n", filepath.Base(f), strings.TrimRight(string(data), "\n"))
	}
	return nil
}
