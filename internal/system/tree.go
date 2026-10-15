package system

import (
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// Tree is a directory that operations can't leave. Names are slash-separated
// and relative to the tree's directory; "." is the directory itself.
// Symbolic links in the tree are never followed, even when its contents
// change while it is in use, so the agent can work inside directories that
// accounts can write to: a name that passes through a link is refused, and
// links are read, packed and created as links. Ownership and modes are set
// on open files, never by name.
type Tree interface {
	// Lstat describes a name without following a final symbolic link.
	Lstat(name string) (FileInfo, error)
	ReadDir(name string) ([]FileInfo, error)
	// Open streams a regular file.
	Open(name string) (io.ReadCloser, error)
	// OpenRandom opens a regular file for reading at any offset.
	OpenRandom(name string) (RandomReader, error)
	// Mkdir creates one directory; its parent must exist. An existing
	// directory fails with ErrExists.
	Mkdir(name string, perm fs.FileMode, owner Owner) error
	// Create writes a new regular file from r, replacing an existing file
	// but never a directory, and returns the bytes written.
	Create(name string, r io.Reader, perm fs.FileMode, owner Owner) (int64, error)
	// Symlink creates a relative symbolic link that stays inside the tree.
	Symlink(target, name string, owner Owner) error
	Remove(name string) error
	Close() error
}

// RandomReader reads a file at any offset, as zip archives need.
type RandomReader interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

// CleanTreeName checks a name inside a tree and returns it cleaned. Absolute
// names and names that climb out with ".." are refused rather than cleaned
// up, since nothing legitimate needs them.
func CleanTreeName(name string) (string, error) {
	if name == "" || name == "." {
		return ".", nil
	}
	if strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("name %q: %w", name, ErrInvalid)
	}
	if path.IsAbs(name) || slices.Contains(strings.Split(name, "/"), "..") {
		return "", fmt.Errorf("name %q leaves its directory: %w", name, ErrInvalid)
	}
	return path.Clean(name), nil
}

// LinkStaysInside reports whether a symbolic link at name pointing to target
// resolves inside the tree, judging by the names alone.
func LinkStaysInside(name, target string) bool {
	if target == "" || path.IsAbs(target) || strings.ContainsRune(target, 0) {
		return false
	}
	resolved := path.Join(path.Dir(name), target)
	return resolved != ".." && !strings.HasPrefix(resolved, "../")
}
