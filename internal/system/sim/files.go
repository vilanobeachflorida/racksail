package sim

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type node struct {
	mode    fs.FileMode
	uid     int
	gid     int
	data    []byte
	target  string
	modTime time.Time
	noCOW   bool
}

func (n *node) clone() *node {
	c := *n
	c.data = slices.Clone(n.data)
	return &c
}

type simFiles struct{ m *Machine }

func cleanAbs(p string) (string, error) {
	if !path.IsAbs(p) {
		return "", invalid("path %q is not absolute", p)
	}
	return path.Clean(p), nil
}

// parentDir checks that the parent of p is an existing directory. The
// caller holds m.mu.
func (m *Machine) parentDir(p string) error {
	parent := path.Dir(p)
	n, ok := m.files[parent]
	if !ok {
		return notFound(parent)
	}
	if !n.mode.IsDir() {
		return invalid("%s is not a directory", parent)
	}
	return nil
}

func (m *Machine) info(p string, n *node) system.FileInfo {
	return system.FileInfo{
		Path:    p,
		Mode:    n.mode,
		Owner:   m.userName(n.uid),
		Group:   m.groupName(n.gid),
		UID:     n.uid,
		GID:     n.gid,
		Size:    int64(len(n.data)),
		ModTime: n.modTime,
		Target:  n.target,
	}
}

func (f simFiles) Stat(ctx context.Context, p string) (system.FileInfo, error) {
	p, err := cleanAbs(p)
	if err != nil {
		return system.FileInfo{}, err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return system.FileInfo{}, err
	}
	defer m.mu.Unlock()
	n, ok := m.files[p]
	if !ok {
		return system.FileInfo{}, notFound(p)
	}
	return m.info(p, n), nil
}

func (f simFiles) ReadFile(ctx context.Context, p string, max int64) ([]byte, error) {
	p, err := cleanAbs(p)
	if err != nil {
		return nil, err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	n, ok := m.files[p]
	if !ok {
		return nil, notFound(p)
	}
	if n.mode.IsDir() {
		return nil, invalid("%s is a directory", p)
	}
	if max > 0 && int64(len(n.data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes: %w", p, max, system.ErrTooLarge)
	}
	return slices.Clone(n.data), nil
}

func (f simFiles) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode, owner system.Owner) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("files", "write", p); err != nil {
		return err
	}
	return m.writeFile(p, data, perm, owner)
}

// writeFile replaces a file. The caller holds m.mu.
func (m *Machine) writeFile(p string, data []byte, perm fs.FileMode, owner system.Owner) error {
	if err := m.parentDir(p); err != nil {
		return err
	}
	if n, ok := m.files[p]; ok && n.mode.IsDir() {
		return invalid("%s is a directory", p)
	}
	uid, gid, err := m.pathOwner(owner)
	if err != nil {
		return err
	}
	m.files[p] = &node{mode: perm.Perm(), uid: uid, gid: gid, data: slices.Clone(data), modTime: m.now()}
	return nil
}

func (f simFiles) WriteFrom(ctx context.Context, p string, r io.Reader, perm fs.FileMode, owner system.Owner) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return f.WriteFile(ctx, p, data, perm, owner)
}

func (f simFiles) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	data, err := f.ReadFile(ctx, p, 0)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f simFiles) Mkdir(ctx context.Context, p string, perm fs.FileMode, owner system.Owner) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("files", "mkdir", p); err != nil {
		return err
	}
	return m.mkdir(p, perm, owner)
}

// mkdir creates one directory. The caller holds m.mu.
func (m *Machine) mkdir(p string, perm fs.FileMode, owner system.Owner) error {
	if _, ok := m.files[p]; ok {
		return fmt.Errorf("%s: %w", p, system.ErrExists)
	}
	if err := m.parentDir(p); err != nil {
		return err
	}
	uid, gid, err := m.pathOwner(owner)
	if err != nil {
		return err
	}
	m.files[p] = &node{mode: fs.ModeDir | perm.Perm() | perm&fs.ModeSticky, uid: uid, gid: gid, modTime: m.now()}
	return nil
}

func (f simFiles) ReadDir(ctx context.Context, p string) ([]system.FileInfo, error) {
	p, err := cleanAbs(p)
	if err != nil {
		return nil, err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	n, ok := m.files[p]
	if !ok {
		return nil, notFound(p)
	}
	if !n.mode.IsDir() {
		return nil, invalid("%s is not a directory", p)
	}
	var out []system.FileInfo
	for child, cn := range m.files {
		if child != p && path.Dir(child) == p {
			out = append(out, m.info(child, cn))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (f simFiles) Remove(ctx context.Context, p string) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("files", "remove", p); err != nil {
		return err
	}
	n, ok := m.files[p]
	if !ok {
		return notFound(p)
	}
	if n.mode.IsDir() {
		for child := range m.files {
			if child != p && isUnder(child, p) {
				return fmt.Errorf("%s: %w", p, system.ErrNotEmpty)
			}
		}
	}
	delete(m.files, p)
	return nil
}

func (f simFiles) RemoveAll(ctx context.Context, p string) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	if p == "/" {
		return invalid("refusing to remove /")
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("files", "remove_all", p); err != nil {
		return err
	}
	m.removeTree(p)
	return nil
}

// removeTree deletes p and everything below it. The caller holds m.mu.
func (m *Machine) removeTree(p string) {
	for child := range m.files {
		if isUnder(child, p) {
			delete(m.files, child)
		}
	}
}

func (f simFiles) Rename(ctx context.Context, from, to string) error {
	from, err := cleanAbs(from)
	if err != nil {
		return err
	}
	if to, err = cleanAbs(to); err != nil {
		return err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("files", "rename", from+" "+to); err != nil {
		return err
	}
	src, ok := m.files[from]
	if !ok {
		return notFound(from)
	}
	if err := m.parentDir(to); err != nil {
		return err
	}
	if isUnder(to, from) && to != from {
		return invalid("can't move %s into itself", from)
	}
	if dst, ok := m.files[to]; ok && dst.mode.IsDir() != src.mode.IsDir() {
		return invalid("%s and %s are of different types", from, to)
	}
	moved := map[string]*node{}
	for p, n := range m.files {
		if isUnder(p, from) {
			moved[to+p[len(from):]] = n
			delete(m.files, p)
		}
	}
	m.removeTree(to)
	for p, n := range moved {
		m.files[p] = n
	}
	return nil
}

func (f simFiles) Symlink(ctx context.Context, target, p string) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("files", "symlink", p); err != nil {
		return err
	}
	if _, ok := m.files[p]; ok {
		return fmt.Errorf("%s: %w", p, system.ErrExists)
	}
	if err := m.parentDir(p); err != nil {
		return err
	}
	m.files[p] = &node{mode: fs.ModeSymlink | 0o777, target: target, modTime: m.now()}
	return nil
}

func (f simFiles) Chown(ctx context.Context, p string, owner system.Owner) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("files", "chown", p); err != nil {
		return err
	}
	n, ok := m.files[p]
	if !ok {
		return notFound(p)
	}
	uid, gid, err := m.pathOwner(owner)
	if err != nil {
		return err
	}
	n.uid, n.gid = uid, gid
	return nil
}

func (f simFiles) Chmod(ctx context.Context, p string, perm fs.FileMode) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("files", "chmod", p); err != nil {
		return err
	}
	n, ok := m.files[p]
	if !ok {
		return notFound(p)
	}
	n.mode = n.mode.Type() | perm.Perm() | perm&fs.ModeSticky
	return nil
}
