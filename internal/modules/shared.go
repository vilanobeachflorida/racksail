package modules

import (
	"context"
	"fmt"
	"path"

	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Modules can name the same software and services: mail's Rspamd keeps its
// statistics in Valkey, which the databases module also runs for sites.
// Turning one of them off, or removing it, must leave alone what another
// still uses.

// ActionKeep is the action of a transition's effects for what it leaves
// in place because another module still uses it.
const ActionKeep = "keep"

// Shared is what other modules still use of what a module names: the
// Debian packages they install, the package repositories they add, the
// runtime directories of their archives, their services and their ports.
// A module still uses its software while it is on, and while it keeps
// resources it was turned off with, until it is removed.
type Shared struct {
	// Each maps what is used to the name of the first module, by id, that
	// uses it. Packages are named exactly, or by a pattern such as
	// php*-fpm.
	packages map[string]string
	patterns []sharedPattern
	sources  map[string]string
	runtimes map[string]string
	units    map[string]string
	ports    map[string]string
}

type sharedPattern struct {
	pattern, module string
}

// Package returns the name of the other module that uses a Debian
// package, if any.
func (s Shared) Package(name string) (string, bool) {
	if m, ok := s.packages[name]; ok {
		return m, true
	}
	for _, p := range s.patterns {
		if ok, err := path.Match(p.pattern, name); err == nil && ok {
			return p.module, true
		}
	}
	return "", false
}

// Source returns the other module that uses the package repository of an
// upstream entry, by its id.
func (s Shared) Source(upstream string) (string, bool) { m, ok := s.sources[upstream]; return m, ok }

// Runtime returns the other module that uses the runtime directory of an
// upstream entry, by its id.
func (s Shared) Runtime(upstream string) (string, bool) { m, ok := s.runtimes[upstream]; return m, ok }

// Unit returns the other module that runs a systemd unit.
func (s Shared) Unit(name string) (string, bool) { m, ok := s.units[name]; return m, ok }

// Port returns the other module that opens a port, such as tcp/443.
func (s Shared) Port(port string) (string, bool) { m, ok := s.ports[port]; return m, ok }

func portName(p Port) string { return fmt.Sprintf("%s/%d", p.Protocol, p.Port) }

// keepsSoftware reports whether a module uses its software in the planned
// state rd shows: it is meant to be on, or it was turned off keeping
// resources and hasn't been removed.
func (m *Manager) keepsSoftware(ctx context.Context, rd store.Reader, id string) (bool, error) {
	spec, err := m.planned(ctx, rd, id)
	if err != nil {
		return false, err
	}
	if spec == nil {
		return m.Enabled(id), nil
	}
	if spec.Enabled {
		return true, nil
	}
	if spec.Removed {
		return false, nil
	}
	usage, err := m.usage(ctx, rd, id)
	return usage.Resources > 0, err
}

// shared returns what modules other than id still use, in the planned
// state rd shows, by the name of the module that uses it.
func (m *Manager) shared(ctx context.Context, rd store.Reader, id string) (Shared, error) {
	s := Shared{packages: map[string]string{}, sources: map[string]string{}, runtimes: map[string]string{}, units: map[string]string{}, ports: map[string]string{}}
	add := func(set map[string]string, key, module string) {
		if _, ok := set[key]; !ok {
			set[key] = module
		}
	}
	for _, other := range m.reg.All() {
		o := other.Manifest()
		if o.ID == id {
			continue
		}
		uses, err := m.keepsSoftware(ctx, rd, o.ID)
		if err != nil {
			return Shared{}, err
		}
		if !uses {
			continue
		}
		for _, u := range o.Upstream {
			switch u.Source.Type {
			case "debian", "apt-repository":
				for _, p := range u.Source.Packages {
					if isPattern(p) {
						s.patterns = append(s.patterns, sharedPattern{pattern: p, module: o.Name})
					} else {
						add(s.packages, p, o.Name)
					}
				}
				if u.Source.Type == "apt-repository" {
					add(s.sources, u.ID, o.Name)
				}
			case "vendor-archive", "racksail-build":
				add(s.runtimes, u.ID, o.Name)
			}
		}
		for _, svc := range o.Services {
			add(s.units, svc.Unit, o.Name)
		}
		for _, p := range o.Ports {
			add(s.ports, portName(p), o.Name)
		}
	}
	return s, nil
}

// kept is the effect of leaving something in place for another module.
func kept(t system.EffectType, target, module string) system.Effect {
	return system.Effect{Type: t, Action: ActionKeep, Target: target, Detail: module + " still uses it"}
}

// sharedPackages splits the packages an upstream entry names exactly into
// those no other module uses and the effects of keeping the others.
func sharedPackages(u Upstream, shared Shared) ([]string, []system.Effect) {
	var remove []string
	var keep []system.Effect
	for _, name := range exactPackages(u) {
		if by, ok := shared.Package(name); ok {
			keep = append(keep, kept(system.EffectPackage, name, by))
			continue
		}
		remove = append(remove, name)
	}
	return remove, keep
}
