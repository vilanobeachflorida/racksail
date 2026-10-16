package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Package is an npm package from a lockfile.
type Package struct {
	Name    string
	Version string
	License string
	// Dev packages are only used to build and test.
	Dev bool
	// Path is where npm installs it, relative to the lockfile.
	Path string
}

type lockfile struct {
	LockfileVersion int                        `json:"lockfileVersion"`
	Packages        map[string]json.RawMessage `json:"packages"`
}

type lockEntry struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	License     json.RawMessage `json:"license"`
	Dev         bool            `json:"dev"`
	DevOptional bool            `json:"devOptional"`
	Link        bool            `json:"link"`
}

// NpmPackages reads every package a lockfile installs. Workspace links and
// the project itself are left out.
func NpmPackages(file string) ([]Package, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var lf lockfile
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if lf.LockfileVersion < 2 {
		return nil, fmt.Errorf("%s is lockfile version %d; regenerate it with npm 7 or later", file, lf.LockfileVersion)
	}
	var out []Package
	for path, raw := range lf.Packages {
		if path == "" || !strings.Contains(path, "node_modules/") {
			continue
		}
		var e lockEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", file, path, err)
		}
		if e.Link {
			continue
		}
		name := e.Name
		if name == "" {
			name = path[strings.LastIndex(path, "node_modules/")+len("node_modules/"):]
		}
		out = append(out, Package{Name: name, Version: e.Version, License: licenseField(e.License), Dev: e.Dev || e.DevOptional, Path: filepath.FromSlash(path)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// licenseField reads the license as npm records it: an SPDX expression, or
// in old packages an object with a type or a list of them.
func licenseField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Type != "" {
		return obj.Type
	}
	var list []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &list) == nil {
		var types []string
		for _, l := range list {
			types = append(types, l.Type)
		}
		if len(types) > 1 {
			return "(" + strings.Join(types, " OR ") + ")"
		}
		return strings.Join(types, "")
	}
	return ""
}
