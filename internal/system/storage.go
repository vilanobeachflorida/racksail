package system

import (
	"context"
	"io"
	"time"
)

// Btrfs manages Btrfs filesystems, subvolumes, snapshots and quotas. Paths
// are absolute paths inside a mounted Btrfs filesystem.
type Btrfs interface {
	// Subvolumes lists the subvolumes of the filesystem mounted at mount.
	Subvolumes(ctx context.Context, mount string) ([]Subvolume, error)
	CreateSubvolume(ctx context.Context, path string) error
	DeleteSubvolume(ctx context.Context, path string) error
	Snapshot(ctx context.Context, source, dest string, readOnly bool) error
	// SetQuota limits the space a subvolume can use. A zero limit removes it.
	// Quotas are switched on for the filesystem when needed.
	SetQuota(ctx context.Context, path string, limitBytes int64) error
	Quota(ctx context.Context, path string) (QuotaUsage, error)
	Usage(ctx context.Context, mount string) (FilesystemUsage, error)
	// Send writes a send stream of snapshot to w, incremental from parent
	// when parent isn't empty.
	Send(ctx context.Context, snapshot, parent string, w io.Writer) error
	// Receive reads a send stream from r into the directory dir.
	Receive(ctx context.Context, dir string, r io.Reader) error
	AddDevice(ctx context.Context, mount, device string) error
	RemoveDevice(ctx context.Context, mount, device string) error
	// Balance converts the filesystem to new data and metadata profiles.
	Balance(ctx context.Context, mount string, profiles BtrfsProfiles) error
	ScrubStart(ctx context.Context, mount string) error
	ScrubStatus(ctx context.Context, mount string) (ScrubStatus, error)
	// SetNoCOW turns off copy-on-write for new files in a directory, as
	// databases need.
	SetNoCOW(ctx context.Context, path string) error
}

// Subvolume is a Btrfs subvolume or snapshot.
type Subvolume struct {
	ID         uint64    `json:"id"`
	Path       string    `json:"path"`
	UUID       string    `json:"uuid,omitempty"`
	ParentUUID string    `json:"parentUuid,omitempty"`
	ReadOnly   bool      `json:"readOnly"`
	CreatedAt  time.Time `json:"createdAt,omitzero"`
}

// QuotaUsage is the space a subvolume uses and its limit.
type QuotaUsage struct {
	ReferencedBytes int64 `json:"referencedBytes"`
	ExclusiveBytes  int64 `json:"exclusiveBytes"`
	LimitBytes      int64 `json:"limitBytes"`
}

// FilesystemUsage is the space use of a filesystem.
type FilesystemUsage struct {
	SizeBytes int64 `json:"sizeBytes"`
	UsedBytes int64 `json:"usedBytes"`
	FreeBytes int64 `json:"freeBytes"`
	BtrfsProfiles
	Devices []string `json:"devices,omitempty"`
}

// BtrfsProfiles are the data and metadata profiles of a Btrfs filesystem:
// single, dup, raid1, raid1c3, raid1c4 or raid10.
type BtrfsProfiles struct {
	Data     string `json:"data,omitempty"`
	Metadata string `json:"metadata,omitempty"`
}

// ScrubStatus reports the progress of a scrub.
type ScrubStatus struct {
	Running        bool      `json:"running"`
	StartedAt      time.Time `json:"startedAt,omitzero"`
	BytesScrubbed  int64     `json:"bytesScrubbed"`
	Errors         int64     `json:"errors"`
	CorrectedCount int64     `json:"corrected"`
}

// Mounts manages mounted filesystems. Mounts that must survive a reboot are
// systemd .mount units written through Units.
type Mounts interface {
	List(ctx context.Context) ([]Mount, error)
	Mount(ctx context.Context, m Mount) error
	Unmount(ctx context.Context, target string) error
}

// Mount is a mounted filesystem.
type Mount struct {
	Source  string   `json:"source"`
	Target  string   `json:"target"`
	FSType  string   `json:"fsType"`
	Options []string `json:"options,omitempty"`
}

// Disks inventories and prepares block devices.
type Disks interface {
	List(ctx context.Context) ([]Disk, error)
	SMART(ctx context.Context, name string) (SMARTReport, error)
	// SelfTest starts a SMART self-test; long tests take hours.
	SelfTest(ctx context.Context, name string, long bool) error
	// Wipe removes filesystem and partition signatures from a device.
	Wipe(ctx context.Context, device string) error
	// Partition writes a new GPT partition table with the given partitions.
	Partition(ctx context.Context, disk string, parts []PartitionSpec) error
	Format(ctx context.Context, spec FormatSpec) error
}

// Disk is a whole block device.
type Disk struct {
	// Name is the kernel name, such as sda or nvme0n1.
	Name string `json:"name"`
	Path string `json:"path"`
	// ByID is a stable /dev/disk/by-id path.
	ByID        string      `json:"byId,omitempty"`
	Model       string      `json:"model,omitempty"`
	Serial      string      `json:"serial,omitempty"`
	WWN         string      `json:"wwn,omitempty"`
	SizeBytes   int64       `json:"sizeBytes"`
	Rotational  bool        `json:"rotational"`
	Transport   string      `json:"transport,omitempty" doc:"sata, nvme, usb, sas or virtio"`
	Removable   bool        `json:"removable"`
	FSType      string      `json:"fsType,omitempty"`
	Label       string      `json:"label,omitempty"`
	UUID        string      `json:"uuid,omitempty"`
	MountPoints []string    `json:"mountPoints,omitempty"`
	Partitions  []Partition `json:"partitions,omitempty"`
	// System is true for the disk the running system lives on.
	System bool `json:"system"`
}

// Partition is a partition of a disk.
type Partition struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Number      int      `json:"number"`
	SizeBytes   int64    `json:"sizeBytes"`
	FSType      string   `json:"fsType,omitempty"`
	Label       string   `json:"label,omitempty"`
	UUID        string   `json:"uuid,omitempty"`
	PartUUID    string   `json:"partUuid,omitempty"`
	MountPoints []string `json:"mountPoints,omitempty"`
}

// SMARTReport is the health of a disk, as smartctl reports it.
type SMARTReport struct {
	Supported bool `json:"supported"`
	Healthy   bool `json:"healthy"`
	// Protocol is ATA, NVMe or SCSI.
	Protocol     string `json:"protocol,omitempty"`
	Model        string `json:"model,omitempty"`
	Serial       string `json:"serial,omitempty"`
	Firmware     string `json:"firmware,omitempty"`
	TemperatureC int    `json:"temperatureC,omitempty"`
	PowerOnHours int    `json:"powerOnHours,omitempty"`
	PowerCycles  int    `json:"powerCycles,omitempty"`
	// ReallocatedSectors and PendingSectors are the ATA attributes that
	// most often warn of a failing disk.
	ReallocatedSectors int64 `json:"reallocatedSectors,omitempty"`
	PendingSectors     int64 `json:"pendingSectors,omitempty"`
	// NVMe health: wear in percent of the rated endurance, spare capacity
	// left, and error counts.
	PercentageUsed   int              `json:"percentageUsed,omitempty"`
	AvailableSpare   int              `json:"availableSpare,omitempty"`
	MediaErrors      int64            `json:"mediaErrors,omitempty"`
	UnsafeShutdowns  int64            `json:"unsafeShutdowns,omitempty"`
	CriticalWarning  int              `json:"criticalWarning,omitempty"`
	DataReadBytes    int64            `json:"dataReadBytes,omitempty"`
	DataWrittenBytes int64            `json:"dataWrittenBytes,omitempty"`
	SelfTest         *SMARTSelfTest   `json:"selfTest,omitempty"`
	Attributes       []SMARTAttribute `json:"attributes,omitempty"`
}

// SMARTSelfTest is the state of the latest self-test.
type SMARTSelfTest struct {
	// Status is running, passed, failed or aborted.
	Status string `json:"status" enum:"running,passed,failed,aborted"`
	// Type is short or long.
	Type             string `json:"type,omitempty"`
	RemainingPercent int    `json:"remainingPercent,omitempty"`
	// LifetimeHours is the disk's power-on hours when the test ran.
	LifetimeHours int `json:"lifetimeHours,omitempty"`
}

// SMARTAttribute is one SMART attribute.
type SMARTAttribute struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Value     int    `json:"value"`
	Worst     int    `json:"worst"`
	Threshold int    `json:"threshold"`
	Raw       int64  `json:"raw"`
	Failing   bool   `json:"failing"`
}

// PartitionSpec is a partition to create.
type PartitionSpec struct {
	Number int `json:"number"`
	// SizeBytes of zero uses the rest of the disk.
	SizeBytes int64 `json:"sizeBytes"`
	// Type is efi, linux, raid or swap.
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// FormatSpec is a filesystem to create.
type FormatSpec struct {
	// Type is btrfs, ext4, xfs, vfat or swap.
	Type string `json:"type"`
	// Devices lists the devices; only btrfs accepts more than one.
	Devices []string `json:"devices"`
	Label   string   `json:"label,omitempty"`
	// Profiles apply to btrfs.
	Profiles BtrfsProfiles `json:"profiles,omitzero"`
}
