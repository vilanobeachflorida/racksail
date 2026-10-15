package agent

import (
	"context"
	"errors"
	"fmt"
	"io"

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
// file. A file that is already there is kept, since a snapshot never
// changes.
var SaveSnapshot = Register(Def[SaveSnapshotInput, SnapshotFileResult]{
	Name:        "SaveSnapshot",
	Summary:     "Save a Btrfs snapshot to a file",
	Description: "Writes the send stream of a read-only snapshot, incremental from parent when it's given, to a file that only root can read. An existing file is kept.",
	Effects: func(in SaveSnapshotInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "write", Target: in.Dest, Detail: in.Snapshot}}
	},
	Run: func(ctx context.Context, env *Env, in SaveSnapshotInput) (SnapshotFileResult, error) {
		files := env.System.Files
		if info, err := files.Stat(ctx, in.Dest); err == nil {
			return SnapshotFileResult{Bytes: info.Size}, nil
		} else if !errors.Is(err, system.ErrNotFound) {
			return SnapshotFileResult{}, err
		}
		if _, err := files.Stat(ctx, in.Snapshot); err != nil {
			return SnapshotFileResult{}, err
		}
		effect := system.Effect{Type: system.EffectFile, Action: "write", Target: in.Dest, Detail: in.Snapshot}
		if env.Check {
			return SnapshotFileResult{Result: Changed(effect)}, nil
		}
		if err := checkNoSymlinks(ctx, files, in.Dest); err != nil {
			return SnapshotFileResult{}, err
		}
		pr, pw := io.Pipe()
		sent := make(chan error, 1)
		go func() {
			err := env.System.Btrfs.Send(ctx, in.Snapshot, in.Parent, pw)
			pw.CloseWithError(err)
			sent <- err
		}()
		counted := &countingReader{r: pr}
		err := files.WriteFrom(ctx, in.Dest, counted, 0o600, system.Owner{})
		// The writer stops reading on failure; unblock the sender.
		pr.CloseWithError(err)
		if serr := <-sent; err == nil && serr != nil {
			err = serr
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
// becomes a read-only snapshot again.
var RestoreSnapshot = Register(Def[RestoreSnapshotInput, Result]{
	Name:        "RestoreSnapshot",
	Summary:     "Restore a Btrfs snapshot from a file",
	Description: "Receives a send stream saved by SaveSnapshot into a directory on Btrfs. An incremental stream needs its parent snapshot received first.",
	Effects: func(in RestoreSnapshotInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "receive", Target: in.Dir, Detail: in.File}}
	},
	Run: func(ctx context.Context, env *Env, in RestoreSnapshotInput) (Result, error) {
		info, err := env.System.Files.Stat(ctx, in.File)
		if err != nil {
			return Result{}, err
		}
		if !info.Mode.IsRegular() {
			return Result{}, fmt.Errorf("%s isn't a file: %w", in.File, system.ErrInvalid)
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "receive", Target: in.Dir, Detail: in.File, DiskBytes: info.Size}
		if env.Check {
			return Changed(effect), nil
		}
		f, err := env.System.Files.Open(ctx, in.File)
		if err != nil {
			return Result{}, err
		}
		defer f.Close()
		return Changed(effect), env.System.Btrfs.Receive(ctx, in.Dir, f)
	},
})

// countingReader counts what passes through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
