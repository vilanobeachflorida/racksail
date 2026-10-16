// Command gen builds Racksail's brand assets from the artwork in this
// directory: the logo in its lockups and color variants as SVG and PNG, the
// favicons and app icons, the mark drawn for terminals, and the copies the
// panel, the documentation site and the theme package use.
//
// Run it from the repository root after changing the artwork:
//
//	go run ./brand/gen
//
// With -check it writes nothing and fails if any generated file is out of
// date, which the tests check too.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	check := flag.Bool("check", false, "report out-of-date files instead of writing them")
	root := flag.String("root", ".", "the repository root")
	flag.Parse()
	if _, err := os.Stat(filepath.Join(*root, "brand", "gen", "artwork.go")); err != nil {
		fmt.Fprintln(os.Stderr, "gen: run this from the repository root, or point -root at it")
		os.Exit(2)
	}
	stale, err := generate(*root, *check)
	for _, path := range stale {
		if *check {
			fmt.Fprintf(os.Stderr, "%s is out of date; run go run ./brand/gen\n", path)
		} else {
			fmt.Println("wrote", path)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	if *check && len(stale) > 0 {
		os.Exit(1)
	}
}

// generate writes every output that differs from what's on disk, or with
// check only lists them. It returns the paths that differed.
func generate(root string, check bool) ([]string, error) {
	var stale []string
	for _, f := range outputs() {
		path := filepath.Join(root, filepath.FromSlash(f.Path))
		old, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return stale, err
		}
		same, err := equivalent(f, old)
		if err != nil {
			return stale, fmt.Errorf("%s: %w", f.Path, err)
		}
		if same {
			continue
		}
		stale = append(stale, f.Path)
		if check {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return stale, err
		}
		if err := os.WriteFile(path, f.Data, 0o644); err != nil {
			return stale, err
		}
	}
	return stale, nil
}

// equivalent reports whether a file on disk already holds what f would
// write. Rasters may differ by a rounding step on antialiased edges, so a
// machine with another processor doesn't rewrite them.
func equivalent(f File, old []byte) (bool, error) {
	if old == nil {
		return false, nil
	}
	switch f.Kind {
	case Raster:
		return samePixels(f.Data, old)
	case Icon:
		a, err := icoEntries(f.Data)
		if err != nil {
			return false, err
		}
		b, err := icoEntries(old)
		if err != nil || len(a) != len(b) {
			return false, nil
		}
		for i := range a {
			if same, err := samePixels(a[i], b[i]); err != nil || !same {
				return false, err
			}
		}
		return true, nil
	default:
		return bytes.Equal(f.Data, old), nil
	}
}

// samePixels compares two PNG images, allowing each premultiplied channel
// to differ by 2. Premultiplying keeps the nearly transparent pixels at the
// edges, whose colors mean little, from counting.
func samePixels(a, b []byte) (bool, error) {
	ia, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		return false, err
	}
	ib, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return false, nil
	}
	if ia.Bounds() != ib.Bounds() {
		return false, nil
	}
	na, nb := toNRGBA(ia), toNRGBA(ib)
	for i := 0; i < len(na.Pix); i += 4 {
		aa, ab := int(na.Pix[i+3]), int(nb.Pix[i+3])
		if diff(aa, ab) > 2 {
			return false, nil
		}
		for c := range 3 {
			if diff(int(na.Pix[i+c])*aa/255, int(nb.Pix[i+c])*ab/255) > 2 {
				return false, nil
			}
		}
	}
	return true, nil
}

func diff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok {
		return n
	}
	b := img.Bounds()
	n := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			n.Set(x, y, img.At(x, y))
		}
	}
	return n
}
