package sim

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type bytesRandom struct {
	*bytes.Reader
}

func (bytesRandom) Close() error { return nil }

func (f simFiles) OpenRandom(ctx context.Context, p string) (system.RandomReader, error) {
	data, err := f.ReadFile(ctx, p, 0)
	if err != nil {
		return nil, err
	}
	return bytesRandom{bytes.NewReader(data)}, nil
}

// OpenTree opens a directory as a Tree. The simulated filesystem never
// lets a path pass through a symbolic link, and the tree resolves links
// itself without leaving the directory, as os.Root does on a real server.
func (f simFiles) OpenTree(ctx context.Context, dir string) (system.Tree, error) {
	dir, err := cleanAbs(dir)
	if err != nil {
		return nil, err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	n, ok := m.files[dir]
	if !ok {
		return nil, notFound(dir)
	}
	if !n.mode.IsDir() {
		return nil, invalid("%s is not a directory", dir)
	}
	return &simTree{m: m, dir: dir}, nil
}

type simTree struct {
	m   *Machine
	dir string
}

func (t *simTree) full(name string) (string, error) {
	name, err := system.CleanTreeName(name)
	if err != nil {
		return "", err
	}
	return path.Join(t.dir, name), nil
}

func (t *simTree) rel(full string) string {
	if full == t.dir {
		return "."
	}
	if t.dir == "/" {
		return full[1:]
	}
	return full[len(t.dir)+1:]
}

// resolve follows symbolic links the way os.Root does: relative links are
// followed as long as they stay in the tree, anything else is refused. The
// caller holds m.mu.
func (t *simTree) resolve(full string) (string, *node, error) {
	for range 40 {
		n, ok := t.m.files[full]
		if !ok {
			return "", nil, notFound(full)
		}
		if n.mode&fs.ModeSymlink == 0 {
			return full, n, nil
		}
		if !system.LinkStaysInside(t.rel(full), n.target) {
			return "", nil, invalid("%s links out of %s", full, t.dir)
		}
		full = path.Join(path.Dir(full), n.target)
	}
	return "", nil, invalid("%s: too many levels of symbolic links", full)
}

func (t *simTree) info(full string, n *node) system.FileInfo {
	info := t.m.info(t.rel(full), n)
	return info
}

func (t *simTree) Lstat(name string) (system.FileInfo, error) {
	full, err := t.full(name)
	if err != nil {
		return system.FileInfo{}, err
	}
	m := t.m
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.files[full]
	if !ok {
		return system.FileInfo{}, notFound(full)
	}
	return t.info(full, n), nil
}

func (t *simTree) ReadDir(name string) ([]system.FileInfo, error) {
	full, err := t.full(name)
	if err != nil {
		return nil, err
	}
	m := t.m
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, n, err := t.resolve(full)
	if err != nil {
		return nil, err
	}
	if !n.mode.IsDir() {
		return nil, invalid("%s is not a directory", full)
	}
	var out []system.FileInfo
	for p, cn := range m.files {
		if p != dir && path.Dir(p) == dir {
			info := t.info(p, cn)
			info.Path = path.Join(t.rel(full), path.Base(p))
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (t *simTree) regular(name string) ([]byte, error) {
	full, err := t.full(name)
	if err != nil {
		return nil, err
	}
	m := t.m
	m.mu.Lock()
	defer m.mu.Unlock()
	_, n, err := t.resolve(full)
	if err != nil {
		return nil, err
	}
	if !n.mode.IsRegular() {
		return nil, invalid("%s isn't a regular file", full)
	}
	return bytes.Clone(n.data), nil
}

func (t *simTree) Open(name string) (io.ReadCloser, error) {
	data, err := t.regular(name)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (t *simTree) OpenRandom(name string) (system.RandomReader, error) {
	data, err := t.regular(name)
	if err != nil {
		return nil, err
	}
	return bytesRandom{bytes.NewReader(data)}, nil
}

func (t *simTree) Mkdir(name string, perm fs.FileMode, owner system.Owner) error {
	full, err := t.full(name)
	if err != nil {
		return err
	}
	m := t.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("files", "mkdir", full); err != nil {
		return err
	}
	return m.mkdir(full, perm, owner)
}

func (t *simTree) Create(name string, r io.Reader, perm fs.FileMode, owner system.Owner) (int64, error) {
	full, err := t.full(name)
	if err != nil {
		return 0, err
	}
	if full == t.dir {
		return 0, invalid("%s is the tree itself", full)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return int64(len(data)), err
	}
	m := t.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("files", "write", full); err != nil {
		return 0, err
	}
	if err := m.writeFile(full, data, perm, owner); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

func (t *simTree) Symlink(target, name string, owner system.Owner) error {
	full, err := t.full(name)
	if err != nil {
		return err
	}
	if !system.LinkStaysInside(t.rel(full), target) {
		return fmt.Errorf("link %s to %q leaves the tree: %w", full, target, system.ErrInvalid)
	}
	m := t.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.record("files", "symlink", full); err != nil {
		return err
	}
	if _, ok := m.files[full]; ok {
		return fmt.Errorf("%s: %w", full, system.ErrExists)
	}
	if err := m.parentDir(full); err != nil {
		return err
	}
	uid, gid, err := m.pathOwner(owner)
	if err != nil {
		return err
	}
	m.files[full] = &node{mode: fs.ModeSymlink | 0o777, uid: uid, gid: gid, target: target, modTime: m.now()}
	return nil
}

func (t *simTree) Remove(name string) error {
	full, err := t.full(name)
	if err != nil {
		return err
	}
	if full == t.dir {
		return invalid("refusing to remove the tree itself")
	}
	return simFiles{t.m}.Remove(context.Background(), full)
}

func (t *simTree) Close() error { return nil }
