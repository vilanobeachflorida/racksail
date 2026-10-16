package modules

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

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
	Packages []system.Package `json:"packages,omitempty"`
	// DownloadBytes and DiskBytes are what installing it takes: from the
	// package index when it can tell, from the manifest otherwise.
	DownloadBytes int64 `json:"downloadBytes,omitempty"`
	DiskBytes     int64 `json:"diskBytes,omitempty"`
	Installed     bool  `json:"installed"`
}

// softwareFresh is how long what FirstUse found stands for the previews
// that read it.
const softwareFresh = 10 * time.Minute

// found is what FirstUse found of a module's software, and when.
type found struct {
	at       time.Time
	software []Software
}

// knownSoftware returns what FirstUse found of a module's software, while
// it is fresh. Plans read it rather than ask the agent themselves.
func (m *Manager) knownSoftware(id string) ([]Software, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.software[id]
	if !ok || m.opts.Now().Sub(f.at) > softwareFresh {
		return nil, false
	}
	return f.software, true
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

// FirstUse describes what a module installs when something first needs
// it, asking the agent about the packages it names exactly. What it finds
// is what previews of turning the module on list for a while after.
func (m *Manager) FirstUse(ctx context.Context, id string) ([]Software, error) {
	out, err := m.firstUse(ctx, id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.software[id] = found{at: m.opts.Now(), software: out}
	m.mu.Unlock()
	return out, nil
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
				sw.Packages = q.Candidates
				sw.DownloadBytes, sw.DiskBytes = 0, 0
				for _, p := range q.Candidates {
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
// it opens, and the memory and disk it takes.
func (m *Manager) turnOnEffects(ctx context.Context, man *Manifest) []system.Effect {
	effects := []system.Effect{{
		Type: system.EffectProcess, Action: "start", Target: "module " + man.ID, Detail: "in the panel",
		MemoryBytes: int64(man.Cost.IdleMemoryMB) << 20,
	}}
	// What the server said its packages take, when it was asked as the
	// change was requested, and the manifest's estimates otherwise.
	software, ok := m.knownSoftware(man.ID)
	if !ok {
		software = manifestSoftware(man)
	}
	var disk int64
	for _, sw := range software {
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
// opened, whose resources it keeps.
func (m *Manager) turnOffEffects(man *Manifest, current State) []system.Effect {
	effects := []system.Effect{{Type: system.EffectProcess, Action: "stop", Target: "module " + man.ID, Detail: "in the panel"}}
	if current != StateActive {
		return effects
	}
	for _, s := range man.Services {
		effects = append(effects, system.Effect{Type: system.EffectService, Action: "stop", Target: s.Unit, Detail: s.Description})
	}
	for _, p := range man.Ports {
		effects = append(effects, system.Effect{Type: system.EffectPort, Action: "close", Target: fmt.Sprintf("%s/%d", p.Protocol, p.Port), Detail: p.Description})
	}
	return effects
}

// ModuleInfo is a module as the modules page shows it: its manifest, its
// state and what uses it.
type ModuleInfo struct {
	Manifest *Manifest `json:"manifest"`
	// Resource is the module's core.module resource.
	Resource store.ID `json:"resource,omitempty" doc:"The module's core.module resource"`
	Enabled  bool     `json:"enabled" doc:"Whether the module is meant to be on"`
	State    State    `json:"state" enum:"off,on,active,stopping,removing"`
	Usage    Usage    `json:"usage"`
	// Installed lists the module's software found on the server.
	Installed []string `json:"installed,omitempty"`
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
