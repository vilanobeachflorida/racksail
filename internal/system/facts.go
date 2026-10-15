package system

import (
	"context"
	"slices"
	"time"
)

// Facts reports what the machine is.
type Facts interface {
	Facts(ctx context.Context) (MachineFacts, error)
}

// MachineFacts describes a machine: its identity, operating system and the
// hardware that decides which modules can run on it.
type MachineFacts struct {
	Hostname  string    `json:"hostname"`
	FQDN      string    `json:"fqdn,omitempty"`
	MachineID string    `json:"machineId,omitempty"`
	BootID    string    `json:"bootId,omitempty"`
	OS        OSRelease `json:"os"`
	Kernel    string    `json:"kernel"`
	// Arch uses Go's names: amd64 or arm64.
	Arch        string   `json:"arch"`
	CPU         CPUInfo  `json:"cpu"`
	MemoryBytes int64    `json:"memoryBytes"`
	SwapBytes   int64    `json:"swapBytes"`
	Firmware    Firmware `json:"firmware"`
	TPM         bool     `json:"tpm" doc:"A TPM 2.0 device is present"`
	// TPMVersion is the major version the TPM reports, such as "2".
	TPMVersion string `json:"tpmVersion,omitempty" doc:"Major version of the TPM, such as 2"`
	// Virtualization is empty on bare metal and names the hypervisor
	// otherwise, as systemd-detect-virt reports it.
	Virtualization string `json:"virtualization,omitempty"`
	// KVM is true when /dev/kvm is usable, so virtual machines can run.
	KVM   bool `json:"kvm"`
	IOMMU bool `json:"iommu" doc:"An IOMMU is active, so devices can be passed through to VMs"`
	// Disks and NICs list the physical block devices and network
	// interfaces, without partitions, loop devices or virtual links.
	Disks []DiskFact `json:"disks,omitempty" doc:"Physical disks"`
	NICs  []NICFact  `json:"nics,omitempty" doc:"Network interfaces backed by a device"`
	// Timezone should be UTC on every Racksail server.
	Timezone    string    `json:"timezone"`
	BootTime    time.Time `json:"bootTime"`
	CollectedAt time.Time `json:"collectedAt"`
}

// OSRelease is the subset of /etc/os-release Racksail uses.
type OSRelease struct {
	ID              string `json:"id"`
	VersionID       string `json:"versionId"`
	VersionCodename string `json:"versionCodename,omitempty"`
	PrettyName      string `json:"prettyName,omitempty"`
}

// IsDebian13 reports whether the OS is the one Racksail supports.
func (o OSRelease) IsDebian13() bool { return o.ID == "debian" && o.VersionID == "13" }

// CPUInfo describes the processors.
type CPUInfo struct {
	Model   string `json:"model"`
	Vendor  string `json:"vendor,omitempty" doc:"GenuineIntel, AuthenticAMD or the arm64 implementer"`
	Sockets int    `json:"sockets,omitempty"`
	Cores   int    `json:"cores"`
	Threads int    `json:"threads"`
	// Flags are the feature flags the kernel reports: "flags" on amd64 and
	// "Features" on arm64.
	Flags []string `json:"flags,omitempty" doc:"Feature flags such as vmx, svm, aes or avx2"`
	// Virtualization reports hardware virtualization support (VT-x, AMD-V or
	// the arm64 equivalent), whether or not KVM is currently usable.
	Virtualization bool `json:"virtualization"`
	// VirtualizationType names the extension: vmx, svm or arm64.
	VirtualizationType string `json:"virtualizationType,omitempty" doc:"vmx (Intel VT-x), svm (AMD-V) or arm64"`
}

// HasFlag reports whether the CPU advertises a feature flag.
func (c CPUInfo) HasFlag(flag string) bool { return slices.Contains(c.Flags, flag) }

// Firmware describes how the machine booted.
type Firmware struct {
	UEFI       bool `json:"uefi"`
	SecureBoot bool `json:"secureBoot"`
}

// DiskFact summarizes a disk for placement and hardware checks. The storage
// module's inventory (Disks.List) has the details.
type DiskFact struct {
	Name       string `json:"name" doc:"Kernel name, such as sda or nvme0n1"`
	Model      string `json:"model,omitempty"`
	Serial     string `json:"serial,omitempty"`
	SizeBytes  int64  `json:"sizeBytes"`
	Rotational bool   `json:"rotational"`
	Removable  bool   `json:"removable"`
	Transport  string `json:"transport,omitempty" doc:"sata, nvme, usb, sas, virtio or scsi"`
}

// NICFact summarizes a network interface backed by a device.
type NICFact struct {
	Name      string `json:"name"`
	MAC       string `json:"mac,omitempty"`
	Driver    string `json:"driver,omitempty"`
	SpeedMbps int    `json:"speedMbps,omitempty"`
	Up        bool   `json:"up"`
}

// VirtualizationFlag returns the feature flag that means hardware
// virtualization on amd64, or "" when there is none.
func VirtualizationFlag(flags []string) string {
	for _, f := range []string{"vmx", "svm"} {
		if slices.Contains(flags, f) {
			return f
		}
	}
	return ""
}
