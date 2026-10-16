package changeset

import (
	"context"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
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

// SnapshotCollection is how often Run looks for snapshots no change set
// keeps.
const SnapshotCollection = 24 * time.Hour

// Run deletes the pre-change snapshots no kept change set lists, when the
// panel starts and then every interval, until ctx is done. A check that
// fails, as while the agent doesn't answer, is tried again sooner.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		wait := interval
		n, err := e.CollectSnapshots(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			e.opts.Log.Warn("looking for snapshots no change set keeps", "error", err)
			wait = min(interval, 10*time.Minute)
		case n > 0:
			e.opts.Log.Info("deleted the snapshots no change set keeps", "count", n)
		}
		timer.Reset(wait)
	}
}

// CollectSnapshots deletes the snapshots taken before change sets were
// applied that no kept change set lists, and returns how many it found.
// Change sets drop their snapshots when they are no longer kept, but some
// outlive that: those of change sets dropped by a panel that stopped before
// it deleted them, of a change whose writes failed or never committed, as
// when the panel stopped in between, and of a preview whose apply stopped
// that way, which stays a preview. Snapshots being taken for a change set
// that isn't recorded yet are left alone, and so is every snapshot of a
// label that isn't a change set's, such as those of updates.
func (e *Engine) CollectSnapshots(ctx context.Context) (int, error) {
	client := e.agent()
	if client == nil {
		return 0, nil
	}
	found, err := changeSnapshots(ctx, client)
	if err != nil {
		return 0, err
	}
	var orphans []agent.Snapshot
	for id, snaps := range found {
		if e.snapshotting(id) {
			continue
		}
		cs, err := e.storage.Get(ctx, id)
		switch {
		case err == nil && !cs.DryRun:
			// Only the snapshots the change set lists are its own.
			for _, s := range snaps {
				if !slices.ContainsFunc(cs.Snapshots, func(kept agent.Snapshot) bool { return kept.Path == s.Path }) {
					orphans = append(orphans, s)
				}
			}
		case err == nil, i18n.ErrNotFound.Is(err):
			orphans = append(orphans, snaps...)
		default:
			return 0, err
		}
	}
	e.dropSnapshots(ctx, orphans)
	return len(orphans), nil
}

// changeSnapshots lists the snapshots SnapshotPaths took for change sets,
// by the change set whose ID labels them, from every Btrfs filesystem.
func changeSnapshots(ctx context.Context, client agent.Client) (map[store.ID][]agent.Snapshot, error) {
	mounts, err := agent.ListMounts.Call(ctx, client, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		return nil, err
	}
	out := map[store.ID][]agent.Snapshot{}
	listed := map[string]bool{}
	for _, m := range mounts.Mounts {
		// Every mount of a filesystem lists all of its subvolumes.
		if m.FSType != "btrfs" || listed[m.Source] {
			continue
		}
		listed[m.Source] = true
		list, err := agent.ListSubvolumes.Call(ctx, client, agent.MountPointInput{Mount: m.Target}, agent.CallOptions{})
		if err != nil {
			return nil, err
		}
		for _, sv := range list.Subvolumes {
			if id, ok := snapshotChangeSet(sv.Path); ok {
				out[id] = append(out[id], agent.Snapshot{Path: sv.Path})
			}
		}
	}
	return out, nil
}

// snapshotChangeSet returns the change set a snapshot was taken for: the
// label of the directory it is in below .snapshots/racksail on its
// filesystem, or below the system's snapshots, when the label is a change
// set's ID.
func snapshotChangeSet(p string) (store.ID, bool) {
	dir := path.Dir(p)
	root := path.Dir(dir)
	if root != agent.SystemSnapshotDir && root != agent.SnapshotDir && !strings.HasSuffix(root, "/.snapshots/racksail") {
		return "", false
	}
	id, err := store.ParseID(path.Base(dir))
	if err != nil || id.Prefix() != store.PrefixChangeSet {
		return "", false
	}
	return id, true
}

// holdSnapshots keeps the collection away from the snapshots of a change
// set until its record is written, which release ends.
func (e *Engine) holdSnapshots(id store.ID) (release func()) {
	e.mu.Lock()
	e.taking[id]++
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.taking[id]--; e.taking[id] <= 0 {
			delete(e.taking, id)
		}
	}
}

// snapshotting reports whether snapshots are being taken for a change set
// that isn't recorded yet.
func (e *Engine) snapshotting(id store.ID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.taking[id] > 0
}

// dropSnapshots deletes snapshots and then the directories that held them,
// once they're empty. Failures are logged; a snapshot left behind only
// costs disk space, and the collection tries it again.
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
