package system

import (
	"context"
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
	// Virtualization is empty on bare metal and names the hypervisor
	// otherwise, as systemd-detect-virt reports it.
	Virtualization string `json:"virtualization,omitempty"`
	// KVM is true when /dev/kvm is usable, so virtual machines can run.
	KVM   bool `json:"kvm"`
	IOMMU bool `json:"iommu" doc:"An IOMMU is active, so devices can be passed through to VMs"`
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
	Cores   int    `json:"cores"`
	Threads int    `json:"threads"`
	// Virtualization reports hardware virtualization support (VT-x, AMD-V or
	// the arm64 equivalent), whether or not KVM is currently usable.
	Virtualization bool `json:"virtualization"`
}

// Firmware describes how the machine booted.
type Firmware struct {
	UEFI       bool `json:"uefi"`
	SecureBoot bool `json:"secureBoot"`
}
