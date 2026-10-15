package installer

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/spec"
)

// Mode is what the installer does.
type Mode string

// Modes.
const (
	// ModeInstall erases the chosen disks and installs from scratch.
	ModeInstall Mode = "install"
	// ModeConvert turns the running Debian 13 system into a Racksail server.
	ModeConvert Mode = "convert"
	// ModeRestore installs and then restores a backup.
	ModeRestore Mode = "restore"
)

// Answers are the answers to the installer's questions.
type Answers struct {
	Format   int      `yaml:"format" json:"format"`
	Mode     Mode     `yaml:"mode" json:"mode"`
	Locale   string   `yaml:"locale,omitempty" json:"locale,omitempty"`
	Keyboard string   `yaml:"keyboard,omitempty" json:"keyboard,omitempty"`
	Hostname string   `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	Mirror   string   `yaml:"mirror,omitempty" json:"mirror,omitempty"`
	Channel  string   `yaml:"channel,omitempty" json:"channel,omitempty"`
	Network  Network  `yaml:"network" json:"network"`
	Disks    Disks    `yaml:"disks" json:"disks"`
	Account  Account  `yaml:"account" json:"account"`
	Restore  *Restore `yaml:"restore,omitempty" json:"restore,omitempty"`
	Reboot   bool     `yaml:"reboot,omitempty" json:"reboot,omitempty"`
}

// Network answers the network question.
type Network struct {
	Mode      string   `yaml:"mode" json:"mode"`
	Interface string   `yaml:"interface,omitempty" json:"interface,omitempty"`
	Address   string   `yaml:"address,omitempty" json:"address,omitempty"`
	Gateway   string   `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	DNS       []string `yaml:"dns,omitempty" json:"dns,omitempty"`
	VLAN      int      `yaml:"vlan,omitempty" json:"vlan,omitempty"`
	IPv6      *IPv6    `yaml:"ipv6,omitempty" json:"ipv6,omitempty"`
}

// IPv6 configures IPv6.
type IPv6 struct {
	Mode    string `yaml:"mode" json:"mode"`
	Address string `yaml:"address,omitempty" json:"address,omitempty"`
	Gateway string `yaml:"gateway,omitempty" json:"gateway,omitempty"`
}

// Disks answers the disks question.
type Disks struct {
	Layout      string   `yaml:"layout" json:"layout"`
	Devices     []string `yaml:"devices,omitempty" json:"devices,omitempty"`
	KeepData    bool     `yaml:"keepData,omitempty" json:"keepData,omitempty"`
	Profile     string   `yaml:"profile,omitempty" json:"profile,omitempty"`
	ConfirmWipe bool     `yaml:"confirmWipe,omitempty" json:"confirmWipe,omitempty"`
}

// Account answers the account question.
type Account struct {
	Username     string   `yaml:"username" json:"username"`
	PasswordHash string   `yaml:"passwordHash,omitempty" json:"passwordHash,omitempty"`
	SSHKeys      []string `yaml:"sshKeys,omitempty" json:"sshKeys,omitempty"`
	GitHubKeys   string   `yaml:"githubKeys,omitempty" json:"githubKeys,omitempty"`
}

// Restore says which backup to restore.
type Restore struct {
	Repository string `yaml:"repository" json:"repository"`
	Snapshot   string `yaml:"snapshot,omitempty" json:"snapshot,omitempty"`
}

// ParseAnswers reads an answers file and checks it.
func ParseAnswers(data []byte) (*Answers, error) {
	if err := spec.ValidateYAML(spec.Answers, data); err != nil {
		return nil, err
	}
	var a Answers
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("reading the answers: %w", err)
	}
	if err := a.Check(); err != nil {
		return nil, err
	}
	return &a, nil
}

// Check enforces the rules the schema can't express.
func (a *Answers) Check() error {
	var errs []error
	if a.Network.Mode == "static" {
		prefix, err := netip.ParsePrefix(a.Network.Address)
		if err != nil || !prefix.Addr().Is4() {
			errs = append(errs, fmt.Errorf("network.address %q must be an IPv4 address with a prefix length, such as 203.0.113.10/24", a.Network.Address))
		}
		gw, err := netip.ParseAddr(a.Network.Gateway)
		if err != nil {
			errs = append(errs, fmt.Errorf("network.gateway %q isn't an address", a.Network.Gateway))
		} else if err == nil && prefix.IsValid() && !prefix.Masked().Contains(gw) {
			errs = append(errs, fmt.Errorf("the gateway %s isn't on the network %s", gw, prefix.Masked()))
		}
	}
	for _, d := range a.Network.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			errs = append(errs, fmt.Errorf("network.dns: %q isn't an address", d))
		}
	}
	if v6 := a.Network.IPv6; v6 != nil && v6.Mode == "static" {
		if p, err := netip.ParsePrefix(v6.Address); err != nil || !p.Addr().Is6() {
			errs = append(errs, fmt.Errorf("network.ipv6.address %q must be an IPv6 address with a prefix length", v6.Address))
		}
	}
	if a.Mode != ModeConvert {
		switch a.Disks.Layout {
		case "single":
			if len(a.Disks.Devices) > 1 {
				errs = append(errs, errors.New("the single layout takes one disk"))
			}
		case "mirror":
			if len(a.Disks.Devices) != 2 {
				errs = append(errs, errors.New("the mirror layout takes exactly two disks"))
			}
		case "custom":
			if len(a.Disks.Devices) == 0 || a.Disks.Profile == "" {
				errs = append(errs, errors.New("the custom layout needs devices and a profile"))
			}
		}
		for _, d := range a.Disks.Devices {
			if !strings.HasPrefix(d, "/dev/") {
				errs = append(errs, fmt.Errorf("disk %q must be a /dev path", d))
			}
		}
		if a.Mode == ModeInstall && !a.Disks.KeepData && !a.Disks.ConfirmWipe {
			errs = append(errs, errors.New("installing erases the chosen disks; set disks.confirmWipe to true to go ahead"))
		}
	}
	return errors.Join(errs...)
}
