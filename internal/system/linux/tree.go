package linux

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// randomFile adapts an open file to system.RandomReader.
type randomFile struct {
	*os.File
	size int64
}

func (r randomFile) Size() int64 { return r.size }

func (f *files) OpenRandom(ctx context.Context, p string) (system.RandomReader, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(f.h.path(p), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, mapErr("open", p, err)
	}
	return regularRandom(file, p)
}

func regularRandom(file *os.File, name string) (system.RandomReader, error) {
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, mapErr("open", name, err)
	}
	if !fi.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%s isn't a regular file: %w", name, system.ErrInvalid)
	}
	return randomFile{File: file, size: fi.Size()}, nil
}

// OpenTree opens dir as a tree. dir itself is resolved as usual; names in
// the tree are resolved one directory at a time without following any
// symbolic link, so neither a name nor a link, even one swapped in while
// the tree is in use, leads out of it or anywhere else.
func (f *files) OpenTree(ctx context.Context, dir string) (system.Tree, error) {
	if err := checkPath(dir); err != nil {
		return nil, err
	}
	fd, err := unix.Open(f.h.path(dir), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErr("open", dir, err)
	}
	return newTree(f.h, &fdDir{h: f.h, fd: fd, path: dir}), nil
}

func newTree(h *host, d *fdDir) *tree {
	return &tree{h: h, d: d, owners: map[system.Owner][2]int{}}
}

type tree struct {
	h *host
	d *fdDir

	mu     sync.Mutex
	owners map[system.Owner][2]int
	db     *accountDB
}

// ids resolves an owner once per tree; extracting an archive would
// otherwise read the account files for every entry.
func (t *tree) ids(o system.Owner) (int, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ids, ok := t.owners[o]; ok {
		return ids[0], ids[1], nil
	}
	uid, gid, err := t.h.lookupIDs(o)
	if err != nil {
		return 0, 0, err
	}
	t.owners[o] = [2]int{uid, gid}
	return uid, gid, nil
}

func (t *tree) accounts() *accountDB {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.db == nil {
		db, err := t.h.accounts()
		if err != nil {
			db = &accountDB{}
		}
		t.db = db
	}
	return t.db
}

// at opens the directory holding name and returns it with name's last
// part.
func (t *tree) at(name string) (*fdDir, string, error) {
	parts, err := nameParts(name)
	if err != nil {
		return nil, "", err
	}
	if len(parts) == 0 {
		return nil, "", fmt.Errorf("%s is the tree itself: %w", t.d.path, system.ErrInvalid)
	}
	return t.d.parent(parts)
}

func (t *tree) Lstat(name string) (system.FileInfo, error) {
	parts, err := nameParts(name)
	if err != nil {
		return system.FileInfo{}, err
	}
	if len(parts) == 0 {
		return t.d.self(".", t.accounts())
	}
	dir, last, err := t.d.parent(parts)
	if err != nil {
		return system.FileInfo{}, err
	}
	defer dir.close()
	return dir.lstat(last, strings.Join(parts, "/"), t.accounts())
}

func (t *tree) ReadDir(name string) ([]system.FileInfo, error) {
	parts, err := nameParts(name)
	if err != nil {
		return nil, err
	}
	dir, err := t.d.dir(parts)
	if err != nil {
		return nil, err
	}
	defer dir.close()
	return dir.list(strings.Join(parts, "/"), t.accounts())
}

func (t *tree) openRegular(name string) (*os.File, error) {
	dir, last, err := t.at(name)
	if err != nil {
		return nil, err
	}
	defer dir.close()
	return dir.openRegular(last)
}

func (t *tree) Open(name string) (io.ReadCloser, error) { return t.openRegular(name) }

func (t *tree) OpenRandom(name string) (system.RandomReader, error) {
	file, err := t.openRegular(name)
	if err != nil {
		return nil, err
	}
	return regularRandom(file, t.d.full(name))
}

func (t *tree) Mkdir(name string, perm fs.FileMode, owner system.Owner) error {
	uid, gid, err := t.ids(owner)
	if err != nil {
		return err
	}
	dir, last, err := t.at(name)
	if err != nil {
		return err
	}
	defer dir.close()
	return dir.mkdir(last, perm, uid, gid)
}

func (t *tree) Create(name string, r io.Reader, perm fs.FileMode, owner system.Owner) (int64, error) {
	uid, gid, err := t.ids(owner)
	if err != nil {
		return 0, err
	}
	dir, last, err := t.at(name)
	if err != nil {
		return 0, err
	}
	defer dir.close()
	return dir.create(last, func(w io.Writer) (int64, error) { return io.Copy(w, r) }, perm.Perm(), uid, gid)
}

// Symlink creates a link that stays inside the tree by its name alone, for
// the programs that will follow it; the tree itself never does.
func (t *tree) Symlink(target, name string, owner system.Owner) error {
	clean, err := system.CleanTreeName(name)
	if err != nil {
		return err
	}
	if !system.LinkStaysInside(clean, target) {
		return fmt.Errorf("link %s to %q leaves the tree: %w", t.d.full(clean), target, system.ErrInvalid)
	}
	uid, gid, err := t.ids(owner)
	if err != nil {
		return err
	}
	dir, last, err := t.at(clean)
	if err != nil {
		return err
	}
	defer dir.close()
	return dir.symlink(target, last, uid, gid)
}

func (t *tree) Remove(name string) error {
	dir, last, err := t.at(name)
	if err != nil {
		return err
	}
	defer dir.close()
	return dir.remove(last)
}

func (t *tree) Close() error {
	t.d.close()
	return nil
}
