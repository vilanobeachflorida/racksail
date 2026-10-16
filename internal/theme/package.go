package theme

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
)

// Limits on theme packages, which people upload and catalogs distribute.
const (
	MaxFileBytes    = 2 << 20
	MaxPackageBytes = 10 << 20
	MaxFiles        = 200
)

// ThemeFile is the theme's own file inside a package.
const ThemeFile = "theme.yaml"

// Package is a theme with the files it refers to.
type Package struct {
	Theme Theme
	// Files maps paths inside the package to their contents.
	Files map[string][]byte
}

// File types by extension, and which assets may use them.
var (
	imageTypes    = []string{".svg", ".png", ".jpg", ".jpeg", ".webp"}
	faviconTypes  = []string{".svg", ".png", ".ico"}
	fontTypes     = []string{".woff2", ".woff"}
	templateTypes = []string{".html"}
)

// Files the operating system or an archiver leaves behind, which are
// skipped rather than refused.
func isJunk(p string) bool {
	base := path.Base(p)
	return base == ".DS_Store" || base == "Thumbs.db" || strings.HasPrefix(p, "__MACOSX/")
}

// ReadPackage reads a theme package from a directory tree: theme.yaml and
// every file it refers to. Symbolic links and other special files are
// refused, as are files the theme doesn't use.
func ReadPackage(fsys fs.FS) (*Package, error) {
	files := map[string][]byte{}
	total := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." || isJunk(p) {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s isn't a regular file; theme packages can't hold links or devices", p)
		}
		if len(files) >= MaxFiles {
			return fmt.Errorf("the package has more than %d files", MaxFiles)
		}
		data, err := readLimited(fsys, p)
		if err != nil {
			return err
		}
		total += len(data)
		if total > MaxPackageBytes {
			return fmt.Errorf("the package is larger than %d MiB", MaxPackageBytes>>20)
		}
		files[p] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return checkPackage(files)
}

func readLimited(fsys fs.FS, p string) ([]byte, error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%s is larger than %d MiB", p, MaxFileBytes>>20)
	}
	return data, nil
}

// ReadArchive reads a theme package from a zip archive, as uploaded in the
// panel. When every file sits in one folder, as when someone zips the
// folder itself, that folder is the package.
func ReadArchive(r io.ReaderAt, size int64) (*Package, error) {
	if size > MaxPackageBytes*2 {
		return nil, fmt.Errorf("the archive is larger than %d MiB", MaxPackageBytes*2>>20)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("the theme package isn't a zip archive: %w", err)
	}
	files := map[string][]byte{}
	var total uint64
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") || isJunk(name) {
			continue
		}
		if !fs.ValidPath(name) || strings.Contains(name, `\`) {
			return nil, fmt.Errorf("the archive holds %q, which points outside the package", name)
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("%s isn't a regular file; theme packages can't hold links or devices", name)
		}
		if len(files) >= MaxFiles {
			return nil, fmt.Errorf("the package has more than %d files", MaxFiles)
		}
		if f.UncompressedSize64 > MaxFileBytes {
			return nil, fmt.Errorf("%s is larger than %d MiB", name, MaxFileBytes>>20)
		}
		total += f.UncompressedSize64
		if total > MaxPackageBytes {
			return nil, fmt.Errorf("the package is larger than %d MiB", MaxPackageBytes>>20)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		// The sizes in the directory may lie, so the reads are bounded too.
		data, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		if len(data) > MaxFileBytes {
			return nil, fmt.Errorf("%s is larger than %d MiB", name, MaxFileBytes>>20)
		}
		files[name] = data
	}
	return checkPackage(stripFolder(files))
}

// stripFolder removes a folder every file shares, unless theme.yaml is
// already at the top.
func stripFolder(files map[string][]byte) map[string][]byte {
	if _, ok := files[ThemeFile]; ok || len(files) == 0 {
		return files
	}
	prefix := ""
	for p := range files {
		dir, _, ok := strings.Cut(p, "/")
		if !ok || (prefix != "" && dir != prefix) {
			return files
		}
		prefix = dir
	}
	out := make(map[string][]byte, len(files))
	for p, data := range files {
		out[strings.TrimPrefix(p, prefix+"/")] = data
	}
	return out
}

// Files that may accompany a theme without it referring to them.
func isDocument(p string) bool {
	base := strings.ToUpper(path.Base(p))
	return !strings.Contains(p, "/") && (strings.HasPrefix(base, "LICENSE") || strings.HasPrefix(base, "README"))
}

func checkPackage(files map[string][]byte) (*Package, error) {
	data, ok := files[ThemeFile]
	if !ok {
		return nil, fmt.Errorf("the package has no %s", ThemeFile)
	}
	t, err := Parse(data)
	if err != nil {
		return nil, err
	}
	var errs []error
	used := map[string]bool{ThemeFile: true}
	for _, ref := range t.references() {
		used[ref.path] = true
		content, ok := files[ref.path]
		if !ok {
			errs = append(errs, fmt.Errorf("%s refers to %s, which isn't in the package", ref.field, ref.path))
			continue
		}
		ext := strings.ToLower(path.Ext(ref.path))
		if !slices.Contains(ref.types, ext) {
			errs = append(errs, fmt.Errorf("%s must be one of %s, not %s", ref.field, strings.Join(ref.types, ", "), ref.path))
			continue
		}
		if err := checkFile(ref.path, ext, content, ref.template); err != nil {
			errs = append(errs, err)
		}
	}
	var unused []string
	for p := range files {
		if !used[p] && !isDocument(p) {
			unused = append(unused, p)
		}
	}
	sort.Strings(unused)
	for _, p := range unused {
		errs = append(errs, fmt.Errorf("%s isn't used by %s; remove it from the package", p, ThemeFile))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	kept := map[string][]byte{}
	for p := range used {
		kept[p] = files[p]
	}
	return &Package{Theme: *t, Files: kept}, nil
}

type reference struct {
	field, path string
	types       []string
	// template is the template's kind for templates, which have their own
	// checks.
	template string
}

// references lists every file the theme refers to, in a fixed order.
func (t *Theme) references() []reference {
	var refs []reference
	add := func(field, p string, types []string, template string) {
		if p != "" {
			refs = append(refs, reference{field, p, types, template})
		}
	}
	a := t.Assets
	add("assets.logoLight", a.LogoLight, imageTypes, "")
	add("assets.logoDark", a.LogoDark, imageTypes, "")
	add("assets.mark", a.Mark, imageTypes, "")
	add("assets.favicon", a.Favicon, faviconTypes, "")
	add("assets.loginBackground", a.LoginBackground, imageTypes, "")
	names := make([]string, 0, len(a.Illustrations))
	for n := range a.Illustrations {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		add("assets.illustrations."+n, a.Illustrations[n], imageTypes, "")
	}
	for _, f := range []struct {
		field string
		font  Font
	}{{"tokens.typography.sans", t.Tokens.Typography.Sans}, {"tokens.typography.mono", t.Tokens.Typography.Mono}} {
		for i, file := range f.font.Files {
			add(fmt.Sprintf("%s.files[%d]", f.field, i), file, fontTypes, "")
		}
	}
	tp := t.Templates
	add("templates.email", tp.Email, templateTypes, TemplateEmail)
	add("templates.error", tp.Error, templateTypes, TemplateError)
	add("templates.maintenance", tp.Maintenance, templateTypes, TemplateMaintenance)
	add("templates.suspended", tp.Suspended, templateTypes, TemplateSuspended)
	return refs
}

// Signatures of binary file types, so a file can't pretend to be what its
// name says.
var signatures = map[string][][]byte{
	".png":   {[]byte("\x89PNG\r\n\x1a\n")},
	".jpg":   {[]byte("\xff\xd8\xff")},
	".jpeg":  {[]byte("\xff\xd8\xff")},
	".ico":   {[]byte("\x00\x00\x01\x00")},
	".woff2": {[]byte("wOF2")},
	".woff":  {[]byte("wOFF")},
}

func checkFile(p, ext string, data []byte, template string) error {
	switch ext {
	case ".svg":
		if err := CheckSVG(data); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	case ".webp":
		if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
			return fmt.Errorf("%s isn't a WebP image", p)
		}
	case ".html":
		if err := CheckTemplate(template, string(data)); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	default:
		ok := false
		for _, sig := range signatures[ext] {
			ok = ok || bytes.HasPrefix(data, sig)
		}
		if !ok {
			return fmt.Errorf("%s isn't a %s file", p, strings.TrimPrefix(ext, "."))
		}
	}
	return nil
}
