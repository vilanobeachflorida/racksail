package linux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type files struct{ h *host }

func checkPath(p string) error {
	if !path.IsAbs(p) || path.Clean(p) != p {
		return fmt.Errorf("path %q must be absolute and clean: %w", p, system.ErrInvalid)
	}
	return nil
}

func mapErr(op, p string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s %s: %w", op, p, system.ErrNotFound)
	// ENOTEMPTY first: Go also reports it as fs.ErrExist.
	case errors.Is(err, syscall.ENOTEMPTY):
		return fmt.Errorf("%s %s: %w", op, p, system.ErrNotEmpty)
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%s %s: %w", op, p, system.ErrExists)
	}
	return fmt.Errorf("%s %s: %w", op, p, err)
}

func fileIDs(fi fs.FileInfo) (int, int) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return -1, -1
}

func (f *files) info(db *accountDB, p string, fi fs.FileInfo) system.FileInfo {
	uid, gid := fileIDs(fi)
	info := system.FileInfo{
		Path: p, Mode: fi.Mode(), UID: uid, GID: gid, Size: fi.Size(), ModTime: fi.ModTime().UTC(),
		Owner: db.userName(uid), Group: db.groupName(gid),
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		if target, err := os.Readlink(f.h.path(p)); err == nil {
			info.Target = target
		}
	}
	return info
}

func (f *files) Stat(ctx context.Context, p string) (system.FileInfo, error) {
	if err := checkPath(p); err != nil {
		return system.FileInfo{}, err
	}
	fi, err := os.Lstat(f.h.path(p))
	if err != nil {
		return system.FileInfo{}, mapErr("stat", p, err)
	}
	db, err := f.h.accounts()
	if err != nil {
		db = &accountDB{}
	}
	return f.info(db, p, fi), nil
}

func (f *files) ReadFile(ctx context.Context, p string, max int64) ([]byte, error) {
	r, err := f.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if max <= 0 {
		return io.ReadAll(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, mapErr("read", p, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes: %w", p, max, system.ErrTooLarge)
	}
	return data, nil
}

func (f *files) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(f.h.path(p), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, mapErr("open", p, err)
	}
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, mapErr("open", p, err)
	}
	if fi.IsDir() {
		file.Close()
		return nil, fmt.Errorf("%s is a directory: %w", p, system.ErrInvalid)
	}
	return file, nil
}

func (f *files) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode, owner system.Owner) error {
	return f.write(p, perm, owner, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func (f *files) WriteFrom(ctx context.Context, p string, r io.Reader, perm fs.FileMode, owner system.Owner) error {
	return f.write(p, perm, owner, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
}

// write replaces p atomically: the data goes to a temporary file in the same
// directory, which is synced and renamed over the target.
func (f *files) write(p string, perm fs.FileMode, owner system.Owner, fill func(io.Writer) error) error {
	if err := checkPath(p); err != nil {
		return err
	}
	uid, gid, err := f.h.lookupIDs(owner)
	if err != nil {
		return err
	}
	target := f.h.path(p)
	if fi, err := os.Lstat(target); err == nil && fi.IsDir() {
		return fmt.Errorf("%s is a directory: %w", p, system.ErrInvalid)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".racksail-*")
	if err != nil {
		return mapErr("write", p, err)
	}
	defer os.Remove(tmp.Name())
	if err := fill(tmp); err != nil {
		tmp.Close()
		return mapErr("write", p, err)
	}
	if err := tmp.Chmod(perm.Perm()); err != nil {
		tmp.Close()
		return mapErr("write", p, err)
	}
	if err := chownIfNeeded(tmp, uid, gid); err != nil {
		tmp.Close()
		return mapErr("write", p, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return mapErr("write", p, err)
	}
	if err := tmp.Close(); err != nil {
		return mapErr("write", p, err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return mapErr("write", p, err)
	}
	return syncDir(filepath.Dir(target))
}

func chownIfNeeded(file *os.File, uid, gid int) error {
	fi, err := file.Stat()
	if err != nil {
		return err
	}
	if cu, cg := fileIDs(fi); cu == uid && cg == gid {
		return nil
	}
	return file.Chown(uid, gid)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	// Some filesystems don't support syncing directories; the rename has
	// already happened, so that isn't worth failing over.
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}

func (f *files) Mkdir(ctx context.Context, p string, perm fs.FileMode, owner system.Owner) error {
	if err := checkPath(p); err != nil {
		return err
	}
	uid, gid, err := f.h.lookupIDs(owner)
	if err != nil {
		return err
	}
	target := f.h.path(p)
	if err := os.Mkdir(target, 0o700); err != nil {
		return mapErr("mkdir", p, err)
	}
	// Mkdir is subject to the umask, so the final mode is set explicitly.
	if err := os.Chmod(target, perm.Perm()|perm&fs.ModeSticky); err != nil {
		return mapErr("mkdir", p, err)
	}
	fi, err := os.Lstat(target)
	if err != nil {
		return mapErr("mkdir", p, err)
	}
	if cu, cg := fileIDs(fi); cu != uid || cg != gid {
		if err := os.Lchown(target, uid, gid); err != nil {
			return mapErr("mkdir", p, err)
		}
	}
	return nil
}

func (f *files) ReadDir(ctx context.Context, p string) ([]system.FileInfo, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(f.h.path(p))
	if err != nil {
		return nil, mapErr("readdir", p, err)
	}
	db, err := f.h.accounts()
	if err != nil {
		db = &accountDB{}
	}
	out := make([]system.FileInfo, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, f.info(db, path.Join(p, e.Name()), fi))
	}
	return out, nil
}

func (f *files) Remove(ctx context.Context, p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	return mapErr("remove", p, os.Remove(f.h.path(p)))
}

func (f *files) RemoveAll(ctx context.Context, p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	if p == "/" {
		return fmt.Errorf("refusing to remove /: %w", system.ErrInvalid)
	}
	return mapErr("remove", p, os.RemoveAll(f.h.path(p)))
}

func (f *files) Rename(ctx context.Context, from, to string) error {
	if err := checkPath(from); err != nil {
		return err
	}
	if err := checkPath(to); err != nil {
		return err
	}
	return mapErr("rename", from, os.Rename(f.h.path(from), f.h.path(to)))
}

func (f *files) Symlink(ctx context.Context, target, p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	return mapErr("symlink", p, os.Symlink(target, f.h.path(p)))
}

func (f *files) Chown(ctx context.Context, p string, owner system.Owner) error {
	if err := checkPath(p); err != nil {
		return err
	}
	uid, gid, err := f.h.lookupIDs(owner)
	if err != nil {
		return err
	}
	target := f.h.path(p)
	fi, err := os.Lstat(target)
	if err != nil {
		return mapErr("chown", p, err)
	}
	if cu, cg := fileIDs(fi); cu == uid && cg == gid {
		return nil
	}
	return mapErr("chown", p, os.Lchown(target, uid, gid))
}

func (f *files) Chmod(ctx context.Context, p string, perm fs.FileMode) error {
	if err := checkPath(p); err != nil {
		return err
	}
	target := f.h.path(p)
	fi, err := os.Lstat(target)
	if err != nil {
		return mapErr("chmod", p, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link: %w", p, system.ErrInvalid)
	}
	return mapErr("chmod", p, os.Chmod(target, perm.Perm()|perm&fs.ModeSticky))
}
