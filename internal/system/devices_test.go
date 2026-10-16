package system

import (
	"testing"
)

func testDisks() []Disk {
	return []Disk{
		{
			Name: "nvme0n1", Path: "/dev/nvme0n1", ByID: "/dev/disk/by-id/nvme-SSD_S1", System: true,
			Partitions: []Partition{
				{Name: "nvme0n1p1", Path: "/dev/nvme0n1p1", Number: 1, FSType: "vfat", UUID: "1A2B-3C4D", PartUUID: "aaaa-01", MountPoints: []string{"/boot/efi"}},
				{Name: "nvme0n1p2", Path: "/dev/nvme0n1p2", Number: 2, FSType: "btrfs", UUID: "root-uuid", PartUUID: "aaaa-02", MountPoints: []string{"/"}},
			},
		},
		{Name: "sda", Path: "/dev/sda", ByID: "/dev/disk/by-id/ata-HDD_H1", FSType: "btrfs", UUID: "pool-uuid"},
		{Name: "sdb", Path: "/dev/sdb", ByID: "/dev/disk/by-id/ata-HDD_H2", FSType: "btrfs", UUID: "pool-uuid"},
		{
			Name: "sdc", Path: "/dev/sdc", ByID: "/dev/disk/by-id/ata-HDD_H3",
			Partitions: []Partition{
				{Name: "sdc1", Path: "/dev/sdc1", Number: 1, FSType: "ext4", UUID: "data-uuid", PartUUID: "cccc-01"},
				{Name: "sdc2", Path: "/dev/sdc2", Number: 2, FSType: "linux_raid_member", UUID: "md-uuid", PartUUID: "cccc-02", Holders: []string{"md0"}},
			},
		},
	}
}

func TestFindDevice(t *testing.T) {
	disks := testDisks()
	for dev, want := range map[string]string{
		"/dev/sda":                         "/dev/sda",
		"/dev/disk/by-id/ata-HDD_H1":       "/dev/sda",
		"/dev/disk/by-id/ata-HDD_H3-part1": "/dev/sdc1",
		"/dev/disk/by-id/ata-HDD_H3-part2": "/dev/sdc2",
		"/dev/disk/by-uuid/DATA-UUID":      "/dev/sdc1",
		"/dev/disk/by-uuid/pool-uuid":      "/dev/sda",
		"/dev/disk/by-partuuid/aaaa-02":    "/dev/nvme0n1p2",
		"/dev/nvme0n1p1":                   "/dev/nvme0n1p1",
	} {
		got, ok := FindDevice(disks, dev)
		if !ok || got.Path() != want {
			t.Errorf("FindDevice(%s) = %s, %v; want %s", dev, got.Path(), ok, want)
		}
	}
	for _, dev := range []string{
		"/dev/sdz", "/dev/disk/by-id/ata-HDD_H3-part9", "/dev/disk/by-uuid/nothing", "/dev/disk/by-partuuid/", "/dev/disk/by-uuid/",
	} {
		if got, ok := FindDevice(disks, dev); ok {
			t.Errorf("FindDevice(%s) = %s, want nothing", dev, got.Path())
		}
	}
	root, _ := FindDevice(disks, "/dev/disk/by-uuid/root-uuid")
	if !root.Disk.System || root.FSType() != "btrfs" || len(root.MountPoints()) != 1 {
		t.Errorf("the root filesystem = %+v", root)
	}
	member, _ := FindDevice(disks, "/dev/disk/by-id/ata-HDD_H3-part2")
	if len(member.Holders()) != 1 {
		t.Errorf("holders = %v", member.Holders())
	}
}

func TestFilesystemDevices(t *testing.T) {
	disks := testDisks()
	pool, _ := FindDevice(disks, "/dev/disk/by-id/ata-HDD_H2")
	var paths []string
	for _, d := range FilesystemDevices(disks, pool) {
		paths = append(paths, d.Path())
	}
	if len(paths) != 2 || paths[0] != "/dev/sdb" || paths[1] != "/dev/sda" {
		t.Errorf("devices of the pool = %v", paths)
	}
	data, _ := FindDevice(disks, "/dev/sdc1")
	if got := FilesystemDevices(disks, data); len(got) != 1 {
		t.Errorf("devices of a one-device filesystem = %v", got)
	}
	empty, _ := FindDevice(disks, "/dev/sdc")
	if got := FilesystemDevices(disks, empty); len(got) != 1 {
		t.Errorf("a disk without a filesystem shares it with nothing: %v", got)
	}
}

func TestStableDevicePath(t *testing.T) {
	for p, want := range map[string]bool{
		"/dev/disk/by-id/wwn-0x5000c500a1b2c3d4":         true,
		"/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_S6Z1":  true,
		"/dev/disk/by-uuid/1A2B-3C4D":                    true,
		"/dev/disk/by-partuuid/7f3e9a10-01":              true,
		"/dev/sda":                                       false,
		"/dev/disk/by-label/data":                        false,
		"/dev/disk/by-path/pci-0000:00:17.0-ata-1":       false,
		"/dev/disk/by-id/../by-uuid/x":                   false,
		"/dev/disk/by-id/a/b":                            false,
		"/dev/disk/by-id/":                               false,
		"/dev/disk/by-id/.hidden":                        false,
		"/dev/disk/by-id/ata-HDD_1\n":                    false,
		"/dev/disk/by-id/usb-Generic_STORAGE_000-0:0":    true,
		"/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive": true,
	} {
		if got := StableDevicePath(p); got != want {
			t.Errorf("StableDevicePath(%q) = %v, want %v", p, got, want)
		}
	}
}
