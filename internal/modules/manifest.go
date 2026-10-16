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
	ID           string       `yaml:"id" json:"id" doc:"The module's id, which names its kinds and paths, such as web"`
	Name         string       `yaml:"name" json:"name" doc:"What the module is called"`
	Description  string       `yaml:"description" json:"description" doc:"What the module does"`
	Version      string       `yaml:"version" json:"version" doc:"The module's version"`
	License      string       `yaml:"license" json:"license" doc:"The module's license, as an SPDX expression"`
	Category     Category     `yaml:"category" json:"category" enum:"core,hosting,apps,storage,operations,home,assistant" doc:"Where the modules page lists it"`
	Required     bool         `yaml:"required,omitempty" json:"required,omitempty" doc:"Whether the module is always on"`
	Dependencies Dependencies `yaml:"dependencies" json:"dependencies" doc:"The modules it needs or uses"`
	Profiles     []Profile    `yaml:"profiles" json:"profiles" doc:"The profiles that turn it on"`
	Hardware     Hardware     `yaml:"hardware" json:"hardware" doc:"What the machine needs to run it"`
	Cost         Cost         `yaml:"cost" json:"cost" doc:"The memory and disk it takes"`
	Upstream     []Upstream   `yaml:"upstream" json:"upstream" doc:"The software it installs, and where from"`
	Ports        []Port       `yaml:"ports,omitempty" json:"ports,omitempty" doc:"The ports it opens"`
	Services     []Service    `yaml:"services,omitempty" json:"services,omitempty" doc:"The systemd services it runs"`
	Capabilities Capabilities `yaml:"capabilities,omitempty" json:"capabilities,omitzero" doc:"The capabilities it provides and needs"`
	// Settings is the JSON Schema of the module's settings.
	Settings    map[string]any `yaml:"settings,omitempty" json:"settings,omitempty" doc:"The JSON Schema of the module's settings"`
	Permissions []Permission   `yaml:"permissions,omitempty" json:"permissions,omitempty" doc:"The permissions it adds, beyond those of its kinds"`
	Hooks       []HookPoint    `yaml:"hooks,omitempty" json:"hooks,omitempty" doc:"The hook points it adds"`
	Recipes     []string       `yaml:"recipes,omitempty" json:"recipes,omitempty" doc:"The recipes it ships, which install like catalog items"`
}

// Dependencies are the modules a module needs or works with.
type Dependencies struct {
	Requires    []string `yaml:"requires,omitempty" json:"requires,omitempty" doc:"Modules that must be on for it to work"`
	RequiresAny []string `yaml:"requiresAny,omitempty" json:"requiresAny,omitempty" doc:"Modules of which at least one must be on"`
	Uses        []string `yaml:"uses,omitempty" json:"uses,omitempty" doc:"Modules it works with when they are on"`
	Note        string   `yaml:"note,omitempty" json:"note,omitempty" doc:"What the dependencies mean, in plain words"`
}

// Hardware is what the machine must have.
type Hardware struct {
	Arch        []string `yaml:"arch" json:"arch" doc:"The processor architectures it runs on"`
	MinMemoryMB int      `yaml:"minMemoryMB,omitempty" json:"minMemoryMB,omitempty" doc:"The memory it needs at least, in MB"`
	MinDiskGB   int      `yaml:"minDiskGB,omitempty" json:"minDiskGB,omitempty" doc:"The disk it needs at least, in GB"`
	// Features lists required hardware: virtualization, kvm, iommu, tpm or
	// uefi.
	Features []string `yaml:"features,omitempty" json:"features,omitempty" doc:"Hardware it needs: virtualization, kvm, iommu, tpm or uefi"`
	Notes    string   `yaml:"notes,omitempty" json:"notes,omitempty" doc:"What the requirements mean, in plain words"`
}

// Cost is the estimated resource use shown before turning a module on.
type Cost struct {
	IdleMemoryMB   int    `yaml:"idleMemoryMB" json:"idleMemoryMB" doc:"The memory it takes while nothing uses it, in MB"`
	ActiveMemoryMB int    `yaml:"activeMemoryMB" json:"activeMemoryMB" doc:"The memory it takes while in use, in MB"`
	DiskMB         int    `yaml:"diskMB" json:"diskMB" doc:"The disk it takes, in MB"`
	Notes          string `yaml:"notes,omitempty" json:"notes,omitempty" doc:"What the costs depend on, in plain words"`
}

// Upstream is software a module installs the first time something needs it.
type Upstream struct {
	ID       string         `yaml:"id" json:"id" doc:"The software's id in the manifest"`
	Name     string         `yaml:"name" json:"name" doc:"What the software is called"`
	License  string         `yaml:"license" json:"license" doc:"The software's license, as an SPDX expression"`
	Homepage string         `yaml:"homepage,omitempty" json:"homepage,omitempty" doc:"The software's own site"`
	Source   UpstreamSource `yaml:"source" json:"source" doc:"Where it comes from and how it is checked"`
	// Install says when it is installed, in plain words.
	Install string `yaml:"install" json:"install" doc:"When it is installed, in plain words"`
	OptIn   bool   `yaml:"optIn,omitempty" json:"optIn,omitempty" doc:"Whether it is only installed once someone chooses to"`
	// DownloadMB and DiskMB estimate its size where the package index can't
	// tell, as for vendor archives.
	DownloadMB int `yaml:"downloadMB,omitempty" json:"downloadMB,omitempty" doc:"How much it downloads, in MB, where the package index can't tell"`
	DiskMB     int `yaml:"diskMB,omitempty" json:"diskMB,omitempty" doc:"How much disk it takes, in MB, where the package index can't tell"`
}

// Port is a port a module opens while it is active.
type Port struct {
	Port        int    `yaml:"port" json:"port" doc:"The port number"`
	Protocol    string `yaml:"protocol" json:"protocol" enum:"tcp,udp" doc:"tcp or udp"`
	Description string `yaml:"description" json:"description" doc:"What listens there"`
}

// Service is a systemd unit a module runs while it is active.
type Service struct {
	Unit        string `yaml:"unit" json:"unit" doc:"The systemd unit"`
	Description string `yaml:"description" json:"description" doc:"What the service does"`
}

// UpstreamSource says where upstream software comes from.
type UpstreamSource struct {
	// Type is debian, apt-repository, vendor-archive, racksail-build,
	// embedded or present.
	Type         string   `yaml:"type" json:"type" doc:"debian, apt-repository, vendor-archive, racksail-build, embedded or present"`
	Packages     []string `yaml:"packages,omitempty" json:"packages,omitempty" doc:"The Debian packages it installs"`
	Repository   string   `yaml:"repository,omitempty" json:"repository,omitempty" doc:"The apt repository it comes from"`
	URL          string   `yaml:"url,omitempty" json:"url,omitempty" doc:"Where an archive is downloaded from"`
	Verification string   `yaml:"verification,omitempty" json:"verification,omitempty" doc:"How a download is checked, such as its checksum or signature"`
}

// Capabilities are what a module offers to and needs from others.
type Capabilities struct {
	Provides []string `yaml:"provides,omitempty" json:"provides,omitempty" doc:"Capabilities it provides, which other modules may need"`
	Needs    []string `yaml:"needs,omitempty" json:"needs,omitempty" doc:"Capabilities it needs another module or add-on to provide"`
}

// Permission is a permission a module adds.
type Permission struct {
	Name        string `yaml:"name" json:"name" doc:"The permission's name, <module>.<name>"`
	Description string `yaml:"description" json:"description" doc:"What it lets someone do"`
}

// HookPoint is a hook point a module adds.
type HookPoint struct {
	Point       string `yaml:"point" json:"point" doc:"The hook point"`
	Description string `yaml:"description" json:"description" doc:"What happens at the point"`
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
