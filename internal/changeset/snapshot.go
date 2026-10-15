package changeset

import (
	"context"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// ErrSnapshotFailed is returned when the snapshot a risky change needs can't
// be taken. Nothing is changed then.
var ErrSnapshotFailed = i18n.Define("changeset.snapshot_failed", http.StatusInternalServerError, "The snapshot taken before {title} failed, so nothing was changed: {reason}")

// snapshotBatch is how many paths one SnapshotPaths call takes.
const snapshotBatch = 256

// snapshotPaths lists the paths the destructive effects of a change set
// remove that a filesystem snapshot could keep: absolute paths, leaving out
// devices, which are wiped or formatted whole.
func snapshotPaths(effects []system.Effect) []string {
	var out []string
	for _, eff := range effects {
		t := eff.Target
		if !eff.Destructive || !path.IsAbs(t) || path.Clean(t) != t || t == "/dev" || strings.HasPrefix(t, "/dev/") || slices.Contains(out, t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (e *Engine) agent() agent.Client {
	if e.opts.Runner == nil {
		return nil
	}
	return e.opts.Runner.Agent
}

// snapshot asks the agent for the snapshots of a change set, in check mode
// for a preview. Paths go in batches the operation accepts; a subvolume
// already snapshotted in an earlier batch is reported once.
func (e *Engine) snapshot(ctx context.Context, cs *ChangeSet, check bool) ([]agent.Snapshot, []system.Effect, error) {
	client := e.agent()
	paths := snapshotPaths(cs.Effects)
	if client == nil || len(paths) == 0 {
		return nil, nil, nil
	}
	var snaps []agent.Snapshot
	var effects []system.Effect
	for start := 0; start < len(paths); start += snapshotBatch {
		batch := paths[start:min(start+snapshotBatch, len(paths))]
		res, err := agent.SnapshotPaths.Call(ctx, client, agent.SnapshotPathsInput{Paths: batch, Label: string(cs.ID)}, agent.CallOptions{Check: check})
		if err != nil {
			return snaps, effects, err
		}
		for _, s := range res.Snapshots {
			if !slices.Contains(snaps, s) {
				snaps = append(snaps, s)
			}
		}
		for _, eff := range res.Effects {
			if !slices.Contains(effects, eff) {
				effects = append(effects, eff)
			}
		}
		if len(res.Unprotected) > 0 {
			e.opts.Log.Info("some of what a change removes isn't on Btrfs, so no snapshot keeps it", "changeSet", cs.ID, "paths", res.Unprotected)
		}
	}
	return snaps, effects, nil
}

// previewSnapshots adds the snapshots applying a change set would take to
// its effects, so the preview shows them. When the agent can't say, the
// preview goes without; applying still takes them.
func (e *Engine) previewSnapshots(ctx context.Context, cs *ChangeSet) {
	_, effects, err := e.snapshot(ctx, cs, true)
	if err != nil {
		e.opts.Log.Warn("listing the snapshots for a preview", "changeSet", cs.ID, "error", err)
		return
	}
	cs.Effects = append(cs.Effects, effects...)
}

// takeSnapshots takes the snapshots of a change set before it is applied.
func (e *Engine) takeSnapshots(ctx context.Context, cs *ChangeSet) error {
	snaps, _, err := e.snapshot(ctx, cs, false)
	if err != nil {
		// Some may have been taken before the failure.
		e.dropSnapshots(ctx, snaps)
		return ErrSnapshotFailed.Wrap(err, "title", cs.Title, "reason", i18n.From(err).Message)
	}
	cs.Snapshots = snaps
	return nil
}

// dropSnapshots deletes snapshots and then the directories that held them,
// once they're empty. Failures are logged; a snapshot left behind only
// costs disk space.
func (e *Engine) dropSnapshots(ctx context.Context, snaps []agent.Snapshot) {
	client := e.agent()
	if client == nil || len(snaps) == 0 {
		return
	}
	var dirs []string
	for _, s := range snaps {
		if _, err := agent.DeleteSubvolume.Call(ctx, client, agent.SubvolumeInput{Path: s.Path}, agent.CallOptions{}); err != nil {
			e.opts.Log.Warn("deleting a snapshot", "path", s.Path, "error", err)
			continue
		}
		if dir := path.Dir(s.Path); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	for _, dir := range dirs {
		if _, err := agent.RemovePath.Call(ctx, client, agent.RemovePathInput{Path: dir}, agent.CallOptions{}); err != nil {
			e.opts.Log.Warn("removing a snapshot directory", "path", dir, "error", err)
		}
	}
}
