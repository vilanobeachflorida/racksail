package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Install says what of a module's upstream software to install the first
// time something needs it.
type Install struct {
	// Packages are the Debian packages to install, for software whose
	// manifest names packages with patterns, such as php8.4-fpm for
	// php*-fpm. Each must match one of the manifest's names. When empty,
	// the packages the manifest names exactly are installed.
	Packages []string
	// Version and Archive are for vendor archives and Racksail's builds:
	// the archive to download, verified against its checksum or a vendor
	// signature, which is unpacked into the version's runtime directory.
	Version string
	Archive *system.Archive
}

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,62}$`)

// InstallSteps returns the steps that install one of a module's upstream
// software the first time a resource needs it, for the module's
// reconcilers to put in front of their own steps. Every step does nothing
// when the software is already there, so planning them again is harmless.
// Software marked opt-in in the manifest is only installed when the admin
// allowed it in the module's settings, which rd, the planned state of the
// reconcile request, shows.
func InstallSteps(ctx context.Context, rd store.Reader, man *Manifest, upstream string, in Install) ([]agent.Step, error) {
	i := slices.IndexFunc(man.Upstream, func(u Upstream) bool { return u.ID == upstream })
	if i < 0 {
		return nil, ErrNotUpstream.New("module", man.Name, "software", upstream)
	}
	u := man.Upstream[i]
	if u.OptIn {
		allowed, err := optedIn(ctx, rd, man.ID, u.ID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, ErrOptIn.New("software", u.Name, "module", man.Name)
		}
	}
	switch u.Source.Type {
	case "embedded", "present":
		return nil, nil
	case "debian", "apt-repository":
		names := in.Packages
		if len(names) == 0 {
			names = exactPackages(u)
		}
		if len(names) == 0 {
			return nil, ErrNotUpstream.New("module", man.Name, "software", u.Name+" without naming its packages")
		}
		var pkgs []system.PackageRequest
		for _, n := range names {
			if !packageAllowed(u, n) {
				return nil, ErrNotUpstream.New("module", man.Name, "software", n)
			}
			pkgs = append(pkgs, system.PackageRequest{Name: n})
		}
		var steps []agent.Step
		if u.Source.Type == "apt-repository" {
			steps = append(steps, agent.EnsureAptSource.Titled("Add the "+u.Name+" package repository", agent.SourceInput{Name: u.ID}))
		}
		return append(steps, agent.InstallPackages.Titled("Install "+u.Name, agent.InstallPackagesInput{Packages: pkgs})), nil
	case "vendor-archive", "racksail-build":
		if in.Archive == nil || !versionPattern.MatchString(in.Version) {
			return nil, ErrNotUpstream.New("module", man.Name, "software", u.Name+" without a version and an archive")
		}
		a := *in.Archive
		a.Dest = RuntimeDir(u.ID, in.Version)
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return []agent.Step{agent.InstallArchive.Titled("Install "+u.Name+" "+in.Version, agent.InstallArchiveInput{Archive: a})}, nil
	}
	return nil, ErrNotUpstream.New("module", man.Name, "software", u.Name)
}

// packageAllowed reports whether a package is one an upstream entry names,
// exactly or by a pattern.
func packageAllowed(u Upstream, name string) bool {
	for _, p := range u.Source.Packages {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// optedIn reports whether the admin allowed opt-in software of a module.
func optedIn(ctx context.Context, rd store.Reader, module, upstream string) (bool, error) {
	if rd == nil {
		return false, nil
	}
	objs, _, err := Kind.List(ctx, rd, store.Query{Index: map[string]string{"module": module}, Limit: 1})
	if err != nil || len(objs) == 0 {
		return false, err
	}
	return slices.Contains(objs[0].Spec.OptIn, upstream), nil
}

// Software is one of a module's upstream software, as the preview before
// turning the module on shows it: where it comes from, when it is
// installed, how much it downloads and takes on disk, and whether it is
// already there.
type Software struct {
	Upstream
	// Packages are the Debian packages installing it brings in, with their
	// sizes, for software the manifest names exactly.
	Packages []SoftwarePackage `json:"packages,omitempty" doc:"The Debian packages installing it brings in, with their sizes, for software the manifest names exactly"`
	// DownloadBytes and DiskBytes are what installing it takes: from the
	// package index when it can tell, from the manifest otherwise.
	DownloadBytes int64 `json:"downloadBytes,omitempty" doc:"How much installing it downloads, in bytes: from the package index when it can tell, from the manifest otherwise"`
	DiskBytes     int64 `json:"diskBytes,omitempty" doc:"How much disk it takes once installed, in bytes, from the package index or the manifest"`
	Installed     bool  `json:"installed" doc:"Whether it is installed already"`
}

// manifestSoftware is a module's software as its manifest estimates it,
// for when the server wasn't asked.
func manifestSoftware(man *Manifest) []Software {
	out := make([]Software, 0, len(man.Upstream))
	for _, u := range man.Upstream {
		out = append(out, Software{
			Upstream: u, DownloadBytes: int64(u.DownloadMB) << 20, DiskBytes: int64(u.DiskMB) << 20,
			Installed: u.Source.Type == "embedded" || u.Source.Type == "present",
		})
	}
	return out
}

// FoundSoftware is what the server last said of one of a module's
// software: whether it is there, and what installing it brings in.
type FoundSoftware struct {
	// ID is the software's id in the manifest.
	ID            string            `json:"id" doc:"The software's id in the module's manifest"`
	Installed     bool              `json:"installed" doc:"Whether the server had it installed"`
	Packages      []SoftwarePackage `json:"packages,omitempty" doc:"The Debian packages installing it brings in, with their sizes"`
	DownloadBytes int64             `json:"downloadBytes,omitempty" doc:"How much installing it downloads, in bytes"`
	DiskBytes     int64             `json:"diskBytes,omitempty" doc:"How much disk it takes once installed, in bytes"`
}

// knownSoftware is a module's software as its status says the server last
// found it, and as the manifest estimates what the server wasn't asked
// about. A plan reads it from the module it plans, so a preview and the
// apply that follows it plan the same, however long apart.
func knownSoftware(man *Manifest, st ModuleStatus) []Software {
	out := manifestSoftware(man)
	for i, sw := range out {
		j := slices.IndexFunc(st.Software, func(f FoundSoftware) bool { return f.ID == sw.ID })
		if j < 0 {
			continue
		}
		f := st.Software[j]
		out[i].Installed, out[i].Packages, out[i].DownloadBytes, out[i].DiskBytes = f.Installed, f.Packages, f.DownloadBytes, f.DiskBytes
	}
	return out
}

// SoftwarePackage is a Debian package installing a module's software
// brings in.
type SoftwarePackage struct {
	Name           string `json:"name" doc:"The package's name"`
	Version        string `json:"version" doc:"The version that would be installed"`
	DownloadBytes  int64  `json:"downloadBytes,omitempty" doc:"How much installing it downloads, in bytes"`
	InstalledBytes int64  `json:"installedBytes,omitempty" doc:"How much disk it takes once installed, in bytes"`
	// Origin is the repository the version comes from.
	Origin string `json:"origin,omitempty" doc:"The repository the version comes from"`
}

// FirstUse describes what a module installs when something first needs
// it, asking the agent about the packages it names exactly. What it finds
// is kept in the module's status, where previews of turning the module on
// read it, until it is asked again.
func (m *Manager) FirstUse(ctx context.Context, id string) ([]Software, error) {
	out, err := m.firstUse(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := m.recordSoftware(ctx, id, out); err != nil {
		return nil, err
	}
	return out, nil
}

// recordSoftware keeps what the server said of a module's software in the
// module's status.
func (m *Manager) recordSoftware(ctx context.Context, id string, software []Software) error {
	rid, ok := m.Resource(id)
	st := m.opts.Services.Store
	if !ok || st == nil {
		return nil
	}
	found := make([]FoundSoftware, 0, len(software))
	for _, sw := range software {
		found = append(found, FoundSoftware{ID: sw.ID, Installed: sw.Installed, Packages: sw.Packages, DownloadBytes: sw.DownloadBytes, DiskBytes: sw.DiskBytes})
	}
	err := st.Write(ctx, store.WriteMeta{Actor: "system:modules"}, func(tx store.Tx) error {
		obj, err := Kind.Get(ctx, tx, rid)
		if err != nil {
			return err
		}
		status := obj.Status
		status.Software = found
		raw, err := json.Marshal(status)
		if err != nil {
			return err
		}
		return tx.UpdateStatus(ctx, rid, raw)
	})
	if store.ErrNotFound.Is(err) {
		return nil
	}
	return err
}

func (m *Manager) firstUse(ctx context.Context, id string) ([]Software, error) {
	man, err := m.manifest(id)
	if err != nil {
		return nil, err
	}
	client := m.opts.Services.Agent
	out := make([]Software, 0, len(man.Upstream))
	for _, u := range man.Upstream {
		sw := Software{Upstream: u, DownloadBytes: int64(u.DownloadMB) << 20, DiskBytes: int64(u.DiskMB) << 20}
		switch u.Source.Type {
		case "embedded", "present":
			sw.Installed = true
		case "debian", "apt-repository":
			names := exactPackages(u)
			if len(names) == 0 || client == nil {
				break
			}
			q, err := agent.QueryPackages.Call(ctx, client, agent.QueryPackagesInput{Names: names}, agent.CallOptions{})
			if err != nil {
				return nil, err
			}
			sw.Installed = len(q.Installed) > 0 && len(q.Candidates) == 0
			if len(q.Candidates) > 0 {
				sw.DownloadBytes, sw.DiskBytes = 0, 0
				for _, p := range q.Candidates {
					sw.Packages = append(sw.Packages, SoftwarePackage{Name: p.Name, Version: p.Version, DownloadBytes: p.DownloadBytes, InstalledBytes: p.InstalledBytes, Origin: p.Origin})
					sw.DownloadBytes += p.DownloadBytes
					sw.DiskBytes += p.InstalledBytes
				}
			}
		case "vendor-archive", "racksail-build":
			if client == nil {
				break
			}
			info, err := agent.StatPath.Call(ctx, client, agent.PathInput{Path: RuntimeDir(u.ID, "")}, agent.CallOptions{})
			if err != nil {
				return nil, err
			}
			sw.Installed = info.Exists
		}
		out = append(out, sw)
	}
	return out, nil
}

// Actions of the effects a module's transitions list for what happens
// later rather than when the change applies: its software is installed the
// first time something needs it, and its services and ports run and open
// while something uses it.
const (
	ActionDownloadOnFirstUse = "download_on_first_use"
	ActionInstallOnFirstUse  = "install_on_first_use"
	ActionStartWhenUsed      = "start_when_used"
	ActionOpenWhenUsed       = "open_when_used"
	ActionRunWhenUsed        = "run_when_used"
)

// Later reports whether an effect of a module's transition happens later,
// once something uses the module, rather than when the change applies.
func Later(e system.Effect) bool {
	switch e.Action {
	case ActionDownloadOnFirstUse, ActionInstallOnFirstUse, ActionStartWhenUsed, ActionOpenWhenUsed, ActionRunWhenUsed:
		return true
	}
	return false
}

// sourceOf says where upstream software comes from, for the preview.
func sourceOf(u Upstream) string {
	switch u.Source.Type {
	case "debian":
		return "Debian's archive"
	case "apt-repository":
		if u.Source.Repository != "" {
			return u.Source.Repository
		}
		return "the vendor's package repository"
	case "racksail-build":
		if u.Source.URL != "" {
			return u.Source.URL
		}
		return "Racksail's builds"
	}
	return u.Source.URL
}

// turnOnEffects are what the preview of turning a module on lists: the
// module starting in the panel, with the memory it takes while idle, and
// what it does once something uses it: the software it downloads and
// installs then, from where and how large, the services it runs, the ports
// it opens, and the memory and disk it takes. What the software takes is
// what the server said when the change was requested, as the module's
// status keeps it, and the manifest's estimates otherwise.
func turnOnEffects(man *Manifest, st ModuleStatus) []system.Effect {
	effects := []system.Effect{{
		Type: system.EffectProcess, Action: "start", Target: "module " + man.ID, Detail: "in the panel",
		MemoryBytes: int64(man.Cost.IdleMemoryMB) << 20,
	}}
	var disk int64
	for _, sw := range knownSoftware(man, st) {
		if sw.Installed {
			continue
		}
		detail := sw.Install
		if sw.OptIn {
			detail = "only when allowed in the module's settings. " + detail
		}
		effects = append(effects, system.Effect{
			Type: system.EffectDownload, Action: ActionDownloadOnFirstUse, Target: sw.Name, Source: sourceOf(sw.Upstream), Detail: detail,
			DownloadBytes: sw.DownloadBytes, DiskBytes: sw.DiskBytes,
		})
		disk += sw.DiskBytes
		if len(sw.Packages) > 0 {
			for _, p := range sw.Packages {
				effects = append(effects, system.Effect{Type: system.EffectPackage, Action: ActionInstallOnFirstUse, Target: p.Name, Detail: p.Version})
			}
			continue
		}
		for _, name := range exactPackages(sw.Upstream) {
			effects = append(effects, system.Effect{Type: system.EffectPackage, Action: ActionInstallOnFirstUse, Target: name, Detail: "for " + sw.Name})
		}
	}
	for _, s := range man.Services {
		effects = append(effects, system.Effect{Type: system.EffectService, Action: ActionStartWhenUsed, Target: s.Unit, Detail: s.Description})
	}
	for _, p := range man.Ports {
		effects = append(effects, system.Effect{Type: system.EffectPort, Action: ActionOpenWhenUsed, Target: fmt.Sprintf("%s/%d", p.Protocol, p.Port), Detail: p.Description})
	}
	run := system.Effect{Type: system.EffectProcess, Action: ActionRunWhenUsed, Target: man.Name, Detail: "what it takes while something uses it"}
	if extra := man.Cost.ActiveMemoryMB - man.Cost.IdleMemoryMB; extra > 0 {
		run.MemoryBytes = int64(extra) << 20
	}
	// The manifest's estimate of its disk covers what the package index
	// doesn't, such as the data its software keeps.
	if rest := int64(man.Cost.DiskMB)<<20 - disk; rest > 0 {
		run.DiskBytes = rest
	}
	if run.MemoryBytes > 0 || run.DiskBytes > 0 {
		effects = append(effects, run)
	}
	return effects
}

// turnOffEffects are what turning a module off stops: the module in the
// panel and, when something uses it, the services it runs and the ports it
// opened, whose resources it keeps. Services and ports another module
// still uses stay.
func (m *Manager) turnOffEffects(man *Manifest, current State, shared Shared) []system.Effect {
	effects := []system.Effect{{Type: system.EffectProcess, Action: "stop", Target: "module " + man.ID, Detail: "in the panel"}}
	if current != StateActive {
		return effects
	}
	for _, s := range man.Services {
		if by, ok := shared.Unit(s.Unit); ok {
			effects = append(effects, kept(system.EffectService, s.Unit, by))
			continue
		}
		effects = append(effects, system.Effect{Type: system.EffectService, Action: "stop", Target: s.Unit, Detail: s.Description})
	}
	for _, p := range man.Ports {
		if by, ok := shared.Port(portName(p)); ok {
			effects = append(effects, kept(system.EffectPort, portName(p), by))
			continue
		}
		effects = append(effects, system.Effect{Type: system.EffectPort, Action: "close", Target: portName(p), Detail: p.Description})
	}
	return effects
}

// ModuleInfo is a module as the modules page shows it: its manifest, its
// state and what uses it.
type ModuleInfo struct {
	Manifest *Manifest `json:"manifest" doc:"The module's manifest: what it is, needs, installs and costs"`
	// Resource is the module's core.module resource.
	Resource store.ID `json:"resource,omitempty" doc:"The module's core.module resource"`
	Enabled  bool     `json:"enabled" doc:"Whether the module is meant to be on"`
	State    State    `json:"state" enum:"off,on,active,stopping,removing" doc:"off, on, active (something uses it), stopping or removing"`
	Usage    Usage    `json:"usage" doc:"How many resources of the module's kinds there are"`
	// Installed lists the module's software found on the server.
	Installed []string `json:"installed,omitempty" doc:"The ids of the module's software found installed on the server"`
	// HardwareProblems say why the module can't be turned on here.
	HardwareProblems []string `json:"hardwareProblems,omitempty" doc:"Why the module can't be turned on on this server"`
	// Needs lists the modules it needs that are off, and Dependents the
	// modules that are on and need it.
	Needs      []string `json:"needs,omitempty" doc:"Modules it needs that are off"`
	Dependents []string `json:"dependents,omitempty" doc:"Modules that are on and need it"`
	// Error is why the module didn't start.
	Error string `json:"error,omitempty" doc:"Why the module didn't start"`
	// Suspends says the module can be turned off while resources use it,
	// keeping them.
	Suspends bool `json:"suspends,omitempty" doc:"Whether the module can be turned off while resources use it, keeping them"`
	// Exportable says the module has resources, which removing it with its
	// data deletes, and which GET /api/v1/modules/{id}/export exports first.
	Exportable bool `json:"exportable,omitempty" doc:"Whether the module has resources to export before it is removed with its data"`
	// Removed says the module was removed and its software uninstalled.
	Removed bool `json:"removed,omitempty" doc:"Whether the module was removed, its software uninstalled until something needs it again"`
}

// Info describes one module.
func (m *Manager) Info(ctx context.Context, id string) (*ModuleInfo, error) {
	mod, ok := m.reg.Get(id)
	if !ok {
		return nil, ErrUnknownModule.New("module", id)
	}
	man := mod.Manifest()
	info := &ModuleInfo{Manifest: man, Enabled: m.Enabled(id), State: m.State(id)}
	if info.State == "" {
		info.State = StateOff
	}
	_, info.Suspends = mod.(TransitionPlanner)
	if rid, ok := m.Resource(id); ok {
		info.Resource = rid
		if st := m.opts.Services.Store; st != nil {
			obj, err := Kind.Get(ctx, st, rid)
			if err != nil && !store.ErrNotFound.Is(err) {
				return nil, err
			}
			if obj != nil {
				info.Usage, info.Installed, info.Removed = obj.Status.Usage, obj.Status.Installed, obj.Spec.Removed
			}
		}
	}
	if m.opts.Facts != nil {
		facts, err := m.opts.Facts(ctx)
		if err != nil {
			return nil, err
		}
		info.HardwareProblems = HardwareProblems(man, facts)
	}
	for _, dep := range man.Dependencies.Requires {
		if !m.Enabled(dep) {
			info.Needs = append(info.Needs, dep)
		}
	}
	dependents, err := m.dependents(ctx, m.opts.Services.Store, id)
	if err != nil {
		return nil, err
	}
	info.Dependents = dependents
	if err, ok := m.Failed()[id]; ok {
		info.Error = strings.TrimSpace(err.Error())
	}
	info.Exportable = info.Usage.Resources > 0
	return info, nil
}

// List describes every module, in the order of their ids.
func (m *Manager) List(ctx context.Context) ([]*ModuleInfo, error) {
	var out []*ModuleInfo
	for _, id := range m.reg.IDs() {
		info, err := m.Info(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}
