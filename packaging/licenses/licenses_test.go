package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	mit := "Copyright (c) 2026 Someone\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\nof this software... The above copyright notice and this permission notice shall be included in\nall copies or substantial portions of the Software."
	bsd := "Redistribution and use in source and binary forms, with or without\nmodification, are permitted provided that the following conditions are met:\n"
	cases := map[string][]string{
		mit: {"MIT"},
		strings.Replace(mit, "The above copyright notice and this permission notice shall be included in\nall copies", "", 1): {"MIT-0"},
		"# MIT License\n\n**" + mit + "**": {"MIT"},
		"Apache License\nVersion 2.0, January 2004\n\nTERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION\n":                                                                                                       {"Apache-2.0"},
		"Licensed under the Apache License, Version 2.0 (the \"License\");":                                                                                                                                                 {"Apache-2.0"},
		bsd + "3. Neither the name of the copyright holder nor the names of its contributors may be used to endorse or promote products derived from this software":                                                         {"BSD-3-Clause"},
		bsd + "2. Redistributions in binary form must reproduce the above copyright notice":                                                                                                                                 {"BSD-2-Clause"},
		bsd + "3. All advertising materials mentioning features or use of this software":                                                                                                                                    {"BSD-4-Clause"},
		"Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted, provided that the above copyright notice and this permission notice appear in all copies.": {"ISC"},
		"Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted.":                                                                                           {"0BSD"},
		"Mozilla Public License Version 2.0\n==================================\n":                                                                                                                                          {"MPL-2.0"},
		"GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n... GNU Lesser General Public License instead":                                                                                                                {"GPL-3.0-only"},
		"GNU LESSER GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n... version 3 of the GNU General Public License":                                                                                                       {"LGPL-3.0-only"},
		"GNU GENERAL PUBLIC LICENSE\nVersion 2, June 1991\n":                                                                                                                                                                {"GPL-2.0-only"},
		"This is free and unencumbered software released into the public domain.":                                                                                                                                           {"Unlicense"},
		"This project is covered by two different licenses: MIT and Apache.\n\n" + mit + "\n\nLicensed under the Apache License, Version 2.0":                                                                               {"Apache-2.0", "MIT"},
		"All rights reserved. Do not copy.": nil,
	}
	for text, want := range cases {
		if got := Classify(text); !slices.Equal(got, want) {
			t.Errorf("Classify(%.60q...) = %v, want %v", text, got, want)
		}
	}
}

// The repository's own Go dependencies are read correctly.
func TestGoModulesOfThisRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the repository's packages with go list")
	}
	mods, err := GoModules(context.Background(), "../..", []string{"./cmd/racksail"}, []string{"linux/amd64"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"github.com/spf13/cobra":           "Apache-2.0",
		"github.com/spf13/pflag":           "BSD-3-Clause",
		"github.com/BurntSushi/toml":       "MIT",
		"github.com/godbus/dbus/v5":        "BSD-2-Clause",
		"go.yaml.in/yaml/v3":               "Apache-2.0 AND MIT",
		"github.com/danielgtaylor/huma/v2": "MIT",
	}
	for _, m := range mods {
		if m.Path == "github.com/vilanobeachflorida/racksail" {
			t.Error("the main module isn't a dependency")
		}
		license, files, err := ModuleLicense(m)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 || license == "" {
			t.Errorf("%s: no license found", m.Path)
		}
		if w, ok := want[m.Path]; ok && license != w {
			t.Errorf("%s = %s, want %s", m.Path, license, w)
		}
	}
}

func TestNpmPackages(t *testing.T) {
	lock := `{
  "name": "example", "lockfileVersion": 3,
  "packages": {
    "": {"name": "example", "license": "AGPL-3.0-or-later"},
    "node_modules/react": {"version": "19.3.1", "license": "MIT"},
    "node_modules/typescript": {"version": "5.9.3", "license": "Apache-2.0", "dev": true},
    "node_modules/a/node_modules/old": {"version": "1.0.0", "license": {"type": "BSD-2-Clause"}},
    "node_modules/dual": {"version": "2.0.0", "license": [{"type": "MIT"}, {"type": "Apache-2.0"}]},
    "node_modules/local": {"resolved": "packages/local", "link": true}
  }
}`
	file := filepath.Join(t.TempDir(), "package-lock.json")
	os.WriteFile(file, []byte(lock), 0o644)
	pkgs, err := NpmPackages(file)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Package{}
	for _, p := range pkgs {
		got[p.Name] = p
	}
	if len(pkgs) != 4 || got["react"].License != "MIT" || !got["typescript"].Dev || got["old"].License != "BSD-2-Clause" || got["dual"].License != "(MIT OR Apache-2.0)" {
		t.Errorf("packages = %+v", pkgs)
	}
	os.WriteFile(file, []byte(`{"lockfileVersion": 1, "dependencies": {}}`), 0o644)
	if _, err := NpmPackages(file); err == nil {
		t.Error("old lockfiles must be refused")
	}
}

func TestPackageRules(t *testing.T) {
	ex, err := parseExceptions([]byte(`
go:
  - {module: example.com/unreadable, license: BSD-3-Clause, reason: The license is in the README.}
npm:
  - {package: data-only, license: CC0-1.0, allow: true, reason: Build-time data.}
  - {package: unused, license: MIT, reason: Left over.}
`))
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	cases := []struct {
		pkg  Package
		want string
	}{
		{Package{Name: "react", Version: "19.3.1", License: "MIT"}, ""},
		{Package{Name: "gpl2", Version: "1.0.0", License: "GPL-2.0-only"}, "isn't on the allowlist"},
		{Package{Name: "either", Version: "1.0.0", License: "(GPL-2.0-only OR MIT)"}, ""},
		{Package{Name: "data-only", Version: "1.0.0", License: "CC0-1.0"}, ""},
		{Package{Name: "data-only", Version: "2.0.0", License: "CC-BY-4.0"}, "exceptions.yaml covers it as"},
		{Package{Name: "silent", Version: "1.0.0"}, "doesn't say its license"},
		{Package{Name: "odd", Version: "1.0.0", License: "SEE LICENSE IN LICENSE.md"}, "odd 1.0.0"},
	}
	for _, tc := range cases {
		got := checkPackage(tc.pkg, ex, used)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("checkPackage(%+v) = %q, want %q", tc.pkg, got, tc.want)
		}
	}
	if !used["data-only"] || used["unused"] {
		t.Errorf("used = %v", used)
	}
	for _, bad := range []string{"go: [{module: x, license: MIT}]", "npm: [{package: x, license: 'not a license!', reason: r}]", "npm: [{name: x}]"} {
		if _, err := parseExceptions([]byte(bad)); err == nil {
			t.Errorf("parseExceptions(%s) should fail", bad)
		}
	}
	if _, err := parseExceptions(exceptionsYAML); err != nil {
		t.Errorf("exceptions.yaml: %v", err)
	}
}

func TestNotices(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the repository's packages with go list")
	}
	ui := t.TempDir()
	os.WriteFile(filepath.Join(ui, "package-lock.json"), []byte(`{"lockfileVersion": 3, "packages": {
  "": {"name": "ui"},
  "node_modules/react": {"version": "19.3.1", "license": "MIT"},
  "node_modules/vite": {"version": "8.3.4", "license": "MIT", "dev": true}
}}`), 0o644)
	os.MkdirAll(filepath.Join(ui, "node_modules/react"), 0o755)
	os.WriteFile(filepath.Join(ui, "node_modules/react/LICENSE"), []byte("MIT License\n\nCopyright (c) Meta Platforms, Inc. and affiliates.\n"), 0o644)
	out := filepath.Join(t.TempDir(), "notices.txt")
	t.Chdir("../..")
	if err := writeNotices(context.Background(), ui, out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	for _, want := range []string{"github.com/spf13/cobra", "react 19.3.1", "Copyright (c) Meta Platforms"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("the notices lack %q", want)
		}
	}
	if bytes.Contains(data, []byte("vite")) {
		t.Error("build tools don't ship, so they aren't in the notices")
	}
}

func TestStandardTexts(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "x", "author": "Jane Doe <jane@example.com> (https://example.com)"}`), 0o644)
	if got := packageAuthor(dir); got != "Jane Doe" {
		t.Errorf("author = %q", got)
	}
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "x", "author": {"name": "Jane Doe"}}`), 0o644)
	if got := packageAuthor(dir); got != "Jane Doe" {
		t.Errorf("author = %q", got)
	}
	text, ok := standardText("MIT", "Jane Doe")
	if !ok || !strings.HasPrefix(text, "Copyright (c) Jane Doe\n\nPermission is hereby granted") || !slices.Equal(Classify(text), []string{"MIT"}) {
		t.Errorf("MIT text = %q", text)
	}
	if text, ok := standardText("ISC", ""); !ok || !slices.Equal(Classify(text), []string{"ISC"}) {
		t.Errorf("ISC text = %q", text)
	}
	if _, ok := standardText("Apache-2.0", "x"); ok {
		t.Error("only short licenses have a standard text here")
	}
}
