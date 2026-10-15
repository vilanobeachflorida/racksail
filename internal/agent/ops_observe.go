package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// ReadJournalInput selects journal entries.
type ReadJournalInput struct {
	Query system.JournalQuery `json:"query"`
}

// Validate implements Input.
func (in ReadJournalInput) Validate() error { return in.Query.Validate() }

// ReadJournal reads the systemd journal: a unit's logs, an account's
// journal namespace, or the whole system's.
var ReadJournal = Register(Def[ReadJournalInput, system.JournalPage]{
	Name:        "ReadJournal",
	Summary:     "Read the journal",
	Description: "Returns journal entries matching the query, oldest first or newest first, with a cursor to continue from.",
	ReadOnly:    true,
	Run: func(ctx context.Context, env *Env, in ReadJournalInput) (system.JournalPage, error) {
		page, err := env.System.Journal.Read(ctx, in.Query)
		if page.Entries == nil {
			page.Entries = []system.JournalEntry{}
		}
		return page, err
	},
})

// MetricsInput names the units to measure besides the whole machine.
type MetricsInput struct {
	Units []string `json:"units,omitempty" doc:"Units and slices to measure, such as an account's slice"`
}

// Validate implements Input.
func (in MetricsInput) Validate() error {
	if len(in.Units) > 1000 {
		return fmt.Errorf("measure at most 1000 units at a time")
	}
	for _, u := range in.Units {
		if !system.ValidUnitName(u) {
			return fmt.Errorf("%q is not a unit name", u)
		}
	}
	return nil
}

// MachineMetrics is one sample of the machine and the units asked for.
type MachineMetrics struct {
	System system.SystemMetrics `json:"system"`
	Units  []system.UnitMetrics `json:"units"`
}

// GetMetrics samples resource use. Counters only grow; the panel computes
// rates from two samples.
var GetMetrics = Register(Def[MetricsInput, MachineMetrics]{
	Name:        "GetMetrics",
	Summary:     "Measure resource use",
	Description: "Samples the machine's CPU, memory, pressure, disks, network and filesystems, and the control groups of the units asked for.",
	ReadOnly:    true,
	Run: func(ctx context.Context, env *Env, in MetricsInput) (MachineMetrics, error) {
		out := MachineMetrics{Units: []system.UnitMetrics{}}
		var err error
		if out.System, err = env.System.Metrics.System(ctx); err != nil {
			return out, err
		}
		if len(in.Units) > 0 {
			units, err := env.System.Metrics.Units(ctx, in.Units...)
			if err != nil {
				return out, err
			}
			out.Units = append(out.Units, units...)
		}
		return out, nil
	},
})

// DriftReport lists drift.
type DriftReport struct {
	Drift []Drift `json:"drift"`
}

// CheckDrift compares the machine with what operations put on it for
// resources, and lists Racksail's failed units.
var CheckDrift = Register(Def[Empty, DriftReport]{
	Name:        "CheckDrift",
	Summary:     "Find what changed behind Racksail's back",
	Description: "Lists files, units and drop-ins written for resources that were changed or removed since, and Racksail units that failed. The panel reconciles the resources named.",
	ReadOnly:    true,
	Run: func(ctx context.Context, env *Env, _ Empty) (DriftReport, error) {
		if env.Runtime == nil {
			return DriftReport{}, errNoRuntime
		}
		drift, err := env.Runtime.CheckDrift(ctx)
		return DriftReport{Drift: drift}, err
	},
})

// ReadAuditInput selects audit records.
type ReadAuditInput struct {
	Filter AuditFilter `json:"filter,omitzero"`
}

// Validate implements Input.
func (in ReadAuditInput) Validate() error {
	if in.Filter.Limit < 0 || in.Filter.Limit > 10000 {
		return fmt.Errorf("limit must be between 0 and 10000")
	}
	if in.Filter.Op != "" && !opNamePattern.MatchString(strings.TrimPrefix(in.Filter.Op, "streams/")) {
		return fmt.Errorf("operation name %q", in.Filter.Op)
	}
	if in.Filter.Owner != "" && !in.Filter.Owner.Valid() {
		return fmt.Errorf("owner %q isn't an id", in.Filter.Owner)
	}
	return nil
}

// AuditTrail lists audit records, newest first.
type AuditTrail struct {
	Records []AuditRecord `json:"records"`
}

// ReadAudit returns the agent's own record of the operations it ran.
var ReadAudit = Register(Def[ReadAuditInput, AuditTrail]{
	Name:        "ReadAudit",
	Summary:     "Read the agent's audit trail",
	Description: "Returns the agent's record of the operations it ran or refused, newest first: who asked, for which resource, what it touched and how it ended. Inputs are kept as hashes only.",
	ReadOnly:    true,
	Run: func(ctx context.Context, env *Env, in ReadAuditInput) (AuditTrail, error) {
		if env.Runtime == nil {
			return AuditTrail{}, errNoRuntime
		}
		records, err := env.Runtime.audit.Recent(in.Filter)
		if records == nil {
			records = []AuditRecord{}
		}
		return AuditTrail{Records: records}, err
	},
})

// SysctlKeysInput names kernel parameters.
type SysctlKeysInput struct {
	Keys []string `json:"keys"`
}

// Validate implements Input.
func (in SysctlKeysInput) Validate() error {
	if len(in.Keys) == 0 || len(in.Keys) > 256 {
		return fmt.Errorf("give between 1 and 256 keys")
	}
	for _, k := range in.Keys {
		if !system.ValidSysctlKey(k) {
			return fmt.Errorf("kernel parameter %q", k)
		}
	}
	return nil
}

// SysctlValues are kernel parameters and their values; missing parameters
// are left out.
type SysctlValues struct {
	Values map[string]string `json:"values"`
}

// GetSysctl reads kernel parameters.
var GetSysctl = Register(Def[SysctlKeysInput, SysctlValues]{
	Name:     "GetSysctl",
	Summary:  "Read kernel parameters",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in SysctlKeysInput) (SysctlValues, error) {
		out := SysctlValues{Values: map[string]string{}}
		for _, k := range slices.Compact(slices.Sorted(slices.Values(in.Keys))) {
			v, err := env.System.Sysctl.Get(ctx, k)
			if errors.Is(err, system.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			out.Values[k] = v
		}
		return out, nil
	},
})
