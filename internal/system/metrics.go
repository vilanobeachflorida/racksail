package system

import (
	"context"
	"time"
)

// Metrics reads resource use. Counters only grow; callers compute rates
// from two samples, so nothing is averaged here.
type Metrics interface {
	// System reports the whole machine.
	System(ctx context.Context) (SystemMetrics, error)
	// Units reports the units named, from their control groups. Units that
	// aren't running are left out.
	Units(ctx context.Context, names ...string) ([]UnitMetrics, error)
}

// SystemMetrics is one sample of the machine's resource use.
type SystemMetrics struct {
	CollectedAt time.Time     `json:"collectedAt"`
	Uptime      time.Duration `json:"uptime"`
	CPUs        int           `json:"cpus"`
	Load        LoadAverage   `json:"load"`
	CPU         CPUTimes      `json:"cpu" doc:"CPU time spent since boot, across all CPUs"`
	Memory      MemoryStats   `json:"memory"`
	Pressure    Pressure      `json:"pressure" doc:"Pressure stall information"`
	Disks       []DiskIO      `json:"disks,omitempty"`
	Networks    []NetIO       `json:"networks,omitempty"`
	Filesystems []FSUsage     `json:"filesystems,omitempty"`
}

// LoadAverage is the run queue length averaged over 1, 5 and 15 minutes.
type LoadAverage struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// CPUTimes are cumulative CPU times.
type CPUTimes struct {
	User    time.Duration `json:"user"`
	Nice    time.Duration `json:"nice"`
	System  time.Duration `json:"system"`
	Idle    time.Duration `json:"idle"`
	IOWait  time.Duration `json:"ioWait"`
	IRQ     time.Duration `json:"irq"`
	SoftIRQ time.Duration `json:"softIrq"`
	Steal   time.Duration `json:"steal"`
}

// Busy is the time not spent idle or waiting for I/O.
func (c CPUTimes) Busy() time.Duration {
	return c.User + c.Nice + c.System + c.IRQ + c.SoftIRQ + c.Steal
}

// Total is all the time accounted for.
func (c CPUTimes) Total() time.Duration { return c.Busy() + c.Idle + c.IOWait }

// MemoryStats describes memory and swap.
type MemoryStats struct {
	TotalBytes     int64 `json:"totalBytes"`
	AvailableBytes int64 `json:"availableBytes"`
	FreeBytes      int64 `json:"freeBytes"`
	BuffersBytes   int64 `json:"buffersBytes"`
	CachedBytes    int64 `json:"cachedBytes"`
	SwapTotalBytes int64 `json:"swapTotalBytes"`
	SwapFreeBytes  int64 `json:"swapFreeBytes"`
}

// UsedBytes is memory that can't be reclaimed.
func (m MemoryStats) UsedBytes() int64 { return m.TotalBytes - m.AvailableBytes }

// Pressure is the pressure stall information for CPU, memory and I/O.
type Pressure struct {
	CPU    PressureStats `json:"cpu"`
	Memory PressureStats `json:"memory"`
	IO     PressureStats `json:"io"`
}

// PressureStats are the share of time some or all tasks were stalled, in
// percent, averaged over 10, 60 and 300 seconds.
type PressureStats struct {
	Some10  float64 `json:"some10"`
	Some60  float64 `json:"some60"`
	Some300 float64 `json:"some300"`
	Full10  float64 `json:"full10"`
	Full60  float64 `json:"full60"`
	Full300 float64 `json:"full300"`
}

// DiskIO are cumulative I/O counters of a block device.
type DiskIO struct {
	Name       string        `json:"name"`
	ReadBytes  int64         `json:"readBytes"`
	WriteBytes int64         `json:"writeBytes"`
	ReadOps    int64         `json:"readOps"`
	WriteOps   int64         `json:"writeOps"`
	BusyTime   time.Duration `json:"busyTime" doc:"Time the device had I/O in flight"`
}

// NetIO are cumulative counters of a network interface.
type NetIO struct {
	Interface string `json:"interface"`
	RxBytes   int64  `json:"rxBytes"`
	TxBytes   int64  `json:"txBytes"`
	RxPackets int64  `json:"rxPackets"`
	TxPackets int64  `json:"txPackets"`
	RxErrors  int64  `json:"rxErrors"`
	TxErrors  int64  `json:"txErrors"`
	RxDropped int64  `json:"rxDropped"`
	TxDropped int64  `json:"txDropped"`
}

// FSUsage is the space and inode use of a mounted filesystem.
type FSUsage struct {
	Mount       string `json:"mount"`
	Device      string `json:"device"`
	FSType      string `json:"fsType"`
	SizeBytes   int64  `json:"sizeBytes"`
	UsedBytes   int64  `json:"usedBytes"`
	FreeBytes   int64  `json:"freeBytes" doc:"Space available to unprivileged users"`
	InodesTotal int64  `json:"inodesTotal"`
	InodesFree  int64  `json:"inodesFree"`
}

// UnitMetrics is one sample of a unit's resource use, from its control
// group. Slices include everything below them.
type UnitMetrics struct {
	Unit            string        `json:"unit"`
	CPUUsage        time.Duration `json:"cpuUsage" doc:"CPU time used since the control group was created"`
	MemoryBytes     int64         `json:"memoryBytes"`
	MemoryPeakBytes int64         `json:"memoryPeakBytes,omitempty"`
	SwapBytes       int64         `json:"swapBytes,omitempty"`
	IOReadBytes     int64         `json:"ioReadBytes"`
	IOWriteBytes    int64         `json:"ioWriteBytes"`
	Tasks           int           `json:"tasks"`
	Pressure        Pressure      `json:"pressure"`
}

// RealFilesystem reports whether a filesystem type holds data on a device,
// as opposed to kernel and memory filesystems.
func RealFilesystem(fsType string) bool {
	switch fsType {
	case "btrfs", "ext4", "ext3", "ext2", "xfs", "vfat", "exfat", "zfs", "f2fs", "nfs", "nfs4", "cifs", "fuse.mergerfs":
		return true
	}
	return false
}
