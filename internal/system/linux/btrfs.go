package linux

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type btrfs struct{ h *host }

var subvolLine = regexp.MustCompile(`^ID (\d+) gen \d+ top level \d+ parent_uuid (\S+)\s+(?:received_uuid (\S+)\s+)?uuid (\S+) path (.+)$`)

func (b *btrfs) mountinfo() ([]mountEntry, error) {
	data, err := os.ReadFile(b.h.path("/proc/self/mountinfo"))
	if err != nil {
		return nil, fmt.Errorf("reading mountinfo: %w", err)
	}
	return parseMountinfo(data), nil
}

func (b *btrfs) Subvolumes(ctx context.Context, mount string) ([]system.Subvolume, error) {
	if err := checkPath(mount); err != nil {
		return nil, err
	}
	all, err := b.h.exec(ctx, progBtrfs, "subvolume", "list", "-q", "-R", "-u", "--", mount)
	if err != nil {
		return nil, fmt.Errorf("listing subvolumes of %s: %w", mount, err)
	}
	ro, err := b.h.exec(ctx, progBtrfs, "subvolume", "list", "-r", "-q", "-u", "--", mount)
	if err != nil {
		return nil, fmt.Errorf("listing read-only subvolumes of %s: %w", mount, err)
	}
	mounts, err := b.mountinfo()
	if err != nil {
		return nil, err
	}
	readOnly := map[uint64]bool{}
	for _, sv := range parseSubvolumeList(ro) {
		readOnly[sv.ID] = true
	}
	device := ""
	if m, ok := holdingMount(mounts, mount); ok && m.FSType == "btrfs" {
		device = m.Source
	}
	svs := parseSubvolumeList(all)
	for i := range svs {
		svs[i].ReadOnly = readOnly[svs[i].ID]
		svs[i].Path = livePath(mounts, device, svs[i].Path)
	}
	return svs, nil
}

func parseSubvolumeList(out []byte) []system.Subvolume {
	var svs []system.Subvolume
	eachLine(out, func(line string) {
		m := subvolLine.FindStringSubmatch(line)
		if m == nil {
			return
		}
		id, _ := strconv.ParseUint(m[1], 10, 64)
		sv := system.Subvolume{ID: id, UUID: m[4], Path: m[5]}
		if m[2] != "-" {
			sv.ParentUUID = m[2]
		}
		if m[3] != "" && m[3] != "-" {
			sv.ReceivedUUID = m[3]
		}
		svs = append(svs, sv)
	})
	return svs
}

// livePath turns a path relative to the top of a Btrfs filesystem, such as
// "@srv/accounts/blog", into the path where it can be reached, such as
// "/srv/racksail/accounts/blog", using the mounts of that filesystem.
func livePath(mounts []mountEntry, device, rel string) string {
	full := "/" + strings.TrimPrefix(rel, "/")
	best, bestLen := "", -1
	for _, m := range mounts {
		if m.FSType != "btrfs" || m.Source != device {
			continue
		}
		if isUnderPath(full, m.Root) && len(m.Root) > bestLen {
			rest := strings.TrimPrefix(full, m.Root)
			best, bestLen = path.Join(m.Target, rest), len(m.Root)
		}
	}
	if bestLen < 0 {
		return full
	}
	return best
}

func isUnderPath(p, dir string) bool {
	return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/")
}

func (b *btrfs) CreateSubvolume(ctx context.Context, p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	_, err := b.h.exec(ctx, progBtrfs, "subvolume", "create", "--", p)
	return wrap("creating subvolume "+p, err)
}

func (b *btrfs) DeleteSubvolume(ctx context.Context, p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	_, err := b.h.exec(ctx, progBtrfs, "subvolume", "delete", "--", p)
	return wrap("deleting subvolume "+p, err)
}

func (b *btrfs) Snapshot(ctx context.Context, source, dest string, readOnly bool) error {
	if err := checkPath(source); err != nil {
		return err
	}
	if err := checkPath(dest); err != nil {
		return err
	}
	args := []string{"subvolume", "snapshot"}
	if readOnly {
		args = append(args, "-r")
	}
	_, err := b.h.exec(ctx, progBtrfs, append(args, "--", source, dest)...)
	return wrap("snapshotting "+source, err)
}

// holdingMount returns the mount p is on: the deepest mount point above it,
// whatever its filesystem.
func holdingMount(mounts []mountEntry, p string) (mountEntry, bool) {
	var best mountEntry
	found := false
	for _, m := range mounts {
		if isUnderPath(p, m.Target) && (!found || len(m.Target) >= len(best.Target)) {
			best, found = m, true
		}
	}
	return best, found
}

// mountPointOf returns where the filesystem holding p is mounted.
func (b *btrfs) mountPointOf(p string) (string, error) {
	mounts, err := b.mountinfo()
	if err != nil {
		return "", err
	}
	m, ok := holdingMount(mounts, p)
	if !ok || m.FSType != "btrfs" {
		return "", fmt.Errorf("%s is not on a Btrfs filesystem: %w", p, system.ErrInvalid)
	}
	return m.Target, nil
}

func (b *btrfs) SetQuota(ctx context.Context, p string, limit int64) error {
	if err := checkPath(p); err != nil {
		return err
	}
	if limit < 0 {
		return fmt.Errorf("quota limit %d: %w", limit, system.ErrInvalid)
	}
	mount, err := b.mountPointOf(p)
	if err != nil {
		return err
	}
	if _, err := b.h.exec(ctx, progBtrfs, "quota", "enable", "--", mount); err != nil {
		return wrap("enabling quotas on "+mount, err)
	}
	value := "none"
	if limit > 0 {
		value = strconv.FormatInt(limit, 10)
	}
	_, err = b.h.exec(ctx, progBtrfs, "qgroup", "limit", value, p)
	return wrap("setting the quota of "+p, err)
}

func (b *btrfs) Quota(ctx context.Context, p string) (system.QuotaUsage, error) {
	if err := checkPath(p); err != nil {
		return system.QuotaUsage{}, err
	}
	out, err := b.h.exec(ctx, progBtrfs, "qgroup", "show", "-reF", "--raw", "--", p)
	if err != nil {
		return system.QuotaUsage{}, wrap("reading the quota of "+p, err)
	}
	return parseQgroupShow(out)
}

func parseQgroupShow(out []byte) (system.QuotaUsage, error) {
	var q system.QuotaUsage
	found := false
	eachLine(out, func(line string) {
		f := strings.Fields(line)
		if found || len(f) < 5 || !strings.Contains(f[0], "/") || f[0] == "qgroupid" {
			return
		}
		if _, err := strconv.ParseInt(f[1], 10, 64); err != nil {
			return
		}
		q.ReferencedBytes, q.ExclusiveBytes = atoi64(f[1]), atoi64(f[2])
		if f[3] != "none" {
			q.LimitBytes = atoi64(f[3])
		}
		found = true
	})
	if !found {
		return q, fmt.Errorf("no qgroup in btrfs output: %w", system.ErrNotFound)
	}
	return q, nil
}

func (b *btrfs) Usage(ctx context.Context, mount string) (system.FilesystemUsage, error) {
	if err := checkPath(mount); err != nil {
		return system.FilesystemUsage{}, err
	}
	out, err := b.h.exec(ctx, progBtrfs, "filesystem", "usage", "-b", "--", mount)
	if err != nil {
		return system.FilesystemUsage{}, wrap("reading the usage of "+mount, err)
	}
	return parseFilesystemUsage(out), nil
}

func parseFilesystemUsage(out []byte) system.FilesystemUsage {
	var u system.FilesystemUsage
	seen := map[string]bool{}
	firstNumber := func(s string) int64 {
		f := strings.Fields(s)
		if len(f) == 0 {
			return 0
		}
		return atoi64(f[0])
	}
	eachLine(out, func(line string) {
		key, value, ok := strings.Cut(line, ":")
		switch {
		case strings.HasPrefix(line, "/dev/"):
			dev := strings.Fields(line)[0]
			if !seen[dev] {
				seen[dev] = true
				u.Devices = append(u.Devices, dev)
			}
		case !ok:
		case key == "Device size":
			u.SizeBytes = firstNumber(value)
		case key == "Used":
			u.UsedBytes = firstNumber(value)
		case key == "Free (estimated)":
			u.FreeBytes = firstNumber(value)
		case strings.HasPrefix(key, "Data,"):
			u.Data = strings.ToLower(strings.TrimPrefix(key, "Data,"))
		case strings.HasPrefix(key, "Metadata,"):
			u.Metadata = strings.ToLower(strings.TrimPrefix(key, "Metadata,"))
		}
	})
	return u
}

func (b *btrfs) Send(ctx context.Context, snapshot, parent string, w io.Writer) error {
	if err := checkPath(snapshot); err != nil {
		return err
	}
	args := []string{"send"}
	if parent != "" {
		if err := checkPath(parent); err != nil {
			return err
		}
		args = append(args, "-p", parent)
	}
	args = append(args, "--", snapshot)
	return wrap("sending "+snapshot, b.h.run.Stream(ctx, system.Command{Path: progBtrfs, Args: args}, nil, w))
}

func (b *btrfs) Receive(ctx context.Context, dir string, r io.Reader, allow func(uuid string) error) error {
	if err := checkPath(dir); err != nil {
		return err
	}
	filter := system.FilterSendStream(r, allow)
	// --chroot confines what the stream does to dir, whatever paths it names.
	err := b.h.run.Stream(ctx, system.Command{Path: progBtrfs, Args: []string{"receive", "--chroot", "--", dir}}, filter, io.Discard)
	if ferr := filter.Err(); ferr != nil {
		// btrfs only saw the stream end early; say why.
		return fmt.Errorf("receiving into %s: %w", dir, ferr)
	}
	return wrap("receiving into "+dir, err)
}

func (b *btrfs) AddDevice(ctx context.Context, mount, device string) error {
	if err := checkPath(mount); err != nil {
		return err
	}
	if err := checkDevice(device); err != nil {
		return err
	}
	_, err := b.h.exec(ctx, progBtrfs, "device", "add", "--", device, mount)
	return wrap("adding "+device+" to "+mount, err)
}

func (b *btrfs) RemoveDevice(ctx context.Context, mount, device string) error {
	if err := checkPath(mount); err != nil {
		return err
	}
	if err := checkDevice(device); err != nil {
		return err
	}
	_, err := b.h.exec(ctx, progBtrfs, "device", "remove", "--", device, mount)
	return wrap("removing "+device+" from "+mount, err)
}

var btrfsProfiles = map[string]bool{"single": true, "dup": true, "raid1": true, "raid1c3": true, "raid1c4": true, "raid10": true}

func (b *btrfs) Balance(ctx context.Context, mount string, p system.BtrfsProfiles) error {
	if err := checkPath(mount); err != nil {
		return err
	}
	args := []string{"balance", "start"}
	if p.Data != "" {
		if !btrfsProfiles[p.Data] {
			return fmt.Errorf("btrfs profile %q: %w", p.Data, system.ErrInvalid)
		}
		args = append(args, "-dconvert="+p.Data)
	}
	if p.Metadata != "" {
		if !btrfsProfiles[p.Metadata] {
			return fmt.Errorf("btrfs profile %q: %w", p.Metadata, system.ErrInvalid)
		}
		args = append(args, "-mconvert="+p.Metadata)
	}
	// Converting profiles rewrites every block, which can take many hours.
	_, err := b.h.run.Run(ctx, system.Command{Path: progBtrfs, Args: append(args, "--", mount), Timeout: 24 * time.Hour})
	return wrap("balancing "+mount, err)
}

func (b *btrfs) ScrubStart(ctx context.Context, mount string) error {
	if err := checkPath(mount); err != nil {
		return err
	}
	_, err := b.h.exec(ctx, progBtrfs, "scrub", "start", "--", mount)
	return wrap("starting a scrub of "+mount, err)
}

func (b *btrfs) ScrubStatus(ctx context.Context, mount string) (system.ScrubStatus, error) {
	if err := checkPath(mount); err != nil {
		return system.ScrubStatus{}, err
	}
	out, err := b.h.exec(ctx, progBtrfs, "scrub", "status", "-R", "--", mount)
	if err != nil {
		return system.ScrubStatus{}, wrap("reading the scrub status of "+mount, err)
	}
	return parseScrubStatus(out), nil
}

func parseScrubStatus(out []byte) system.ScrubStatus {
	var s system.ScrubStatus
	var errorsTotal int64
	eachLine(out, func(line string) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Status":
			s.Running = value == "running"
		case "Scrub started":
			if t, err := time.Parse("Mon Jan _2 15:04:05 2006", value); err == nil {
				s.StartedAt = t.UTC()
			}
		case "data_bytes_scrubbed", "tree_bytes_scrubbed":
			s.BytesScrubbed += atoi64(value)
		case "read_errors", "csum_errors", "verify_errors", "super_errors", "uncorrectable_errors":
			errorsTotal += atoi64(value)
		case "corrected_errors":
			s.CorrectedCount = atoi64(value)
		}
	})
	s.Errors = errorsTotal
	return s
}

func (b *btrfs) SetNoCOW(ctx context.Context, p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	_, err := b.h.exec(ctx, progChattr, "+C", "--", p)
	return wrap("turning off copy-on-write for "+p, err)
}

func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

var devicePattern = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9/_.:-]*$`)

func checkDevice(dev string) error {
	if !devicePattern.MatchString(dev) || strings.Contains(dev, "..") {
		return fmt.Errorf("device %q: %w", dev, system.ErrInvalid)
	}
	return nil
}
