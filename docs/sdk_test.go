package docs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// goExports lists what a Go package exports at its top level.
func goExports(t *testing.T, dir string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					out[d.Name.Name] = d.Name.IsExported()
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						out[s.Name.Name] = s.Name.IsExported()
					case *ast.ValueSpec:
						for _, n := range s.Names {
							out[n.Name] = n.IsExported()
						}
					}
				}
			}
		}
	}
	return out
}

var (
	tsExport = regexp.MustCompile(`(?m)^export\s+(?:declare\s+)?(?:const|let|function|class|type|interface|enum)\s+(\w+)`)
	pyName   = regexp.MustCompile(`(?m)^(?:(?:def|class)\s+(\w+)|(\w+)\s*(?::[^=\n]*)?=)`)
	pyAll    = regexp.MustCompile(`(?s)__all__\s*=\s*\[(.*?)\]`)
	pyQuoted = regexp.MustCompile(`"(\w+)"`)
)

// tsExports lists what a TypeScript module exports.
func tsExports(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range tsExport.FindAllStringSubmatch(read(t, path), -1) {
		out[m[1]] = true
	}
	return out
}

// pyExports lists what a Python module offers: the names in __all__, which
// it defines.
func pyExports(t *testing.T, path string) map[string]bool {
	t.Helper()
	src := read(t, path)
	defined := map[string]bool{}
	for _, m := range pyName.FindAllStringSubmatch(src, -1) {
		defined[m[1]+m[2]] = true
	}
	all := pyAll.FindStringSubmatch(src)
	if all == nil {
		t.Fatalf("%s has no __all__", path)
	}
	out := map[string]bool{}
	for _, m := range pyQuoted.FindAllStringSubmatch(all[1], -1) {
		out[m[1]] = defined[m[1]]
	}
	return out
}

var codeBlock = regexp.MustCompile("(?ms)^```(\\w*)\n(.*?)\n```$")

// codeBlocks returns the code blocks of a Markdown document in a language.
func codeBlocks(md, lang string) []string {
	var out []string
	for _, m := range codeBlock.FindAllStringSubmatch(md, -1) {
		if m[1] == lang {
			out = append(out, m[2])
		}
	}
	return out
}

// TestSDKReadmesNameWhatExists keeps the SDKs' READMEs to what the SDKs
// define: every name sdk/README.md's table gives, and every name the
// examples in each SDK's README use, is one the SDK exports.
func TestSDKReadmesNameWhatExists(t *testing.T) {
	sdks := []struct {
		column  string
		readme  string
		exports map[string]bool
		// prefix is how code names the SDK, and uses finds the names an
		// example uses.
		prefix string
		uses   *regexp.Regexp
		lang   string
	}{
		{"Go", "../sdk/go/README.md", goExports(t, "../sdk/go"), "addon.", regexp.MustCompile(`\baddon\.(\w+)`), "go"},
		{"TypeScript", "../sdk/ts/README.md", tsExports(t, "../sdk/ts/src/index.ts"), "",
			regexp.MustCompile(`import\s*\{([^}]*)\}\s*from\s*"@racksail/addon-sdk"`), "ts"},
		{"Python", "../sdk/python/README.md", pyExports(t, "../sdk/python/src/racksail_addon/__init__.py"), "racksail_addon.",
			regexp.MustCompile(`\bracksail_addon\.(\w+)`), "python"},
	}

	var header []string
	rows := 0
	for _, line := range strings.Split(read(t, "../sdk/README.md"), "\n") {
		if !strings.HasPrefix(line, "|") {
			header = nil
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if header == nil {
			header = cells
			continue
		}
		if strings.HasPrefix(cells[0], "---") || len(header) < 4 || header[1] != "Go" {
			continue
		}
		rows++
		for i, sdk := range sdks {
			if header[i+1] != sdk.column {
				t.Fatalf("sdk/README.md's table has %q where %s belongs", header[i+1], sdk.column)
			}
			name := strings.TrimPrefix(strings.Trim(cells[i+1], "`"), sdk.prefix)
			if !sdk.exports[name] {
				t.Errorf("sdk/README.md says the %s SDK has %s, which it doesn't export", sdk.column, cells[i+1])
			}
		}
	}
	if rows == 0 {
		t.Error("sdk/README.md has no table of what each SDK has")
	}

	for _, sdk := range sdks {
		blocks := codeBlocks(read(t, sdk.readme), sdk.lang)
		if len(blocks) == 0 {
			t.Errorf("%s has no %s example", sdk.readme, sdk.lang)
		}
		for _, block := range blocks {
			for _, m := range sdk.uses.FindAllStringSubmatch(block, -1) {
				for _, name := range strings.Split(m[1], ",") {
					if name = strings.TrimSpace(name); name != "" && !sdk.exports[name] {
						t.Errorf("%s uses %s%s, which the SDK doesn't export", sdk.readme, sdk.prefix, name)
					}
				}
			}
		}
	}
}
