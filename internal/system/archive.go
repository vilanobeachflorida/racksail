package system

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ArchiveFormat names a format Racksail packs and unpacks.
type ArchiveFormat string

// Archive formats.
const (
	FormatTar    ArchiveFormat = "tar"
	FormatTarGz  ArchiveFormat = "tar.gz"
	FormatTarZst ArchiveFormat = "tar.zst"
	FormatZip    ArchiveFormat = "zip"
)

// Valid reports whether f is a known format.
func (f ArchiveFormat) Valid() bool {
	switch f {
	case FormatTar, FormatTarGz, FormatTarZst, FormatZip:
		return true
	}
	return false
}

// FormatOf guesses the format from a file name, or returns "".
func FormatOf(name string) ArchiveFormat {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz
	case strings.HasSuffix(lower, ".tar.zst"), strings.HasSuffix(lower, ".tzst"):
		return FormatTarZst
	case strings.HasSuffix(lower, ".tar"):
		return FormatTar
	case strings.HasSuffix(lower, ".zip"):
		return FormatZip
	}
	return ""
}

// PackSpec describes an archive made from a directory of a tree.
type PackSpec struct {
	// Source is the directory to pack and Dest the archive to write, both
	// names inside the tree.
	Source string
	Dest   string
	Format ArchiveFormat
	// Exclude holds glob patterns matched against each path relative to
	// Source and against its last element, such as ".git" or "*.log".
	Exclude []string
	Perm    fs.FileMode
	Owner   Owner
	// MaxBytes bounds the contents packed; zero means DefaultArchiveMaxBytes.
	MaxBytes int64
}

// UnpackSpec describes an archive unpacked into a directory of a tree.
type UnpackSpec struct {
	Archive string
	Dest    string
	Format  ArchiveFormat
	// StripComponents drops leading path elements from every entry.
	StripComponents int
	// Owner owns everything unpacked.
	Owner Owner
	// Merge allows unpacking into a directory that isn't empty, replacing
	// files with the same names.
	Merge bool
	// MaxBytes and MaxEntries bound what the archive may unpack to, against
	// archives that expand to far more than they weigh. Zero means the
	// defaults.
	MaxBytes   int64
	MaxEntries int
}

// ArchiveStats counts what was packed or unpacked.
type ArchiveStats struct {
	Files   int   `json:"files"`
	Dirs    int   `json:"dirs"`
	Links   int   `json:"links"`
	Skipped int   `json:"skipped,omitempty" doc:"Entries left out: devices, sockets and pipes"`
	Bytes   int64 `json:"bytes" doc:"Bytes of file contents"`
}

// Archive limits.
const (
	DefaultArchiveMaxBytes   = 64 << 30
	DefaultArchiveMaxEntries = 1_000_000
	// TreeTempPrefix starts the names of files trees write before renaming
	// them into place; packing leaves them out.
	TreeTempPrefix = ".racksail-"
)

func (s PackSpec) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultArchiveMaxBytes
}

func (s UnpackSpec) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultArchiveMaxBytes
}

func (s UnpackSpec) maxEntries() int {
	if s.MaxEntries > 0 {
		return s.MaxEntries
	}
	return DefaultArchiveMaxEntries
}

// Pack writes the archive described by spec. The archive replaces Dest
// atomically, and Dest is left out when it lies inside Source.
func Pack(ctx context.Context, t Tree, spec PackSpec) (ArchiveStats, error) {
	var stats ArchiveStats
	if !spec.Format.Valid() {
		return stats, fmt.Errorf("archive format %q: %w", spec.Format, ErrInvalid)
	}
	src, err := CleanTreeName(spec.Source)
	if err != nil {
		return stats, err
	}
	dest, err := CleanTreeName(spec.Dest)
	if err != nil {
		return stats, err
	}
	if dest == "." {
		return stats, fmt.Errorf("the archive needs a file name: %w", ErrInvalid)
	}
	if info, err := t.Lstat(src); err != nil {
		return stats, err
	} else if !info.IsDir() {
		return stats, fmt.Errorf("%s isn't a directory: %w", src, ErrInvalid)
	}
	for _, p := range spec.Exclude {
		if _, err := path.Match(p, ""); err != nil {
			return stats, fmt.Errorf("exclude pattern %q: %w", p, ErrInvalid)
		}
	}
	perm := spec.Perm
	if perm == 0 {
		perm = 0o640
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := writeArchive(ctx, t, spec, src, dest, pw, &stats)
		pw.CloseWithError(err)
		done <- err
	}()
	_, createErr := t.Create(dest, pr, perm, spec.Owner)
	pr.CloseWithError(createErr)
	packErr := <-done
	if packErr != nil {
		return stats, packErr
	}
	return stats, createErr
}

// packEntry is one entry to pack, in walk order.
type packEntry struct {
	name string // inside the tree
	rel  string // inside the archive
	info FileInfo
}

func writeArchive(ctx context.Context, t Tree, spec PackSpec, src, dest string, w io.Writer, stats *ArchiveStats) error {
	var entries []packEntry
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, err := t.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Path < children[j].Path })
		for _, c := range children {
			base := path.Base(c.Path)
			name := path.Join(dir, base)
			childRel := path.Join(rel, base)
			if name == dest || strings.HasPrefix(base, TreeTempPrefix) || excluded(spec.Exclude, childRel, base) {
				continue
			}
			entries = append(entries, packEntry{name: name, rel: childRel, info: c})
			if c.IsDir() {
				if err := walk(name, childRel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(src, ""); err != nil {
		return err
	}

	switch spec.Format {
	case FormatZip:
		zw := zip.NewWriter(w)
		if err := packZip(ctx, t, entries, zw, spec.maxBytes(), stats); err != nil {
			return err
		}
		return zw.Close()
	default:
		cw, err := compressor(spec.Format, w)
		if err != nil {
			return err
		}
		tw := tar.NewWriter(cw)
		if err := packTar(ctx, t, entries, tw, spec.maxBytes(), stats); err != nil {
			return err
		}
		if err := tw.Close(); err != nil {
			return err
		}
		return cw.Close()
	}
}

func excluded(patterns []string, rel, base string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
		if ok, _ := path.Match(p, base); ok {
			return true
		}
	}
	return false
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func compressor(f ArchiveFormat, w io.Writer) (io.WriteCloser, error) {
	switch f {
	case FormatTarGz:
		return gzip.NewWriter(w), nil
	case FormatTarZst:
		return zstd.NewWriter(w)
	case FormatTar:
		return nopWriteCloser{w}, nil
	}
	return nil, fmt.Errorf("archive format %q: %w", f, ErrInvalid)
}

func packTar(ctx context.Context, t Tree, entries []packEntry, tw *tar.Writer, limit int64, stats *ArchiveStats) error {
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr := &tar.Header{Name: e.rel, Mode: int64(e.info.Perm()), ModTime: e.info.ModTime.UTC(), Format: tar.FormatPAX}
		switch {
		case e.info.IsDir():
			hdr.Typeflag, hdr.Name = tar.TypeDir, e.rel+"/"
			stats.Dirs++
		case e.info.IsSymlink():
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, e.info.Target
			stats.Links++
		case e.info.Mode.IsRegular():
			hdr.Typeflag, hdr.Size = tar.TypeReg, e.info.Size
			if stats.Bytes+e.info.Size > limit {
				return fmt.Errorf("the files to pack are larger than %d bytes: %w", limit, ErrTooLarge)
			}
		default:
			stats.Skipped++
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		n, err := copyFile(t, e.name, tw, e.info.Size)
		if err != nil {
			return err
		}
		stats.Files++
		stats.Bytes += n
	}
	return nil
}

func packZip(ctx context.Context, t Tree, entries []packEntry, zw *zip.Writer, limit int64, stats *ArchiveStats) error {
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr := &zip.FileHeader{Name: e.rel, Method: zip.Deflate, Modified: e.info.ModTime.UTC()}
		switch {
		case e.info.IsDir():
			hdr.Name += "/"
			hdr.Method = zip.Store
			hdr.SetMode(fs.ModeDir | e.info.Perm())
			if _, err := zw.CreateHeader(hdr); err != nil {
				return err
			}
			stats.Dirs++
		case e.info.IsSymlink():
			hdr.Method = zip.Store
			hdr.SetMode(fs.ModeSymlink | 0o777)
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(w, e.info.Target); err != nil {
				return err
			}
			stats.Links++
		case e.info.Mode.IsRegular():
			if stats.Bytes+e.info.Size > limit {
				return fmt.Errorf("the files to pack are larger than %d bytes: %w", limit, ErrTooLarge)
			}
			hdr.SetMode(e.info.Perm())
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			n, err := copyFile(t, e.name, w, e.info.Size)
			if err != nil {
				return err
			}
			stats.Files++
			stats.Bytes += n
		default:
			stats.Skipped++
		}
	}
	return nil
}

// copyFile copies exactly size bytes, the size the header announced; a file
// that changes while it is packed must not corrupt the archive.
func copyFile(t Tree, name string, w io.Writer, size int64) (int64, error) {
	r, err := t.Open(name)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	n, err := io.Copy(w, io.LimitReader(r, size))
	if err != nil {
		return n, err
	}
	if n < size {
		// The file shrank; pad so the archive stays well-formed.
		pad, err := io.CopyN(w, zeroReader{}, size-n)
		return n + pad, err
	}
	return n, nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// Unpack unpacks the archive described by spec. Entries can't leave Dest:
// absolute names, names with "..", and links that point out of Dest are
// refused, and the tree never follows a link out of itself.
func Unpack(ctx context.Context, t Tree, spec UnpackSpec) (ArchiveStats, error) {
	var stats ArchiveStats
	if !spec.Format.Valid() {
		return stats, fmt.Errorf("archive format %q: %w", spec.Format, ErrInvalid)
	}
	if spec.StripComponents < 0 || spec.StripComponents > 16 {
		return stats, fmt.Errorf("strip components %d: %w", spec.StripComponents, ErrInvalid)
	}
	archive, err := CleanTreeName(spec.Archive)
	if err != nil {
		return stats, err
	}
	dest, err := CleanTreeName(spec.Dest)
	if err != nil {
		return stats, err
	}
	if err := prepareDest(t, dest, spec.Merge, spec.Owner); err != nil {
		return stats, err
	}
	u := &unpacker{ctx: ctx, t: t, spec: spec, dest: dest, stats: &stats, made: map[string]bool{dest: true}}
	if spec.Format == FormatZip {
		r, err := t.OpenRandom(archive)
		if err != nil {
			return stats, err
		}
		defer r.Close()
		zr, err := zip.NewReader(r, r.Size())
		if err != nil {
			return stats, fmt.Errorf("reading %s: %v: %w", archive, err, ErrInvalid)
		}
		return stats, u.zip(zr)
	}
	r, err := t.Open(archive)
	if err != nil {
		return stats, err
	}
	defer r.Close()
	dr, err := decompressor(spec.Format, r)
	if err != nil {
		return stats, fmt.Errorf("reading %s: %v: %w", archive, err, ErrInvalid)
	}
	defer dr.Close()
	return stats, u.tar(tar.NewReader(dr))
}

func decompressor(f ArchiveFormat, r io.Reader) (io.ReadCloser, error) {
	switch f {
	case FormatTarGz:
		return gzip.NewReader(r)
	case FormatTarZst:
		d, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return d.IOReadCloser(), nil
	case FormatTar:
		return io.NopCloser(r), nil
	}
	return nil, fmt.Errorf("archive format %q: %w", f, ErrInvalid)
}

// prepareDest makes sure the destination exists and, unless merging, that
// it is empty.
func prepareDest(t Tree, dest string, merge bool, owner Owner) error {
	info, err := t.Lstat(dest)
	switch {
	case errors.Is(err, ErrNotFound):
		return mkdirAll(t, dest, owner, nil)
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s isn't a directory: %w", dest, ErrExists)
	case merge:
		return nil
	}
	children, err := t.ReadDir(dest)
	if err != nil {
		return err
	}
	if len(children) > 0 {
		return fmt.Errorf("%s isn't empty: %w", dest, ErrNotEmpty)
	}
	return nil
}

// mkdirAll creates name and its missing parents. made remembers directories
// already known to exist.
func mkdirAll(t Tree, name string, owner Owner, made map[string]bool) error {
	if name == "." || made[name] {
		return nil
	}
	info, err := t.Lstat(name)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and isn't a directory: %w", name, ErrExists)
		}
		if made != nil {
			made[name] = true
		}
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := mkdirAll(t, path.Dir(name), owner, made); err != nil {
		return err
	}
	if err := t.Mkdir(name, 0o755, owner); err != nil && !errors.Is(err, ErrExists) {
		return err
	}
	if made != nil {
		made[name] = true
	}
	return nil
}

type unpacker struct {
	ctx   context.Context
	t     Tree
	spec  UnpackSpec
	dest  string
	stats *ArchiveStats
	made  map[string]bool
	count int
}

// target maps an archive entry to a name in the tree, or "" when the entry
// is stripped away.
func (u *unpacker) target(entry string) (string, error) {
	entry = strings.TrimSuffix(entry, "/")
	if entry == "" || entry == "." {
		return "", nil
	}
	clean, err := CleanTreeName(entry)
	if err != nil {
		return "", err
	}
	parts := strings.Split(clean, "/")
	if len(parts) <= u.spec.StripComponents {
		return "", nil
	}
	return path.Join(u.dest, path.Join(parts[u.spec.StripComponents:]...)), nil
}

func (u *unpacker) next() error {
	if err := u.ctx.Err(); err != nil {
		return err
	}
	u.count++
	if u.count > u.spec.maxEntries() {
		return fmt.Errorf("the archive has more than %d entries: %w", u.spec.maxEntries(), ErrTooLarge)
	}
	return nil
}

func filePerm(mode fs.FileMode) fs.FileMode { return mode.Perm() | 0o600 }

func dirPerm(mode fs.FileMode) fs.FileMode { return mode.Perm() | 0o700 }

func (u *unpacker) dir(name string, perm fs.FileMode) error {
	if err := mkdirAll(u.t, path.Dir(name), u.spec.Owner, u.made); err != nil {
		return err
	}
	if !u.made[name] {
		info, err := u.t.Lstat(name)
		switch {
		case err == nil && !info.IsDir():
			return fmt.Errorf("%s exists and isn't a directory: %w", name, ErrExists)
		case errors.Is(err, ErrNotFound):
			if err := u.t.Mkdir(name, perm, u.spec.Owner); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		u.made[name] = true
	}
	u.stats.Dirs++
	return nil
}

func (u *unpacker) file(name string, r io.Reader, perm fs.FileMode) error {
	if err := mkdirAll(u.t, path.Dir(name), u.spec.Owner, u.made); err != nil {
		return err
	}
	if info, err := u.t.Lstat(name); err == nil && info.IsDir() {
		return fmt.Errorf("%s exists and is a directory: %w", name, ErrExists)
	}
	room := u.spec.maxBytes() - u.stats.Bytes
	n, err := u.t.Create(name, io.LimitReader(r, room+1), perm, u.spec.Owner)
	if err != nil {
		return err
	}
	if n > room {
		u.t.Remove(name)
		return fmt.Errorf("the archive unpacks to more than %d bytes: %w", u.spec.maxBytes(), ErrTooLarge)
	}
	u.stats.Files++
	u.stats.Bytes += n
	return nil
}

func (u *unpacker) symlink(name, target string) error {
	// The link must stay inside the destination, not just inside the tree.
	rel := strings.TrimPrefix(strings.TrimPrefix(name, u.dest), "/")
	if u.dest == "." {
		rel = name
	}
	if !LinkStaysInside(rel, target) {
		return fmt.Errorf("the archive links %s to %q, outside the destination: %w", rel, target, ErrInvalid)
	}
	if err := mkdirAll(u.t, path.Dir(name), u.spec.Owner, u.made); err != nil {
		return err
	}
	if info, err := u.t.Lstat(name); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s exists and is a directory: %w", name, ErrExists)
		}
		if err := u.t.Remove(name); err != nil {
			return err
		}
	}
	if err := u.t.Symlink(target, name, u.spec.Owner); err != nil {
		return err
	}
	u.stats.Links++
	return nil
}

func (u *unpacker) tar(tr *tar.Reader) error {
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the archive: %v: %w", err, ErrInvalid)
		}
		if err := u.next(); err != nil {
			return err
		}
		name, err := u.target(hdr.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		mode := fs.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = u.dir(name, dirPerm(mode))
		case tar.TypeReg:
			err = u.file(name, tr, filePerm(mode))
		case tar.TypeSymlink:
			err = u.symlink(name, hdr.Linkname)
		case tar.TypeLink:
			err = u.hardLink(name, hdr.Linkname, filePerm(mode))
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			u.stats.Skipped++
		}
		if err != nil {
			return err
		}
	}
}

// hardLink unpacks a hard link as a copy of the file it names, which must
// already be unpacked; copies can't be used to reach outside the archive.
func (u *unpacker) hardLink(name, linkname string, perm fs.FileMode) error {
	source, err := u.target(linkname)
	if err != nil {
		return err
	}
	if source == "" {
		return fmt.Errorf("hard link %s points at a stripped entry: %w", name, ErrInvalid)
	}
	info, err := u.t.Lstat(source)
	if err != nil {
		return fmt.Errorf("hard link %s to %s: %w", name, linkname, err)
	}
	if !info.Mode.IsRegular() {
		return fmt.Errorf("hard link %s to %s, which isn't a regular file: %w", name, linkname, ErrInvalid)
	}
	r, err := u.t.Open(source)
	if err != nil {
		return err
	}
	defer r.Close()
	return u.file(name, r, perm)
}

func (u *unpacker) zip(zr *zip.Reader) error {
	for _, f := range zr.File {
		if err := u.next(); err != nil {
			return err
		}
		name, err := u.target(f.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = u.dir(name, dirPerm(mode))
		case mode&fs.ModeSymlink != 0:
			err = u.zipSymlink(f, name)
		case mode.IsRegular():
			err = u.zipFile(f, name, filePerm(mode))
		default:
			u.stats.Skipped++
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *unpacker) zipFile(f *zip.File, name string, perm fs.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("reading %s from the archive: %v: %w", f.Name, err, ErrInvalid)
	}
	defer rc.Close()
	return u.file(name, rc, perm)
}

func (u *unpacker) zipSymlink(f *zip.File, name string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("reading %s from the archive: %v: %w", f.Name, err, ErrInvalid)
	}
	defer rc.Close()
	target, err := io.ReadAll(io.LimitReader(rc, 4096))
	if err != nil {
		return err
	}
	return u.symlink(name, string(target))
}
