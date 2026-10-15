package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/theme"
	"github.com/vilanobeachflorida/racksail/packaging/spdx"
)

// Limits keep items small, since every panel with the marketplace on
// fetches what it offers.
const (
	maxItemFiles = 64
	maxFileSize  = 2 << 20
	maxItemSize  = 8 << 20
)

// kinds maps the catalog's directories to the item kinds they hold.
var kinds = map[string]recipes.ItemKind{
	"apps":           recipes.KindApp,
	"runtimes":       recipes.KindRuntime,
	"services":       recipes.KindService,
	"stacks":         recipes.KindStack,
	"dns-templates":  recipes.KindDNSTemplate,
	"automations":    recipes.KindAutomation,
	"tuning-presets": recipes.KindTuningPreset,
	"models":         recipes.KindModel,
	"themes":         recipes.KindTheme,
	"addons":         recipes.KindAddon,
}

// topLevel is what may sit at the top of a catalog besides the kind
// directories: its documentation, license, maintenance flags, the keys that
// sign its index and this tool.
var topLevel = map[string]bool{"README.md": true, "LICENSE": true, "maintenance.yaml": true, "keys": true, "tool": true}

// Files with fixed names in an item's directory.
const (
	recipeFile = "recipe.yaml"
	themeFile  = "theme.yaml"
	addonFile  = "addon.yaml"
)

// File is one file of an item.
type File struct {
	// Path is relative to the catalog root, with forward slashes.
	Path   string
	Size   int64
	SHA256 string
}

// Item is one catalog item: a directory holding its recipe and the files
// the recipe refers to.
type Item struct {
	// Dir is relative to the catalog root, such as apps/wordpress.
	Dir    string
	Recipe *recipes.Recipe
	Files  []File
}

// File returns the item's file with a path relative to its directory.
func (it *Item) File(name string) (File, bool) {
	want := it.Dir + "/" + name
	for _, f := range it.Files {
		if f.Path == want {
			return f, true
		}
	}
	return File{}, false
}

// Catalog is a checked catalog.
type Catalog struct {
	Root        string
	Items       []*Item
	Maintenance Maintenance
}

// Item returns the item with an id.
func (c *Catalog) Item(id string) (*Item, bool) {
	for _, it := range c.Items {
		if it.Recipe.ID == id {
			return it, true
		}
	}
	return nil, false
}

// problems collects everything wrong with a catalog, so one run reports all
// of it.
type problems []error

func (p *problems) add(where, format string, args ...any) {
	*p = append(*p, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
}

func (p problems) err() error { return errors.Join(p...) }

// Load reads the catalog in root and checks every item against the schema
// and the catalog's rules.
func Load(root string) (*Catalog, error) {
	c := &Catalog{Root: root}
	var probs problems
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("reading the catalog: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		kind, isKind := kinds[name]
		switch {
		case isKind && e.IsDir():
			c.loadKind(name, kind, &probs)
		case isKind:
			probs.add(name, "must be a directory of items")
		case !topLevel[name]:
			probs.add(name, "doesn't belong at the top of the catalog; items go in one of the kind directories (%s)", strings.Join(kindDirs(), ", "))
		}
	}
	sort.Slice(c.Items, func(i, j int) bool { return c.Items[i].Dir < c.Items[j].Dir })

	seen := map[string]string{}
	for _, it := range c.Items {
		if other, ok := seen[it.Recipe.ID]; ok {
			probs.add(it.Dir, "the id %s is already used by %s; ids are unique across the catalog", it.Recipe.ID, other)
			continue
		}
		seen[it.Recipe.ID] = it.Dir
	}

	c.Maintenance, err = loadMaintenance(filepath.Join(root, "maintenance.yaml"))
	if err != nil {
		probs.add("maintenance.yaml", "%v", err)
	} else {
		c.checkMaintenance(&probs)
	}
	if err := probs.err(); err != nil {
		return nil, err
	}
	return c, nil
}

func kindDirs() []string {
	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Catalog) loadKind(dir string, kind recipes.ItemKind, probs *problems) {
	entries, err := os.ReadDir(filepath.Join(c.Root, dir))
	if err != nil {
		probs.add(dir, "%v", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		rel := dir + "/" + name
		switch {
		case strings.HasPrefix(name, "."):
		case name == "README.md" && e.Type().IsRegular():
		case e.IsDir():
			if it := c.loadItem(rel, kind, probs); it != nil {
				c.Items = append(c.Items, it)
			}
		default:
			probs.add(rel, "every item is a directory; only README.md may sit next to them")
		}
	}
}

// loadItem reads an item's directory and checks it. It returns nil when the
// item can't be read well enough to check further.
func (c *Catalog) loadItem(rel string, kind recipes.ItemKind, probs *problems) *Item {
	it := &Item{Dir: rel}
	contents := map[string][]byte{}
	var total int64
	err := filepath.WalkDir(filepath.Join(c.Root, filepath.FromSlash(rel)), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		r, _ := filepath.Rel(c.Root, p)
		r = filepath.ToSlash(r)
		if strings.HasPrefix(name, ".") && r != rel {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			probs.add(r, "symbolic links aren't allowed in the catalog")
			return nil
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			probs.add(r, "only regular files are allowed in the catalog")
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileSize {
			probs.add(r, "is %d bytes; files in the catalog are at most %d", info.Size(), maxFileSize)
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		it.Files = append(it.Files, File{Path: r, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		contents[strings.TrimPrefix(r, rel+"/")] = data
		total += int64(len(data))
		return nil
	})
	if err != nil {
		probs.add(rel, "%v", err)
		return nil
	}
	sort.Slice(it.Files, func(i, j int) bool { return it.Files[i].Path < it.Files[j].Path })
	if len(it.Files) > maxItemFiles {
		probs.add(rel, "has %d files; an item has at most %d", len(it.Files), maxItemFiles)
	}
	if total > maxItemSize {
		probs.add(rel, "holds %d bytes; an item holds at most %d", total, maxItemSize)
	}

	data, ok := contents[recipeFile]
	if !ok {
		probs.add(rel, "has no %s", recipeFile)
		return nil
	}
	r, err := recipes.Parse(data)
	if err != nil {
		probs.add(rel+"/"+recipeFile, "%v", err)
		return nil
	}
	it.Recipe = r
	checkItem(it, kind, contents, probs)
	return it
}

// itemsWithHealth must say how to tell that they work, and itemsWithTests
// must have an install test, since they install running software or create
// resources.
var (
	itemsWithHealth = []recipes.ItemKind{recipes.KindApp, recipes.KindService, recipes.KindStack}
	itemsWithTests  = []recipes.ItemKind{
		recipes.KindApp, recipes.KindRuntime, recipes.KindService, recipes.KindStack,
		recipes.KindDNSTemplate, recipes.KindAutomation, recipes.KindTuningPreset, recipes.KindModel,
	}
)

func checkItem(it *Item, kind recipes.ItemKind, contents map[string][]byte, probs *problems) {
	r := it.Recipe
	where := it.Dir + "/" + recipeFile
	if base := path.Base(it.Dir); r.ID != base {
		probs.add(where, "the id is %s but the directory is %s; they must match", r.ID, base)
	}
	if r.Kind != kind {
		probs.add(where, "is a %s, so it belongs in %s, not %s", r.Kind, dirOf(r.Kind), path.Dir(it.Dir))
	}
	if !slices.ContainsFunc(r.Maintainers, func(m recipes.Maintainer) bool { return m.GitHub != "" }) {
		probs.add(where, "needs at least one maintainer with a GitHub account, so problems reach someone")
	}
	checkLicense(where, r, probs)

	if r.Icon == "" {
		probs.add(where, "needs an icon (an SVG or PNG file in the item's directory)")
	} else if data, ok := contents[r.Icon]; !ok {
		probs.add(where, "the icon %s isn't in the item's directory", r.Icon)
	} else if err := checkIcon(r.Icon, data); err != nil {
		probs.add(it.Dir+"/"+r.Icon, "%v", err)
	}

	if slices.Contains(itemsWithHealth, kind) && len(r.Health) == 0 {
		probs.add(where, "needs at least one health check, so a panel can tell that what it installed works")
	}
	if slices.Contains(itemsWithTests, kind) {
		if r.Tests.TimeoutSeconds == 0 {
			probs.add(where, "needs an install test: tests.timeoutSeconds, and tests.inputs for the inputs it requires")
		} else if _, err := r.Values(r.Tests.Inputs); err != nil {
			probs.add(where, "the install test's inputs don't work: %v", err)
		}
	}

	urls := map[string]bool{}
	var downloads int64
	for _, d := range r.Downloads {
		if urls[d.URL] {
			probs.add(where, "downloads %s twice", d.URL)
		}
		urls[d.URL] = true
		downloads += d.Size
	}
	if kind == recipes.KindModel && int64(r.Requires.MemoryMB)<<20 < downloads {
		probs.add(where, "requires.memoryMB is %d, but the model's files take %d MB; it can't fit in less memory than that", r.Requires.MemoryMB, (downloads+(1<<20)-1)>>20)
	}

	_, hasTheme := contents[themeFile]
	_, hasAddon := contents[addonFile]
	switch kind {
	case recipes.KindTheme:
		checkTheme(it, contents, probs)
	case recipes.KindAddon:
		checkAddon(it, contents, probs)
	}
	if hasTheme && kind != recipes.KindTheme {
		probs.add(it.Dir+"/"+themeFile, "only themes have a theme.yaml")
	}
	if hasAddon && kind != recipes.KindAddon {
		probs.add(it.Dir+"/"+addonFile, "only add-ons have an addon.yaml")
	}
}

func dirOf(kind recipes.ItemKind) string {
	for dir, k := range kinds {
		if k == kind {
			return dir
		}
	}
	return string(kind)
}

// checkLicense makes sure an item labelled open source only installs
// software under licenses the Open Source Initiative approved.
func checkLicense(where string, r *recipes.Recipe, probs *problems) {
	expr, err := spdx.Parse(r.License)
	if err != nil {
		probs.add(where, "license: %v", err)
		return
	}
	if r.LicenseClass != recipes.OpenSource {
		return
	}
	for _, l := range spdx.Licenses(expr) {
		if !spdx.OSIApproved(l.ID) {
			probs.add(where, "is labelled open-source, but %s isn't an OSI-approved license; label it free-to-use or proprietary, or correct the SPDX identifier", l.ID)
		}
	}
}

func checkTheme(it *Item, contents map[string][]byte, probs *problems) {
	where := it.Dir + "/" + themeFile
	data, ok := contents[themeFile]
	if !ok {
		probs.add(it.Dir, "is a theme, so it needs its %s next to the recipe", themeFile)
		return
	}
	t, err := theme.Parse(data)
	if err != nil {
		probs.add(where, "%v", err)
		return
	}
	r := it.Recipe
	if t.ID != r.ID || t.Version != r.Version || t.License != r.License {
		probs.add(where, "says %s %s (%s), but the recipe says %s %s (%s); they must agree", t.ID, t.Version, t.License, r.ID, r.Version, r.License)
	}
	for _, asset := range themeAssets(t) {
		if _, ok := contents[asset]; !ok {
			probs.add(where, "refers to %s, which isn't in the item's directory", asset)
		}
	}
}

// themeAssets lists every file a theme refers to.
func themeAssets(t *theme.Theme) []string {
	var out []string
	add := func(paths ...string) {
		for _, p := range paths {
			if p != "" {
				out = append(out, p)
			}
		}
	}
	a := t.Assets
	add(a.LogoLight, a.LogoDark, a.Mark, a.Favicon, a.LoginBackground)
	for _, p := range a.Illustrations {
		add(p)
	}
	add(t.Templates.Email, t.Templates.Error, t.Templates.Maintenance, t.Templates.Suspended)
	add(t.Tokens.Typography.Sans.Files...)
	add(t.Tokens.Typography.Mono.Files...)
	sort.Strings(out)
	return slices.Compact(out)
}

func checkAddon(it *Item, contents map[string][]byte, probs *problems) {
	where := it.Dir + "/" + addonFile
	data, ok := contents[addonFile]
	if !ok {
		probs.add(it.Dir, "is an add-on, so it needs its %s next to the recipe", addonFile)
		return
	}
	m, err := hooks.ParseAddonManifest(data)
	if err != nil {
		probs.add(where, "%v", err)
		return
	}
	r := it.Recipe
	if m.ID != r.ID || m.Version != r.Version || m.License != r.License {
		probs.add(where, "says %s %s (%s), but the recipe says %s %s (%s); they must agree", m.ID, m.Version, m.License, r.ID, r.Version, r.License)
	}
}

// pngSignature starts every PNG file.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

func checkIcon(name string, data []byte) error {
	switch strings.ToLower(path.Ext(name)) {
	case ".svg":
		return checkSVG(data)
	case ".png":
		if !bytes.HasPrefix(data, pngSignature) {
			return errors.New("isn't a PNG file")
		}
		return checkPNG(data)
	}
	return errors.New("icons are SVG or PNG files")
}
