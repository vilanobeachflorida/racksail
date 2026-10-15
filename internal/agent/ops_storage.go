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

// SubvolumeInput names a Btrfs subvolume.
type SubvolumeInput struct {
	Path string `json:"path" doc:"Absolute path of the subvolume"`
}

// Validate implements Input.
func (in SubvolumeInput) Validate() error {
	if err := checkWrite(in.Path); err != nil {
		return err
	}
	if isWriteRoot(in.Path) {
		return fmt.Errorf("%s is a Racksail root directory", in.Path)
	}
	return nil
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
		if err := checkNoSymlinks(ctx, env.System.Files, in.Path); err != nil {
			return Result{}, err
		}
		if _, err := env.System.Files.Stat(ctx, in.Path); err == nil {
			ok, err := isSubvolume(ctx, env.System.Btrfs, in.Path)
			if err != nil {
				return Result{}, err
			}
			if !ok {
				return Result{}, fmt.Errorf("%s exists and isn't a subvolume: %w", in.Path, system.ErrExists)
			}
			return Unchanged(), nil
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
	Effects: func(in SubvolumeInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "delete-subvolume", Target: in.Path, Destructive: true}}
	},
	Run: func(ctx context.Context, env *Env, in SubvolumeInput) (Result, error) {
		if _, err := env.System.Files.Stat(ctx, in.Path); errors.Is(err, system.ErrNotFound) {
			return Unchanged(), nil
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
	if err := cleanAbs(in.Source); err != nil {
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
		if _, err := env.System.Files.Stat(ctx, in.Dest); err == nil {
			return Unchanged(), nil
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

// SnapshotDir is where pre-change snapshots of the system disk go. Other
// Btrfs filesystems keep theirs in .snapshots/racksail at their mount point.
const SnapshotDir = "/.snapshots/racksail"

// SnapshotPaths snapshots the subvolumes that hold the given paths, once each,
// so data a change removes can be brought back. Snapshots of the system disk
// go to /.snapshots/racksail/<label>, others next to their filesystem. Taking
// them again does nothing.
var SnapshotPaths = Register(Def[SnapshotPathsInput, SnapshotResult]{
	Name:    "SnapshotPaths",
	Summary: "Snapshot what a change is about to remove",
	Description: "Takes read-only Btrfs snapshots of the subvolumes holding the given paths, once per subvolume, " +
		"before a risky change removes data under them. Paths that aren't on Btrfs are reported as unprotected.",
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
			dir := path.Join(SnapshotDir, in.Label)
			if mt.Source != rootDevice {
				dir = path.Join(mt.Target, ".snapshots", "racksail", in.Label)
			}
			if source == "" || checkWrite(dir) != nil {
				out.Unprotected = append(out.Unprotected, p)
				continue
			}
			if taken[source] {
				continue
			}
			taken[source] = true
			dest := path.Join(dir, snapshotName(source))
			out.Snapshots = append(out.Snapshots, Snapshot{Source: source, Path: dest})
			if _, err := env.System.Files.Stat(ctx, dest); err == nil {
				continue
			}
			out.Changed = true
			out.Effects = append(out.Effects, system.Effect{Type: system.EffectStorage, Action: "snapshot", Target: dest, Detail: source})
			if env.Check {
				continue
			}
			if err := makeSnapshotDir(ctx, env.System.Files, dir); err != nil {
				return out, err
			}
			if err := env.System.Btrfs.Snapshot(ctx, source, dest, true); err != nil {
				return out, err
			}
		}
		return out, nil
	},
})

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
// only: a snapshot of the system disk holds a copy of everything on it.
func makeSnapshotDir(ctx context.Context, f system.Files, dir string) error {
	if err := checkNoSymlinks(ctx, f, dir); err != nil {
		return err
	}
	var missing []string
	for d := dir; checkWrite(d) == nil; d = path.Dir(d) {
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
		if slices.Contains(usage.Devices, in.Device) {
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
		if !slices.Contains(usage.Devices, in.Device) {
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
	if err := checkWrite(in.Mount.Target); err != nil {
		return err
	}
	if in.Mount.Source == "" || in.Mount.FSType == "" || strings.HasPrefix(in.Mount.Source, "-") {
		return fmt.Errorf("a mount needs a source and a filesystem type")
	}
	return nil
}

// EnsureMounted mounts a filesystem at runtime. Mounts that survive reboots
// are .mount units installed with EnsureUnit.
var EnsureMounted = Register(Def[MountInput, Result]{
	Name:    "EnsureMounted",
	Summary: "Mount a filesystem",
	Effects: func(in MountInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "mount", Target: in.Mount.Target, Detail: in.Mount.Source}}
	},
	Run: func(ctx context.Context, env *Env, in MountInput) (Result, error) {
		mounts, err := env.System.Mounts.List(ctx)
		if err != nil {
			return Result{}, err
		}
		for _, m := range mounts {
			if m.Target == in.Mount.Target {
				if m.Source == in.Mount.Source && m.FSType == in.Mount.FSType {
					return Unchanged(), nil
				}
				return Result{}, fmt.Errorf("%s is already mounted from %s: %w", m.Target, m.Source, system.ErrExists)
			}
		}
		effect := system.Effect{Type: system.EffectStorage, Action: "mount", Target: in.Mount.Target, Detail: in.Mount.Source}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Mounts.Mount(ctx, in.Mount)
	},
})

// UnmountInput names a mount point.
type UnmountInput struct {
	Target string `json:"target" doc:"Mount point"`
}

// Validate implements Input.
func (in UnmountInput) Validate() error { return checkWrite(in.Target) }

// Unmount unmounts a filesystem Racksail mounted.
var Unmount = Register(Def[UnmountInput, Result]{
	Name:    "Unmount",
	Summary: "Unmount a filesystem",
	Effects: func(in UnmountInput) []system.Effect {
		return []system.Effect{{Type: system.EffectStorage, Action: "unmount", Target: in.Target}}
	},
	Run: func(ctx context.Context, env *Env, in UnmountInput) (Result, error) {
		mounts, err := env.System.Mounts.List(ctx)
		if err != nil {
			return Result{}, err
		}
		if !slices.ContainsFunc(mounts, func(m system.Mount) bool { return m.Target == in.Target }) {
			return Unchanged(), nil
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

// deviceUnused refuses devices that hold the system, are mounted or belong
// to a mounted filesystem (members of a multi-device Btrfs pool share its
// UUID), so destructive disk operations can only hit spare disks.
func deviceUnused(ctx context.Context, sys *system.Backend, device string) error {
	disks, err := sys.Disks.List(ctx)
	if err != nil {
		return err
	}
	type dev struct {
		path, uuid string
		mounts     []string
		system     bool
	}
	var all []dev
	for _, d := range disks {
		all = append(all, dev{d.Path, d.UUID, d.MountPoints, d.System})
		for _, p := range d.Partitions {
			all = append(all, dev{p.Path, p.UUID, p.MountPoints, d.System})
		}
	}
	mountedUUIDs := map[string]bool{}
	for _, d := range all {
		if len(d.mounts) > 0 && d.uuid != "" {
			mountedUUIDs[d.uuid] = true
		}
	}
	name := strings.TrimPrefix(device, "/dev/")
	for _, d := range disks {
		target := d.Path == device || d.Name == name || (d.ByID != "" && d.ByID == device)
		for _, p := range d.Partitions {
			if p.Path == device || p.Name == name {
				target = true
			}
		}
		if !target {
			continue
		}
		if d.System {
			return fmt.Errorf("%s holds the running system: %w", d.Path, system.ErrExists)
		}
		own := []dev{{d.Path, d.UUID, d.MountPoints, d.System}}
		for _, p := range d.Partitions {
			own = append(own, dev{p.Path, p.UUID, p.MountPoints, d.System})
		}
		for _, x := range own {
			if len(x.mounts) > 0 {
				return fmt.Errorf("%s is mounted at %s: %w", x.path, strings.Join(x.mounts, ", "), system.ErrExists)
			}
			if x.uuid != "" && mountedUUIDs[x.uuid] {
				return fmt.Errorf("%s belongs to a mounted filesystem: %w", x.path, system.ErrExists)
			}
		}
		return nil
	}
	return fmt.Errorf("device %s: %w", device, system.ErrNotFound)
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
		effect := system.Effect{Type: system.EffectStorage, Action: "partition", Target: in.Disk, Destructive: true}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Disks.Partition(ctx, in.Disk, in.Partitions)
	},
})

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
		effect := system.Effect{Type: system.EffectStorage, Action: "format", Target: strings.Join(in.Spec.Devices, ","), Detail: in.Spec.Type, Destructive: true}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Disks.Format(ctx, in.Spec)
	},
})
