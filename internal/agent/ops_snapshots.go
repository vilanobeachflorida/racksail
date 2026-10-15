package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Snapshots can also travel through files, such as backups kept on a data
// disk of the same server, without passing through the panel.

// SaveSnapshotInput names a snapshot and the file to save it in.
type SaveSnapshotInput struct {
	Snapshot string `json:"snapshot" doc:"A read-only snapshot"`
	// Parent makes the file incremental: only what changed since it.
	Parent string `json:"parent,omitempty" doc:"An earlier snapshot whose file the restore will have first"`
	Dest   string `json:"dest" doc:"File to write the send stream to"`
}

// Validate implements Input.
func (in SaveSnapshotInput) Validate() error {
	if err := (SnapshotStreamInput{Snapshot: in.Snapshot, Parent: in.Parent}).Validate(); err != nil {
		return err
	}
	return checkWrite(in.Dest)
}

// SnapshotFileResult is what saving a snapshot to a file did.
type SnapshotFileResult struct {
	Result
	Bytes int64 `json:"bytes" doc:"Size of the send stream"`
}

// SaveSnapshot writes the Btrfs send stream of a read-only snapshot to a
// file. A file it saved before is kept, since a snapshot never changes.
var SaveSnapshot = Register(Def[SaveSnapshotInput, SnapshotFileResult]{
	Name:    "SaveSnapshot",
	Summary: "Save a Btrfs snapshot to a file",
	Description: "Writes the send stream of a read-only snapshot, incremental from parent when it's given, to a file that only root can read, " +
		"and remembers what it wrote, so RestoreSnapshot only ever reads streams the agent saved. A file it saved before is kept.",
	Effects: func(in SaveSnapshotInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "write", Target: in.Dest, Detail: in.Snapshot}}
	},
	Run: func(ctx context.Context, env *Env, in SaveSnapshotInput) (SnapshotFileResult, error) {
		if env.Runtime == nil {
			return SnapshotFileResult{}, errNoRuntime
		}
		files, err := beneath(env, in.Dest, false)
		if err != nil {
			return SnapshotFileResult{}, err
		}
		if info, err := files.Stat(ctx, in.Dest); err == nil {
			if _, err := env.Runtime.savedSnapshot(ctx, in.Dest, info); err != nil {
				return SnapshotFileResult{}, ErrConflict.Wrap(err, "op", "SaveSnapshot", "reason", err.Error())
			}
			return SnapshotFileResult{Bytes: info.Size}, nil
		} else if !errors.Is(err, system.ErrNotFound) {
			return SnapshotFileResult{}, err
		}
		snapshots, err := beneath(env, in.Snapshot, true)
		if err != nil {
			return SnapshotFileResult{}, err
		}
		if _, err := snapshots.Stat(ctx, in.Snapshot); err != nil {
			return SnapshotFileResult{}, err
		}
		effect := system.Effect{Type: system.EffectFile, Action: "write", Target: in.Dest, Detail: in.Snapshot}
		if env.Check {
			return SnapshotFileResult{Result: Changed(effect)}, nil
		}
		pr, pw := io.Pipe()
		sent := make(chan error, 1)
		go func() {
			err := env.System.Btrfs.Send(ctx, in.Snapshot, in.Parent, pw)
			pw.CloseWithError(err)
			sent <- err
		}()
		counted := &countingReader{r: pr, h: sha256.New()}
		err = files.WriteFrom(ctx, in.Dest, counted, 0o600, system.Owner{})
		// The writer stops reading on failure; unblock the sender.
		pr.CloseWithError(err)
		if serr := <-sent; err == nil && serr != nil {
			err = serr
		}
		var info system.FileInfo
		if err == nil {
			info, err = files.Stat(ctx, in.Dest)
		}
		if err == nil {
			err = env.Runtime.rememberSnapshot(ctx, in.Dest, savedSnapshot{
				Snapshot: in.Snapshot, SHA256: hex.EncodeToString(counted.h.Sum(nil)), Size: info.Size, ModTime: info.ModTime,
			})
		}
		if err != nil {
			if rerr := files.Remove(ctx, in.Dest); rerr != nil && !errors.Is(rerr, system.ErrNotFound) {
				env.Log.Warn("removing a partial snapshot file", "path", in.Dest, "error", rerr)
			}
			return SnapshotFileResult{}, fmt.Errorf("saving %s: %w", in.Snapshot, err)
		}
		effect.DiskBytes = counted.n
		return SnapshotFileResult{Result: Changed(effect), Bytes: counted.n}, nil
	},
})

// RestoreSnapshotInput names a saved snapshot and where to receive it.
type RestoreSnapshotInput struct {
	File string `json:"file" doc:"A file SaveSnapshot wrote"`
	Dir  string `json:"dir" doc:"Directory on Btrfs to receive the snapshot into"`
}

// Validate implements Input.
func (in RestoreSnapshotInput) Validate() error {
	if err := checkRead(in.File); err != nil {
		return err
	}
	return checkWrite(in.Dir)
}

// RestoreSnapshot receives a saved send stream into a directory, where it
// becomes a read-only snapshot again. Only files SaveSnapshot wrote, still
// as it left them, are received, and btrfs receive is confined to the
// directory.
var RestoreSnapshot = Register(Def[RestoreSnapshotInput, Result]{
	Name:    "RestoreSnapshot",
	Summary: "Restore a Btrfs snapshot from a file",
	Description: "Receives a send stream saved by SaveSnapshot into a directory on Btrfs. The file must be the one the agent saved: " +
		"owned by root, readable by root only and unchanged since. An incremental stream needs its parent snapshot received first.",
	Effects: func(in RestoreSnapshotInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "receive", Target: in.Dir, Detail: in.File}}
	},
	Run: func(ctx context.Context, env *Env, in RestoreSnapshotInput) (Result, error) {
		if env.Runtime == nil {
			return Result{}, errNoRuntime
		}
		files, err := beneath(env, in.File, true)
		if err != nil {
			return Result{}, err
		}
		info, err := files.Stat(ctx, in.File)
		if err != nil {
			return Result{}, err
		}
		if !info.Mode.IsRegular() || info.UID != 0 || info.Perm() != 0o600 {
			return Result{}, fmt.Errorf("%s isn't a file only root can read and write: %w", in.File, system.ErrInvalid)
		}
		saved, err := env.Runtime.savedSnapshot(ctx, in.File, info)
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", err, system.ErrInvalid)
		}
		dirs, err := beneath(env, in.Dir, false)
		if err != nil {
			return Result{}, err
		}
		if dir, err := dirs.Stat(ctx, in.Dir); err != nil {
			return Result{}, err
		} else if !dir.IsDir() {
			return Result{}, fmt.Errorf("%s isn't a directory: %w", in.Dir, system.ErrInvalid)
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "receive", Target: in.Dir, Detail: in.File, DiskBytes: info.Size}
		if env.Check {
			return Changed(effect), nil
		}
		before, err := subvolumesIn(ctx, env, in.Dir)
		if err != nil {
			return Result{}, err
		}
		f, err := files.Open(ctx, in.File)
		if err != nil {
			return Result{}, err
		}
		defer f.Close()
		h := sha256.New()
		r := io.TeeReader(f, h)
		err = env.System.Btrfs.Receive(ctx, in.Dir, r)
		if err == nil {
			_, err = io.Copy(io.Discard, r)
		}
		if err == nil && hex.EncodeToString(h.Sum(nil)) != saved.SHA256 {
			// Whatever arrived came from a file someone changed: it goes.
			err = fmt.Errorf("%s no longer holds the stream SaveSnapshot wrote: %w", in.File, system.ErrInvalid)
			after, lerr := subvolumesIn(ctx, env, in.Dir)
			if lerr != nil {
				return Result{}, errors.Join(err, lerr)
			}
			for p := range after {
				if !before[p] {
					if derr := env.System.Btrfs.DeleteSubvolume(ctx, p); derr != nil {
						err = errors.Join(err, derr)
					}
				}
			}
		}
		if err != nil {
			return Result{}, err
		}
		return Changed(effect), nil
	},
})

// subvolumesIn lists the subvolumes below dir.
func subvolumesIn(ctx context.Context, env *Env, dir string) (map[string]bool, error) {
	svs, err := env.System.Btrfs.Subvolumes(ctx, dir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, sv := range svs {
		if strings.HasPrefix(sv.Path, dir+"/") {
			out[sv.Path] = true
		}
	}
	return out, nil
}

// countingReader counts and hashes what passes through it.
type countingReader struct {
	r io.Reader
	h hash.Hash
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.h != nil {
		c.h.Write(p[:n])
	}
	return n, err
}
