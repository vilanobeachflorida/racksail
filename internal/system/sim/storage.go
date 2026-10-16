package sim

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type btrfsFS struct {
	devices  []string
	profiles system.BtrfsProfiles
	quotas   bool
	scrub    system.ScrubStatus
}

func (m *Machine) seedStorage(now time.Time) {
	const gib = int64(1) << 30
	m.disks = []*system.Disk{
		{
			Name: "nvme0n1", Path: "/dev/nvme0n1", ByID: "/dev/disk/by-id/nvme-SIM_SSD_512GB_S0001",
			Model: "SIM SSD 512GB", Serial: "S0001", SizeBytes: 512 * 1000 * 1000 * 1000, Transport: "nvme", System: true,
			Partitions: []system.Partition{
				{Name: "nvme0n1p1", Path: "/dev/nvme0n1p1", Number: 1, SizeBytes: gib, FSType: "vfat", Label: "EFI", UUID: "1A2B-3C4D", MountPoints: []string{"/boot/efi"}},
				{Name: "nvme0n1p2", Path: "/dev/nvme0n1p2", Number: 2, SizeBytes: 512*1000*1000*1000 - gib, FSType: "btrfs", Label: "racksail", UUID: "6f1c2d3e-0000-4000-8000-000000000001",
					MountPoints: []string{"/", "/.snapshots", "/srv/racksail", "/var/lib/racksail", "/var/lib/racksail-db", "/var/log"}},
			},
		},
		{Name: "nvme1n1", Path: "/dev/nvme1n1", ByID: "/dev/disk/by-id/nvme-SIM_SSD_1TB_S0002", Model: "SIM SSD 1TB", Serial: "S0002", SizeBytes: 1000 * 1000 * 1000 * 1000, Transport: "nvme"},
		{Name: "sda", Path: "/dev/sda", ByID: "/dev/disk/by-id/ata-SIM_HDD_4TB_H0001", Model: "SIM HDD 4TB", Serial: "H0001", SizeBytes: 4 * 1000 * 1000 * 1000 * 1000, Rotational: true, Transport: "sata"},
		{Name: "sdb", Path: "/dev/sdb", ByID: "/dev/disk/by-id/ata-SIM_HDD_4TB_H0002", Model: "SIM HDD 4TB", Serial: "H0002", SizeBytes: 4 * 1000 * 1000 * 1000 * 1000, Rotational: true, Transport: "sata"},
	}
	root := "/dev/nvme0n1p2"
	m.btrfsFS[root] = &btrfsFS{devices: []string{root}, profiles: system.BtrfsProfiles{Data: "single", Metadata: "dup"}}
	m.mounts = []system.Mount{
		{Source: root, Target: "/", FSType: "btrfs", Options: []string{"rw", "noatime", "compress=zstd:1", "subvol=/@"}},
		{Source: "/dev/nvme0n1p1", Target: "/boot/efi", FSType: "vfat", Options: []string{"rw", "umask=0077"}},
		{Source: root, Target: "/var/log", FSType: "btrfs", Options: []string{"rw", "noatime", "compress=zstd:1", "subvol=/@log"}},
		{Source: root, Target: "/var/lib/racksail", FSType: "btrfs", Options: []string{"rw", "noatime", "compress=zstd:1", "subvol=/@state"}},
		{Source: root, Target: "/srv/racksail", FSType: "btrfs", Options: []string{"rw", "noatime", "compress=zstd:1", "subvol=/@srv"}},
		{Source: root, Target: "/var/lib/racksail-db", FSType: "btrfs", Options: []string{"rw", "noatime", "subvol=/@db"}},
		{Source: root, Target: "/.snapshots", FSType: "btrfs", Options: []string{"rw", "noatime", "subvol=/@snapshots"}},
	}
	for i, p := range []string{"/", "/var/log", "/var/lib/racksail", "/srv/racksail", "/var/lib/racksail-db", "/.snapshots"} {
		m.subvols[p] = &system.Subvolume{ID: m.nextSub + uint64(i), Path: p, UUID: fmt.Sprintf("6f1c2d3e-0000-4000-8000-%012d", i+10), CreatedAt: now.Add(-72 * time.Hour)}
	}
	m.nextSub += 6
	m.files["/var/lib/racksail-db"].noCOW = true
}

// mountOf returns the mount that holds p. The caller holds m.mu.
func (m *Machine) mountOf(p string) (system.Mount, bool) {
	best, found := system.Mount{}, false
	for _, mt := range m.mounts {
		if isUnder(p, mt.Target) && (!found || len(mt.Target) > len(best.Target)) {
			best, found = mt, true
		}
	}
	return best, found
}

// btrfsOf returns the Btrfs filesystem holding p and the device that names
// it, whichever of its devices it was mounted from. The caller holds m.mu.
func (m *Machine) btrfsOf(p string) (*btrfsFS, string, error) {
	mt, ok := m.mountOf(p)
	if !ok || mt.FSType != "btrfs" {
		return nil, "", invalid("%s is not on a Btrfs filesystem", p)
	}
	if fsys, ok := m.btrfsFS[mt.Source]; ok {
		return fsys, mt.Source, nil
	}
	for dev, fsys := range m.btrfsFS {
		if slices.Contains(fsys.devices, mt.Source) {
			return fsys, dev, nil
		}
	}
	return nil, "", invalid("%s is not on a Btrfs filesystem", p)
}

// kernelPath returns the kernel's path of the device dev names. The caller
// holds m.mu.
func (m *Machine) kernelPath(dev string) (string, string, bool) {
	d, p := m.findDevice(dev)
	switch {
	case p != nil:
		return p.Path, p.UUID, true
	case d != nil:
		return d.Path, d.UUID, true
	}
	return "", "", false
}

// subvolumeFS reports which filesystem a subvolume lives on. The caller
// holds m.mu.
func (m *Machine) subvolumeFS(p string) string {
	_, dev, err := m.btrfsOf(p)
	if err != nil {
		return ""
	}
	return dev
}

// treeSize sums file sizes under p, leaving out nested subvolumes. The
// caller holds m.mu.
func (m *Machine) treeSize(p string) int64 {
	var total int64
	for fp, n := range m.files {
		if !isUnder(fp, p) || n.mode.IsDir() {
			continue
		}
		if m.inNestedSubvolume(fp, p) {
			continue
		}
		total += int64(len(n.data))
	}
	return total
}

func (m *Machine) inNestedSubvolume(fp, root string) bool {
	for sp := range m.subvols {
		if sp != root && isUnder(sp, root) && isUnder(fp, sp) {
			return true
		}
	}
	return false
}

type simBtrfs struct{ m *Machine }

func (b simBtrfs) Subvolumes(ctx context.Context, mount string) ([]system.Subvolume, error) {
	m := b.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	_, dev, err := m.btrfsOf(mount)
	if err != nil {
		return nil, err
	}
	var out []system.Subvolume
	for p, sv := range m.subvols {
		if m.subvolumeFS(p) == dev {
			out = append(out, *sv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (b simBtrfs) CreateSubvolume(ctx context.Context, p string) error {
	b.m.delay(ctx, opSubvolume, 0)
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "create_subvolume", p); err != nil {
		return err
	}
	if _, _, err := m.btrfsOf(path.Dir(p)); err != nil {
		return err
	}
	if err := m.mkdir(p, 0o755, system.Owner{}); err != nil {
		return err
	}
	m.addSubvolume(p, false, "")
	return nil
}

func (m *Machine) addSubvolume(p string, readOnly bool, parentUUID string) *system.Subvolume {
	m.nextSub++
	sv := &system.Subvolume{
		ID: m.nextSub, Path: p, ReadOnly: readOnly, ParentUUID: parentUUID, CreatedAt: m.now(),
		UUID: fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x", hash(p)>>32, hash(p)&0xffff, m.nextSub&0xfff, hash(p+"x")&0xfff, hash(p+m.now().String())&0xffffffffffff),
	}
	m.subvols[p] = sv
	return sv
}

func (b simBtrfs) DeleteSubvolume(ctx context.Context, p string) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "delete_subvolume", p); err != nil {
		return err
	}
	if _, ok := m.subvols[p]; !ok {
		return notFound("subvolume " + p)
	}
	for _, mt := range m.mounts {
		if mt.Target == p {
			return invalid("subvolume %s is mounted", p)
		}
	}
	for sp := range m.subvols {
		if sp != p && isUnder(sp, p) {
			return fmt.Errorf("subvolume %s contains subvolume %s: %w", p, sp, system.ErrNotEmpty)
		}
	}
	m.removeTree(p)
	delete(m.subvols, p)
	delete(m.quotas, p)
	return nil
}

func (b simBtrfs) Snapshot(ctx context.Context, source, dest string, readOnly bool) error {
	b.m.delay(ctx, opSnapshot, 0)
	source, err := cleanAbs(source)
	if err != nil {
		return err
	}
	if dest, err = cleanAbs(dest); err != nil {
		return err
	}
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "snapshot", source+" "+dest); err != nil {
		return err
	}
	src, ok := m.subvols[source]
	if !ok {
		return notFound("subvolume " + source)
	}
	if m.subvolumeFS(source) != m.subvolumeFS(path.Dir(dest)) {
		return invalid("snapshots must stay on the filesystem of %s", source)
	}
	if _, exists := m.files[dest]; exists {
		return fmt.Errorf("%s: %w", dest, system.ErrExists)
	}
	if err := m.parentDir(dest); err != nil {
		return err
	}
	// Like Btrfs, a snapshot doesn't descend into nested subvolumes; they
	// show up as empty directories.
	copies := map[string]*node{}
	for fp, n := range m.files {
		if !isUnder(fp, source) {
			continue
		}
		c := n.clone()
		if m.inNestedSubvolume(fp, source) {
			if _, isSub := m.subvols[fp]; !isSub {
				continue
			}
			c = &node{mode: n.mode, uid: n.uid, gid: n.gid, modTime: n.modTime}
		}
		rel := strings.TrimPrefix(fp, source)
		if source == "/" {
			rel = fp
			if fp == "/" {
				rel = ""
			}
		}
		copies[dest+rel] = c
	}
	for fp, n := range copies {
		m.files[fp] = n
	}
	m.addSubvolume(dest, readOnly, src.UUID)
	return nil
}

func (b simBtrfs) SetQuota(ctx context.Context, p string, limit int64) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	if limit < 0 {
		return invalid("quota limit %d", limit)
	}
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "set_quota", p); err != nil {
		return err
	}
	if _, ok := m.subvols[p]; !ok {
		return notFound("subvolume " + p)
	}
	fsys, _, err := m.btrfsOf(p)
	if err != nil {
		return err
	}
	fsys.quotas = true
	if limit == 0 {
		delete(m.quotas, p)
	} else {
		m.quotas[p] = limit
	}
	return nil
}

func (b simBtrfs) Quota(ctx context.Context, p string) (system.QuotaUsage, error) {
	p, err := cleanAbs(p)
	if err != nil {
		return system.QuotaUsage{}, err
	}
	m := b.m
	if err := m.lock(ctx); err != nil {
		return system.QuotaUsage{}, err
	}
	defer m.mu.Unlock()
	if _, ok := m.subvols[p]; !ok {
		return system.QuotaUsage{}, notFound("subvolume " + p)
	}
	used := m.treeSize(p)
	return system.QuotaUsage{ReferencedBytes: used, ExclusiveBytes: used, LimitBytes: m.quotas[p]}, nil
}

func (b simBtrfs) Usage(ctx context.Context, mount string) (system.FilesystemUsage, error) {
	m := b.m
	if err := m.lock(ctx); err != nil {
		return system.FilesystemUsage{}, err
	}
	defer m.mu.Unlock()
	fsys, dev, err := m.btrfsOf(mount)
	if err != nil {
		return system.FilesystemUsage{}, err
	}
	var size int64
	for _, d := range fsys.devices {
		size += m.deviceSize(d)
	}
	var used int64 = 1 << 30
	for fp, n := range m.files {
		if !n.mode.IsDir() && m.subvolumeFS(fp) == dev {
			used += int64(len(n.data))
		}
	}
	return system.FilesystemUsage{SizeBytes: size, UsedBytes: used, FreeBytes: size - used, BtrfsProfiles: fsys.profiles, Devices: slices.Clone(fsys.devices)}, nil
}

// sendEntry is a line of a simulated send stream: the header, naming the
// snapshot and the parent an incremental stream builds on by UUID as Btrfs
// does, then one entry per file.
type sendEntry struct {
	Format     string      `json:"format,omitempty"`
	Name       string      `json:"name,omitempty"`
	UUID       string      `json:"uuid,omitempty"`
	Parent     string      `json:"parent,omitempty"`
	ParentUUID string      `json:"parentUuid,omitempty"`
	Path       string      `json:"path,omitempty"`
	Mode       fs.FileMode `json:"mode,omitempty"`
	UID        int         `json:"uid,omitempty"`
	GID        int         `json:"gid,omitempty"`
	Data       string      `json:"data,omitempty"`
	Target     string      `json:"target,omitempty"`
}

const sendFormat = "racksail-sim-send/1"

func (b simBtrfs) Send(ctx context.Context, snapshot, parent string, w io.Writer) error {
	snapshot, err := cleanAbs(snapshot)
	if err != nil {
		return err
	}
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	sv, ok := m.subvols[snapshot]
	if !ok {
		m.mu.Unlock()
		return notFound("subvolume " + snapshot)
	}
	if !sv.ReadOnly {
		m.mu.Unlock()
		return invalid("only read-only snapshots can be sent")
	}
	header := sendEntry{Format: sendFormat, Name: path.Base(snapshot), UUID: sv.UUID}
	if parent != "" {
		p, ok := m.subvols[parent]
		if !ok {
			m.mu.Unlock()
			return notFound("parent subvolume " + parent)
		}
		header.Parent, header.ParentUUID = path.Base(parent), p.UUID
	}
	if err := m.record("btrfs", "send", snapshot); err != nil {
		m.mu.Unlock()
		return err
	}
	entries := []sendEntry{header}
	var paths []string
	for fp := range m.files {
		if isUnder(fp, snapshot) && fp != snapshot && !m.inNestedSubvolume(fp, snapshot) {
			paths = append(paths, fp)
		}
	}
	sort.Strings(paths)
	for _, fp := range paths {
		n := m.files[fp]
		entries = append(entries, sendEntry{
			Path: strings.TrimPrefix(fp, snapshot+"/"), Mode: n.mode, UID: n.uid, GID: n.gid,
			Data: base64.StdEncoding.EncodeToString(n.data), Target: n.target,
		})
	}
	m.mu.Unlock()
	enc := json.NewEncoder(w)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

func (b simBtrfs) Receive(ctx context.Context, dir string, r io.Reader, allow func(uuid string) error) error {
	dir, err := cleanAbs(dir)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 256<<20)
	var header sendEntry
	var entries []sendEntry
	for sc.Scan() {
		var e sendEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return invalid("send stream: %v", err)
		}
		if header.Format == "" {
			header = e
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if header.Format != sendFormat || header.Name == "" || strings.ContainsAny(header.Name, "/\x00") || header.Name == "." || header.Name == ".." {
		return invalid("not a send stream")
	}
	m := b.m
	// The caller's decision about the parent looks at the machine too, so
	// it is asked before the machine is locked.
	parent, err := m.receiveParentUUID(ctx, dir, header)
	if err != nil {
		return err
	}
	if parent != "" {
		if allow == nil {
			return invalid("send stream: the stream builds on subvolume %s", parent)
		}
		if err := allow(parent); err != nil {
			return fmt.Errorf("send stream: the parent is subvolume %s: %w", parent, err)
		}
	}
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "receive", dir); err != nil {
		return err
	}
	_, fsDev, err := m.btrfsOf(dir)
	if err != nil {
		return err
	}
	if parent != "" && !m.hasParent(fsDev, parent) {
		return notFound("the parent snapshot, subvolume " + parent)
	}
	dest := path.Join(dir, header.Name)
	files := map[string]*node{}
	for _, e := range entries {
		p := path.Join(dest, e.Path)
		if !isUnder(p, dest) || p == dest {
			return invalid("send stream entry %q escapes the snapshot", e.Path)
		}
		if e.Mode&fs.ModeDevice != 0 {
			return invalid("send stream: device nodes aren't received")
		}
		data, err := base64.StdEncoding.DecodeString(e.Data)
		if err != nil {
			return invalid("send stream entry %q: %v", e.Path, err)
		}
		// As with every other way files arrive, set-ID bits don't.
		mode := e.Mode &^ (fs.ModeSetuid | fs.ModeSetgid)
		files[p] = &node{mode: mode, uid: e.UID, gid: e.GID, data: data, target: e.Target, modTime: m.now()}
	}
	if err := m.mkdir(dest, 0o755, system.Owner{}); err != nil {
		return err
	}
	for p, n := range files {
		m.files[p] = n
	}
	sv := m.addSubvolume(dest, true, "")
	sv.ReceivedUUID = header.UUID
	return nil
}

// receiveParentUUID returns the UUID of the subvolume an incremental stream
// builds on, empty for a full stream.
func (m *Machine) receiveParentUUID(ctx context.Context, dir string, header sendEntry) (string, error) {
	if header.ParentUUID != "" || header.Parent == "" || header.Parent == "." {
		return header.ParentUUID, nil
	}
	// Streams from before parents were named by UUID name one in dir.
	if err := m.lock(ctx); err != nil {
		return "", err
	}
	defer m.mu.Unlock()
	p, ok := m.subvols[path.Join(dir, header.Parent)]
	if !ok {
		return "", notFound("parent snapshot " + path.Join(dir, header.Parent))
	}
	return p.UUID, nil
}

// hasParent reports whether the filesystem has the subvolume an
// incremental stream builds on, found as btrfs receive finds it: one
// received from it first, then the subvolume itself. The caller holds m.mu.
func (m *Machine) hasParent(fsDev, uuid string) bool {
	for p, sv := range m.subvols {
		if m.subvolumeFS(p) == fsDev && (sv.ReceivedUUID == uuid || sv.UUID == uuid) {
			return true
		}
	}
	return false
}

func (b simBtrfs) AddDevice(ctx context.Context, mount, device string) error {
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "add_device", mount+" "+device); err != nil {
		return err
	}
	fsys, _, err := m.btrfsOf(mount)
	if err != nil {
		return err
	}
	if err := m.deviceFree(device); err != nil {
		return err
	}
	dev, _, ok := m.kernelPath(device)
	if !ok {
		return notFound("device " + device)
	}
	_, uuid, _ := m.kernelPath(fsys.devices[0])
	fsys.devices = append(fsys.devices, dev)
	m.setDeviceFS(dev, "btrfs", "", uuid)
	return nil
}

func (b simBtrfs) RemoveDevice(ctx context.Context, mount, device string) error {
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "remove_device", mount+" "+device); err != nil {
		return err
	}
	fsys, _, err := m.btrfsOf(mount)
	if err != nil {
		return err
	}
	if dev, _, ok := m.kernelPath(device); ok {
		device = dev
	}
	i := slices.Index(fsys.devices, device)
	if i < 0 {
		return notFound("device " + device + " in " + mount)
	}
	if len(fsys.devices)-1 < minDevices(fsys.profiles.Data) || len(fsys.devices)-1 < minDevices(fsys.profiles.Metadata) {
		return invalid("removing %s would leave too few devices for %s/%s", device, fsys.profiles.Data, fsys.profiles.Metadata)
	}
	if i == 0 {
		return invalid("%s is the device the filesystem is mounted from", device)
	}
	fsys.devices = slices.Delete(fsys.devices, i, i+1)
	m.setDeviceFS(device, "", "", "")
	return nil
}

func minDevices(profile string) int {
	switch profile {
	case "raid1", "raid10":
		return 2
	case "raid1c3":
		return 3
	case "raid1c4":
		return 4
	}
	return 1
}

func validProfile(p string) bool {
	switch p {
	case "single", "dup", "raid1", "raid1c3", "raid1c4", "raid10":
		return true
	}
	return false
}

func (b simBtrfs) Balance(ctx context.Context, mount string, profiles system.BtrfsProfiles) error {
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "balance", mount); err != nil {
		return err
	}
	fsys, _, err := m.btrfsOf(mount)
	if err != nil {
		return err
	}
	for _, p := range []string{profiles.Data, profiles.Metadata} {
		if p == "" {
			continue
		}
		if !validProfile(p) {
			return invalid("btrfs profile %q", p)
		}
		if len(fsys.devices) < minDevices(p) {
			return invalid("profile %s needs %d devices, the filesystem has %d", p, minDevices(p), len(fsys.devices))
		}
	}
	if profiles.Data != "" {
		fsys.profiles.Data = profiles.Data
	}
	if profiles.Metadata != "" {
		fsys.profiles.Metadata = profiles.Metadata
	}
	return nil
}

func (b simBtrfs) ScrubStart(ctx context.Context, mount string) error {
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "scrub_start", mount); err != nil {
		return err
	}
	fsys, dev, err := m.btrfsOf(mount)
	if err != nil {
		return err
	}
	var scrubbed int64
	for fp, n := range m.files {
		if !n.mode.IsDir() && m.subvolumeFS(fp) == dev {
			scrubbed += int64(len(n.data))
		}
	}
	fsys.scrub = system.ScrubStatus{StartedAt: m.now(), BytesScrubbed: scrubbed}
	return nil
}

func (b simBtrfs) ScrubStatus(ctx context.Context, mount string) (system.ScrubStatus, error) {
	m := b.m
	if err := m.lock(ctx); err != nil {
		return system.ScrubStatus{}, err
	}
	defer m.mu.Unlock()
	fsys, _, err := m.btrfsOf(mount)
	if err != nil {
		return system.ScrubStatus{}, err
	}
	return fsys.scrub, nil
}

func (b simBtrfs) SetNoCOW(ctx context.Context, p string) error {
	p, err := cleanAbs(p)
	if err != nil {
		return err
	}
	m := b.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("btrfs", "set_nocow", p); err != nil {
		return err
	}
	n, ok := m.files[p]
	if !ok {
		return notFound(p)
	}
	if !n.mode.IsDir() {
		return invalid("%s is not a directory", p)
	}
	n.noCOW = true
	return nil
}

type simMounts struct{ m *Machine }

func (s simMounts) List(ctx context.Context) ([]system.Mount, error) {
	m := s.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	out := make([]system.Mount, len(m.mounts))
	for i, mt := range m.mounts {
		mt.Options = slices.Clone(mt.Options)
		out[i] = mt
	}
	return out, nil
}

func (s simMounts) Mount(ctx context.Context, mt system.Mount) error {
	target, err := cleanAbs(mt.Target)
	if err != nil {
		return err
	}
	mt.Target = target
	m := s.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("mounts", "mount", target); err != nil {
		return err
	}
	if mt.Source == "" || mt.FSType == "" {
		return invalid("a mount needs a source and a filesystem type")
	}
	n, ok := m.files[target]
	if !ok || !n.mode.IsDir() {
		return notFound("mount point " + target)
	}
	for _, other := range m.mounts {
		if other.Target == target {
			return fmt.Errorf("%s is already mounted: %w", target, system.ErrExists)
		}
	}
	if strings.HasPrefix(mt.Source, "/dev/") {
		d, p := m.findDevice(mt.Source)
		if d == nil {
			return notFound("device " + mt.Source)
		}
		fstype, kernelPath := d.FSType, d.Path
		if p != nil {
			fstype, kernelPath = p.FSType, p.Path
		}
		if fstype != mt.FSType {
			return invalid("%s holds %q, not %s", mt.Source, fstype, mt.FSType)
		}
		// Like the kernel, the mount table names the device itself, not
		// the link it was reached through.
		mt.Source = kernelPath
	}
	mt.Options = slices.Clone(mt.Options)
	m.mounts = append(m.mounts, mt)
	return nil
}

func (s simMounts) Unmount(ctx context.Context, target string) error {
	m := s.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("mounts", "unmount", target); err != nil {
		return err
	}
	if target == "/" {
		return invalid("refusing to unmount /")
	}
	for i, mt := range m.mounts {
		if mt.Target == target {
			m.mounts = slices.Delete(m.mounts, i, i+1)
			return nil
		}
	}
	return notFound("mount " + target)
}

type simDisks struct{ m *Machine }

// findDevice returns the disk and, for partitions, the partition that dev
// names: a kernel name or path, or one of the links udev keeps under
// /dev/disk. The caller holds m.mu.
func (m *Machine) findDevice(dev string) (*system.Disk, *system.Partition) {
	name := strings.TrimPrefix(dev, "/dev/")
	for _, d := range m.disks {
		if d.Name == name || d.Path == dev || d.ByID == dev {
			return d, nil
		}
		for i := range d.Partitions {
			if d.Partitions[i].Name == name || d.Partitions[i].Path == dev {
				return d, &d.Partitions[i]
			}
		}
	}
	inventory := make([]system.Disk, len(m.disks))
	for i, d := range m.disks {
		inventory[i] = *d
	}
	found, ok := system.FindDevice(inventory, dev)
	if !ok {
		return nil, nil
	}
	return m.findDevice(found.Path())
}

func (m *Machine) deviceSize(dev string) int64 {
	d, p := m.findDevice(dev)
	switch {
	case p != nil:
		return p.SizeBytes
	case d != nil:
		return d.SizeBytes
	}
	return 0
}

// fsUUID makes up the UUID of a new filesystem on dev.
func (m *Machine) fsUUID(dev, fstype, label string) string {
	return fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x", hash(dev)>>32, hash(dev)&0xffff, hash(fstype)&0xfff, hash(label)&0xfff, hash(dev+m.now().String())&0xffffffffffff)
}

// setDeviceFS records a filesystem on dev; uuid is shared by every device
// of a multi-device filesystem.
func (m *Machine) setDeviceFS(dev, fstype, label, uuid string) {
	d, p := m.findDevice(dev)
	if fstype == "" {
		uuid = ""
	}
	switch {
	case p != nil:
		p.FSType, p.Label, p.UUID = fstype, label, uuid
	case d != nil:
		d.FSType, d.Label, d.UUID = fstype, label, uuid
	}
}

// deviceFree checks that nothing uses a device. The caller holds m.mu.
func (m *Machine) deviceFree(dev string) error {
	d, p := m.findDevice(dev)
	if d == nil {
		return notFound("device " + dev)
	}
	if d.System {
		return invalid("%s holds the running system", d.Path)
	}
	path, uuid := d.Path, d.UUID
	if p != nil {
		path, uuid = p.Path, p.UUID
	}
	for _, mt := range m.mounts {
		if mt.Source == path {
			return invalid("%s is mounted at %s", path, mt.Target)
		}
		if uuid != "" {
			if other, op := m.findDevice(mt.Source); other != nil && ((op != nil && op.UUID == uuid) || (op == nil && other.UUID == uuid)) {
				return invalid("%s belongs to the filesystem mounted at %s", path, mt.Target)
			}
		}
	}
	for _, fsys := range m.btrfsFS {
		if slices.Contains(fsys.devices, path) {
			return invalid("%s belongs to a Btrfs filesystem", path)
		}
	}
	return nil
}

func (s simDisks) List(ctx context.Context) ([]system.Disk, error) {
	m := s.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	out := make([]system.Disk, len(m.disks))
	for i, d := range m.disks {
		c := *d
		c.Partitions = slices.Clone(d.Partitions)
		c.MountPoints = m.mountPointsOf(d.Path)
		for j := range c.Partitions {
			c.Partitions[j].MountPoints = m.mountPointsOf(c.Partitions[j].Path)
		}
		out[i] = c
	}
	return out, nil
}

// mountPointsOf lists where a device is mounted, sorted as lsblk sorts
// them. The caller holds m.mu.
func (m *Machine) mountPointsOf(dev string) []string {
	var out []string
	for _, mt := range m.mounts {
		if mt.Source == dev {
			out = append(out, mt.Target)
		}
	}
	sort.Strings(out)
	return out
}

func (s simDisks) Wipe(ctx context.Context, device string) error {
	m := s.m
	m.delay(ctx, opWipe, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("disks", "wipe", device); err != nil {
		return err
	}
	if err := m.deviceFree(device); err != nil {
		return err
	}
	d, p := m.findDevice(device)
	if p != nil {
		p.FSType, p.Label, p.UUID = "", "", ""
		return nil
	}
	for _, part := range d.Partitions {
		if err := m.deviceFree(part.Path); err != nil {
			return err
		}
	}
	d.FSType, d.Label, d.UUID, d.Partitions = "", "", "", nil
	return nil
}

func (s simDisks) Partition(ctx context.Context, disk string, parts []system.PartitionSpec) error {
	m := s.m
	m.delay(ctx, opPartition, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("disks", "partition", disk); err != nil {
		return err
	}
	d, p := m.findDevice(disk)
	if d == nil || p != nil {
		return notFound("disk " + disk)
	}
	if err := m.deviceFree(d.Path); err != nil {
		return err
	}
	for _, part := range d.Partitions {
		if err := m.deviceFree(part.Path); err != nil {
			return err
		}
	}
	const reserved = 2 << 20
	free := d.SizeBytes - reserved
	var out []system.Partition
	for i, ps := range parts {
		switch ps.Type {
		case "efi", "linux", "raid", "swap":
		default:
			return invalid("partition type %q", ps.Type)
		}
		if ps.Number != i+1 {
			return invalid("partitions must be numbered from 1 in order")
		}
		size := ps.SizeBytes
		if size == 0 {
			if i != len(parts)-1 {
				return invalid("only the last partition can use the rest of the disk")
			}
			size = free
		}
		if size <= 0 || size > free {
			return invalid("partition %d doesn't fit on %s", ps.Number, d.Name)
		}
		free -= size
		name := fmt.Sprintf("%s%d", d.Name, ps.Number)
		if strings.HasPrefix(d.Name, "nvme") {
			name = fmt.Sprintf("%sp%d", d.Name, ps.Number)
		}
		partUUID := fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x", hash(name)>>32, hash(d.Name)&0xffff, ps.Number&0xfff, hash(ps.Type)&0xfff, hash(name+m.now().String())&0xffffffffffff)
		out = append(out, system.Partition{Name: name, Path: "/dev/" + name, Number: ps.Number, SizeBytes: size, Label: ps.Name, PartUUID: partUUID})
	}
	d.Partitions = out
	d.FSType, d.Label, d.UUID = "", "", ""
	return nil
}

func (s simDisks) Format(ctx context.Context, spec system.FormatSpec) error {
	m := s.m
	m.delay(ctx, opFormat, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("disks", "format", strings.Join(spec.Devices, ",")); err != nil {
		return err
	}
	switch spec.Type {
	case "btrfs", "ext4", "xfs", "vfat", "swap":
	default:
		return invalid("filesystem type %q", spec.Type)
	}
	if len(spec.Devices) == 0 || (len(spec.Devices) > 1 && spec.Type != "btrfs") {
		return invalid("%s takes exactly one device", spec.Type)
	}
	for _, dev := range spec.Devices {
		if err := m.deviceFree(dev); err != nil {
			return err
		}
		if d, p := m.findDevice(dev); p == nil && len(d.Partitions) > 0 {
			return invalid("%s has partitions; wipe it first", dev)
		}
	}
	if spec.Type == "btrfs" {
		profiles := spec.Profiles
		if profiles.Data == "" {
			profiles.Data = "single"
		}
		if profiles.Metadata == "" {
			profiles.Metadata = "dup"
			if len(spec.Devices) > 1 {
				profiles.Metadata = "raid1"
			}
		}
		for _, p := range []string{profiles.Data, profiles.Metadata} {
			if !validProfile(p) || len(spec.Devices) < minDevices(p) {
				return invalid("profile %s with %d devices", p, len(spec.Devices))
			}
		}
		var paths []string
		for _, dev := range spec.Devices {
			d, p := m.findDevice(dev)
			if p != nil {
				paths = append(paths, p.Path)
			} else {
				paths = append(paths, d.Path)
			}
		}
		m.btrfsFS[paths[0]] = &btrfsFS{devices: paths, profiles: profiles}
	}
	// Every device of a multi-device Btrfs filesystem carries its UUID.
	uuid := m.fsUUID(spec.Devices[0], spec.Type, spec.Label)
	for _, dev := range spec.Devices {
		m.setDeviceFS(dev, spec.Type, spec.Label, uuid)
	}
	return nil
}
