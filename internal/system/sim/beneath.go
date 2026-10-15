package sim

import (
	"context"
	"io"
	"io/fs"
	"path"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// simBeneath is the simulated view of the files below a root. Nothing in
// the simulated filesystem ever follows a link, so a path that passes
// through one doesn't exist there; the view refuses such paths outright, as
// the real one does.
type simBeneath struct {
	f           simFiles
	root, limit string
}

// Beneath implements system.Files.
func (f simFiles) Beneath(root string) system.Files { return simBeneath{f: f, root: root, limit: root} }

// Beneath implements system.Files.
func (b simBeneath) Beneath(root string) system.Files {
	return simBeneath{f: b.f, root: b.root, limit: root}
}

// check refuses paths outside the view and paths that pass through a link
// below the root.
func (b simBeneath) check(ctx context.Context, p string) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	if !isUnder(p, b.limit) || !isUnder(b.limit, b.root) {
		return invalid("%s is outside %s", p, b.limit)
	}
	m := b.f.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	for dir := path.Dir(p); dir != b.root && isUnder(dir, b.root) && dir != "/"; dir = path.Dir(dir) {
		if n, ok := m.files[dir]; ok && n.mode&fs.ModeSymlink != 0 {
			return invalid("%s is a symbolic link, which isn't followed here", dir)
		}
	}
	return nil
}

func (b simBeneath) Stat(ctx context.Context, p string) (system.FileInfo, error) {
	if err := b.check(ctx, p); err != nil {
		return system.FileInfo{}, err
	}
	return b.f.Stat(ctx, p)
}

func (b simBeneath) ReadFile(ctx context.Context, p string, max int64) ([]byte, error) {
	if err := b.check(ctx, p); err != nil {
		return nil, err
	}
	return b.f.ReadFile(ctx, p, max)
}

func (b simBeneath) WriteFile(ctx context.Context, p string, data []byte, perm fs.FileMode, owner system.Owner) error {
	if err := b.check(ctx, p); err != nil {
		return err
	}
	return b.f.WriteFile(ctx, p, data, perm, owner)
}

func (b simBeneath) WriteFrom(ctx context.Context, p string, r io.Reader, perm fs.FileMode, owner system.Owner) error {
	if err := b.check(ctx, p); err != nil {
		return err
	}
	return b.f.WriteFrom(ctx, p, r, perm, owner)
}

func (b simBeneath) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := b.check(ctx, p); err != nil {
		return nil, err
	}
	return b.f.Open(ctx, p)
}

func (b simBeneath) OpenRandom(ctx context.Context, p string) (system.RandomReader, error) {
	if err := b.check(ctx, p); err != nil {
		return nil, err
	}
	return b.f.OpenRandom(ctx, p)
}

func (b simBeneath) OpenTree(ctx context.Context, dir string) (system.Tree, error) {
	if err := b.check(ctx, dir); err != nil {
		return nil, err
	}
	return b.f.OpenTree(ctx, dir)
}

func (b simBeneath) Mkdir(ctx context.Context, p string, perm fs.FileMode, owner system.Owner) error {
	if err := b.check(ctx, p); err != nil {
		return err
	}
	return b.f.Mkdir(ctx, p, perm, owner)
}

func (b simBeneath) ReadDir(ctx context.Context, p string) ([]system.FileInfo, error) {
	if err := b.check(ctx, p); err != nil {
		return nil, err
	}
	return b.f.ReadDir(ctx, p)
}

func (b simBeneath) Remove(ctx context.Context, p string) error {
	if err := b.check(ctx, p); err != nil {
		return err
	}
	return b.f.Remove(ctx, p)
}

func (b simBeneath) RemoveAll(ctx context.Context, p string) error {
	if err := b.check(ctx, p); err != nil {
		return err
	}
	return b.f.RemoveAll(ctx, p)
}

func (b simBeneath) Rename(ctx context.Context, from, to string) error {
	if err := b.check(ctx, from); err != nil {
		return err
	}
	if err := b.check(ctx, to); err != nil {
		return err
	}
	return b.f.Rename(ctx, from, to)
}

func (b simBeneath) Symlink(ctx context.Context, target, p string) error {
	if err := b.check(ctx, p); err != nil {
		return err
	}
	return b.f.Symlink(ctx, target, p)
}

func (b simBeneath) Chown(ctx context.Context, p string, owner system.Owner) error {
	if err := b.check(ctx, p); err != nil {
		return err
	}
	return b.f.Chown(ctx, p, owner)
}

func (b simBeneath) Chmod(ctx context.Context, p string, perm fs.FileMode) error {
	if err := b.check(ctx, p); err != nil {
		return err
	}
	return b.f.Chmod(ctx, p, perm)
}
