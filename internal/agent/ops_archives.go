package agent

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Archives are packed and unpacked inside a directory opened as a tree,
// reached from its managed root without following links, and links in the
// tree are never followed either, so neither the archive's names nor links
// an account plants can make the agent read or write anything elsewhere.

// openTree opens an archive's root as a tree, reached from its managed
// root without following links, so a link planted in place of the root
// can't move the whole tree somewhere else.
func openTree(ctx context.Context, env *Env, root string) (system.Tree, error) {
	files, err := beneath(env, root, false)
	if err != nil {
		return nil, err
	}
	return files.OpenTree(ctx, root)
}

// ArchiveResult is what packing or unpacking did.
type ArchiveResult struct {
	Result
	Stats system.ArchiveStats `json:"stats"`
}

// CreateArchiveInput packs a directory.
type CreateArchiveInput struct {
	// Root is the directory both names are inside, such as an account's.
	Root string `json:"root" doc:"Directory the source and the archive are in"`
	// Source and Dest are relative to Root.
	Source  string               `json:"source" doc:"Directory to pack, relative to root"`
	Dest    string               `json:"dest" doc:"Archive to write, relative to root"`
	Format  system.ArchiveFormat `json:"format" enum:"tar,tar.gz,tar.zst,zip"`
	Exclude []string             `json:"exclude,omitempty" doc:"Glob patterns of paths to leave out, such as .git"`
	Mode    Mode                 `json:"mode" doc:"Permission bits of the archive"`
	Owner   system.Owner         `json:"owner,omitzero" doc:"Owner of the archive"`
	// MaxBytes bounds what is packed; the default is 64 GiB.
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// Validate implements Input.
func (in CreateArchiveInput) Validate() error {
	if err := checkWrite(in.Root); err != nil {
		return err
	}
	if _, err := system.CleanTreeName(in.Source); err != nil {
		return err
	}
	dest, err := system.CleanTreeName(in.Dest)
	if err != nil {
		return err
	}
	if dest == "." {
		return fmt.Errorf("the archive needs a file name")
	}
	if !in.Format.Valid() {
		return fmt.Errorf("archive format %q", in.Format)
	}
	if len(in.Exclude) > 256 {
		return fmt.Errorf("too many exclude patterns")
	}
	for _, p := range in.Exclude {
		if _, err := path.Match(p, ""); err != nil || len(p) > 256 {
			return fmt.Errorf("exclude pattern %q", p)
		}
	}
	if in.MaxBytes < 0 {
		return fmt.Errorf("maxBytes must not be negative")
	}
	if err := checkFileMode(in.Mode, false); err != nil {
		return err
	}
	return checkOwner(in.Owner)
}

// CreateArchive packs a directory into an archive.
var CreateArchive = Register(Def[CreateArchiveInput, ArchiveResult]{
	Name:        "CreateArchive",
	Summary:     "Pack a directory into an archive",
	Description: "Packs a directory into a tar, tar.gz, tar.zst or zip archive inside the same root. Links are packed as links and never followed; the archive replaces any file of the same name atomically.",
	Effects: func(in CreateArchiveInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "write", Target: path.Join(in.Root, in.Dest)}}
	},
	Run: func(ctx context.Context, env *Env, in CreateArchiveInput) (ArchiveResult, error) {
		t, err := openTree(ctx, env, in.Root)
		if err != nil {
			return ArchiveResult{}, err
		}
		defer t.Close()
		info, err := t.Lstat(in.Source)
		if err != nil {
			return ArchiveResult{}, err
		}
		if !info.IsDir() {
			return ArchiveResult{}, fmt.Errorf("%s isn't a directory: %w", in.Source, system.ErrInvalid)
		}
		effect := system.Effect{Type: system.EffectFile, Action: "write", Target: path.Join(in.Root, in.Dest)}
		if env.Check {
			return ArchiveResult{Result: Changed(effect)}, nil
		}
		stats, err := system.Pack(ctx, t, system.PackSpec{
			Source: in.Source, Dest: in.Dest, Format: in.Format, Exclude: in.Exclude,
			Perm: in.Mode.FileMode(), Owner: in.Owner, MaxBytes: in.MaxBytes,
		})
		if err != nil {
			return ArchiveResult{}, err
		}
		effect.DiskBytes = stats.Bytes
		return ArchiveResult{Result: Changed(effect), Stats: stats}, nil
	},
})

// ExtractArchiveInput unpacks an archive.
type ExtractArchiveInput struct {
	Root    string               `json:"root" doc:"Directory the archive and the destination are in"`
	Archive string               `json:"archive" doc:"Archive to unpack, relative to root"`
	Dest    string               `json:"dest" doc:"Directory to unpack into, relative to root; created when missing"`
	Format  system.ArchiveFormat `json:"format" enum:"tar,tar.gz,tar.zst,zip"`
	// StripComponents drops leading directories from every entry.
	StripComponents int          `json:"stripComponents,omitempty"`
	Owner           system.Owner `json:"owner,omitzero" doc:"Owner of everything unpacked"`
	// Merge unpacks into a directory that isn't empty, replacing files of
	// the same names.
	Merge      bool  `json:"merge,omitempty" doc:"Unpack into a directory that isn't empty"`
	MaxBytes   int64 `json:"maxBytes,omitempty" doc:"Most bytes the archive may unpack to; 64 GiB by default"`
	MaxEntries int   `json:"maxEntries,omitempty" doc:"Most entries the archive may have; a million by default"`
}

// Validate implements Input.
func (in ExtractArchiveInput) Validate() error {
	if err := checkWrite(in.Root); err != nil {
		return err
	}
	if _, err := system.CleanTreeName(in.Archive); err != nil {
		return err
	}
	if _, err := system.CleanTreeName(in.Dest); err != nil {
		return err
	}
	if !in.Format.Valid() {
		return fmt.Errorf("archive format %q", in.Format)
	}
	if in.StripComponents < 0 || in.StripComponents > 16 || in.MaxBytes < 0 || in.MaxEntries < 0 {
		return fmt.Errorf("strip components and limits must be sensible")
	}
	return checkOwner(in.Owner)
}

// ExtractArchive unpacks an archive into a directory.
var ExtractArchive = Register(Def[ExtractArchiveInput, ArchiveResult]{
	Name:    "ExtractArchive",
	Summary: "Unpack an archive",
	Description: "Unpacks a tar, tar.gz, tar.zst or zip archive into a directory inside the same root, owned by the given user. " +
		"Entries with absolute names or names that climb out, and links that lead out of the destination, are refused, and sizes are bounded.",
	Effects: func(in ExtractArchiveInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "unpack", Target: path.Join(in.Root, in.Dest)}}
	},
	Run: func(ctx context.Context, env *Env, in ExtractArchiveInput) (ArchiveResult, error) {
		t, err := openTree(ctx, env, in.Root)
		if err != nil {
			return ArchiveResult{}, err
		}
		defer t.Close()
		if info, err := t.Lstat(in.Archive); err != nil {
			return ArchiveResult{}, err
		} else if !info.Mode.IsRegular() {
			return ArchiveResult{}, fmt.Errorf("%s isn't a file: %w", in.Archive, system.ErrInvalid)
		}
		effect := system.Effect{Type: system.EffectFile, Action: "unpack", Target: path.Join(in.Root, in.Dest)}
		if env.Check {
			if !in.Merge {
				entries, err := t.ReadDir(in.Dest)
				if err == nil && len(entries) > 0 {
					return ArchiveResult{}, fmt.Errorf("%s isn't empty: %w", in.Dest, system.ErrNotEmpty)
				} else if err != nil && !errors.Is(err, system.ErrNotFound) {
					return ArchiveResult{}, err
				}
			}
			return ArchiveResult{Result: Changed(effect)}, nil
		}
		stats, err := system.Unpack(ctx, t, system.UnpackSpec{
			Archive: in.Archive, Dest: in.Dest, Format: in.Format, StripComponents: in.StripComponents,
			Owner: in.Owner, Merge: in.Merge, MaxBytes: in.MaxBytes, MaxEntries: in.MaxEntries,
		})
		if err != nil {
			return ArchiveResult{}, err
		}
		effect.DiskBytes = stats.Bytes
		return ArchiveResult{Result: Changed(effect), Stats: stats}, nil
	},
})
