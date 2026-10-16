package addons

import (
	"context"
	"fmt"
	"regexp"

	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// An account's add-on runs where the account's own services do: in the
// account's slice, which holds them all to the limits of the account's
// plan, so an add-on can't take more of the server than the account may.
// Its own memory and CPU limits, which the account owner approves, are
// capped by the plan as well.

// Placement is where an account's add-ons run.
type Placement struct {
	// Slug is the account's: its add-ons run in
	// racksail-accounts-<slug>.slice.
	Slug string
	// Limits are what the account's plan lets the whole account use. No
	// add-on of the account gets more memory or CPU than they allow; zero
	// leaves the add-on's own limit.
	Limits system.ResourceLimits
}

// Accounts finds where an account's add-ons run. The module that keeps
// accounts provides it; without one, account add-ons don't run, since
// nothing could hold them to an account.
type Accounts interface {
	// AddonPlacement returns the placement of an account's add-ons. rd
	// is the state the add-on is planned against.
	AddonPlacement(ctx context.Context, rd store.Reader, account store.ID) (Placement, error)
}

// slugPattern is what account slugs look like: lowercase letters and
// digits, starting with a letter.
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9]{2,15}$`)

// placement returns where an account's add-on runs.
func (r *Runtime) placement(ctx context.Context, rd store.Reader, account store.ID, name string) (Placement, error) {
	var accounts Accounts
	if r.opts.Accounts != nil {
		accounts = r.opts.Accounts()
	}
	if accounts == nil {
		return Placement{}, ErrNoAccount.New("addon", name, "reason", "nothing on this server keeps accounts, so there is no account to run it in")
	}
	p, err := accounts.AddonPlacement(ctx, rd, account)
	if err != nil {
		return Placement{}, err
	}
	if !slugPattern.MatchString(p.Slug) {
		return Placement{}, fmt.Errorf("addons: account %s has the slug %q, which can't name a slice", account, p.Slug)
	}
	return p, nil
}

// limit caps an add-on's memory, in MB, and CPU, in percent of one CPU, to
// what the account's plan allows.
func (p Placement) limit(mem, cpu int) (int, int) {
	if m := p.Limits.MemoryMaxBytes >> 20; m > 0 && int64(mem) > m {
		mem = int(m)
	}
	if c := p.Limits.CPUQuotaPercent; c > 0 && cpu > c {
		cpu = c
	}
	return mem, cpu
}

// serviceSpec is what an add-on's service is rendered from: its limits,
// the hosts it may reach and, for an account's add-on, the account's slice
// and the plan's caps. rd is the state the add-on is planned against.
func (r *Runtime) serviceSpec(ctx context.Context, rd store.Reader, obj *store.Object[AddonSpec, AddonStatus], inst string, man *hooks.AddonManifest, hosts []Host) (ServiceSpec, error) {
	mem, cpu := limits(obj.Spec.Approved.Resources, man.Permissions.Resources)
	svc := ServiceSpec{ID: inst, Version: man.Version, MemoryMB: mem, CPUPercent: cpu, Hosts: hosts}
	if obj.Kind != AccountAddon.Name() {
		return svc, nil
	}
	p, err := r.placement(ctx, rd, obj.Scope.ID, man.Name)
	if err != nil {
		return ServiceSpec{}, err
	}
	svc.Slice = system.AccountSlice(p.Slug)
	svc.MemoryMB, svc.CPUPercent = p.limit(svc.MemoryMB, svc.CPUPercent)
	return svc, nil
}
