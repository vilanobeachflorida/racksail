package linux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// The agent reaches what accounts can change only through views that
// resolve every path one directory at a time, each opened with O_NOFOLLOW
// relative to the one before, starting from a directory Racksail manages.
// No symbolic link below that directory is followed, not even one an
// account swaps in while an operation runs, so no link can lead a read or a
// write the agent does as root anywhere else. It is what openat2 does with
// RESOLVE_BENEATH and RESOLVE_NO_SYMLINKS, built from calls every Unix has,
// so the tests exercise the same code on any machine.

const (
	dirFlags  = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	readFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
)

// fdDir is an open directory that names are resolved below.
type fdDir struct {
	h    *host
	fd   int
	path string // where the directory is, for messages
}

func (d *fdDir) close() { unix.Close(d.fd) }

func (d *fdDir) full(name string) string { return path.Join(d.path, name) }

// nameParts checks a slash-separated name relative to a directory and
// splits it. "." is the directory itself and has no parts.
func nameParts(name string) ([]string, error) {
	clean, err := system.CleanTreeName(name)
	if err != nil {
		return nil, err
	}
	if clean == "." {
		return nil, nil
	}
	return strings.Split(clean, "/"), nil
}

// componentError explains why a directory on the way couldn't be opened.
func componentError(dirfd int, name, full string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EMLINK) {
		var st unix.Stat_t
		if unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
			if uint32(st.Mode)&unix.S_IFMT == unix.S_IFLNK {
				return fmt.Errorf("%s is a symbolic link, which isn't followed here: %w", full, system.ErrInvalid)
			}
			return fmt.Errorf("%s isn't a directory: %w", full, system.ErrInvalid)
		}
	}
	return mapErr("open", full, err)
}

// dir opens the directory the parts lead to without following symbolic
// links. The caller closes it.
func (d *fdDir) dir(parts []string) (*fdDir, error) {
	fd, err := unix.Openat(d.fd, ".", dirFlags, 0)
	if err != nil {
		return nil, mapErr("open", d.path, err)
	}
	where := d.path
	for _, p := range parts {
		next, err := unix.Openat(fd, p, dirFlags, 0)
		where = path.Join(where, p)
		if err != nil {
			err = componentError(fd, p, where, err)
			unix.Close(fd)
			return nil, err
		}
		unix.Close(fd)
		fd = next
	}
	return &fdDir{h: d.h, fd: fd, path: where}, nil
}

// parent opens the directory holding the last part and returns it with
// that part's name.
func (d *fdDir) parent(parts []string) (*fdDir, string, error) {
	if len(parts) == 0 {
		return nil, "", fmt.Errorf("%s has no parent here: %w", d.path, system.ErrInvalid)
	}
	p, err := d.dir(parts[:len(parts)-1])
	if err != nil {
		return nil, "", err
	}
	return p, parts[len(parts)-1], nil
}

// statMode turns a stat result into io/fs mode bits.
func statMode(st *unix.Stat_t) fs.FileMode {
	m := uint32(st.Mode)
	mode := fs.FileMode(m & 0o777)
	switch m & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case unix.S_IFSOCK:
		mode |= fs.ModeSocket
	case unix.S_IFBLK:
		mode |= fs.ModeDevice
	case unix.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	}
	if m&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if m&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if m&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}

func readlinkAt(dirfd int, name string) (string, error) {
	for size := 256; size <= 1<<16; size *= 4 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(dirfd, name, buf)
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
	return "", fmt.Errorf("the link %s is too long: %w", name, system.ErrInvalid)
}

func (d *fdDir) info(dirfd int, name, shown string, st *unix.Stat_t, db *accountDB) system.FileInfo {
	sec, nsec := st.Mtim.Unix()
	info := system.FileInfo{
		Path: shown, Mode: statMode(st), UID: int(st.Uid), GID: int(st.Gid), Size: st.Size,
		ModTime: time.Unix(sec, nsec).UTC(), Owner: db.userName(int(st.Uid)), Group: db.groupName(int(st.Gid)),
	}
	if info.IsSymlink() {
		if target, err := readlinkAt(dirfd, name); err == nil {
			info.Target = target
		}
	}
	return info
}

// lstat describes an entry without following it.
func (d *fdDir) lstat(name, shown string, db *accountDB) (system.FileInfo, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return system.FileInfo{}, mapErr("stat", d.full(name), err)
	}
	return d.info(d.fd, name, shown, &st, db), nil
}

// self describes the directory itself.
func (d *fdDir) self(shown string, db *accountDB) (system.FileInfo, error) {
	var st unix.Stat_t
	if err := unix.Fstat(d.fd, &st); err != nil {
		return system.FileInfo{}, mapErr("stat", d.path, err)
	}
	return d.info(d.fd, ".", shown, &st, db), nil
}

// openRegular opens a regular file for reading. A link, a directory or a
// special file is refused; O_NONBLOCK keeps a FIFO from holding the agent
// while that is checked.
func (d *fdDir) openRegular(name string) (*os.File, error) {
	fd, err := unix.Openat(d.fd, name, readFlags, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
			return nil, fmt.Errorf("%s is a symbolic link, which isn't followed here: %w", d.full(name), system.ErrInvalid)
		}
		return nil, mapErr("open", d.full(name), err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, mapErr("open", d.full(name), err)
	}
	switch uint32(st.Mode) & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFDIR:
		unix.Close(fd)
		return nil, fmt.Errorf("%s is a directory: %w", d.full(name), system.ErrInvalid)
	default:
		unix.Close(fd)
		return nil, fmt.Errorf("%s isn't a regular file: %w", d.full(name), system.ErrInvalid)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), d.full(name)), nil
}

// list describes the entries of the directory; shown is how its own path
// is given in the results.
func (d *fdDir) list(shown string, db *accountDB) ([]system.FileInfo, error) {
	fd, err := unix.Openat(d.fd, ".", dirFlags, 0)
	if err != nil {
		return nil, mapErr("readdir", d.path, err)
	}
	f := os.NewFile(uintptr(fd), d.path)
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, mapErr("readdir", d.path, err)
	}
	out := make([]system.FileInfo, 0, len(names))
	for _, n := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		out = append(out, d.info(fd, n, path.Join(shown, n), &st, db))
	}
	return out, nil
}

// own sets the mode and owner of an open file.
func own(fd int, perm fs.FileMode, uid, gid int) error {
	mode := uint32(perm.Perm())
	if perm&fs.ModeSticky != 0 {
		mode |= unix.S_ISVTX
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if int(st.Uid) == uid && int(st.Gid) == gid {
		return nil
	}
	return unix.Fchown(fd, uid, gid)
}

func randomName() string {
	var b [8]byte
	rand.Read(b[:])
	return system.TreeTempPrefix + hex.EncodeToString(b[:])
}

// create replaces name with a new regular file that fill writes, through a
// temporary file in the same directory that is renamed over it, so readers
// see the old or the new contents. A directory is never replaced.
func (d *fdDir) create(name string, fill func(io.Writer) (int64, error), perm fs.FileMode, uid, gid int) (int64, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil && uint32(st.Mode)&unix.S_IFMT == unix.S_IFDIR {
		return 0, fmt.Errorf("%s is a directory: %w", d.full(name), system.ErrExists)
	}
	tmp := randomName()
	fd, err := unix.Openat(d.fd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return 0, mapErr("write", d.full(name), err)
	}
	f := os.NewFile(uintptr(fd), d.full(tmp))
	n, err := fill(f)
	if err == nil {
		err = own(fd, perm, uid, gid)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Renameat(d.fd, tmp, d.fd, name)
	}
	if err != nil {
		unix.Unlinkat(d.fd, tmp, 0)
		return n, mapErr("write", d.full(name), err)
	}
	// Some filesystems can't sync a directory; the rename has happened.
	if err := unix.Fsync(d.fd); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EBADF) {
		return n, mapErr("write", d.full(name), err)
	}
	return n, nil
}

func (d *fdDir) mkdir(name string, perm fs.FileMode, uid, gid int) error {
	if err := unix.Mkdirat(d.fd, name, 0o700); err != nil {
		return mapErr("mkdir", d.full(name), err)
	}
	fd, err := unix.Openat(d.fd, name, dirFlags, 0)
	if err != nil {
		return mapErr("mkdir", d.full(name), err)
	}
	defer unix.Close(fd)
	return mapErr("mkdir", d.full(name), own(fd, perm.Perm()|perm&fs.ModeSticky, uid, gid))
}

func (d *fdDir) remove(name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return mapErr("remove", d.full(name), err)
	}
	flags := 0
	if uint32(st.Mode)&unix.S_IFMT == unix.S_IFDIR {
		flags = unix.AT_REMOVEDIR
	}
	return mapErr("remove", d.full(name), unix.Unlinkat(d.fd, name, flags))
}

// removeAll removes name and everything below it, never following a link:
// a link is removed, not what it points at.
func (d *fdDir) removeAll(name string) error {
	return removeAllAt(d.fd, name, d.full(name))
}

func removeAllAt(dirfd int, name, where string) error {
	for attempt := 0; ; attempt++ {
		var st unix.Stat_t
		if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return nil
			}
			return mapErr("remove", where, err)
		}
		if uint32(st.Mode)&unix.S_IFMT != unix.S_IFDIR {
			if err := unix.Unlinkat(dirfd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return mapErr("remove", where, err)
			}
			return nil
		}
		fd, err := unix.Openat(dirfd, name, dirFlags, 0)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				return nil
			}
			return mapErr("remove", where, err)
		}
		f := os.NewFile(uintptr(fd), where)
		names, err := f.Readdirnames(-1)
		if err == nil {
			for _, n := range names {
				if err = removeAllAt(fd, n, path.Join(where, n)); err != nil {
					break
				}
			}
		}
		f.Close()
		if err != nil {
			return mapErr("remove", where, err)
		}
		err = unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR)
		switch {
		case err == nil, errors.Is(err, unix.ENOENT):
			return nil
		case errors.Is(err, unix.ENOTEMPTY) && attempt < 3:
			// Something was added meanwhile; go through it again.
			continue
		}
		return mapErr("remove", where, err)
	}
}

func (d *fdDir) symlink(target, name string, uid, gid int) error {
	if err := unix.Symlinkat(target, d.fd, name); err != nil {
		return mapErr("symlink", d.full(name), err)
	}
	return d.chown(name, uid, gid)
}

// chown changes the owner of an entry itself, a link included.
func (d *fdDir) chown(name string, uid, gid int) error {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return mapErr("chown", d.full(name), err)
	}
	if int(st.Uid) == uid && int(st.Gid) == gid {
		return nil
	}
	return mapErr("chown", d.full(name), unix.Fchownat(d.fd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW))
}

// chmod changes the mode through an open file, so a link swapped in after
// the check is never followed.
func (d *fdDir) chmod(name string, perm fs.FileMode) error {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return mapErr("chmod", d.full(name), err)
	}
	flags := readFlags
	switch uint32(st.Mode) & unix.S_IFMT {
	case unix.S_IFLNK:
		return fmt.Errorf("%s is a symbolic link: %w", d.full(name), system.ErrInvalid)
	case unix.S_IFDIR:
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(d.fd, name, flags, 0)
	if err != nil {
		return mapErr("chmod", d.full(name), err)
	}
	defer unix.Close(fd)
	mode := uint32(perm.Perm())
	if perm&fs.ModeSticky != 0 {
		mode |= unix.S_ISVTX
	}
	return mapErr("chmod", d.full(name), unix.Fchmod(fd, mode))
}

// beneath is the Files view of the files below a root directory that
// resolves paths below the root without following links. The root itself
// is resolved as usual; paths must lie at or below limit.
type beneath struct {
	f     *files
	root  string
	limit string
}

// Beneath implements system.Files.
func (f *files) Beneath(root string) system.Files { return &beneath{f: f, root: root, limit: root} }

// Beneath implements system.Files: the new view still resolves from the
// outer root, so links between the two roots aren't followed either.
func (b *beneath) Beneath(root string) system.Files {
	return &beneath{f: b.f, root: b.root, limit: root}
}

func inside(p, root string) bool { return root == "/" || p == root || strings.HasPrefix(p, root+"/") }

// locate checks p and returns its parts below the root.
func (b *beneath) locate(p string) ([]string, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	if err := checkPath(b.root); err != nil {
		return nil, err
	}
	if !inside(p, b.limit) || !inside(b.limit, b.root) {
		return nil, fmt.Errorf("%s is outside %s: %w", p, b.limit, system.ErrInvalid)
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p, b.root), "/")
	if rel == "" {
		return nil, nil
	}
	return strings.Split(rel, "/"), nil
}

func (b *beneath) openRoot() (*fdDir, error) {
	fd, err := unix.Open(b.f.h.path(b.root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErr("open", b.root, err)
	}
	return &fdDir{h: b.f.h, fd: fd, path: b.root}, nil
}

// at opens the root and the directory holding p, and returns that
// directory and p's last part. p must lie below the root.
func (b *beneath) at(p string) (*fdDir, string, error) {
	parts, err := b.locate(p)
	if err != nil {
		return nil, "", err
	}
	if len(parts) == 0 {
		return nil, "", fmt.Errorf("%s is the root of this view: %w", p, system.ErrInvalid)
	}
	root, err := b.openRoot()
	if err != nil {
		return nil, "", err
	}
	defer root.close()
	return root.parent(parts)
}

func (b *beneath) accounts() *accountDB {
	db, err := b.f.h.accounts()
	if err != nil {
		db = &accountDB{}
	}
	return db
}

func (b *beneath) Stat(ctx context.Context, p string) (system.FileInfo, error) {
	parts, err := b.locate(p)
	if err != nil {
		return system.FileInfo{}, err
	}
	root, err := b.openRoot()
	if err != nil {
		return system.FileInfo{}, err
	}
	defer root.close()
	if len(parts) == 0 {
		return root.self(p, b.accounts())
	}
	dir, name, err := root.parent(parts)
	if err != nil {
		return system.FileInfo{}, err
	}
	defer dir.close()
	return dir.lstat(name, p, b.accounts())
}

func (b *beneath) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	dir, name, err := b.at(p)
	if err != nil {
		return nil, err
	}
	defer dir.close()
	return dir.openRegular(name)
}

func (b *beneath) OpenRandom(ctx context.Context, p string) (system.RandomReader, error) {
	dir, name, err := b.at(p)
	if err != nil {
		return nil, err
	}
	defer dir.close()
	f, err := dir.openRegular(name)
	if err != nil {
		return nil, err
	}
	return regularRandom(f, p)
}

func (b *beneath) ReadFile(ctx context.Context, p string, max int64) ([]byte, error) {
	r, err := b.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readLimited(r, p, max)
}

func (b *beneath) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode, owner system.Owner) error {
	return b.write(p, perm, owner, func(w io.Writer) (int64, error) {
		n, err := w.Write(data)
		return int64(n), err
	})
}

func (b *beneath) WriteFrom(ctx context.Context, p string, r io.Reader, perm fs.FileMode, owner system.Owner) error {
	return b.write(p, perm, owner, func(w io.Writer) (int64, error) { return io.Copy(w, r) })
}

func (b *beneath) write(p string, perm fs.FileMode, owner system.Owner, fill func(io.Writer) (int64, error)) error {
	uid, gid, err := b.f.h.lookupIDs(owner)
	if err != nil {
		return err
	}
	dir, name, err := b.at(p)
	if err != nil {
		return err
	}
	defer dir.close()
	_, err = dir.create(name, fill, perm, uid, gid)
	return err
}

func (b *beneath) Mkdir(ctx context.Context, p string, perm fs.FileMode, owner system.Owner) error {
	parts, err := b.locate(p)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		// The root's parent is outside the view.
		return b.f.Mkdir(ctx, p, perm, owner)
	}
	uid, gid, err := b.f.h.lookupIDs(owner)
	if err != nil {
		return err
	}
	dir, name, err := b.at(p)
	if err != nil {
		return err
	}
	defer dir.close()
	return dir.mkdir(name, perm, uid, gid)
}

func (b *beneath) ReadDir(ctx context.Context, p string) ([]system.FileInfo, error) {
	parts, err := b.locate(p)
	if err != nil {
		return nil, err
	}
	root, err := b.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.close()
	dir, err := root.dir(parts)
	if err != nil {
		return nil, err
	}
	defer dir.close()
	return dir.list(p, b.accounts())
}

func (b *beneath) Remove(ctx context.Context, p string) error {
	dir, name, err := b.at(p)
	if err != nil {
		return err
	}
	defer dir.close()
	return dir.remove(name)
}

func (b *beneath) RemoveAll(ctx context.Context, p string) error {
	dir, name, err := b.at(p)
	if err != nil {
		return err
	}
	defer dir.close()
	return dir.removeAll(name)
}

func (b *beneath) Rename(ctx context.Context, from, to string) error {
	src, srcName, err := b.at(from)
	if err != nil {
		return err
	}
	defer src.close()
	dst, dstName, err := b.at(to)
	if err != nil {
		return err
	}
	defer dst.close()
	return mapErr("rename", from, unix.Renameat(src.fd, srcName, dst.fd, dstName))
}

func (b *beneath) Symlink(ctx context.Context, target, p string) error {
	dir, name, err := b.at(p)
	if err != nil {
		return err
	}
	defer dir.close()
	uid, gid, err := b.f.h.lookupIDs(system.Owner{})
	if err != nil {
		return err
	}
	return dir.symlink(target, name, uid, gid)
}

func (b *beneath) Chown(ctx context.Context, p string, owner system.Owner) error {
	uid, gid, err := b.f.h.lookupIDs(owner)
	if err != nil {
		return err
	}
	parts, err := b.locate(p)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		root, err := b.openRoot()
		if err != nil {
			return err
		}
		defer root.close()
		var st unix.Stat_t
		if err := unix.Fstat(root.fd, &st); err != nil {
			return mapErr("chown", p, err)
		}
		if int(st.Uid) == uid && int(st.Gid) == gid {
			return nil
		}
		return mapErr("chown", p, unix.Fchown(root.fd, uid, gid))
	}
	dir, name, err := b.at(p)
	if err != nil {
		return err
	}
	defer dir.close()
	return dir.chown(name, uid, gid)
}

func (b *beneath) Chmod(ctx context.Context, p string, perm fs.FileMode) error {
	parts, err := b.locate(p)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		root, err := b.openRoot()
		if err != nil {
			return err
		}
		defer root.close()
		mode := uint32(perm.Perm())
		if perm&fs.ModeSticky != 0 {
			mode |= unix.S_ISVTX
		}
		return mapErr("chmod", p, unix.Fchmod(root.fd, mode))
	}
	dir, name, err := b.at(p)
	if err != nil {
		return err
	}
	defer dir.close()
	return dir.chmod(name, perm)
}

// OpenTree opens a directory below the root as a tree, without following
// links on the way to it.
func (b *beneath) OpenTree(ctx context.Context, p string) (system.Tree, error) {
	parts, err := b.locate(p)
	if err != nil {
		return nil, err
	}
	root, err := b.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.close()
	dir, err := root.dir(parts)
	if err != nil {
		return nil, err
	}
	return newTree(b.f.h, dir), nil
}

// readLimited reads r whole, failing with ErrTooLarge past max when max is
// positive.
func readLimited(r io.Reader, p string, max int64) ([]byte, error) {
	if max <= 0 {
		data, err := io.ReadAll(r)
		return data, mapErr("read", p, err)
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
