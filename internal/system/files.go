package system

import (
	"context"
	"io"
	"io/fs"
	"time"
)

// Files manages files and directories. Paths are absolute. Operations don't
// follow symbolic links at the final path element.
type Files interface {
	// Stat describes a path without following a final symbolic link.
	Stat(ctx context.Context, path string) (FileInfo, error)
	// ReadFile returns a file's contents. When max is positive, files larger
	// than max fail with ErrTooLarge.
	ReadFile(ctx context.Context, path string, max int64) ([]byte, error)
	// WriteFile replaces a file atomically: readers see the old or the new
	// contents, never a mix. The parent directory must exist.
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode, owner Owner) error
	// WriteFrom is WriteFile for contents streamed from r.
	WriteFrom(ctx context.Context, path string, r io.Reader, perm fs.FileMode, owner Owner) error
	// Open streams a file's contents.
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// OpenRandom opens a regular file for reading at any offset.
	OpenRandom(ctx context.Context, path string) (RandomReader, error)
	// OpenTree opens a directory as a Tree, for work inside directories
	// other users can change.
	OpenTree(ctx context.Context, dir string) (Tree, error)
	// Mkdir creates one directory. The parent must exist; an existing path
	// fails with ErrExists.
	Mkdir(ctx context.Context, path string, perm fs.FileMode, owner Owner) error
	ReadDir(ctx context.Context, path string) ([]FileInfo, error)
	// Remove deletes a file, a symbolic link or an empty directory.
	Remove(ctx context.Context, path string) error
	// RemoveAll deletes a path and everything below it.
	RemoveAll(ctx context.Context, path string) error
	Rename(ctx context.Context, from, to string) error
	Symlink(ctx context.Context, target, path string) error
	Chown(ctx context.Context, path string, owner Owner) error
	Chmod(ctx context.Context, path string, perm fs.FileMode) error
}

// FileInfo describes a file.
type FileInfo struct {
	Path string `json:"path"`
	// Mode holds the type and permission bits, as in io/fs.
	Mode    fs.FileMode `json:"mode"`
	Owner   string      `json:"owner"`
	Group   string      `json:"group"`
	UID     int         `json:"uid"`
	GID     int         `json:"gid"`
	Size    int64       `json:"size"`
	ModTime time.Time   `json:"modTime"`
	// Target is the destination of a symbolic link.
	Target string `json:"target,omitempty"`
}

// IsDir reports whether the file is a directory.
func (f FileInfo) IsDir() bool { return f.Mode.IsDir() }

// IsSymlink reports whether the file is a symbolic link.
func (f FileInfo) IsSymlink() bool { return f.Mode&fs.ModeSymlink != 0 }

// Perm returns the permission bits.
func (f FileInfo) Perm() fs.FileMode { return f.Mode.Perm() }
