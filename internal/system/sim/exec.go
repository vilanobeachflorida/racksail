package sim

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Program simulates an external program. It may read and change the
// machine through m.Backend(); it must not hold on to cmd after returning.
// A non-zero ExitCode is reported as an *system.ExitError.
type Program func(ctx context.Context, m *Machine, cmd system.Command) (system.Output, error)

var (
	programsMu sync.RWMutex
	programs   = map[string]Program{}
)

// RegisterProgram simulates the program at path on every machine. Modules
// call it from init functions in their simulation code.
func RegisterProgram(path string, p Program) {
	programsMu.Lock()
	defer programsMu.Unlock()
	programs[path] = p
}

// SetProgram simulates the program at path on this machine only, taking
// precedence over RegisterProgram. Tests use it to script failures.
func (m *Machine) SetProgram(path string, p Program) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.programs[path] = p
}

type simExec struct{ m *Machine }

func (e simExec) Run(ctx context.Context, cmd system.Command) (system.Output, error) {
	if err := cmd.Validate(); err != nil {
		return system.Output{}, err
	}
	m := e.m
	if err := m.lock(ctx); err != nil {
		return system.Output{}, err
	}
	err := m.record("exec", "run", cmd.Path)
	p, ok := m.programs[cmd.Path]
	if cmd.User != "" {
		if _, known := m.users[cmd.User]; !known && err == nil {
			err = notFound("user " + cmd.User)
		}
	}
	m.mu.Unlock()
	if err != nil {
		return system.Output{}, err
	}
	if !ok {
		programsMu.RLock()
		p, ok = programs[cmd.Path]
		programsMu.RUnlock()
	}
	if !ok {
		return system.Output{}, fmt.Errorf("%s: %w", cmd.Path, ErrNotSimulated)
	}
	ctx, cancel := context.WithTimeout(ctx, cmd.EffectiveTimeout())
	defer cancel()
	out, err := p(ctx, m, cmd)
	if err != nil {
		return out, err
	}
	limit := cmd.EffectiveMaxOutput()
	if len(out.Stdout) > limit {
		out.Stdout = out.Stdout[:limit]
	}
	if len(out.Stderr) > limit {
		out.Stderr = out.Stderr[:limit]
	}
	if out.ExitCode != 0 {
		return out, &system.ExitError{Program: cmd.Path, ExitCode: out.ExitCode, Stderr: string(out.Stderr)}
	}
	return out, nil
}

type simFacts struct{ m *Machine }

func (f simFacts) Facts(ctx context.Context) (system.MachineFacts, error) {
	m := f.m
	if err := m.lock(ctx); err != nil {
		return system.MachineFacts{}, err
	}
	defer m.mu.Unlock()
	facts := m.facts
	facts.CPU.Flags = slices.Clone(m.facts.CPU.Flags)
	facts.Hostname = m.hostname
	for _, d := range m.disks {
		facts.Disks = append(facts.Disks, system.DiskFact{
			Name: d.Name, Model: d.Model, Serial: d.Serial, SizeBytes: d.SizeBytes,
			Rotational: d.Rotational, Removable: d.Removable, Transport: d.Transport,
		})
	}
	for _, ifc := range m.ifaces {
		if ifc.Type != "ether" {
			continue
		}
		facts.NICs = append(facts.NICs, system.NICFact{Name: ifc.Name, MAC: ifc.MAC, Driver: "virtio_net", SpeedMbps: ifc.SpeedMbps, Up: ifc.Up})
	}
	facts.CollectedAt = m.now()
	return facts, nil
}
