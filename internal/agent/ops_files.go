package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

const maxFileContent = 64 << 20

// WriteFileInput writes one file.
type WriteFileInput struct {
	Path    string       `json:"path" doc:"Absolute path of the file"`
	Content []byte       `json:"content,omitempty" doc:"The file's contents; empty when absent"`
	Mode    Mode         `json:"mode" doc:"Permission bits in octal, such as 0644"`
	Owner   system.Owner `json:"owner,omitzero" doc:"Owner; root when empty"`
}

// Validate implements Input.
func (in WriteFileInput) Validate() error {
	if err := checkWrite(in.Path); err != nil {
		return err
	}
	if len(in.Content) > maxFileContent {
		return fmt.Errorf("content is larger than %d bytes", maxFileContent)
	}
	if err := checkFileMode(in.Mode, false); err != nil {
		return err
	}
	return checkOwner(in.Owner)
}

// WriteFile replaces a file atomically when its contents, mode or owner
// differ from what is asked.
var WriteFile = Register(Def[WriteFileInput, Result]{
	Name:        "WriteFile",
	Summary:     "Write a file",
	Description: "Writes a file atomically inside a location Racksail manages. Nothing happens when the file already has the same contents, mode and owner.",
	Effects: func(in WriteFileInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "write", Target: in.Path, DiskBytes: int64(len(in.Content))}}
	},
	Run: func(ctx context.Context, env *Env, in WriteFileInput) (Result, error) {
		f := env.System.Files
		if err := checkNoSymlinks(ctx, f, in.Path); err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectFile, Action: "write", Target: in.Path, DiskBytes: int64(len(in.Content))}
		info, err := f.Stat(ctx, in.Path)
		switch {
		case err == nil && info.IsDir():
			return Result{}, fmt.Errorf("%s is a directory: %w", in.Path, system.ErrExists)
		case err == nil && !info.IsSymlink() && info.Perm() == in.Mode.FileMode().Perm() && ownedBy(info, in.Owner):
			current, err := f.ReadFile(ctx, in.Path, maxFileContent)
			if err == nil && bytes.Equal(current, in.Content) {
				return Unchanged(), nil
			}
		case err != nil && !errors.Is(err, system.ErrNotFound):
			return Result{}, err
		}
		if env.Check {
			return Changed(effect), nil
		}
		if err := f.WriteFile(ctx, in.Path, in.Content, in.Mode.FileMode(), in.Owner); err != nil {
			return Result{}, err
		}
		return Changed(effect), nil
	},
})

func ownedBy(info system.FileInfo, o system.Owner) bool {
	user, group := o.User, o.Group
	if user == "" {
		user = "root"
	}
	if group == "" {
		if o.User != "" {
			// The user's primary group; the adapter resolves it on write.
			return info.Owner == user
		}
		group = "root"
	}
	return info.Owner == user && info.Group == group
}

// EnsureDirInput creates a directory.
type EnsureDirInput struct {
	Path  string       `json:"path" doc:"Absolute path of the directory"`
	Mode  Mode         `json:"mode" doc:"Permission bits in octal, such as 0755"`
	Owner system.Owner `json:"owner,omitzero" doc:"Owner; root when empty"`
	// Parents creates missing parent directories, owned by root with mode
	// 0755.
	Parents bool `json:"parents,omitempty" doc:"Create missing parent directories"`
}

// Validate implements Input.
func (in EnsureDirInput) Validate() error {
	if err := checkWrite(in.Path); err != nil {
		return err
	}
	if err := checkFileMode(in.Mode, true); err != nil {
		return err
	}
	return checkOwner(in.Owner)
}

// EnsureDir makes sure a directory exists with the given mode and owner.
var EnsureDir = Register(Def[EnsureDirInput, Result]{
	Name:        "EnsureDir",
	Summary:     "Create a directory",
	Description: "Creates a directory, or fixes the mode and owner of an existing one.",
	Effects: func(in EnsureDirInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "mkdir", Target: in.Path}}
	},
	Run: func(ctx context.Context, env *Env, in EnsureDirInput) (Result, error) {
		f := env.System.Files
		if err := checkNoSymlinks(ctx, f, in.Path); err != nil {
			return Result{}, err
		}
		info, err := f.Stat(ctx, in.Path)
		if err == nil {
			if !info.IsDir() {
				return Result{}, fmt.Errorf("%s exists and isn't a directory: %w", in.Path, system.ErrExists)
			}
			want := in.Mode.FileMode()
			sameMode := info.Perm() == want.Perm() && info.Mode&fs.ModeSticky == want&fs.ModeSticky
			if sameMode && ownedBy(info, in.Owner) {
				return Unchanged(), nil
			}
			effect := system.Effect{Type: system.EffectFile, Action: "chmod", Target: in.Path}
			if env.Check {
				return Changed(effect), nil
			}
			if err := f.Chown(ctx, in.Path, in.Owner); err != nil {
				return Result{}, err
			}
			if err := f.Chmod(ctx, in.Path, in.Mode.FileMode()); err != nil {
				return Result{}, err
			}
			return Changed(effect), nil
		}
		if !errors.Is(err, system.ErrNotFound) {
			return Result{}, err
		}
		var missing []string
		if in.Parents {
			for dir := path.Dir(in.Path); checkWrite(dir) == nil; dir = path.Dir(dir) {
				if _, err := f.Stat(ctx, dir); err == nil {
					break
				}
				missing = append([]string{dir}, missing...)
			}
		}
		var effects []system.Effect
		for _, dir := range append(missing, in.Path) {
			effects = append(effects, system.Effect{Type: system.EffectFile, Action: "mkdir", Target: dir})
		}
		if env.Check {
			return Changed(effects...), nil
		}
		for _, dir := range missing {
			if err := f.Mkdir(ctx, dir, 0o755, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
				return Result{}, err
			}
		}
		if err := f.Mkdir(ctx, in.Path, in.Mode.FileMode(), in.Owner); err != nil {
			return Result{}, err
		}
		return Changed(effects...), nil
	},
})

// RemovePathInput removes a file or directory.
type RemovePathInput struct {
	Path      string `json:"path" doc:"Absolute path to remove"`
	Recursive bool   `json:"recursive,omitempty" doc:"Remove a directory with everything in it"`
}

// Validate implements Input.
func (in RemovePathInput) Validate() error {
	if err := checkWrite(in.Path); err != nil {
		return err
	}
	if isWriteRoot(in.Path) {
		return fmt.Errorf("%s is a Racksail root directory and can't be removed", in.Path)
	}
	return nil
}

// RemovePath deletes a file, a link or a directory.
var RemovePath = Register(Def[RemovePathInput, Result]{
	Name:        "RemovePath",
	Summary:     "Remove a file or directory",
	Description: "Removes a file, a symbolic link or a directory. Directories must be empty unless recursive is set.",
	Effects: func(in RemovePathInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "delete", Target: in.Path, Destructive: true}}
	},
	Run: func(ctx context.Context, env *Env, in RemovePathInput) (Result, error) {
		f := env.System.Files
		if err := checkNoSymlinks(ctx, f, in.Path); err != nil {
			return Result{}, err
		}
		if _, err := f.Stat(ctx, in.Path); errors.Is(err, system.ErrNotFound) {
			return Unchanged(), nil
		} else if err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectFile, Action: "delete", Target: in.Path, Destructive: true}
		if env.Check {
			return Changed(effect), nil
		}
		var err error
		if in.Recursive {
			err = f.RemoveAll(ctx, in.Path)
		} else {
			err = f.Remove(ctx, in.Path)
		}
		if err != nil {
			return Result{}, err
		}
		return Changed(effect), nil
	},
})

// EnsureSymlinkInput creates a symbolic link.
type EnsureSymlinkInput struct {
	Path   string `json:"path" doc:"Where the link is"`
	Target string `json:"target" doc:"What the link points to"`
}

// Validate implements Input.
func (in EnsureSymlinkInput) Validate() error {
	if err := checkWrite(in.Path); err != nil {
		return err
	}
	target := in.Target
	if !path.IsAbs(target) {
		target = path.Join(path.Dir(in.Path), target)
	}
	return checkRead(path.Clean(target))
}

// EnsureSymlink points a symbolic link at a target, replacing an existing
// link with a different target. Releases switch atomically this way.
var EnsureSymlink = Register(Def[EnsureSymlinkInput, Result]{
	Name:        "EnsureSymlink",
	Summary:     "Point a symbolic link at a target",
	Description: "Creates a symbolic link, or repoints an existing one. Regular files and directories are never replaced.",
	Effects: func(in EnsureSymlinkInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "link", Target: in.Path, Detail: in.Target}}
	},
	Run: func(ctx context.Context, env *Env, in EnsureSymlinkInput) (Result, error) {
		f := env.System.Files
		if err := checkNoSymlinks(ctx, f, in.Path); err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectFile, Action: "link", Target: in.Path, Detail: in.Target}
		info, err := f.Stat(ctx, in.Path)
		switch {
		case err == nil && info.IsSymlink() && info.Target == in.Target:
			return Unchanged(), nil
		case err == nil && !info.IsSymlink():
			return Result{}, fmt.Errorf("%s exists and isn't a symbolic link: %w", in.Path, system.ErrExists)
		case err != nil && !errors.Is(err, system.ErrNotFound):
			return Result{}, err
		}
		if env.Check {
			return Changed(effect), nil
		}
		if err == nil {
			// Build the new link beside the old one and rename it over, so the
			// link is never missing.
			tmp := in.Path + ".racksail-new"
			if err := f.Remove(ctx, tmp); err != nil && !errors.Is(err, system.ErrNotFound) {
				return Result{}, err
			}
			if err := f.Symlink(ctx, in.Target, tmp); err != nil {
				return Result{}, err
			}
			if err := f.Rename(ctx, tmp, in.Path); err != nil {
				return Result{}, err
			}
			return Changed(effect), nil
		}
		if err := f.Symlink(ctx, in.Target, in.Path); err != nil {
			return Result{}, err
		}
		return Changed(effect), nil
	},
})

// ReadFileInput reads a file.
type ReadFileInput struct {
	Path     string `json:"path" doc:"Absolute path of the file"`
	MaxBytes int64  `json:"maxBytes,omitempty" doc:"Fail when the file is larger; defaults to 16 MiB"`
}

// Validate implements Input.
func (in ReadFileInput) Validate() error {
	if in.MaxBytes < 0 || in.MaxBytes > maxFileContent {
		return fmt.Errorf("maxBytes must be at most %d", maxFileContent)
	}
	return checkRead(in.Path)
}

// FileContent is the contents of a file.
type FileContent struct {
	Content []byte `json:"content"`
}

// ReadFile returns the contents of a file.
var ReadFile = Register(Def[ReadFileInput, FileContent]{
	Name:     "ReadFile",
	Summary:  "Read a file",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in ReadFileInput) (FileContent, error) {
		limit := in.MaxBytes
		if limit == 0 {
			limit = 16 << 20
		}
		data, err := env.System.Files.ReadFile(ctx, in.Path, limit)
		return FileContent{Content: data}, err
	},
})

// PathInput names one path.
type PathInput struct {
	Path string `json:"path" doc:"Absolute path"`
}

// Validate implements Input.
func (in PathInput) Validate() error { return checkRead(in.Path) }

// PathInfo describes a path that may not exist.
type PathInfo struct {
	Exists bool            `json:"exists"`
	Info   system.FileInfo `json:"info,omitzero"`
}

// StatPath describes a path without following a final symbolic link.
var StatPath = Register(Def[PathInput, PathInfo]{
	Name:     "StatPath",
	Summary:  "Describe a file or directory",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in PathInput) (PathInfo, error) {
		info, err := env.System.Files.Stat(ctx, in.Path)
		if errors.Is(err, system.ErrNotFound) {
			return PathInfo{}, nil
		}
		return PathInfo{Exists: err == nil, Info: info}, err
	},
})

// DirListing lists a directory.
type DirListing struct {
	Entries []system.FileInfo `json:"entries"`
}

// ListDir lists the entries of a directory.
var ListDir = Register(Def[PathInput, DirListing]{
	Name:     "ListDir",
	Summary:  "List a directory",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in PathInput) (DirListing, error) {
		entries, err := env.System.Files.ReadDir(ctx, in.Path)
		if entries == nil {
			entries = []system.FileInfo{}
		}
		return DirListing{Entries: entries}, err
	},
})
