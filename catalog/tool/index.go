package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/recipes"
)

// Names of what a published catalog holds next to its items.
const (
	indexFile     = "index.json"
	signatureFile = "index.json.sig"
)

// IndexOptions say how to build an index.
type IndexOptions struct {
	// Name identifies the catalog, such as official.
	Name string
	// Built is recorded in the index and in the archive, so building the
	// same commit twice gives the same files.
	Built time.Time
	// HideAfter leaves out items that have been unmaintained for longer.
	HideAfter time.Duration
}

// DefaultHideAfter is how long an item may stay unmaintained before the
// index leaves it out.
const DefaultHideAfter = 180 * 24 * time.Hour

// BuildIndex describes the catalog's items for panels: what each is, where
// its files are and their checksums, and the maintenance flags.
func BuildIndex(c *Catalog, opts IndexOptions) *recipes.Index {
	idx := &recipes.Index{Format: 1, Catalog: opts.Name, Built: opts.Built.UTC().Truncate(time.Second), Items: []recipes.IndexItem{}}
	m := c.Maintenance
	for _, it := range c.Items {
		r := it.Recipe
		item := recipes.IndexItem{
			ID:           r.ID,
			Kind:         r.Kind,
			Name:         r.Name,
			Version:      r.Version,
			Summary:      r.Summary,
			LicenseClass: r.LicenseClass,
			Categories:   r.Categories,
			Path:         it.Dir + "/" + recipeFile,
			Requires:     r.Requires,
			Featured:     slices.Contains(m.Featured, r.ID),
		}
		if f, ok := it.File(recipeFile); ok {
			item.SHA256 = f.SHA256
		}
		if r.Icon != "" {
			item.Icon = it.Dir + "/" + r.Icon
		}
		for _, f := range it.Files {
			item.Files = append(item.Files, recipes.IndexFile{Path: f.Path, SHA256: f.SHA256, Size: f.Size})
		}
		if _, ok := m.mark(m.Broken, r.ID); ok {
			item.Broken = true
		}
		if mark, ok := m.mark(m.Unmaintained, r.ID); ok {
			since, err := mark.SinceTime()
			if err == nil && opts.HideAfter > 0 && idx.Built.Sub(since) > opts.HideAfter {
				continue
			}
			item.Unmaintained = true
		}
		idx.Items = append(idx.Items, item)
	}
	return idx
}

// encodeIndex gives the exact bytes that are signed and published.
func encodeIndex(idx *recipes.Index) ([]byte, error) {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Publish writes a catalog as panels fetch it: the index, its signature
// when key is set, and every file the index lists, at the paths it lists.
// out must not exist or be empty, so nothing stale is published.
func Publish(c *Catalog, idx *recipes.Index, out string, key ed25519.PrivateKey) error {
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s isn't empty; publish into a new directory", out)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, it := range idx.Items {
		for _, f := range it.Files {
			data, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(f.Path)))
			if err != nil {
				return err
			}
			// The catalog may have changed since it was checked.
			if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != f.SHA256 {
				return fmt.Errorf("%s changed while the index was being built", f.Path)
			}
			dest := filepath.Join(out, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				return err
			}
		}
	}
	data, err := encodeIndex(idx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, indexFile), data, 0o644); err != nil {
		return err
	}
	if key != nil {
		if err := os.WriteFile(filepath.Join(out, signatureFile), ed25519.Sign(key, data), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Verify checks a published catalog: the index's signature against pub, and
// that every file the index lists is there with the listed size and
// checksum.
func Verify(dir string, pub ed25519.PublicKey) (*recipes.Index, error) {
	data, err := os.ReadFile(filepath.Join(dir, indexFile))
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(filepath.Join(dir, signatureFile))
	if err != nil {
		return nil, err
	}
	idx, err := recipes.VerifyIndex(data, sig, pub)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, it := range idx.Items {
		listed := false
		for _, f := range it.Files {
			listed = listed || f.Path == it.Path
			content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", it.ID, err))
				continue
			}
			sum := sha256.Sum256(content)
			if int64(len(content)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 {
				errs = append(errs, fmt.Errorf("%s: %s doesn't match the index", it.ID, f.Path))
			}
		}
		if !listed {
			errs = append(errs, fmt.Errorf("%s: the index doesn't list its recipe among its files", it.ID))
		}
	}
	return idx, errors.Join(errs...)
}

// Archive packs a published catalog into one gzip-compressed tar file, for
// mirrors and for panels that fetch the whole catalog at once. Entries are
// sorted and carry the index's build time, so the same catalog always gives
// the same archive.
func Archive(dir string, built time.Time, dest string) error {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return fmt.Errorf("%s isn't a regular file", rel)
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	slices.Sort(paths)

	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	mtime := built.UTC().Truncate(time.Second)
	for _, p := range paths {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: p, ModTime: mtime, Mode: 0o644, Typeflag: tar.TypeReg, Size: info.Size()}
		if info.IsDir() {
			hdr = &tar.Header{Name: p + "/", ModTime: mtime, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, buf.Bytes(), 0o644)
}
