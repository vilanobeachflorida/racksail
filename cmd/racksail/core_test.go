package main

import (
	"context"
	"errors"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/modules"
)

// knot stands in for a module that runs a service and checks on it.
type knot struct {
	modules.Base
	down bool
}

func (k *knot) HealthChecks() []modules.HealthCheck {
	return []modules.HealthCheck{{Name: "knot", Check: func(ctx context.Context, svc *modules.Services) error {
		if svc == nil {
			return errors.New("the check got no services")
		}
		if k.down {
			return errors.New("knot isn't answering")
		}
		return nil
	}}}
}

func TestModuleHealthChecks(t *testing.T) {
	reg := modules.NewRegistry()
	mod := &knot{Base: modules.NewBase([]byte(`
id: dns
name: DNS
description: Zones and records.
version: 0.1.0
license: AGPL-3.0-or-later
category: hosting
dependencies: {}
profiles: [dns-server]
hardware: {arch: [amd64, arm64]}
cost: {idleMemoryMB: 1, activeMemoryMB: 2, diskMB: 3}
upstream: []
`))}
	if err := reg.Add(mod, nil); err != nil {
		t.Fatal(err)
	}
	svc := &modules.Services{}
	manager := modules.NewManager(reg, modules.ManagerOptions{Services: svc})
	checks := moduleHealthChecks(reg, manager, svc)
	if len(checks) != 1 || checks[0].Name != "dns.knot" {
		t.Fatalf("checks = %+v", checks)
	}
	if checks[0].Active() {
		t.Error("a module that is off isn't checked")
	}
	ctx := context.Background()
	if err := manager.TurnOn(ctx, "dns"); err != nil {
		t.Fatal(err)
	}
	if !checks[0].Active() || checks[0].Check(ctx) != nil {
		t.Error("once it's on, its check runs and passes")
	}
	mod.down = true
	if err := checks[0].Check(ctx); err == nil {
		t.Error("a failing service fails the check")
	}
}
