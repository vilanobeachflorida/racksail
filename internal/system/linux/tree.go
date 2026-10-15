package linux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sync"
	"syscall"

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

// OpenTree opens dir as an os.Root, which resolves every name beneath the
// directory with the kernel's help, so neither a name nor a symbolic link,
// even one swapped in while the tree is in use, leads out of it.
func (f *files) OpenTree(ctx context.Context, dir string) (system.Tree, error) {
	if err := checkPath(dir); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(f.h.path(dir))
	if err != nil {
		return nil, mapErr("open", dir, err)
	}
	return &tree{h: f.h, root: root, dir: dir, owners: map[system.Owner][2]int{}}, nil
}

type tree struct {
	h    *host
	root *os.Root
	dir  string

	mu     sync.Mutex
	owners map[system.Owner][2]int
	db     *accountDB
}

func (t *tree) name(name string) (string, error) { return system.CleanTreeName(name) }

func (t *tree) full(name string) string { return path.Join(t.dir, name) }

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

func (t *tree) info(name string, fi fs.FileInfo) system.FileInfo {
	uid, gid := fileIDs(fi)
	db := t.accounts()
	info := system.FileInfo{
		Path: name, Mode: fi.Mode(), UID: uid, GID: gid, Size: fi.Size(), ModTime: fi.ModTime().UTC(),
		Owner: db.userName(uid), Group: db.groupName(gid),
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		if target, err := t.root.Readlink(name); err == nil {
			info.Target = target
		}
	}
	return info
}

func (t *tree) Lstat(name string) (system.FileInfo, error) {
	name, err := t.name(name)
	if err != nil {
		return system.FileInfo{}, err
	}
	fi, err := t.root.Lstat(name)
	if err != nil {
		return system.FileInfo{}, mapErr("stat", t.full(name), err)
	}
	return t.info(name, fi), nil
}

func (t *tree) ReadDir(name string) ([]system.FileInfo, error) {
	name, err := t.name(name)
	if err != nil {
		return nil, err
	}
	dir, err := t.root.Open(name)
	if err != nil {
		return nil, mapErr("readdir", t.full(name), err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, mapErr("readdir", t.full(name), err)
	}
	out := make([]system.FileInfo, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, t.info(path.Join(name, e.Name()), fi))
	}
	return out, nil
}

func (t *tree) openRegular(name string) (*os.File, error) {
	name, err := t.name(name)
	if err != nil {
		return nil, err
	}
	file, err := t.root.Open(name)
	if err != nil {
		return nil, mapErr("open", t.full(name), err)
	}
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, mapErr("open", t.full(name), err)
	}
	if !fi.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%s isn't a regular file: %w", t.full(name), system.ErrInvalid)
	}
	return file, nil
}

func (t *tree) Open(name string) (io.ReadCloser, error) { return t.openRegular(name) }

func (t *tree) OpenRandom(name string) (system.RandomReader, error) {
	file, err := t.openRegular(name)
	if err != nil {
		return nil, err
	}
	return regularRandom(file, t.full(name))
}

// own sets the mode and owner through an open file, so a name swapped for a
// link in the meantime can't redirect them.
func (t *tree) own(file *os.File, perm fs.FileMode, owner system.Owner) error {
	uid, gid, err := t.ids(owner)
	if err != nil {
		return err
	}
	if err := file.Chmod(perm); err != nil {
		return err
	}
	fi, err := file.Stat()
	if err != nil {
		return err
	}
	if cu, cg := fileIDs(fi); cu == uid && cg == gid {
		return nil
	}
	return file.Chown(uid, gid)
}

func (t *tree) Mkdir(name string, perm fs.FileMode, owner system.Owner) error {
	name, err := t.name(name)
	if err != nil {
		return err
	}
	if err := t.root.Mkdir(name, 0o700); err != nil {
		return mapErr("mkdir", t.full(name), err)
	}
	dir, err := t.root.Open(name)
	if err != nil {
		return mapErr("mkdir", t.full(name), err)
	}
	defer dir.Close()
	return mapErr("mkdir", t.full(name), t.own(dir, perm.Perm()|perm&fs.ModeSticky, owner))
}

func tempName(dir string) string {
	var b [8]byte
	rand.Read(b[:])
	return path.Join(dir, system.TreeTempPrefix+hex.EncodeToString(b[:]))
}

func (t *tree) Create(name string, r io.Reader, perm fs.FileMode, owner system.Owner) (int64, error) {
	name, err := t.name(name)
	if err != nil {
		return 0, err
	}
	if name == "." {
		return 0, fmt.Errorf("%s is the tree itself: %w", t.dir, system.ErrInvalid)
	}
	if fi, err := t.root.Lstat(name); err == nil && fi.IsDir() {
		return 0, fmt.Errorf("%s is a directory: %w", t.full(name), system.ErrExists)
	}
	tmp := tempName(path.Dir(name))
	file, err := t.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, mapErr("write", t.full(name), err)
	}
	n, err := io.Copy(file, r)
	if err == nil {
		err = t.own(file, perm.Perm(), owner)
	}
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = t.root.Rename(tmp, name)
	}
	if err != nil {
		t.root.Remove(tmp)
		return n, mapErr("write", t.full(name), err)
	}
	return n, nil
}

func (t *tree) Symlink(target, name string, owner system.Owner) error {
	name, err := t.name(name)
	if err != nil {
		return err
	}
	if !system.LinkStaysInside(name, target) {
		return fmt.Errorf("link %s to %q leaves the tree: %w", t.full(name), target, system.ErrInvalid)
	}
	uid, gid, err := t.ids(owner)
	if err != nil {
		return err
	}
	if err := t.root.Symlink(target, name); err != nil {
		return mapErr("symlink", t.full(name), err)
	}
	fi, err := t.root.Lstat(name)
	if err != nil {
		return mapErr("symlink", t.full(name), err)
	}
	if cu, cg := fileIDs(fi); cu == uid && cg == gid {
		return nil
	}
	return mapErr("symlink", t.full(name), t.root.Lchown(name, uid, gid))
}

func (t *tree) Remove(name string) error {
	name, err := t.name(name)
	if err != nil {
		return err
	}
	if name == "." {
		return fmt.Errorf("refusing to remove the tree itself: %w", system.ErrInvalid)
	}
	return mapErr("remove", t.full(name), t.root.Remove(name))
}

func (t *tree) Close() error { return t.root.Close() }
