package theme

import (
	"bytes"
	"testing"
)

// Themes, packages, SVG files, templates and custom CSS all come from
// outside, so their readers must never panic, whatever they're given.

func FuzzParse(f *testing.F) {
	d := Default()
	data, _ := d.YAML()
	f.Add(data)
	f.Add([]byte("format: 1\nid: x1\nname: x\nversion: 1.0.0\nlicense: MIT\ntokens: {}\n"))
	f.Add([]byte(`{"format":1,"tokens":{"light":{"criticalDanger":"#000000"}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		th, err := Parse(data)
		if err == nil && len(th.Check()) > 0 {
			t.Error("a theme with problems was accepted")
		}
	})
}

func FuzzReadArchive(f *testing.F) {
	f.Add([]byte("PK\x05\x06" + string(make([]byte, 18))))
	f.Add([]byte("not a zip"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadArchive(bytes.NewReader(data), int64(len(data)))
	})
}

func FuzzCheckSVG(f *testing.F) {
	f.Add([]byte(logoSVG))
	f.Add([]byte(`<svg onload="x()"/>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = CheckSVG(data)
	})
}

func FuzzCheckTemplate(f *testing.F) {
	src, _ := DefaultTemplate(TemplateEmail)
	f.Add(src)
	f.Add("{{#each}}")
	f.Fuzz(func(t *testing.T, src string) {
		if CheckTemplate(TemplateEmail, src) != nil {
			return
		}
		d := Default()
		out, err := d.RenderTemplate(TemplateEmail, src, map[string]string{"title": "<script>"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains([]byte(out), []byte("<script>")) && !bytes.Contains([]byte(src), []byte("<script>")) {
			t.Error("a variable's value got in unescaped")
		}
	})
}

func FuzzCheckCustomCSS(f *testing.F) {
	f.Add(".a { color: red }")
	f.Add("} :root { --rs-critical-danger: green }")
	f.Fuzz(func(t *testing.T, css string) {
		if CheckCustomCSS(css) != nil {
			return
		}
		// Accepted CSS never closes the layer it is wrapped in.
		wrapped, err := WrapCustomCSS(css)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stripCSS(wrapped); err != nil {
			t.Errorf("wrapped CSS is unbalanced: %v", err)
		}
	})
}
