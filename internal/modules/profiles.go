package modules

import (
	"context"
	"fmt"
	"slices"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Profile is a bundle of modules the setup wizard turns on.
type Profile string

// Profiles.
const (
	AllInOne           Profile = "all-in-one"
	WebHosting         Profile = "web-hosting"
	HomeServer         Profile = "home-server"
	VirtualizationHost Profile = "virtualization-host"
	MailServer         Profile = "mail-server"
	DNSServer          Profile = "dns-server"
	Minimal            Profile = "minimal"
)

// ProfileInfo describes a profile in the setup wizard.
type ProfileInfo struct {
	ID          Profile `json:"id" enum:"all-in-one,web-hosting,home-server,virtualization-host,mail-server,dns-server,minimal"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
}

// Profiles lists the profiles in the order the wizard shows them.
func Profiles() []ProfileInfo {
	return []ProfileInfo{
		{AllInOne, "All-in-one", "Websites, email, DNS, databases, apps, storage and virtual machines on one server."},
		{WebHosting, "Web hosting", "Websites, databases, email and DNS for you or your customers."},
		{HomeServer, "Home server", "Storage, shares, apps and virtual machines for a home or small office."},
		{VirtualizationHost, "Virtualization host", "Virtual machines and containers on your own hardware."},
		{MailServer, "Mail server", "Email with its own DNS."},
		{DNSServer, "DNS server", "Authoritative DNS for your domains."},
		{Minimal, "Minimal", "Only the network module. Turn on what you need later."},
	}
}

// ValidProfile reports whether p is a known profile.
func ValidProfile(p Profile) bool {
	return slices.ContainsFunc(Profiles(), func(i ProfileInfo) bool { return i.ID == p })
}

// HardwareProblems explains why a module can't run on a machine. An empty
// result means it can.
func HardwareProblems(man *Manifest, facts system.MachineFacts) []string {
	var out []string
	if len(man.Hardware.Arch) > 0 && facts.Arch != "" && !slices.Contains(man.Hardware.Arch, facts.Arch) {
		out = append(out, fmt.Sprintf("it runs on %v, this machine is %s", man.Hardware.Arch, facts.Arch))
	}
	if mem := int64(man.Hardware.MinMemoryMB) << 20; mem > 0 && facts.MemoryBytes > 0 && facts.MemoryBytes < mem {
		out = append(out, fmt.Sprintf("it needs %d MB of memory, this machine has %d MB", man.Hardware.MinMemoryMB, facts.MemoryBytes>>20))
	}
	if disk := int64(man.Hardware.MinDiskGB) << 30; disk > 0 {
		var total int64
		for _, d := range facts.Disks {
			total += d.SizeBytes
		}
		if total > 0 && total < disk {
			out = append(out, fmt.Sprintf("it needs %d GB of disk, this machine has %d GB", man.Hardware.MinDiskGB, total>>30))
		}
	}
	for _, f := range man.Hardware.Features {
		switch {
		case f == "virtualization" && !facts.CPU.Virtualization:
			out = append(out, "the processor has no hardware virtualization (VT-x or AMD-V), or it is turned off in the firmware")
		case f == "kvm" && !facts.KVM:
			out = append(out, "/dev/kvm isn't available")
		case f == "iommu" && !facts.IOMMU:
			out = append(out, "no IOMMU is active")
		case f == "tpm" && !facts.TPM:
			out = append(out, "there is no TPM")
		case f == "uefi" && !facts.Firmware.UEFI:
			out = append(out, "the machine didn't boot with UEFI")
		}
	}
	return out
}

// ProfileModules lists the modules a profile turns on, in an order where
// dependencies come first: the required modules and those whose manifest
// names the profile.
func (m *Manager) ProfileModules(p Profile) []string {
	var ids []string
	for _, mod := range m.reg.All() {
		man := mod.Manifest()
		if man.Required || slices.Contains(man.Profiles, p) {
			ids = append(ids, man.ID)
		}
	}
	return dependencyOrder(m.reg, ids)
}

// Skipped is a module a profile would turn on but doesn't, with why.
type Skipped struct {
	Module string `json:"module"`
	Reason string `json:"reason"`
}

// ProfileRequest builds the change set that turns on a profile's modules:
// one turn_on for each that is off, dependencies first. Modules whose
// hardware requirements the machine doesn't meet, such as virtual machines
// without hardware virtualization, are left out and returned with why.
func (m *Manager) ProfileRequest(ctx context.Context, p Profile) (changeset.Request, []Skipped, error) {
	turnOn, skipped, err := m.profilePlan(ctx, p)
	if err != nil {
		return changeset.Request{}, skipped, err
	}
	if len(turnOn) == 0 {
		return changeset.Request{}, skipped, ErrNothingToTurnOn.New("profile", profileName(p))
	}
	req, err := m.turnOnRequest(ctx, turnOn)
	if err != nil {
		return changeset.Request{}, skipped, err
	}
	req.Title = "Turn on the " + profileName(p) + " profile"
	return req, skipped, nil
}

// ProfileSkipped returns the modules of a profile that ProfileRequest
// leaves out, with why, whichever are on: what a retry of turning the
// profile on answers with beside the first change set.
func (m *Manager) ProfileSkipped(ctx context.Context, p Profile) ([]Skipped, error) {
	_, skipped, err := m.profilePlan(ctx, p)
	return skipped, err
}

// profilePlan sorts a profile's modules into those to turn on, the ones
// that are off and can run here, and those the machine can't run.
func (m *Manager) profilePlan(ctx context.Context, p Profile) (turnOn []string, skipped []Skipped, err error) {
	if !ValidProfile(p) {
		return nil, nil, ErrUnknownProfile.New("profile", string(p))
	}
	var facts system.MachineFacts
	if m.opts.Facts != nil {
		if facts, err = m.opts.Facts(ctx); err != nil {
			return nil, nil, err
		}
	}
	out := map[string]bool{}
	for _, id := range m.ProfileModules(p) {
		mod, _ := m.reg.Get(id)
		man := mod.Manifest()
		if problems := HardwareProblems(man, facts); len(problems) > 0 {
			skipped = append(skipped, Skipped{Module: id, Reason: problems[0]})
			out[id] = true
			continue
		}
		missing := slices.ContainsFunc(man.Dependencies.Requires, func(dep string) bool { return out[dep] })
		if missing {
			skipped = append(skipped, Skipped{Module: id, Reason: "a module it needs can't run here"})
			out[id] = true
			continue
		}
		if m.Enabled(id) {
			continue
		}
		turnOn = append(turnOn, id)
	}
	return turnOn, skipped, nil
}

func profileName(p Profile) string {
	for _, info := range Profiles() {
		if info.ID == p {
			return info.Name
		}
	}
	return string(p)
}
