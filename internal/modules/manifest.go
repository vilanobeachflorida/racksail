package modules

import (
	"bytes"
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/spec"
)

// Category groups modules on the modules page.
type Category string

// Categories.
const (
	CategoryCore       Category = "core"
	CategoryHosting    Category = "hosting"
	CategoryApps       Category = "apps"
	CategoryStorage    Category = "storage"
	CategoryOperations Category = "operations"
	CategoryHome       Category = "home"
	CategoryAssistant  Category = "assistant"
)

// Manifest is a module's manifest.yaml.
type Manifest struct {
	ID           string       `yaml:"id" json:"id" doc:"The module's id, also the package name under modules/ and the prefix of its kinds"`
	Name         string       `yaml:"name" json:"name" doc:"What the panel calls the module"`
	Description  string       `yaml:"description" json:"description" doc:"What the module does, in a sentence or two"`
	Version      string       `yaml:"version" json:"version" doc:"The module's version"`
	License      string       `yaml:"license" json:"license" doc:"The SPDX license expression of the module's code"`
	Category     Category     `yaml:"category" json:"category" enum:"core,hosting,apps,storage,operations,home,assistant" doc:"Where the modules page groups it"`
	Required     bool         `yaml:"required,omitempty" json:"required,omitempty" doc:"Whether the module is always on and can't be turned off"`
	Dependencies Dependencies `yaml:"dependencies" json:"dependencies" doc:"The modules it needs or works with"`
	Profiles     []Profile    `yaml:"profiles" json:"profiles" doc:"The setup profiles that turn the module on"`
	Hardware     Hardware     `yaml:"hardware" json:"hardware" doc:"What the server must have for the module to run"`
	Cost         Cost         `yaml:"cost" json:"cost" doc:"Its estimated resource use, shown before it is turned on"`
	Upstream     []Upstream   `yaml:"upstream" json:"upstream" doc:"The software it installs, only when something first needs it"`
	Ports        []Port       `yaml:"ports,omitempty" json:"ports,omitempty" doc:"The ports it opens in the firewall while it is active"`
	Services     []Service    `yaml:"services,omitempty" json:"services,omitempty" doc:"The services it runs while it is active"`
	Capabilities Capabilities `yaml:"capabilities,omitempty" json:"capabilities,omitzero" doc:"What it offers to other modules and needs from them"`
	// Settings is the JSON Schema of the module's settings.
	Settings    map[string]any `yaml:"settings,omitempty" json:"settings,omitempty" doc:"The JSON Schema of the module's settings"`
	Permissions []Permission   `yaml:"permissions,omitempty" json:"permissions,omitempty" doc:"The permissions it adds beyond those of its kinds' operations"`
	Hooks       []HookPoint    `yaml:"hooks,omitempty" json:"hooks,omitempty" doc:"The hook points it adds beyond those of its kinds' operations"`
	Recipes     []string       `yaml:"recipes,omitempty" json:"recipes,omitempty" doc:"The ids of the recipes the module ships"`
}

// Dependencies are the modules a module needs or works with.
type Dependencies struct {
	Requires    []string `yaml:"requires,omitempty" json:"requires,omitempty" doc:"Modules that must be on"`
	RequiresAny []string `yaml:"requiresAny,omitempty" json:"requiresAny,omitempty" doc:"Modules of which at least one must be on"`
	Uses        []string `yaml:"uses,omitempty" json:"uses,omitempty" doc:"Modules it works with when they are on"`
	Note        string   `yaml:"note,omitempty" json:"note,omitempty" doc:"Dependencies that can't be listed up front, in plain words"`
}

// Hardware is what the machine must have.
type Hardware struct {
	Arch        []string `yaml:"arch" json:"arch" doc:"The processor architectures it runs on, such as amd64 and arm64"`
	MinMemoryMB int      `yaml:"minMemoryMB,omitempty" json:"minMemoryMB,omitempty" doc:"The least memory the server must have, in MB"`
	MinDiskGB   int      `yaml:"minDiskGB,omitempty" json:"minDiskGB,omitempty" doc:"The least disk space the server must have, in GB"`
	// Features lists required hardware: virtualization, kvm, iommu, tpm or
	// uefi.
	Features []string `yaml:"features,omitempty" json:"features,omitempty" doc:"Hardware it can't run without: virtualization, kvm, iommu, tpm or uefi"`
	Notes    string   `yaml:"notes,omitempty" json:"notes,omitempty" doc:"More about what it needs, in plain words"`
}

// Cost is the estimated resource use shown before turning a module on.
type Cost struct {
	IdleMemoryMB   int    `yaml:"idleMemoryMB" json:"idleMemoryMB" doc:"Memory used while it is on but nothing needs it yet, in MB"`
	ActiveMemoryMB int    `yaml:"activeMemoryMB" json:"activeMemoryMB" doc:"Typical memory once its software runs, idle, in MB"`
	DiskMB         int    `yaml:"diskMB" json:"diskMB" doc:"Disk its software takes once installed, in MB"`
	Notes          string `yaml:"notes,omitempty" json:"notes,omitempty" doc:"More about what it costs, in plain words"`
}

// Upstream is software a module installs the first time something needs it.
type Upstream struct {
	ID       string         `yaml:"id" json:"id" doc:"The software's id in the manifest"`
	Name     string         `yaml:"name" json:"name" doc:"The software's name"`
	License  string         `yaml:"license" json:"license" doc:"The software's SPDX license expression"`
	Homepage string         `yaml:"homepage,omitempty" json:"homepage,omitempty" doc:"The software's home page"`
	Source   UpstreamSource `yaml:"source" json:"source" doc:"Where the software comes from"`
	// Install says when it is installed, in plain words.
	Install string `yaml:"install" json:"install" doc:"When it is installed, in plain words"`
	OptIn   bool   `yaml:"optIn,omitempty" json:"optIn,omitempty" doc:"Whether it is installed only when an admin asks for it"`
	// DownloadMB and DiskMB estimate its size where the package index can't
	// tell, as for vendor archives.
	DownloadMB int `yaml:"downloadMB,omitempty" json:"downloadMB,omitempty" doc:"About how much is downloaded to install it, in MB, for software whose size the package index can't tell"`
	DiskMB     int `yaml:"diskMB,omitempty" json:"diskMB,omitempty" doc:"About how much disk it takes once installed, in MB, for software whose size the package index can't tell"`
}

// Port is a port a module opens while it is active.
type Port struct {
	Port        int    `yaml:"port" json:"port" doc:"The port number"`
	Protocol    string `yaml:"protocol" json:"protocol" enum:"tcp,udp" doc:"tcp or udp"`
	Description string `yaml:"description" json:"description" doc:"What the port is for"`
}

// Service is a systemd unit a module runs while it is active.
type Service struct {
	Unit        string `yaml:"unit" json:"unit" doc:"The systemd unit, such as knot.service"`
	Description string `yaml:"description" json:"description" doc:"What the service does"`
}

// UpstreamSource says where upstream software comes from.
type UpstreamSource struct {
	// Type is debian, apt-repository, vendor-archive, racksail-build,
	// embedded or present.
	Type         string   `yaml:"type" json:"type" doc:"debian, apt-repository, vendor-archive, racksail-build, embedded or present"`
	Packages     []string `yaml:"packages,omitempty" json:"packages,omitempty" doc:"The Debian packages that install it"`
	Repository   string   `yaml:"repository,omitempty" json:"repository,omitempty" doc:"The apt repository the packages come from"`
	URL          string   `yaml:"url,omitempty" json:"url,omitempty" doc:"Where the software's releases or builds are published"`
	Verification string   `yaml:"verification,omitempty" json:"verification,omitempty" doc:"How a download is checked, such as against a checksum or a signature"`
}

// Capabilities are what a module offers to and needs from others.
type Capabilities struct {
	Provides []string `yaml:"provides,omitempty" json:"provides,omitempty" doc:"What the module offers other modules, such as backups.snapshots"`
	Needs    []string `yaml:"needs,omitempty" json:"needs,omitempty" doc:"What it needs other modules to provide, such as network.firewall"`
}

// Permission is a permission a module adds.
type Permission struct {
	Name        string `yaml:"name" json:"name" doc:"The permission's name, which starts with the module's id"`
	Description string `yaml:"description" json:"description" doc:"What the permission allows"`
}

// HookPoint is a hook point a module adds.
type HookPoint struct {
	Point       string `yaml:"point" json:"point" doc:"The hook point, such as system.knot.zone"`
	Description string `yaml:"description" json:"description" doc:"When it runs and what hooks there can do"`
}

// ParseManifest reads a manifest and checks it against
// spec/schemas/module.schema.json.
func ParseManifest(data []byte) (*Manifest, error) {
	if err := spec.ValidateYAML(spec.Module, data); err != nil {
		return nil, err
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("reading the manifest: %w", err)
	}
	return &m, nil
}

// Base gives a module its manifest. Module types embed it.
type Base struct {
	manifest *Manifest
	err      error
}

// NewBase parses an embedded manifest. A broken manifest is reported when
// the module is registered.
func NewBase(manifest []byte) Base {
	m, err := ParseManifest(manifest)
	return Base{manifest: m, err: err}
}

// Manifest implements Module.
func (b Base) Manifest() *Manifest { return b.manifest }

// ManifestErr reports a manifest that didn't parse.
func (b Base) ManifestErr() error { return b.err }
