package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// SubvolumeInput names a Btrfs subvolume: one in a location Racksail
// manages, or for deleting, a snapshot SnapshotPaths took of the system.
type SubvolumeInput struct {
	Path string `json:"path" doc:"Absolute path of the subvolume"`
}

// Validate implements Input.
func (in SubvolumeInput) Validate() error {
	if systemSnapshot(in.Path) {
		return nil
	}
	if err := checkWrite(in.Path); err != nil {
		return err
	}
	if isWriteRoot(in.Path) {
		return fmt.Errorf("%s is a Racksail root directory", in.Path)
	}
	return nil
}

// subvolumeView returns the view of files a subvolume operation works
// through. Snapshots of the system are reached below their own directory,
// which no other operation reaches, and only to be deleted.
func subvolumeView(env *Env, p string, deleting bool) (system.Files, error) {
	if systemSnapshot(p) {
		if !deleting {
			return nil, fmt.Errorf("%s is a snapshot of the system: %w", p, system.ErrInvalid)
		}
		return env.System.Files.Beneath(SystemSnapshotDir), nil
	}
	return beneath(env, p, false)
}

func isSubvolume(ctx context.Context, b system.Btrfs, p string) (bool, error) {
	svs, err := b.Subvolumes(ctx, path.Dir(p))
	if err != nil {
		return false, err
	}
	for _, sv := range svs {
		if sv.Path == p {
			return true, nil
		}
	}
	return false, nil
}

// EnsureSubvolume creates a Btrfs subvolume, such as an account's data.
var EnsureSubvolume = Register(Def[SubvolumeInput, Result]{
	Name:    "EnsureSubvolume",
	Summary: "Create a Btrfs subvolume",
	Effects: func(in SubvolumeInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "create-subvolume", Target: in.Path}}
	},
	Run: func(ctx context.Context, env *Env, in SubvolumeInput) (Result, error) {
		files, err := subvolumeView(env, in.Path, false)
		if err != nil {
			return Result{}, err
		}
		if info, err := files.Stat(ctx, in.Path); err == nil {
			if !info.IsDir() {
				return Result{}, fmt.Errorf("%s exists and isn't a subvolume: %w", in.Path, system.ErrExists)
			}
			ok, err := isSubvolume(ctx, env.System.Btrfs, in.Path)
			if err != nil {
				return Result{}, err
			}
			if !ok {
				return Result{}, fmt.Errorf("%s exists and isn't a subvolume: %w", in.Path, system.ErrExists)
			}
			return Unchanged(), nil
		} else if !errors.Is(err, system.ErrNotFound) {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "create-subvolume", Target: in.Path}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Btrfs.CreateSubvolume(ctx, in.Path)
	},
})

// DeleteSubvolume removes a Btrfs subvolume and everything in it.
var DeleteSubvolume = Register(Def[SubvolumeInput, Result]{
	Name:    "DeleteSubvolume",
	Summary: "Delete a Btrfs subvolume",
	Description: "Deletes a subvolume in a location Racksail manages, or a snapshot of the system that SnapshotPaths took, " +
		"with everything in it.",
	Effects: func(in SubvolumeInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "delete-subvolume", Target: in.Path, Destructive: true}}
	},
	Run: func(ctx context.Context, env *Env, in SubvolumeInput) (Result, error) {
		files, err := subvolumeView(env, in.Path, true)
		if err != nil {
			return Result{}, err
		}
		info, err := files.Stat(ctx, in.Path)
		switch {
		case errors.Is(err, system.ErrNotFound):
			return Unchanged(), nil
		case err != nil:
			return Result{}, err
		case !info.IsDir():
			return Result{}, fmt.Errorf("%s isn't a subvolume: %w", in.Path, system.ErrInvalid)
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "delete-subvolume", Target: in.Path, Destructive: true}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Btrfs.DeleteSubvolume(ctx, in.Path)
	},
})

// SnapshotInput is a snapshot to take.
type SnapshotInput struct {
	Source   string `json:"source" doc:"Subvolume to snapshot"`
	Dest     string `json:"dest" doc:"Where the snapshot goes"`
	ReadOnly bool   `json:"readOnly,omitempty" doc:"Make the snapshot read-only, as backups and send need"`
}

// Validate implements Input.
func (in SnapshotInput) Validate() error {
	if err := checkRead(in.Source); err != nil {
		return err
	}
	return checkWrite(in.Dest)
}

// CreateSnapshot snapshots a subvolume. Taking a snapshot that already exists
// does nothing, so retried steps are safe.
var CreateSnapshot = Register(Def[SnapshotInput, Result]{
	Name:    "CreateSnapshot",
	Summary: "Snapshot a Btrfs subvolume",
	Effects: func(in SnapshotInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "snapshot", Target: in.Dest, Detail: in.Source}}
	},
	Run: func(ctx context.Context, env *Env, in SnapshotInput) (Result, error) {
		source, err := beneath(env, in.Source, true)
		if err != nil {
			return Result{}, err
		}
		if info, err := source.Stat(ctx, in.Source); err != nil {
			return Result{}, err
		} else if !info.IsDir() {
			return Result{}, fmt.Errorf("%s isn't a subvolume: %w", in.Source, system.ErrInvalid)
		}
		dest, err := beneath(env, in.Dest, false)
		if err != nil {
			return Result{}, err
		}
		if _, err := dest.Stat(ctx, in.Dest); err == nil {
			return Unchanged(), nil
		} else if !errors.Is(err, system.ErrNotFound) {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "snapshot", Target: in.Dest, Detail: in.Source}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Btrfs.Snapshot(ctx, in.Source, in.Dest, in.ReadOnly)
	},
})

// SnapshotPathsInput asks for read-only snapshots of the Btrfs subvolumes
// that hold some paths, taken before a change removes data under them.
type SnapshotPathsInput struct {
	Paths []string `json:"paths" doc:"Paths a change is about to remove"`
	Label string   `json:"label" doc:"Names the directory the snapshots go in, such as the ID of the change set they protect"`
}

var snapshotLabelPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Validate implements Input.
func (in SnapshotPathsInput) Validate() error {
	if len(in.Paths) == 0 || len(in.Paths) > 256 {
		return fmt.Errorf("give between 1 and 256 paths")
	}
	for _, p := range in.Paths {
		if err := cleanAbs(p); err != nil {
			return err
		}
	}
	if !snapshotLabelPattern.MatchString(in.Label) {
		return fmt.Errorf("label %q must be lowercase letters, digits and underscores, starting with a letter", in.Label)
	}
	return nil
}

// Snapshot is a read-only snapshot of a subvolume.
type Snapshot struct {
	Source string `json:"source" doc:"The subvolume, such as /srv/racksail"`
	Path   string `json:"path" doc:"Where the snapshot is"`
}

// SnapshotResult is what SnapshotPaths took, or in check mode would take.
type SnapshotResult struct {
	Result
	Snapshots []Snapshot `json:"snapshots" doc:"The snapshots, one per subvolume"`
	// Unprotected paths aren't on a Btrfs filesystem Racksail manages, or
	// are snapshots themselves, so no snapshot covers them.
	Unprotected []string `json:"unprotected,omitempty" doc:"Paths no snapshot covers, because they aren't on Btrfs"`
}

// SnapshotDir is where pre-change snapshots of the system disk's subvolumes
// that Racksail manages go, such as the accounts' area. Other Btrfs
// filesystems keep theirs in .snapshots/racksail at their mount point.
const SnapshotDir = "/.snapshots/racksail"

// SystemSnapshotDir is where snapshots of the system's own subvolumes go,
// such as the root filesystem before an upgrade. They hold everything on
// the system, root's secrets included, so only the agent reaches them: no
// operation reads, sends or copies them, and DeleteSubvolume removes them.
const SystemSnapshotDir = "/.snapshots/racksail-system"

// systemSnapshot reports whether p is a snapshot SnapshotPaths took of the
// system: SystemSnapshotDir/<label>/<name>.
func systemSnapshot(p string) bool {
	rest, ok := strings.CutPrefix(p, SystemSnapshotDir+"/")
	if !ok || cleanAbs(p) != nil {
		return false
	}
	label, name, ok := strings.Cut(rest, "/")
	return ok && snapshotLabelPattern.MatchString(label) && snapshotNamePattern.MatchString(name)
}

var snapshotNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,200}-[0-9a-f]{8}$`)

// SnapshotPaths snapshots the subvolumes that hold the given paths, once each,
// so data a change removes can be brought back. Snapshots of the system disk
// go to /.snapshots/racksail/<label>, or /.snapshots/racksail-system/<label>
// for the system's own subvolumes, others next to their filesystem. Taking
// them again does nothing.
var SnapshotPaths = Register(Def[SnapshotPathsInput, SnapshotResult]{
	Name:    "SnapshotPaths",
	Summary: "Snapshot what a change is about to remove",
	Description: "Takes read-only Btrfs snapshots of the subvolumes holding the given paths, once per subvolume, " +
		"before a risky change removes data under them. Paths that aren't on Btrfs are reported as unprotected. " +
		"Snapshots of the system's own subvolumes, such as the root filesystem, go where only the agent reaches them.",
	Run: func(ctx context.Context, env *Env, in SnapshotPathsInput) (SnapshotResult, error) {
		out := SnapshotResult{Snapshots: []Snapshot{}}
		mounts, err := env.System.Mounts.List(ctx)
		if err != nil {
			return out, err
		}
		var rootDevice string
		for _, m := range mounts {
			if m.Target == "/" {
				rootDevice = m.Source
			}
		}
		subvolumes := map[string][]system.Subvolume{}
		taken := map[string]bool{}
		for _, p := range in.Paths {
			mt, ok := mountOf(mounts, p)
			if !ok || mt.FSType != "btrfs" || strings.Contains(p+"/", "/.snapshots/") {
				out.Unprotected = append(out.Unprotected, p)
				continue
			}
			svs, ok := subvolumes[mt.Target]
			if !ok {
				if svs, err = env.System.Btrfs.Subvolumes(ctx, mt.Target); err != nil {
					return out, err
				}
				subvolumes[mt.Target] = svs
			}
			source := ""
			for _, sv := range svs {
				if (p == sv.Path || strings.HasPrefix(p, strings.TrimSuffix(sv.Path, "/")+"/")) && len(sv.Path) > len(source) {
					source = sv.Path
				}
			}
			// A subvolume Racksail may read in full is snapshotted where
			// Racksail reads; the system's own are kept from the panel.
			systemDisk := mt.Source == rootDevice
			sealed := source != "" && checkRead(source) != nil
			dir := path.Join(SnapshotDir, in.Label)
			switch {
			case !systemDisk && sealed:
				source = ""
			case !systemDisk:
				dir = path.Join(mt.Target, ".snapshots", "racksail", in.Label)
			case sealed:
				dir = path.Join(SystemSnapshotDir, in.Label)
			}
			if source == "" || (!sealed && checkWrite(dir) != nil) {
				out.Unprotected = append(out.Unprotected, p)
				continue
			}
			if taken[source] {
				continue
			}
			taken[source] = true
			dest := path.Join(dir, snapshotName(source))
			out.Snapshots = append(out.Snapshots, Snapshot{Source: source, Path: dest})
			files, err := snapshotView(env, dest, systemDisk)
			if err != nil {
				return out, err
			}
			if _, err := files.Stat(ctx, dest); err == nil {
				continue
			} else if !errors.Is(err, system.ErrNotFound) {
				return out, err
			}
			out.Changed = true
			out.Effects = append(out.Effects, system.Effect{Type: system.EffectStorage, Action: "snapshot", Target: dest, Detail: source})
			if env.Check {
				continue
			}
			if err := makeSnapshotDir(ctx, files, dir, systemDisk); err != nil {
				return out, err
			}
			if err := env.System.Btrfs.Snapshot(ctx, source, dest, true); err != nil {
				return out, err
			}
		}
		return out, nil
	},
})

// snapshotView returns the view of files below which a snapshot goes. On
// the system disk the agent picks every directory on the way, so they are
// reached from / without following links.
func snapshotView(env *Env, dest string, systemDisk bool) (system.Files, error) {
	if systemDisk {
		return env.System.Files.Beneath("/"), nil
	}
	return beneath(env, dest, false)
}

// mountOf returns the mount a path is on: the deepest mount point above it.
func mountOf(mounts []system.Mount, p string) (system.Mount, bool) {
	var best system.Mount
	found := false
	for _, m := range mounts {
		if (m.Target == "/" || p == m.Target || strings.HasPrefix(p, m.Target+"/")) && (!found || len(m.Target) > len(best.Target)) {
			best, found = m, true
		}
	}
	return best, found
}

// snapshotName names the snapshot of a subvolume after its path, such as
// srv-racksail-abdff825 for /srv/racksail and root-8a5edab2 for /. The hash
// of the path keeps names apart that would otherwise read the same, such as
// those of /srv/a-b and /srv/a/b, however many calls the snapshots of one
// label take.
func snapshotName(source string) string {
	name := strings.ReplaceAll(strings.Trim(source, "/"), "/", "-")
	if name == "" {
		name = "root"
	}
	sum := sha256.Sum256([]byte(source))
	return name + "-" + hex.EncodeToString(sum[:4])
}

// makeSnapshotDir creates the directory snapshots go in, readable by root
// only: a snapshot holds a copy of everything in its subvolume. f is a view
// below the directory's managed root, or below / on the system disk, where
// the agent creates whatever is missing on the way.
func makeSnapshotDir(ctx context.Context, f system.Files, dir string, systemDisk bool) error {
	var missing []string
	for d := dir; d != "/" && (systemDisk || checkWrite(d) == nil); d = path.Dir(d) {
		if _, err := f.Stat(ctx, d); err == nil {
			break
		} else if !errors.Is(err, system.ErrNotFound) {
			return err
		}
		missing = append([]string{d}, missing...)
	}
	for _, d := range missing {
		if err := f.Mkdir(ctx, d, 0o700, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
			return err
		}
	}
	return nil
}

// QuotaInput sets a subvolume quota.
type QuotaInput struct {
	Path       string `json:"path" doc:"Subvolume"`
	LimitBytes int64  `json:"limitBytes" doc:"Space limit in bytes; zero removes the limit"`
}

// Validate implements Input.
func (in QuotaInput) Validate() error {
	if in.LimitBytes < 0 {
		return fmt.Errorf("limit must not be negative")
	}
	return checkWrite(in.Path)
}

// SetSubvolumeQuota limits the space a subvolume may use.
var SetSubvolumeQuota = Register(Def[QuotaInput, Result]{
	Name:    "SetSubvolumeQuota",
	Summary: "Limit the space of a subvolume",
	Effects: func(in QuotaInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "quota", Target: in.Path, DiskBytes: in.LimitBytes}}
	},
	Run: func(ctx context.Context, env *Env, in QuotaInput) (Result, error) {
		if err := checkNoSymlinks(ctx, env, in.Path); err != nil {
			return Result{}, err
		}
		q, err := env.System.Btrfs.Quota(ctx, in.Path)
		if err == nil && q.LimitBytes == in.LimitBytes {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "quota", Target: in.Path, DiskBytes: in.LimitBytes}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Btrfs.SetQuota(ctx, in.Path, in.LimitBytes)
	},
})

// MountPointInput names a mounted filesystem.
type MountPointInput struct {
	Mount string `json:"mount" doc:"Where the filesystem is mounted"`
}

// Validate implements Input.
func (in MountPointInput) Validate() error { return cleanAbs(in.Mount) }

// SubvolumeList lists subvolumes.
type SubvolumeList struct {
	Subvolumes []system.Subvolume `json:"subvolumes"`
}

// ListSubvolumes lists the subvolumes of a Btrfs filesystem.
var ListSubvolumes = Register(Def[MountPointInput, SubvolumeList]{
	Name:     "ListSubvolumes",
	Summary:  "List Btrfs subvolumes",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in MountPointInput) (SubvolumeList, error) {
		svs, err := env.System.Btrfs.Subvolumes(ctx, in.Mount)
		if svs == nil {
			svs = []system.Subvolume{}
		}
		return SubvolumeList{Subvolumes: svs}, err
	},
})

// FilesystemUsage reports the space use of a Btrfs filesystem.
var FilesystemUsage = Register(Def[MountPointInput, system.FilesystemUsage]{
	Name:     "FilesystemUsage",
	Summary:  "Report the space use of a filesystem",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in MountPointInput) (system.FilesystemUsage, error) {
		return env.System.Btrfs.Usage(ctx, in.Mount)
	},
})

// BtrfsDeviceInput adds or removes a device of a filesystem.
type BtrfsDeviceInput struct {
	Mount  string `json:"mount" doc:"Where the filesystem is mounted"`
	Device string `json:"device" doc:"Block device, such as /dev/sdb"`
}

var devicePattern = regexp.MustCompile(`^/dev/(disk/by-id/)?[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

func checkDevicePath(dev string) error {
	if !devicePattern.MatchString(dev) || strings.Contains(dev, "..") {
		return fmt.Errorf("device %q", dev)
	}
	return nil
}

// Validate implements Input.
func (in BtrfsDeviceInput) Validate() error {
	if err := checkWrite(in.Mount); err != nil {
		return err
	}
	return checkDevicePath(in.Device)
}

// AddBtrfsDevice grows a pool with another device.
var AddBtrfsDevice = Register(Def[BtrfsDeviceInput, Result]{
	Name:    "AddBtrfsDevice",
	Summary: "Add a device to a Btrfs pool",
	Effects: func(in BtrfsDeviceInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "add-device", Target: in.Mount, Detail: in.Device, Destructive: true}}
	},
	Run: func(ctx context.Context, env *Env, in BtrfsDeviceInput) (Result, error) {
		usage, err := env.System.Btrfs.Usage(ctx, in.Mount)
		if err != nil {
			return Result{}, err
		}
		if slices.Contains(usage.Devices, kernelDevice(ctx, env.System, in.Device)) {
			return Unchanged(), nil
		}
		if err := deviceUnused(ctx, env.System, in.Device); err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "add-device", Target: in.Mount, Detail: in.Device, Destructive: true}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Btrfs.AddDevice(ctx, in.Mount, in.Device)
	},
})

// RemoveBtrfsDevice moves data off a device and removes it from a pool.
var RemoveBtrfsDevice = Register(Def[BtrfsDeviceInput, Result]{
	Name:    "RemoveBtrfsDevice",
	Summary: "Remove a device from a Btrfs pool",
	Effects: func(in BtrfsDeviceInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "remove-device", Target: in.Mount, Detail: in.Device}}
	},
	Run: func(ctx context.Context, env *Env, in BtrfsDeviceInput) (Result, error) {
		usage, err := env.System.Btrfs.Usage(ctx, in.Mount)
		if err != nil {
			return Result{}, err
		}
		if !slices.Contains(usage.Devices, kernelDevice(ctx, env.System, in.Device)) {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "remove-device", Target: in.Mount, Detail: in.Device}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Btrfs.RemoveDevice(ctx, in.Mount, in.Device)
	},
})

// BalanceInput converts a pool's profiles.
type BalanceInput struct {
	Mount    string               `json:"mount" doc:"Where the filesystem is mounted"`
	Profiles system.BtrfsProfiles `json:"profiles" doc:"Target data and metadata profiles"`
}

// Validate implements Input.
func (in BalanceInput) Validate() error {
	if err := checkWrite(in.Mount); err != nil {
		return err
	}
	for _, p := range []string{in.Profiles.Data, in.Profiles.Metadata} {
		switch p {
		case "", "single", "dup", "raid1", "raid1c3", "raid1c4", "raid10":
		default:
			return fmt.Errorf("btrfs profile %q", p)
		}
	}
	return nil
}

// BalanceBtrfs converts a pool to other profiles, for example from single to
// raid1 after adding a disk.
var BalanceBtrfs = Register(Def[BalanceInput, Result]{
	Name:    "BalanceBtrfs",
	Summary: "Convert a Btrfs pool's profiles",
	Effects: func(in BalanceInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "balance", Target: in.Mount, Detail: in.Profiles.Data + "/" + in.Profiles.Metadata}}
	},
	Run: func(ctx context.Context, env *Env, in BalanceInput) (Result, error) {
		usage, err := env.System.Btrfs.Usage(ctx, in.Mount)
		if err != nil {
			return Result{}, err
		}
		if (in.Profiles.Data == "" || in.Profiles.Data == usage.Data) && (in.Profiles.Metadata == "" || in.Profiles.Metadata == usage.Metadata) {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "balance", Target: in.Mount, Detail: in.Profiles.Data + "/" + in.Profiles.Metadata}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Btrfs.Balance(ctx, in.Mount, in.Profiles)
	},
})

// StartScrub starts checking a pool's data against its checksums.
var StartScrub = Register(Def[MountPointInput, Result]{
	Name:    "StartScrub",
	Summary: "Start a Btrfs scrub",
	Effects: func(in MountPointInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "scrub", Target: in.Mount}}
	},
	Run: func(ctx context.Context, env *Env, in MountPointInput) (Result, error) {
		st, err := env.System.Btrfs.ScrubStatus(ctx, in.Mount)
		if err == nil && st.Running {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "scrub", Target: in.Mount}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Btrfs.ScrubStart(ctx, in.Mount)
	},
})

// ScrubStatus reports the progress of a scrub.
var ScrubStatus = Register(Def[MountPointInput, system.ScrubStatus]{
	Name:     "ScrubStatus",
	Summary:  "Report scrub progress",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in MountPointInput) (system.ScrubStatus, error) {
		return env.System.Btrfs.ScrubStatus(ctx, in.Mount)
	},
})

// MountInput is a filesystem to mount.
type MountInput struct {
	Mount system.Mount `json:"mount"`
}

// Validate implements Input.
func (in MountInput) Validate() error {
	if err := checkMountPoint(in.Mount.Target); err != nil {
		return err
	}
	_, err := system.CheckMount(in.Mount)
	return err
}

// checkMountPoint validates where a filesystem is mounted: a directory right
// below the data disks' location, or the accounts' area itself, never the
// platform's state. Mount points are kept where no operation can put a
// symbolic link, since mount and systemd follow one at a mount point:
// nothing but the agent writes in /, /mnt or /srv, and in the data disks'
// location only directories are made.
func checkMountPoint(p string) error {
	if err := cleanAbs(p); err != nil {
		return err
	}
	if p == accountsArea || (path.Dir(p) == system.DataDisksDir && mountNamePattern.MatchString(path.Base(p))) {
		return nil
	}
	return fmt.Errorf("filesystems are mounted at %s/<name> or at %s, not at %s", system.DataDisksDir, accountsArea, p)
}

var mountNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// mountPointParent reports whether p is the directory mount points are made
// in, where only directories may be made.
func mountPointParent(p string) bool { return p == system.DataDisksDir }

// checkDataDevice checks the device a mount names against the machine.
// Mount points are also where file operations write, so whatever a mount
// brings there becomes theirs to change: only a data disk may come, never
// the system disk, nor a filesystem mounted anywhere else, where it would be
// someone else's. The device must be a disk or partition of the inventory,
// off the system disk, with nothing such as RAID or LVM built on it, and
// hold the filesystem the mount names. It returns the kernel paths of the
// devices holding the filesystem, as the mount table shows them. In check
// mode an earlier step may still create the partition or the filesystem,
// so only what is already there is checked.
func checkDataDevice(ctx context.Context, sys *system.Backend, m system.Mount, check bool) ([]string, error) {
	disks, err := sys.Disks.List(ctx)
	if err != nil {
		return nil, err
	}
	dev, ok := system.FindDevice(disks, m.Source)
	if !ok {
		if check {
			return nil, checkPlannedDevice(disks, m.Source)
		}
		return nil, fmt.Errorf("%s isn't a disk or partition of this server: %w", m.Source, system.ErrNotFound)
	}
	switch {
	case dev.Disk.System:
		return nil, fmt.Errorf("%s is on %s, the disk the system runs from: %w", m.Source, dev.Disk.Path, system.ErrInvalid)
	case dev.Partition == nil && len(dev.Disk.Partitions) > 0:
		return nil, fmt.Errorf("%s has partitions; mount one of them: %w", m.Source, system.ErrInvalid)
	case len(dev.Holders()) > 0:
		return nil, fmt.Errorf("%s is in use by %s: %w", m.Source, strings.Join(dev.Holders(), ", "), system.ErrExists)
	case dev.FSType() != m.FSType && !(check && dev.FSType() == ""):
		fstype := dev.FSType()
		if fstype == "" {
			fstype = "no filesystem"
		}
		return nil, fmt.Errorf("%s holds %s, not %s: %w", m.Source, fstype, m.FSType, system.ErrInvalid)
	}
	mounts, err := sys.Mounts.List(ctx)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, d := range system.FilesystemDevices(disks, dev) {
		if d.Disk.System {
			return nil, fmt.Errorf("%s shares its filesystem with %s, on the disk the system runs from: %w", m.Source, d.Path(), system.ErrInvalid)
		}
		if len(d.Holders()) > 0 {
			return nil, fmt.Errorf("%s shares its filesystem with %s, which is in use by %s: %w", m.Source, d.Path(), strings.Join(d.Holders(), ", "), system.ErrExists)
		}
		for _, mp := range d.MountPoints() {
			if mp != m.Target {
				return nil, fmt.Errorf("%s is already mounted at %s: %w", m.Source, mp, system.ErrExists)
			}
		}
		for _, mt := range mounts {
			if mt.Source == d.Path() && mt.Target != m.Target {
				return nil, fmt.Errorf("%s is already mounted at %s: %w", m.Source, mt.Target, system.ErrExists)
			}
		}
		paths = append(paths, d.Path())
	}
	return paths, nil
}

// checkPlannedDevice checks, in check mode, a device that isn't there yet,
// such as the first partition of a disk an earlier step partitions: as far
// as its name tells, it must not be on the system disk.
func checkPlannedDevice(disks []system.Disk, source string) error {
	for _, d := range disks {
		if d.ByID != "" && strings.HasPrefix(source, d.ByID+"-part") && d.System {
			return fmt.Errorf("%s is on %s, the disk the system runs from: %w", source, d.Path, system.ErrInvalid)
		}
	}
	return nil
}

// EnsureMounted mounts a filesystem at runtime. Mounts that survive reboots
// are .mount units installed with EnsureUnit.
var EnsureMounted = Register(Def[MountInput, Result]{
	Name:    "EnsureMounted",
	Summary: "Mount a filesystem",
	Description: "Mounts a data disk at " + system.DataDisksDir + "/<name> or over the accounts' area, always with nosuid and nodev, " +
		"so nothing on it runs with privileges it brought along. The disk is named by its link under /dev/disk; the system disk, " +
		"a disk with RAID or LVM on it and a filesystem mounted anywhere else are refused, as is a mount point that is a symbolic link.",
	Effects: func(in MountInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "mount", Target: in.Mount.Target, Detail: in.Mount.Source}}
	},
	Run: func(ctx context.Context, env *Env, in MountInput) (Result, error) {
		mount, err := system.CheckMount(in.Mount)
		if err != nil {
			return Result{}, err
		}
		if err := checkNoSymlinks(ctx, env, mount.Target); err != nil {
			return Result{}, err
		}
		devices, err := checkDataDevice(ctx, env.System, mount, env.Check)
		if err != nil {
			return Result{}, err
		}
		mounts, err := env.System.Mounts.List(ctx)
		if err != nil {
			return Result{}, err
		}
		for _, m := range mounts {
			if m.Target == mount.Target {
				if slices.Contains(devices, m.Source) && m.FSType == mount.FSType {
					return Unchanged(), nil
				}
				return Result{}, fmt.Errorf("%s is already mounted from %s: %w", m.Target, m.Source, system.ErrExists)
			}
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "mount", Target: mount.Target, Detail: mount.Source}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Mounts.Mount(ctx, mount)
	},
})

// UnmountInput names a mount point.
type UnmountInput struct {
	Target string `json:"target" doc:"Mount point"`
}

// Validate implements Input.
func (in UnmountInput) Validate() error { return checkMountPoint(in.Target) }

// Unmount unmounts a data disk Racksail mounted.
var Unmount = Register(Def[UnmountInput, Result]{
	Name:        "Unmount",
	Summary:     "Unmount a filesystem",
	Description: "Unmounts a data disk mounted at " + system.DataDisksDir + "/<name> or over the accounts' area. Filesystems of the system disk stay where they are.",
	Effects: func(in UnmountInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "unmount", Target: in.Target}}
	},
	Run: func(ctx context.Context, env *Env, in UnmountInput) (Result, error) {
		mounts, err := env.System.Mounts.List(ctx)
		if err != nil {
			return Result{}, err
		}
		i := slices.IndexFunc(mounts, func(m system.Mount) bool { return m.Target == in.Target })
		if i < 0 {
			return Unchanged(), nil
		}
		disks, err := env.System.Disks.List(ctx)
		if err != nil {
			return Result{}, err
		}
		if dev, ok := system.FindDevice(disks, mounts[i].Source); ok && dev.Disk.System {
			return Result{}, fmt.Errorf("%s holds the system disk's %s: %w", in.Target, mounts[i].Source, system.ErrInvalid)
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "unmount", Target: in.Target}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Mounts.Unmount(ctx, in.Target)
	},
})

// MountList lists mounted filesystems.
type MountList struct {
	Mounts []system.Mount `json:"mounts"`
}

// ListMounts lists mounted filesystems.
var ListMounts = Register(Def[Empty, MountList]{
	Name:     "ListMounts",
	Summary:  "List mounted filesystems",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, _ Empty) (MountList, error) {
		mounts, err := env.System.Mounts.List(ctx)
		if mounts == nil {
			mounts = []system.Mount{}
		}
		return MountList{Mounts: mounts}, err
	},
})

// DiskList lists disks.
type DiskList struct {
	Disks []system.Disk `json:"disks"`
}

// ListDisks lists block devices with their partitions.
var ListDisks = Register(Def[Empty, DiskList]{
	Name:     "ListDisks",
	Summary:  "List disks",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, _ Empty) (DiskList, error) {
		disks, err := env.System.Disks.List(ctx)
		if disks == nil {
			disks = []system.Disk{}
		}
		return DiskList{Disks: disks}, err
	},
})

// DiskInput names a disk.
type DiskInput struct {
	Name string `json:"name" doc:"Kernel name, such as sda or nvme0n1"`
}

var diskNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)

// Validate implements Input.
func (in DiskInput) Validate() error {
	if !diskNamePattern.MatchString(in.Name) {
		return fmt.Errorf("disk name %q", in.Name)
	}
	return nil
}

// DiskHealth reads a disk's SMART report.
var DiskHealth = Register(Def[DiskInput, system.SMARTReport]{
	Name:     "DiskHealth",
	Summary:  "Read a disk's health",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in DiskInput) (system.SMARTReport, error) {
		return env.System.Disks.SMART(ctx, in.Name)
	},
})

// SelfTestInput starts a SMART self-test.
type SelfTestInput struct {
	Name string `json:"name" doc:"Kernel name of the disk"`
	Long bool   `json:"long,omitempty" doc:"Run the long test, which takes hours"`
}

// Validate implements Input.
func (in SelfTestInput) Validate() error { return DiskInput{Name: in.Name}.Validate() }

// StartDiskSelfTest starts a SMART self-test, as burn-in and health checks do.
var StartDiskSelfTest = Register(Def[SelfTestInput, Result]{
	Name:    "StartDiskSelfTest",
	Summary: "Start a disk self-test",
	Effects: func(in SelfTestInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "self-test", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in SelfTestInput) (Result, error) {
		effect := system.Effect{Type: system.EffectStorage, Action: "self-test", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Disks.SelfTest(ctx, in.Name, in.Long)
	},
})

// kernelDevice returns the kernel's path of a device named by a link under
// /dev/disk, as Btrfs lists its devices, or dev itself when the inventory
// doesn't know it.
func kernelDevice(ctx context.Context, sys *system.Backend, dev string) string {
	disks, err := sys.Disks.List(ctx)
	if err != nil {
		return dev
	}
	if d, ok := system.FindDevice(disks, dev); ok {
		return d.Path()
	}
	return dev
}

// deviceUnused refuses devices that hold the system, are mounted, carry a
// RAID array, LVM or an encrypted volume, or belong to a mounted filesystem
// (members of a multi-device Btrfs pool share its UUID), so destructive disk
// operations can only hit spare disks. A whole disk is in use when any of
// its partitions is.
func deviceUnused(ctx context.Context, sys *system.Backend, device string) error {
	disks, err := sys.Disks.List(ctx)
	if err != nil {
		return err
	}
	found, ok := system.FindDevice(disks, device)
	if !ok {
		return fmt.Errorf("device %s: %w", device, system.ErrNotFound)
	}
	if found.Disk.System {
		return fmt.Errorf("%s holds the running system: %w", found.Disk.Path, system.ErrExists)
	}
	mountedUUIDs := map[string]bool{}
	for _, d := range disks {
		all := []system.BlockDevice{{Disk: d}}
		for i := range d.Partitions {
			all = append(all, system.BlockDevice{Disk: d, Partition: &d.Partitions[i]})
		}
		for _, x := range all {
			if len(x.MountPoints()) > 0 && x.UUID() != "" {
				mountedUUIDs[strings.ToLower(x.UUID())] = true
			}
		}
	}
	own := []system.BlockDevice{found}
	if found.Partition == nil {
		for i := range found.Disk.Partitions {
			own = append(own, system.BlockDevice{Disk: found.Disk, Partition: &found.Disk.Partitions[i]})
		}
	}
	for _, x := range own {
		switch {
		case len(x.MountPoints()) > 0:
			return fmt.Errorf("%s is mounted at %s: %w", x.Path(), strings.Join(x.MountPoints(), ", "), system.ErrExists)
		case len(x.Holders()) > 0:
			return fmt.Errorf("%s is in use by %s: %w", x.Path(), strings.Join(x.Holders(), ", "), system.ErrExists)
		case x.UUID() != "" && mountedUUIDs[strings.ToLower(x.UUID())]:
			return fmt.Errorf("%s belongs to a mounted filesystem: %w", x.Path(), system.ErrExists)
		}
	}
	return nil
}

// DeviceInput names a block device.
type DeviceInput struct {
	Device string `json:"device" doc:"Block device, such as /dev/sdb"`
}

// Validate implements Input.
func (in DeviceInput) Validate() error { return checkDevicePath(in.Device) }

// WipeDevice removes every filesystem and partition signature from a spare
// device.
var WipeDevice = Register(Def[DeviceInput, Result]{
	Name:    "WipeDevice",
	Summary: "Wipe a disk",
	Effects: func(in DeviceInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "wipe", Target: in.Device, Destructive: true}}
	},
	Run: func(ctx context.Context, env *Env, in DeviceInput) (Result, error) {
		if err := deviceUnused(ctx, env.System, in.Device); err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "wipe", Target: in.Device, Destructive: true}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Disks.Wipe(ctx, in.Device)
	},
})

// PartitionInput is a partition table to write.
type PartitionInput struct {
	Disk       string                 `json:"disk" doc:"Kernel name of the disk"`
	Partitions []system.PartitionSpec `json:"partitions"`
}

// Validate implements Input.
func (in PartitionInput) Validate() error {
	if err := (DiskInput{Name: in.Disk}).Validate(); err != nil {
		return err
	}
	if len(in.Partitions) == 0 || len(in.Partitions) > 16 {
		return fmt.Errorf("give between 1 and 16 partitions")
	}
	return nil
}

// PartitionDisk writes a new GPT partition table to a spare disk.
var PartitionDisk = Register(Def[PartitionInput, Result]{
	Name:    "PartitionDisk",
	Summary: "Partition a disk",
	Effects: func(in PartitionInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "partition", Target: in.Disk, Destructive: true}}
	},
	Run: func(ctx context.Context, env *Env, in PartitionInput) (Result, error) {
		if err := deviceUnused(ctx, env.System, "/dev/"+in.Disk); err != nil {
			return Result{}, err
		}
		// Debian 13 installs gdisk, which provides sgdisk, only on demand.
		pkg, err := ensurePackage(ctx, env, "gdisk")
		if err != nil {
			return Result{}, err
		}
		effects := append(pkg, system.Effect{Type: system.EffectStorage, Action: "partition", Target: in.Disk, Destructive: true})
		if env.Check {
			return Changed(effects...), nil
		}
		return Changed(effects...), env.System.Disks.Partition(ctx, in.Disk, in.Partitions)
	},
})

// formatPackages are the packages that provide the mkfs tools Debian 13
// doesn't install by default; btrfs-progs and e2fsprogs are present, and
// mkswap comes with util-linux.
var formatPackages = map[string]string{"xfs": "xfsprogs", "vfat": "dosfstools"}

// FormatInput is a filesystem to create.
type FormatInput struct {
	Spec system.FormatSpec `json:"spec"`
}

// Validate implements Input.
func (in FormatInput) Validate() error {
	if len(in.Spec.Devices) == 0 {
		return fmt.Errorf("no devices given")
	}
	for _, d := range in.Spec.Devices {
		if err := checkDevicePath(d); err != nil {
			return err
		}
	}
	switch in.Spec.Type {
	case "btrfs", "ext4", "xfs", "vfat", "swap":
	default:
		return fmt.Errorf("filesystem type %q", in.Spec.Type)
	}
	return nil
}

// FormatDevice creates a filesystem on spare devices.
var FormatDevice = Register(Def[FormatInput, Result]{
	Name:    "FormatDevice",
	Summary: "Create a filesystem",
	Effects: func(in FormatInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "format", Target: strings.Join(in.Spec.Devices, ","), Detail: in.Spec.Type, Destructive: true}}
	},
	Run: func(ctx context.Context, env *Env, in FormatInput) (Result, error) {
		for _, d := range in.Spec.Devices {
			if err := deviceUnused(ctx, env.System, d); err != nil {
				return Result{}, err
			}
		}
		var effects []system.Effect
		if pkgName, ok := formatPackages[in.Spec.Type]; ok {
			pkg, err := ensurePackage(ctx, env, pkgName)
			if err != nil {
				return Result{}, err
			}
			effects = append(effects, pkg...)
		}
		effects = append(effects, system.Effect{Type: system.EffectStorage, Action: "format", Target: strings.Join(in.Spec.Devices, ","), Detail: in.Spec.Type, Destructive: true})
		if env.Check {
			return Changed(effects...), nil
		}
		return Changed(effects...), env.System.Disks.Format(ctx, in.Spec)
	},
})
