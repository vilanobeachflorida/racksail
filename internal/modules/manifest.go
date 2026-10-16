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
	ID           string       `yaml:"id" json:"id"`
	Name         string       `yaml:"name" json:"name"`
	Description  string       `yaml:"description" json:"description"`
	Version      string       `yaml:"version" json:"version"`
	License      string       `yaml:"license" json:"license"`
	Category     Category     `yaml:"category" json:"category" enum:"core,hosting,apps,storage,operations,home,assistant"`
	Required     bool         `yaml:"required,omitempty" json:"required,omitempty"`
	Dependencies Dependencies `yaml:"dependencies" json:"dependencies"`
	Profiles     []Profile    `yaml:"profiles" json:"profiles"`
	Hardware     Hardware     `yaml:"hardware" json:"hardware"`
	Cost         Cost         `yaml:"cost" json:"cost"`
	Upstream     []Upstream   `yaml:"upstream" json:"upstream"`
	Ports        []Port       `yaml:"ports,omitempty" json:"ports,omitempty"`
	Services     []Service    `yaml:"services,omitempty" json:"services,omitempty"`
	Capabilities Capabilities `yaml:"capabilities,omitempty" json:"capabilities,omitzero"`
	// Settings is the JSON Schema of the module's settings.
	Settings    map[string]any `yaml:"settings,omitempty" json:"settings,omitempty"`
	Permissions []Permission   `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Hooks       []HookPoint    `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Recipes     []string       `yaml:"recipes,omitempty" json:"recipes,omitempty"`
}

// Dependencies are the modules a module needs or works with.
type Dependencies struct {
	Requires    []string `yaml:"requires,omitempty" json:"requires,omitempty"`
	RequiresAny []string `yaml:"requiresAny,omitempty" json:"requiresAny,omitempty"`
	Uses        []string `yaml:"uses,omitempty" json:"uses,omitempty"`
	Note        string   `yaml:"note,omitempty" json:"note,omitempty"`
}

// Hardware is what the machine must have.
type Hardware struct {
	Arch        []string `yaml:"arch" json:"arch"`
	MinMemoryMB int      `yaml:"minMemoryMB,omitempty" json:"minMemoryMB,omitempty"`
	MinDiskGB   int      `yaml:"minDiskGB,omitempty" json:"minDiskGB,omitempty"`
	// Features lists required hardware: virtualization, kvm, iommu, tpm or
	// uefi.
	Features []string `yaml:"features,omitempty" json:"features,omitempty"`
	Notes    string   `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Cost is the estimated resource use shown before turning a module on.
type Cost struct {
	IdleMemoryMB   int    `yaml:"idleMemoryMB" json:"idleMemoryMB"`
	ActiveMemoryMB int    `yaml:"activeMemoryMB" json:"activeMemoryMB"`
	DiskMB         int    `yaml:"diskMB" json:"diskMB"`
	Notes          string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Upstream is software a module installs the first time something needs it.
type Upstream struct {
	ID       string         `yaml:"id" json:"id"`
	Name     string         `yaml:"name" json:"name"`
	License  string         `yaml:"license" json:"license"`
	Homepage string         `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Source   UpstreamSource `yaml:"source" json:"source"`
	// Install says when it is installed, in plain words.
	Install string `yaml:"install" json:"install"`
	OptIn   bool   `yaml:"optIn,omitempty" json:"optIn,omitempty"`
	// DownloadMB and DiskMB estimate its size where the package index can't
	// tell, as for vendor archives.
	DownloadMB int `yaml:"downloadMB,omitempty" json:"downloadMB,omitempty"`
	DiskMB     int `yaml:"diskMB,omitempty" json:"diskMB,omitempty"`
}

// Port is a port a module opens while it is active.
type Port struct {
	Port        int    `yaml:"port" json:"port"`
	Protocol    string `yaml:"protocol" json:"protocol" enum:"tcp,udp"`
	Description string `yaml:"description" json:"description"`
}

// Service is a systemd unit a module runs while it is active.
type Service struct {
	Unit        string `yaml:"unit" json:"unit"`
	Description string `yaml:"description" json:"description"`
}

// UpstreamSource says where upstream software comes from.
type UpstreamSource struct {
	// Type is debian, apt-repository, vendor-archive, racksail-build,
	// embedded or present.
	Type         string   `yaml:"type" json:"type"`
	Packages     []string `yaml:"packages,omitempty" json:"packages,omitempty"`
	Repository   string   `yaml:"repository,omitempty" json:"repository,omitempty"`
	URL          string   `yaml:"url,omitempty" json:"url,omitempty"`
	Verification string   `yaml:"verification,omitempty" json:"verification,omitempty"`
}

// Capabilities are what a module offers to and needs from others.
type Capabilities struct {
	Provides []string `yaml:"provides,omitempty" json:"provides,omitempty"`
	Needs    []string `yaml:"needs,omitempty" json:"needs,omitempty"`
}

// Permission is a permission a module adds.
type Permission struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
}

// HookPoint is a hook point a module adds.
type HookPoint struct {
	Point       string `yaml:"point" json:"point"`
	Description string `yaml:"description" json:"description"`
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
