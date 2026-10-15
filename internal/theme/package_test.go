package theme

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

const logoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs><linearGradient id="g"/></defs><rect width="10" height="10" fill="url(#g)"/><use href="#g"/></svg>`

var (
	pngFile   = []byte("\x89PNG\r\n\x1a\nrest")
	woff2File = []byte("wOF2rest")
)

func packageFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	th := Default()
	th.ID, th.Name = "acme", "Acme"
	th.Assets = Assets{LogoLight: "logo.svg", Favicon: "favicon.png", Illustrations: map[string]string{"empty": "art/empty.svg"}}
	th.Tokens.Typography.Sans = Font{Family: "Acme Sans", Files: []string{"fonts/acme.woff2"}}
	th.Templates = Templates{Error: "templates/error.html"}
	data, err := th.YAML()
	if err != nil {
		t.Fatal(err)
	}
	errorPage, _ := DefaultTemplate(TemplateError)
	return fstest.MapFS{
		"theme.yaml":           {Data: data},
		"logo.svg":             {Data: []byte(logoSVG)},
		"favicon.png":          {Data: pngFile},
		"art/empty.svg":        {Data: []byte(logoSVG)},
		"fonts/acme.woff2":     {Data: woff2File},
		"templates/error.html": {Data: []byte(errorPage)},
		"LICENSE":              {Data: []byte("MIT")},
		".DS_Store":            {Data: []byte("junk")},
	}
}

func TestReadPackage(t *testing.T) {
	pkg, err := ReadPackage(packageFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Theme.ID != "acme" || len(pkg.Files) != 6 {
		t.Errorf("package = %s with %d files", pkg.Theme.ID, len(pkg.Files))
	}
	if _, ok := pkg.Files[".DS_Store"]; ok {
		t.Error("junk files are skipped")
	}
	if logo := pkg.Logo(); logo == nil || !strings.HasPrefix(logo.DataURL(), "data:image/svg+xml;base64,") {
		t.Errorf("logo = %v", logo)
	}
}

func TestReadPackageRefusals(t *testing.T) {
	tests := []struct {
		name   string
		change func(fstest.MapFS)
		want   string
	}{
		{"missing theme", func(m fstest.MapFS) { delete(m, "theme.yaml") }, "no theme.yaml"},
		{"missing file", func(m fstest.MapFS) { delete(m, "favicon.png") }, "isn't in the package"},
		{"unused file", func(m fstest.MapFS) { m["extra.png"] = &fstest.MapFile{Data: pngFile} }, "extra.png isn't used"},
		{"disguised file", func(m fstest.MapFS) { m["favicon.png"] = &fstest.MapFile{Data: []byte("<html>")} }, "isn't a png file"},
		{"disguised font", func(m fstest.MapFS) { m["fonts/acme.woff2"] = &fstest.MapFile{Data: pngFile} }, "isn't a woff2 file"},
		{"script in an SVG", func(m fstest.MapFS) {
			m["logo.svg"] = &fstest.MapFile{Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)}
		}, "<script>"},
		{"remote image in an SVG", func(m fstest.MapFS) {
			m["art/empty.svg"] = &fstest.MapFile{Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url(https://x.example/a.svg)"/></svg>`)}
		}, "only parts of the same file"},
		{"script in a template", func(m fstest.MapFS) {
			m["templates/error.html"] = &fstest.MapFile{Data: []byte(`<p>{{message}}</p><script>x()</script>`)}
		}, "<script"},
		{"symlink", func(m fstest.MapFS) {
			m["logo.svg"] = &fstest.MapFile{Data: []byte("x"), Mode: fs.ModeSymlink}
		}, "regular file"},
		{"large file", func(m fstest.MapFS) {
			m["favicon.png"] = &fstest.MapFile{Data: append(append([]byte{}, pngFile...), make([]byte, MaxFileBytes)...)}
		}, "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := packageFiles(t)
			tt.change(files)
			_, err := ReadPackage(files)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
	// A wrong type for a field is caught before the file's contents.
	files := packageFiles(t)
	th := Default()
	th.Assets.Favicon = "fonts/acme.woff2"
	data, _ := th.YAML()
	files["theme.yaml"] = &fstest.MapFile{Data: data}
	if _, err := ReadPackage(files); err == nil || !strings.Contains(err.Error(), "assets.favicon must be one of") {
		t.Errorf("err = %v", err)
	}
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadArchive(t *testing.T) {
	files := map[string][]byte{}
	for name, f := range packageFiles(t) {
		files["acme/"+name] = f.Data
	}
	files["__MACOSX/acme/._logo.svg"] = []byte("junk")
	data := zipOf(t, files)
	pkg, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Theme.ID != "acme" || pkg.Files["logo.svg"] == nil {
		t.Errorf("package = %+v", pkg.Theme.ID)
	}
	for name, archive := range map[string]map[string][]byte{
		"escape":    {"../theme.yaml": []byte("x")},
		"absolute":  {"/etc/passwd": []byte("x")},
		"backslash": {`a\..\theme.yaml`: []byte("x")},
	} {
		data := zipOf(t, archive)
		if _, err := ReadArchive(bytes.NewReader(data), int64(len(data))); err == nil || !strings.Contains(err.Error(), "outside the package") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := ReadArchive(bytes.NewReader([]byte("not a zip")), 9); err == nil {
		t.Error("garbage isn't an archive")
	}
}

func TestCheckSVG(t *testing.T) {
	if err := CheckSVG([]byte(logoSVG)); err != nil {
		t.Errorf("a plain drawing: %v", err)
	}
	for name, svg := range map[string]string{
		"not svg":        `<html></html>`,
		"empty":          ``,
		"handler":        `<svg xmlns="http://www.w3.org/2000/svg" onload="x()"/>`,
		"external link":  `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="https://x.example/s.svg#a"/></svg>`,
		"foreign object": `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject/></svg>`,
		"image":          `<svg xmlns="http://www.w3.org/2000/svg"><image href="#x"/></svg>`,
		"entity":         `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY a "aaa">]><svg xmlns="http://www.w3.org/2000/svg">&a;</svg>`,
		"import":         `<svg xmlns="http://www.w3.org/2000/svg"><style>@import "x.css";</style></svg>`,
		"stylesheet":     `<?xml-stylesheet href="x.css"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"broken":         `<svg xmlns="http://www.w3.org/2000/svg"><g></svg>`,
		// Escapes and image functions can't hide a reference from the checks.
		"escaped import":      `<svg xmlns="http://www.w3.org/2000/svg"><style>@\69mport "https://x.example/a.css";</style></svg>`,
		"image-set":           `<svg xmlns="http://www.w3.org/2000/svg"><style>rect{fill:image-set("https://x.example/a.png" 1x)}</style></svg>`,
		"escaped url":         `<svg xmlns="http://www.w3.org/2000/svg"><rect fill="u\72l(https://x.example/a.svg#p)"/></svg>`,
		"style attribute url": `<svg xmlns="http://www.w3.org/2000/svg"><rect style="filter: url('https://x.example/f.svg#f')"/></svg>`,
		"cdata import":        `<svg xmlns="http://www.w3.org/2000/svg"><style><![CDATA[@import "x.css";]]></style></svg>`,
	} {
		if err := CheckSVG([]byte(svg)); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	styled := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><style>.a{fill:#01214F}@media (prefers-color-scheme:dark){.a{fill:#FFFFFF}}</style>` +
		`<defs><linearGradient id="g"/></defs><rect class="a" style="stroke: url(#g)" fill="url(#g)" width="10" height="10"/><text x="1" y="9">a\b</text></svg>`
	if err := CheckSVG([]byte(styled)); err != nil {
		t.Errorf("styles that refer to the image's own parts are fine: %v", err)
	}
}
