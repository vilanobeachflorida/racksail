package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/spec"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixture = "testdata/catalog"

var built = time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

// copyTree copies the test catalog so a test can change it.
func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func replaceIn(t *testing.T, file, old, new string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(old)) {
		t.Fatalf("%s doesn't contain %q", file, old)
	}
	if err := os.WriteFile(file, bytes.Replace(data, []byte(old), []byte(new), 1), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	c, err := Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[recipes.ItemKind]bool{}
	for _, it := range c.Items {
		seen[it.Recipe.Kind] = true
		if _, ok := it.File(recipeFile); !ok {
			t.Errorf("%s: the recipe isn't among its files", it.Dir)
		}
	}
	for _, k := range kinds {
		if !seen[k] {
			t.Errorf("the test catalog has no %s", k)
		}
	}
	if len(c.Items) != len(kinds) {
		t.Errorf("loaded %d items, want %d", len(c.Items), len(kinds))
	}
	theme, _ := c.Item("contrast-theme")
	if len(theme.Files) != 4 {
		t.Errorf("the theme's files = %+v", theme.Files)
	}
}

func TestOfficialCatalog(t *testing.T) {
	if _, err := Load(".."); err != nil {
		t.Fatalf("the official catalog has problems:\n%v", err)
	}
}

func TestProblems(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, root string)
		want   string
	}{
		{"id and directory", func(t *testing.T, root string) {
			replaceIn(t, root+"/apps/hello-site/recipe.yaml", "id: hello-site", "id: other-site")
		}, "the id is other-site but the directory is hello-site"},
		{"kind and directory", func(t *testing.T, root string) {
			replaceIn(t, root+"/apps/hello-site/recipe.yaml", "kind: app", "kind: service")
		}, "is a service, so it belongs in services, not apps"},
		{"missing recipe", func(t *testing.T, root string) {
			os.Remove(root + "/apps/hello-site/recipe.yaml")
		}, "has no recipe.yaml"},
		{"schema", func(t *testing.T, root string) {
			replaceIn(t, root+"/apps/hello-site/recipe.yaml", "licenseClass: open-source", "licenseClass: maybe")
		}, "apps/hello-site/recipe.yaml"},
		{"missing icon", func(t *testing.T, root string) {
			os.Remove(root + "/apps/hello-site/icon.svg")
		}, "the icon icon.svg isn't in the item's directory"},
		{"no icon", func(t *testing.T, root string) {
			replaceIn(t, root+"/apps/hello-site/recipe.yaml", "icon: icon.svg\n", "")
		}, "needs an icon"},
		{"icon with a script", func(t *testing.T, root string) {
			os.WriteFile(root+"/apps/hello-site/icon.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0o644)
		}, "contains a <script> element"},
		{"broken png", func(t *testing.T, root string) {
			os.WriteFile(root+"/services/cache-service/icon.png", []byte("GIF89a"), 0o644)
		}, "isn't a PNG file"},
		{"unknown top-level entry", func(t *testing.T, root string) {
			os.WriteFile(root+"/notes.txt", []byte("x"), 0o644)
		}, "notes.txt: doesn't belong at the top of the catalog"},
		{"file next to items", func(t *testing.T, root string) {
			os.WriteFile(root+"/apps/notes.txt", []byte("x"), 0o644)
		}, "apps/notes.txt: every item is a directory"},
		{"symbolic link", func(t *testing.T, root string) {
			if err := os.Symlink("/etc/hostname", root+"/apps/hello-site/link"); err != nil {
				t.Skip("symbolic links aren't available:", err)
			}
		}, "symbolic links aren't allowed"},
		{"large file", func(t *testing.T, root string) {
			os.WriteFile(root+"/apps/hello-site/big.bin", make([]byte, maxFileSize+1), 0o644)
		}, "files in the catalog are at most"},
		{"theme and recipe disagree", func(t *testing.T, root string) {
			replaceIn(t, root+"/themes/contrast-theme/theme.yaml", "version: 1.0.0", "version: 1.0.1")
		}, "they must agree"},
		{"theme asset missing", func(t *testing.T, root string) {
			os.Remove(root + "/themes/contrast-theme/mark.svg")
		}, "refers to mark.svg"},
		{"theme without its package", func(t *testing.T, root string) {
			os.Remove(root + "/themes/contrast-theme/theme.yaml")
		}, "needs its theme.yaml"},
		{"add-on and recipe disagree", func(t *testing.T, root string) {
			replaceIn(t, root+"/addons/notes-addon/addon.yaml", "license: MIT", "license: Apache-2.0")
		}, "they must agree"},
		{"add-on without its manifest", func(t *testing.T, root string) {
			os.Remove(root + "/addons/notes-addon/addon.yaml")
		}, "needs its addon.yaml"},
		{"package in the wrong kind", func(t *testing.T, root string) {
			os.WriteFile(root+"/apps/hello-site/theme.yaml", []byte("format: 1\n"), 0o644)
		}, "only themes have a theme.yaml"},
		{"open source label on another license", func(t *testing.T, root string) {
			replaceIn(t, root+"/apps/hello-site/recipe.yaml", "license: MIT", "license: LicenseRef-Example")
		}, "isn't an OSI-approved license"},
		{"no maintainer on GitHub", func(t *testing.T, root string) {
			replaceIn(t, root+"/apps/hello-site/recipe.yaml", "{name: Example maintainers, github: example}", "{name: Example maintainers}")
		}, "needs at least one maintainer with a GitHub account"},
		{"no health check", func(t *testing.T, root string) {
			replaceIn(t, root+"/apps/hello-site/recipe.yaml", "health:\n  - {type: http, target: \"https://${inputs.domain}/\", expect: \"200\"}\n", "")
		}, "needs at least one health check"},
		{"no install test", func(t *testing.T, root string) {
			replaceIn(t, root+"/runtimes/php-runtime/recipe.yaml", "tests:\n  timeoutSeconds: 1800\n", "")
		}, "needs an install test"},
		// Every official item has a health check and an install test, those
		// that run nothing or create no resources too.
		{"theme without a health check", func(t *testing.T, root string) {
			replaceIn(t, root+"/themes/contrast-theme/recipe.yaml", "health:\n  - {type: resource, target: contrast-theme}\n", "")
		}, "themes/contrast-theme/recipe.yaml: needs at least one health check, so a panel can tell that what it installed works: " +
			"a check of what it runs, or a resource check of what it creates or of the item itself (type: resource, target: contrast-theme)"},
		{"add-on without an install test", func(t *testing.T, root string) {
			replaceIn(t, root+"/addons/notes-addon/recipe.yaml", "tests:\n  timeoutSeconds: 120\n", "")
		}, "addons/notes-addon/recipe.yaml: needs an install test"},
		{"DNS template without a health check", func(t *testing.T, root string) {
			replaceIn(t, root+"/dns-templates/mail-records/recipe.yaml", "health:\n  - {type: resource, target: mx}\n  - {type: resource, target: spf}\n", "")
		}, "dns-templates/mail-records/recipe.yaml: needs at least one health check"},
		{"resource check of a resource the recipe doesn't create", func(t *testing.T, root string) {
			replaceIn(t, root+"/automations/uptime-alerts/recipe.yaml", "target: workflow}", "target: schedule}")
		}, "health check 1 waits for schedule, which is neither one of its resources nor the recipe's id"},
		{"install test without a required input", func(t *testing.T, root string) {
			replaceIn(t, root+"/stacks/blog-stack/recipe.yaml", ", dbPassword: correct-horse-battery", "")
		}, "the install test's inputs don't work"},
		{"download listed twice", func(t *testing.T, root string) {
			replaceIn(t, root+"/apps/hello-site/recipe.yaml", "health:", "  - url: https://downloads.example.com/hello-site-1.0.0.tar.gz\n    sha256: f92b7faf2625dbd94e941242e8720b1948fa0e2193da3c99d0afcdbd20dc6e61\n    size: 2048\nhealth:")
		}, "downloads https://downloads.example.com/hello-site-1.0.0.tar.gz twice"},
		{"model larger than its memory", func(t *testing.T, root string) {
			replaceIn(t, root+"/models/tiny-model/recipe.yaml", "memoryMB: 1024", "memoryMB: 256")
		}, "it can't fit in less memory than that"},
		{"duplicate id", func(t *testing.T, root string) {
			src := root + "/apps/hello-site"
			dst := root + "/stacks/hello-site"
			os.MkdirAll(dst, 0o755)
			for _, f := range []string{"recipe.yaml", "icon.svg"} {
				data, _ := os.ReadFile(src + "/" + f)
				os.WriteFile(dst+"/"+f, data, 0o644)
			}
		}, "the id hello-site is already used by"},
		{"featured item missing", func(t *testing.T, root string) {
			replaceIn(t, root+"/maintenance.yaml", "featured: [hello-site, blog-stack]", "featured: [hello-site, nowhere]")
		}, "features nowhere, which isn't in the catalog"},
		{"featured and broken", func(t *testing.T, root string) {
			replaceIn(t, root+"/maintenance.yaml", "featured: [hello-site, blog-stack]", "featured: [hello-site, fast-site]")
		}, "fast-site is featured and broken"},
		{"date that isn't one", func(t *testing.T, root string) {
			replaceIn(t, root+"/maintenance.yaml", "since: 2026-09-01", "since: last week")
		}, "must be a date"},
		{"unknown maintenance field", func(t *testing.T, root string) {
			os.WriteFile(root+"/maintenance.yaml", []byte("hidden: [hello-site]\n"), 0o644)
		}, "maintenance.yaml: reading it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := copyTree(t, fixture)
			tc.change(t, root)
			_, err := Load(root)
			if err == nil {
				t.Fatalf("Load should report %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load reported:\n%v\nwant a problem containing %q", err, tc.want)
			}
		})
	}
}

func TestIndexMatchesTheGoldenFile(t *testing.T) {
	c, err := Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	data, err := encodeIndex(BuildIndex(c, IndexOptions{Name: "test", Built: built, HideAfter: DefaultHideAfter}))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "index.golden.json")
	if *update {
		if err := os.WriteFile(golden, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("the index differs from %s; run go test ./catalog/tool -update after an intended change\n%s", golden, data)
	}
}

func TestMaintenanceFlags(t *testing.T) {
	c, err := Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	idx := BuildIndex(c, IndexOptions{Name: "test", Built: built, HideAfter: DefaultHideAfter})
	flags := map[string]recipes.IndexItem{}
	for _, it := range idx.Items {
		flags[it.ID] = it
	}
	if !flags["hello-site"].Featured || !flags["fast-site"].Broken || !flags["uptime-alerts"].Unmaintained {
		t.Errorf("flags = %+v", flags)
	}
	if flags["php-runtime"].Featured || flags["php-runtime"].Broken || flags["php-runtime"].Unmaintained {
		t.Error("php-runtime has no maintenance flags")
	}

	// A year later the unmaintained item is left out.
	later := BuildIndex(c, IndexOptions{Name: "test", Built: built.AddDate(1, 0, 0), HideAfter: DefaultHideAfter})
	if len(later.Items) != len(idx.Items)-1 || slices.ContainsFunc(later.Items, func(it recipes.IndexItem) bool { return it.ID == "uptime-alerts" }) {
		t.Errorf("items a year later = %d", len(later.Items))
	}
}

func TestPublishVerifyAndArchive(t *testing.T) {
	keys := t.TempDir()
	pub, err := GenerateKeys(keys+"/signing.key", keys+"/signing.pub")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(keys + "/signing.key")
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the signing key is readable by others: %v", info.Mode())
	}
	if _, err := GenerateKeys(keys+"/signing.key", keys+"/other.pub"); err == nil {
		t.Error("keygen must not overwrite a key")
	}
	data, _ := os.ReadFile(keys + "/signing.key")
	key, err := ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(key.Public()) {
		t.Fatal("the key pair doesn't match")
	}

	c, err := Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	idx := BuildIndex(c, IndexOptions{Name: "test", Built: built})
	out := filepath.Join(t.TempDir(), "published")
	if err := Publish(c, idx, out, key); err != nil {
		t.Fatal(err)
	}
	if err := Publish(c, idx, out, key); err == nil {
		t.Error("publishing into a directory that isn't empty must fail")
	}
	got, err := Verify(out, pub)
	if err != nil || len(got.Items) != len(c.Items) {
		t.Fatalf("Verify = %v items, %v", got, err)
	}

	// Archives of the same catalog are identical.
	a1, a2 := filepath.Join(t.TempDir(), "a.tar.gz"), filepath.Join(t.TempDir(), "b.tar.gz")
	if err := Archive(out, built, a1); err != nil {
		t.Fatal(err)
	}
	if err := Archive(out, built, a2); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(a1)
	b2, _ := os.ReadFile(a2)
	if !bytes.Equal(b1, b2) {
		t.Error("archiving the same catalog twice gave different files")
	}
	names := tarNames(t, b1)
	for _, want := range []string{indexFile, signatureFile, "apps/hello-site/recipe.yaml", "themes/contrast-theme/mark.svg"} {
		if !slices.Contains(names, want) {
			t.Errorf("the archive lacks %s: %v", want, names)
		}
	}

	// Anything changed after signing is caught.
	tampered := copyTree(t, out)
	os.WriteFile(tampered+"/apps/hello-site/icon.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	if _, err := Verify(tampered, pub); err == nil || !strings.Contains(err.Error(), "doesn't match the index") {
		t.Errorf("a changed file must fail verification: %v", err)
	}
	forged := copyTree(t, out)
	replaceIn(t, forged+"/"+indexFile, `"catalog": "test"`, `"catalog": "evil"`)
	if _, err := Verify(forged, pub); err == nil {
		t.Error("a changed index must fail verification")
	}
	other, err := GenerateKeys(keys+"/other.key", keys+"/other2.pub")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(out, other); err == nil {
		t.Error("another key must not verify the catalog")
	}
}

func tarNames(t *testing.T, data []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		if !hdr.ModTime.Equal(built) || hdr.Uid != 0 || hdr.Uname != "" {
			t.Errorf("%s: header isn't reproducible: %+v", hdr.Name, hdr)
		}
		names = append(names, hdr.Name)
	}
}

func TestCommands(t *testing.T) {
	keys := t.TempDir()
	var out, errOut bytes.Buffer
	cli := func(args ...string) error {
		out.Reset()
		errOut.Reset()
		return run(t.Context(), args, &out, &errOut)
	}
	if err := cli("validate", "-dir", fixture); err != nil || !strings.Contains(out.String(), "10 items, no problems") {
		t.Fatalf("validate: %v\n%s%s", err, out.String(), errOut.String())
	}
	if err := cli("keygen", "-private", keys+"/k", "-public", keys+"/k.pub"); err != nil {
		t.Fatal(err)
	}
	if err := cli("keygen", "-private", keys+"/o", "-public", keys+"/o.pub"); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(t.TempDir(), "out")
	err := cli("index", "-dir", fixture, "-out", published, "-key", keys+"/k", "-public", keys+"/o.pub", "-time", "2026-10-15T00:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "doesn't belong to") {
		t.Errorf("a key that doesn't match -public must be refused: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "catalog.tar.gz")
	t.Setenv("CATALOG_TEST_KEY", mustRead(t, keys+"/k"))
	err = cli("index", "-dir", fixture, "-out", published, "-key-env", "CATALOG_TEST_KEY", "-public", keys+"/k.pub", "-time", "2026-10-15T00:00:00Z", "-archive", archive)
	if err != nil || !strings.Contains(out.String(), "10 of 10 items, signed") {
		t.Fatalf("index: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(archive); err != nil {
		t.Error(err)
	}
	if err := cli("verify", "-dir", published, "-public", keys+"/k.pub"); err != nil || !strings.Contains(out.String(), "signature good") {
		t.Errorf("verify: %v\n%s", err, out.String())
	}
	if err := cli("verify", "-dir", published, "-public", keys+"/o.pub"); err == nil {
		t.Error("verify with another key must fail")
	}
	if err := cli("index", "-dir", fixture); err == nil {
		t.Error("index without -out must fail")
	}
	if err := cli("nonsense"); err == nil {
		t.Error("an unknown command must fail")
	}
}

func mustRead(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The recipe schema and the recipe types describe the same fields, so
// whatever the schema accepts the parser can read, and the other way round.
func TestSchemaMatchesTypes(t *testing.T) {
	raw, err := spec.SchemaJSON(spec.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	defs, _ := schema["$defs"].(map[string]any)
	resolve := func(node map[string]any) map[string]any {
		if ref, ok := node["$ref"].(string); ok {
			def, _ := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			return def
		}
		if items, ok := node["items"].(map[string]any); ok {
			return resolveItems(items, defs)
		}
		return node
	}
	var compare func(path string, node map[string]any, typ reflect.Type)
	compare = func(path string, node map[string]any, typ reflect.Type) {
		node = resolve(node)
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		props, _ := node["properties"].(map[string]any)
		var schemaFields, goFields []string
		for name := range props {
			schemaFields = append(schemaFields, name)
		}
		fields := map[string]reflect.StructField{}
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			goFields = append(goFields, name)
			fields[name] = f
		}
		sort.Strings(schemaFields)
		sort.Strings(goFields)
		if !slices.Equal(schemaFields, goFields) {
			t.Errorf("%s: the schema has %v, the Go type %s has %v", path, schemaFields, typ, goFields)
			return
		}
		for name, child := range props {
			childNode, _ := child.(map[string]any)
			ft := fields[name].Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				compare(path+"."+name, childNode, ft)
			}
		}
	}
	compare("recipe", schema, reflect.TypeFor[recipes.Recipe]())
}

func resolveItems(items map[string]any, defs map[string]any) map[string]any {
	if ref, ok := items["$ref"].(string); ok {
		def, _ := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		return def
	}
	return items
}
