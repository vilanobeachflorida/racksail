package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Machine describes the virtual machine a scenario runs on.
type Machine struct {
	Arch string // amd64 or arm64
	// Firmware is bios (amd64 only), uefi or secureboot.
	Firmware string
	// Accel is kvm, hvf or tcg.
	Accel    string
	CPUs     int
	MemoryMB int
	// Disks are qcow2 images, attached on DiskBus: virtio, sata or nvme.
	Disks   []string
	DiskBus string
	// FirmwareCode and FirmwareVars are the UEFI images; Vars is the
	// machine's own writable copy.
	FirmwareCode, FirmwareVars string
	// PanelPort and SSHPort are forwarded from 127.0.0.1 to the guest's
	// panel and SSH server.
	PanelPort, SSHPort int
	SerialLog          string
}

// Media is what is plugged in for one boot.
type Media struct {
	// ISO is the installer image, as a CD-ROM that boots first.
	ISO string
	// Answers is a raw image of a USB stick carrying the answers file.
	Answers string
	// Seed is a raw image of a cloud-init NoCloud seed.
	Seed string
	// NoReboot makes QEMU exit when the guest reboots, which is how an
	// unattended install ends.
	NoReboot bool
}

// Binary is the QEMU program for the machine's architecture.
func (m Machine) Binary() string {
	if m.Arch == "arm64" {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

// Check refuses combinations QEMU or the firmware can't boot.
func (m Machine) Check() error {
	switch m.Arch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("the architecture is amd64 or arm64, not %q", m.Arch)
	}
	switch m.Firmware {
	case "bios":
		if m.Arch != "amd64" {
			return fmt.Errorf("%s machines boot with UEFI only", m.Arch)
		}
	case "uefi", "secureboot":
		if m.FirmwareCode == "" || m.FirmwareVars == "" {
			return fmt.Errorf("%s needs the firmware's code and variables images", m.Firmware)
		}
	default:
		return fmt.Errorf("the firmware is bios, uefi or secureboot, not %q", m.Firmware)
	}
	switch m.Accel {
	case "kvm", "hvf", "tcg":
	default:
		return fmt.Errorf("the accelerator is kvm, hvf or tcg, not %q", m.Accel)
	}
	switch m.DiskBus {
	case "virtio", "sata", "nvme":
	default:
		return fmt.Errorf("the disk bus is virtio, sata or nvme, not %q", m.DiskBus)
	}
	if len(m.Disks) == 0 || len(m.Disks) > 4 {
		return fmt.Errorf("a machine has from 1 to 4 disks, not %d", len(m.Disks))
	}
	return nil
}

// DiskSerial names a disk so the guest sees a stable path for it, whatever
// order devices are found in.
func DiskSerial(i int) string { return "racksail" + strconv.Itoa(i) }

// DevicePaths are the paths the guest sees the disks at, under
// /dev/disk/by-id, which answers files use so a USB stick or a CD-ROM can't
// take a disk's name.
func (m Machine) DevicePaths() []string {
	var out []string
	for i := range m.Disks {
		serial := DiskSerial(i)
		switch m.DiskBus {
		case "virtio":
			out = append(out, "/dev/disk/by-id/virtio-"+serial)
		case "sata":
			out = append(out, "/dev/disk/by-id/ata-QEMU_HARDDISK_"+serial)
		case "nvme":
			out = append(out, "/dev/disk/by-id/nvme-QEMU_NVMe_Ctrl_"+serial)
		}
	}
	return out
}

// Args is the QEMU command line for one boot of the machine with media.
// Defaults are off, so the machine has exactly the devices listed here.
func (m Machine) Args(media Media) []string {
	args := []string{"-name", "racksail-e2e", "-nodefaults", "-no-user-config", "-display", "none", "-monitor", "none"}

	machine := "q35"
	if m.Arch == "arm64" {
		machine = "virt,gic-version=max"
	}
	machine += ",accel=" + m.Accel
	secure := m.Firmware == "secureboot"
	if secure && m.Arch == "amd64" {
		// OVMF keeps Secure Boot's variables in SMM-protected flash.
		machine += ",smm=on"
	}
	cpu := "host"
	if m.Accel == "tcg" {
		cpu = "max"
	}
	args = append(args, "-machine", machine, "-cpu", cpu, "-smp", strconv.Itoa(m.CPUs), "-m", strconv.Itoa(m.MemoryMB), "-rtc", "base=utc")
	if secure && m.Arch == "amd64" {
		args = append(args, "-global", "driver=cfi.pflash01,property=secure,value=on")
	}
	if m.Firmware != "bios" {
		args = append(args,
			"-drive", "if=pflash,format=raw,unit=0,readonly=on,file="+m.FirmwareCode,
			"-drive", "if=pflash,format=raw,unit=1,file="+m.FirmwareVars)
	}

	if m.Arch == "arm64" {
		args = append(args, "-device", "virtio-gpu-pci")
	} else {
		args = append(args, "-device", "VGA")
	}
	args = append(args, "-device", "virtio-rng-pci", "-serial", "file:"+m.SerialLog)
	args = append(args,
		"-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp:127.0.0.1:%d-:8443,hostfwd=tcp:127.0.0.1:%d-:22", m.PanelPort, m.SSHPort),
		"-device", "virtio-net-pci,netdev=net0")

	if m.DiskBus == "sata" {
		args = append(args, "-device", "ahci,id=ahci")
	}
	for i, disk := range m.Disks {
		id := "disk" + strconv.Itoa(i)
		args = append(args, "-drive", "file="+disk+",if=none,id="+id+",format=qcow2,discard=unmap")
		device := ""
		switch m.DiskBus {
		case "virtio":
			device = "virtio-blk-pci,drive=" + id + ",serial=" + DiskSerial(i)
		case "sata":
			device = "ide-hd,drive=" + id + ",bus=ahci." + strconv.Itoa(i) + ",serial=" + DiskSerial(i)
		case "nvme":
			device = "nvme,drive=" + id + ",serial=" + DiskSerial(i)
		}
		if i == 0 && media.ISO == "" {
			device += ",bootindex=1"
		}
		args = append(args, "-device", device)
	}

	if media.ISO != "" {
		args = append(args,
			"-device", "virtio-scsi-pci,id=scsi0",
			"-drive", "file="+media.ISO+",if=none,id=cd0,media=cdrom,readonly=on,format=raw",
			"-device", "scsi-cd,drive=cd0,bus=scsi0.0,bootindex=0")
	}
	if media.Answers != "" {
		args = append(args,
			"-device", "qemu-xhci,id=xhci",
			"-drive", "file="+media.Answers+",if=none,id=answers,format=raw,readonly=on",
			"-device", "usb-storage,bus=xhci.0,drive=answers,removable=on")
	}
	if media.Seed != "" {
		args = append(args,
			"-drive", "file="+media.Seed+",if=none,id=seed,format=raw,readonly=on",
			"-device", "virtio-blk-pci,drive=seed")
	}
	if media.NoReboot {
		args = append(args, "-no-reboot")
	}
	return args
}

// firmwareCandidates lists where distributions and Homebrew put UEFI
// firmware for QEMU, as code and variables pairs, best first. The Secure
// Boot variables have Microsoft's keys enrolled, so Debian's shim boots.
var firmwareCandidates = map[string][][2]string{
	"amd64/uefi": {
		{"/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd"},
		{"/usr/share/OVMF/OVMF_CODE.fd", "/usr/share/OVMF/OVMF_VARS.fd"},
		{"/usr/share/edk2/ovmf/OVMF_CODE.fd", "/usr/share/edk2/ovmf/OVMF_VARS.fd"},
		{"/opt/homebrew/share/qemu/edk2-x86_64-code.fd", "/opt/homebrew/share/qemu/edk2-i386-vars.fd"},
		{"/usr/local/share/qemu/edk2-x86_64-code.fd", "/usr/local/share/qemu/edk2-i386-vars.fd"},
	},
	"amd64/secureboot": {
		{"/usr/share/OVMF/OVMF_CODE_4M.secboot.fd", "/usr/share/OVMF/OVMF_VARS_4M.ms.fd"},
		{"/usr/share/OVMF/OVMF_CODE.secboot.fd", "/usr/share/OVMF/OVMF_VARS.ms.fd"},
		{"/usr/share/edk2/ovmf/OVMF_CODE.secboot.fd", "/usr/share/edk2/ovmf/OVMF_VARS.secboot.fd"},
	},
	"arm64/uefi": {
		{"/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/AAVMF/AAVMF_VARS.fd"},
		{"/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw", "/usr/share/edk2/aarch64/vars-template-pflash.raw"},
		{"/opt/homebrew/share/qemu/edk2-aarch64-code.fd", "/opt/homebrew/share/qemu/edk2-arm-vars.fd"},
		{"/usr/local/share/qemu/edk2-aarch64-code.fd", "/usr/local/share/qemu/edk2-arm-vars.fd"},
	},
	"arm64/secureboot": {
		{"/usr/share/AAVMF/AAVMF_CODE.ms.fd", "/usr/share/AAVMF/AAVMF_VARS.ms.fd"},
	},
}

// FindFirmware picks the UEFI firmware for an architecture and firmware
// kind, from the first pair whose files both exist.
func FindFirmware(arch, firmware string, exists func(string) bool) (code, vars string, err error) {
	if firmware == "bios" {
		return "", "", nil
	}
	for _, pair := range firmwareCandidates[arch+"/"+firmware] {
		if exists(pair[0]) && exists(pair[1]) {
			return pair[0], pair[1], nil
		}
	}
	pkg := "ovmf"
	if arch == "arm64" {
		pkg = "qemu-efi-aarch64"
	}
	return "", "", fmt.Errorf("no %s firmware for %s found; install %s, or give -firmware-code and -firmware-vars", firmware, arch, pkg)
}

// PickAccel chooses the fastest accelerator that can run the guest: KVM on
// Linux and Hypervisor.framework on macOS when the guest has the host's
// architecture, and emulation otherwise.
func PickAccel(goos, hostArch, guestArch string, kvmUsable bool) string {
	if hostArch != guestArch {
		return "tcg"
	}
	switch {
	case goos == "linux" && kvmUsable:
		return "kvm"
	case goos == "darwin":
		return "hvf"
	}
	return "tcg"
}

// ArchOf guesses an image's architecture from its file name, as release
// images are named.
func ArchOf(name string) string {
	switch {
	case strings.Contains(name, "arm64"), strings.Contains(name, "aarch64"):
		return "arm64"
	case strings.Contains(name, "amd64"), strings.Contains(name, "x86_64"):
		return "amd64"
	}
	return ""
}
