package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type disks struct{ h *host }

type lsblkDevice struct {
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	Type        string        `json:"type"`
	Size        any           `json:"size"`
	Model       *string       `json:"model"`
	Serial      *string       `json:"serial"`
	WWN         *string       `json:"wwn"`
	Rota        any           `json:"rota"`
	Tran        *string       `json:"tran"`
	RM          any           `json:"rm"`
	FSType      *string       `json:"fstype"`
	Label       *string       `json:"label"`
	UUID        *string       `json:"uuid"`
	PartUUID    *string       `json:"partuuid"`
	PartN       any           `json:"partn"`
	MountPoints []*string     `json:"mountpoints"`
	Children    []lsblkDevice `json:"children"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// flexInt reads numbers that older lsblk versions print as strings.
func flexInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func flexBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "1" || b == "true"
	case float64:
		return b != 0
	}
	return false
}

func mountPoints(ps []*string) []string {
	var out []string
	for _, p := range ps {
		if v := deref(p); v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// parseLsblk converts lsblk's JSON tree into disks. byID maps kernel names
// to stable /dev/disk/by-id paths.
func parseLsblk(data []byte, byID map[string]string) ([]system.Disk, error) {
	var doc struct {
		BlockDevices []lsblkDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("reading lsblk output: %w", err)
	}
	var out []system.Disk
	for _, d := range doc.BlockDevices {
		if d.Type != "disk" {
			continue
		}
		disk := system.Disk{
			Name: d.Name, Path: d.Path, ByID: byID[d.Name], Model: deref(d.Model), Serial: deref(d.Serial), WWN: deref(d.WWN),
			SizeBytes: flexInt(d.Size), Rotational: flexBool(d.Rota), Transport: deref(d.Tran), Removable: flexBool(d.RM),
			FSType: deref(d.FSType), Label: deref(d.Label), UUID: deref(d.UUID), MountPoints: mountPoints(d.MountPoints),
		}
		if slices.Contains(disk.MountPoints, "/") {
			disk.System = true
		}
		for _, c := range d.Children {
			if c.Type != "part" {
				continue
			}
			p := system.Partition{
				Name: c.Name, Path: c.Path, Number: int(flexInt(c.PartN)), SizeBytes: flexInt(c.Size),
				FSType: deref(c.FSType), Label: deref(c.Label), UUID: deref(c.UUID), PartUUID: deref(c.PartUUID),
				MountPoints: mountPoints(c.MountPoints),
			}
			if slices.Contains(p.MountPoints, "/") || slices.Contains(p.MountPoints, "/boot/efi") {
				disk.System = true
			}
			disk.Partitions = append(disk.Partitions, p)
		}
		out = append(out, disk)
	}
	return out, nil
}

// diskIDs maps kernel device names to their preferred by-id link, favouring
// model and serial based names over WWN ones.
func (d *disks) diskIDs() map[string]string {
	dir := "/dev/disk/by-id"
	entries, err := os.ReadDir(d.h.path(dir))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range entries {
		if strings.Contains(e.Name(), "-part") {
			continue
		}
		target, err := os.Readlink(d.h.path(path.Join(dir, e.Name())))
		if err != nil {
			continue
		}
		name := path.Base(target)
		link := path.Join(dir, e.Name())
		cur, ok := out[name]
		if !ok || (strings.HasPrefix(path.Base(cur), "wwn-") && !strings.HasPrefix(e.Name(), "wwn-")) {
			out[name] = link
		}
	}
	return out
}

func (d *disks) List(ctx context.Context) ([]system.Disk, error) {
	out, err := d.h.exec(ctx, progLsblk, "--json", "--bytes", "--tree",
		"--output", "NAME,PATH,TYPE,SIZE,MODEL,SERIAL,WWN,ROTA,TRAN,RM,FSTYPE,LABEL,UUID,PARTUUID,PARTN,MOUNTPOINTS")
	if err != nil {
		return nil, fmt.Errorf("listing disks: %w", err)
	}
	return parseLsblk(out, d.diskIDs())
}

var diskNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)

func diskDevice(name string) (string, error) {
	name = strings.TrimPrefix(name, "/dev/")
	if !diskNamePattern.MatchString(name) {
		return "", fmt.Errorf("disk %q: %w", name, system.ErrInvalid)
	}
	return "/dev/" + name, nil
}

func (d *disks) SMART(ctx context.Context, name string) (system.SMARTReport, error) {
	dev, err := diskDevice(name)
	if err != nil {
		return system.SMARTReport{}, err
	}
	out, err := d.h.exec(ctx, progSmartctl, "--json=c", "--all", "--", dev)
	// smartctl's exit status is a bit mask; only the two lowest bits mean it
	// couldn't talk to the disk. The others report findings in the output.
	var exitErr *system.ExitError
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode&3 == 0) {
		return system.SMARTReport{}, fmt.Errorf("reading SMART data of %s: %w", dev, err)
	}
	return parseSmartctl(out)
}

func parseSmartctl(data []byte) (system.SMARTReport, error) {
	var doc struct {
		SmartSupport *struct {
			Available bool `json:"available"`
			Enabled   bool `json:"enabled"`
		} `json:"smart_support"`
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current int `json:"current"`
		} `json:"temperature"`
		PowerOnTime struct {
			Hours int `json:"hours"`
		} `json:"power_on_time"`
		ATA struct {
			Table []struct {
				ID         int    `json:"id"`
				Name       string `json:"name"`
				Value      int    `json:"value"`
				Worst      int    `json:"worst"`
				Thresh     int    `json:"thresh"`
				WhenFailed string `json:"when_failed"`
				Raw        struct {
					Value int64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		NVMe *json.RawMessage `json:"nvme_smart_health_information_log"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return system.SMARTReport{}, fmt.Errorf("reading smartctl output: %w", err)
	}
	r := system.SMARTReport{
		Supported:    doc.NVMe != nil || (doc.SmartSupport != nil && doc.SmartSupport.Available && doc.SmartSupport.Enabled),
		Healthy:      doc.SmartStatus != nil && doc.SmartStatus.Passed,
		TemperatureC: doc.Temperature.Current,
		PowerOnHours: doc.PowerOnTime.Hours,
	}
	for _, a := range doc.ATA.Table {
		r.Attributes = append(r.Attributes, system.SMARTAttribute{
			ID: a.ID, Name: a.Name, Value: a.Value, Worst: a.Worst, Threshold: a.Thresh, Raw: a.Raw.Value,
			Failing: a.WhenFailed != "" && a.WhenFailed != "-",
		})
	}
	return r, nil
}

func (d *disks) SelfTest(ctx context.Context, name string, long bool) error {
	dev, err := diskDevice(name)
	if err != nil {
		return err
	}
	kind := "short"
	if long {
		kind = "long"
	}
	_, err = d.h.exec(ctx, progSmartctl, "--test="+kind, "--", dev)
	return wrap("starting a self-test on "+dev, err)
}

func (d *disks) Wipe(ctx context.Context, device string) error {
	if err := checkDevice(device); err != nil {
		return err
	}
	_, err := d.h.exec(ctx, progWipefs, "--all", "--", device)
	return wrap("wiping "+device, err)
}

var (
	partitionTypeCodes = map[string]string{"efi": "EF00", "linux": "8300", "raid": "FD00", "swap": "8200"}
	labelPattern       = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,32}$`)
)

func (d *disks) Partition(ctx context.Context, disk string, parts []system.PartitionSpec) error {
	dev, err := diskDevice(disk)
	if err != nil {
		return err
	}
	args := []string{"--zap-all"}
	for i, p := range parts {
		code, ok := partitionTypeCodes[p.Type]
		if !ok || p.Number != i+1 || p.SizeBytes < 0 || !labelPattern.MatchString(p.Name) {
			return fmt.Errorf("partition %d of %s: %w", p.Number, dev, system.ErrInvalid)
		}
		end := "0"
		if p.SizeBytes > 0 {
			end = "+" + strconv.FormatInt(p.SizeBytes/1024, 10) + "K"
		}
		n := strconv.Itoa(p.Number)
		args = append(args, "--new="+n+":0:"+end, "--typecode="+n+":"+code)
		if p.Name != "" {
			args = append(args, "--change-name="+n+":"+p.Name)
		}
	}
	if _, err := d.h.exec(ctx, progSgdisk, append(args, "--", dev)...); err != nil {
		return wrap("partitioning "+dev, err)
	}
	_, err = d.h.exec(ctx, progUdevadm, "settle")
	return wrap("waiting for partitions of "+dev, err)
}

func (d *disks) Format(ctx context.Context, spec system.FormatSpec) error {
	if len(spec.Devices) == 0 || !labelPattern.MatchString(spec.Label) {
		return fmt.Errorf("format: %w", system.ErrInvalid)
	}
	for _, dev := range spec.Devices {
		if err := checkDevice(dev); err != nil {
			return err
		}
	}
	if spec.Type != "btrfs" && len(spec.Devices) != 1 {
		return fmt.Errorf("%s takes exactly one device: %w", spec.Type, system.ErrInvalid)
	}
	var prog string
	var args []string
	switch spec.Type {
	case "btrfs":
		prog = progMkfsBtrfs
		if spec.Label != "" {
			args = append(args, "--label", spec.Label)
		}
		for _, p := range [][2]string{{"--data", spec.Profiles.Data}, {"--metadata", spec.Profiles.Metadata}} {
			if p[1] == "" {
				continue
			}
			if !btrfsProfiles[p[1]] {
				return fmt.Errorf("btrfs profile %q: %w", p[1], system.ErrInvalid)
			}
			args = append(args, p[0], p[1])
		}
	case "ext4":
		prog = progMkfsExt4
		// The device has been wiped and checked by the operation, and mke2fs
		// would otherwise stop to ask about whole-disk filesystems.
		args = append(args, "-F")
		if spec.Label != "" {
			args = append(args, "-L", spec.Label)
		}
	case "xfs":
		prog = progMkfsXfs
		if spec.Label != "" {
			args = append(args, "-L", spec.Label)
		}
	case "vfat":
		prog = progMkfsVfat
		if spec.Label != "" {
			args = append(args, "-n", strings.ToUpper(spec.Label))
		}
	case "swap":
		prog = progMkswap
		if spec.Label != "" {
			args = append(args, "--label", spec.Label)
		}
	default:
		return fmt.Errorf("filesystem type %q: %w", spec.Type, system.ErrInvalid)
	}
	args = append(append(args, "--"), spec.Devices...)
	_, err := d.h.exec(ctx, prog, args...)
	return wrap("creating a "+spec.Type+" filesystem on "+strings.Join(spec.Devices, ", "), err)
}
