package system

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Links udev keeps under /dev/disk name a block device by what it is, so the
// name stays the same whatever order the kernel finds the disks in.
const (
	DiskByIDDir       = "/dev/disk/by-id"
	DiskByUUIDDir     = "/dev/disk/by-uuid"
	DiskByPartUUIDDir = "/dev/disk/by-partuuid"
)

var stableDevicePattern = regexp.MustCompile(`^/dev/disk/(by-id|by-uuid|by-partuuid)/[A-Za-z0-9][A-Za-z0-9_.:+@=-]{0,127}$`)

// StableDevicePath reports whether p names a block device by one of the
// links under /dev/disk: by-id for a disk (with -partN for its partitions),
// by-uuid for a filesystem and by-partuuid for a partition.
func StableDevicePath(p string) bool {
	return stableDevicePattern.MatchString(p) && !strings.Contains(p, "..")
}

// systemMountPoints are where a disk holds the running system.
var systemMountPoints = []string{"/", "/boot", "/boot/efi", "/usr", "/var"}

// IsSystemMountPoint reports whether a filesystem mounted at p is part of the
// running system, so the disk holding it is the system disk.
func IsSystemMountPoint(p string) bool { return slices.Contains(systemMountPoints, p) }

// BlockDevice is a disk, or a partition of one, as the inventory lists it.
type BlockDevice struct {
	Disk Disk
	// Partition is nil for a whole disk.
	Partition *Partition
}

// Path is the kernel's path of the device, such as /dev/sdb1, which the
// mount table shows.
func (d BlockDevice) Path() string {
	if d.Partition != nil {
		return d.Partition.Path
	}
	return d.Disk.Path
}

// FSType is the filesystem on the device, empty when it holds none.
func (d BlockDevice) FSType() string {
	if d.Partition != nil {
		return d.Partition.FSType
	}
	return d.Disk.FSType
}

// UUID is the filesystem's UUID, which every device of a multi-device
// filesystem shares.
func (d BlockDevice) UUID() string {
	if d.Partition != nil {
		return d.Partition.UUID
	}
	return d.Disk.UUID
}

// MountPoints are where the device is mounted.
func (d BlockDevice) MountPoints() []string {
	if d.Partition != nil {
		return d.Partition.MountPoints
	}
	return d.Disk.MountPoints
}

// Holders are the devices built on this one, such as RAID arrays, LVM
// volumes or encrypted volumes.
func (d BlockDevice) Holders() []string {
	if d.Partition != nil {
		return d.Partition.Holders
	}
	return d.Disk.Holders
}

// FindDevice finds what a device path names in the inventory: a kernel
// path such as /dev/sdb1, a disk's by-id link, the same link with -partN
// for its partition N, a filesystem's by-uuid link (the first device of a
// multi-device filesystem) or a partition's by-partuuid link.
func FindDevice(disks []Disk, dev string) (BlockDevice, bool) {
	for _, d := range disks {
		if d.Path == dev || (d.ByID != "" && d.ByID == dev) {
			return BlockDevice{Disk: d}, true
		}
		for i := range d.Partitions {
			p := &d.Partitions[i]
			if p.Path == dev || (d.ByID != "" && dev == d.ByID+"-part"+strconv.Itoa(p.Number)) {
				return BlockDevice{Disk: d, Partition: p}, true
			}
		}
	}
	if uuid, ok := strings.CutPrefix(dev, DiskByUUIDDir+"/"); ok && uuid != "" {
		for _, d := range disks {
			if strings.EqualFold(d.UUID, uuid) {
				return BlockDevice{Disk: d}, true
			}
			for i := range d.Partitions {
				if strings.EqualFold(d.Partitions[i].UUID, uuid) {
					return BlockDevice{Disk: d, Partition: &d.Partitions[i]}, true
				}
			}
		}
	}
	if partUUID, ok := strings.CutPrefix(dev, DiskByPartUUIDDir+"/"); ok && partUUID != "" {
		for _, d := range disks {
			for i := range d.Partitions {
				if strings.EqualFold(d.Partitions[i].PartUUID, partUUID) {
					return BlockDevice{Disk: d, Partition: &d.Partitions[i]}, true
				}
			}
		}
	}
	return BlockDevice{}, false
}

// FilesystemDevices returns every device that holds the filesystem on dev:
// dev itself and, for a multi-device filesystem such as a Btrfs pool, the
// other devices sharing its UUID.
func FilesystemDevices(disks []Disk, dev BlockDevice) []BlockDevice {
	out := []BlockDevice{dev}
	uuid := dev.UUID()
	if uuid == "" {
		return out
	}
	for _, d := range disks {
		candidates := []BlockDevice{{Disk: d}}
		for i := range d.Partitions {
			candidates = append(candidates, BlockDevice{Disk: d, Partition: &d.Partitions[i]})
		}
		for _, c := range candidates {
			if c.Path() != dev.Path() && strings.EqualFold(c.UUID(), uuid) {
				out = append(out, c)
			}
		}
	}
	return out
}
