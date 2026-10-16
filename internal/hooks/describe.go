package hooks

import (
	"sort"
	"strings"
	"time"
)

// PointInfo describes a hook point and what is attached to it, for the API
// and the admin's view of the hook pipeline.
type PointInfo struct {
	Point       Point  `json:"point" doc:"The hook point, such as web.site.create.validate"`
	Description string `json:"description,omitempty" doc:"What happens at the point"`
	// Kind, Operation and Stage split the points of the change pipeline.
	Kind      string `json:"kind,omitempty" doc:"The kind the point is about, for points of the change pipeline"`
	Operation string `json:"operation,omitempty" doc:"The operation: create, update, delete or an action's name"`
	Stage     string `json:"stage,omitempty" enum:"validate,plan,applied" doc:"The stage of the change pipeline"`
	// Config says system hooks patch configuration here, Checked that the
	// patches go through the service's own check, and Invariants are the
	// settings they can only make stricter.
	Config     bool     `json:"config,omitempty" doc:"Whether system hooks patch generated configuration here"`
	Checked    bool     `json:"checked,omitempty" doc:"Whether patches pass the service's own configuration test before they are kept"`
	Invariants []string `json:"invariants,omitempty" doc:"Settings hooks can only make stricter, unless they hold core.override"`
	// Hooks are the registrations that run here, those of patterns that
	// name the point included, in running order.
	Hooks []HookInfo `json:"hooks,omitempty" doc:"The hooks that run here, those attached to patterns that name it included, in the order they run"`
}

// HookInfo describes one registration.
type HookInfo struct {
	Owner    string        `json:"owner" doc:"Who registered the hook: module:<id>, addon:<id>, rule:<id> or core:<part>"`
	Label    string        `json:"label,omitempty" doc:"What the hook is called"`
	Kind     Kind          `json:"kind" enum:"observe,validate,modify,system,provide,extend" doc:"What sort of hook it is"`
	Point    Point         `json:"point" doc:"The point or pattern the hook is attached to"`
	Priority int           `json:"priority" doc:"Where it runs among the hooks of its point: lower first"`
	Deadline time.Duration `json:"deadline,omitempty" doc:"How long the hook has to answer, in nanoseconds"`
	Failure  FailureMode   `json:"failureMode,omitempty" enum:"block,allow" doc:"What happens when it fails or runs out of time"`
	Security bool          `json:"security,omitempty" doc:"Whether it guards security, so safe mode leaves it on"`
	Override bool          `json:"override,omitempty" doc:"Whether it holds core.override"`
	// Provider and Extension name what provide and extend hooks add.
	Provider string `json:"provider,omitempty" doc:"For a provide hook, the capability it provides"`
	UI       string `json:"ui,omitempty" doc:"For an extend hook, what it adds to the panel"`
	Failures int    `json:"failures,omitempty" doc:"How many times in a row it failed"`
	Disabled bool   `json:"disabled,omitempty" doc:"Whether the circuit breaker switched it off"`
}

func (r *Registry) info(reg Registration) HookInfo {
	h := HookInfo{
		Owner: reg.Owner, Label: reg.Label, Kind: reg.Kind, Point: reg.Point, Priority: reg.Priority, Deadline: reg.Deadline,
		Failure: reg.Failure, Security: reg.Security, Override: reg.Override,
	}
	if reg.Provider != nil {
		h.Provider = reg.Provider.Name
	}
	if reg.Extension != nil && reg.Extension.UI != nil {
		h.UI = reg.Extension.UI.Label
	}
	if b := r.breakers[breakerKey(reg.Owner, reg.Point)]; b != nil {
		h.Failures, h.Disabled = b.failures, b.disabled != nil
	}
	return h
}

// Describe lists the declared points whose names start with prefix, every
// one when it is empty, with the hooks that run at each, disabled ones
// included.
func (r *Registry) Describe(prefix string) []PointInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []PointInfo
	for p, desc := range r.points {
		if !strings.HasPrefix(string(p), prefix) {
			continue
		}
		pi := PointInfo{Point: p, Description: desc}
		if kind, op, stage, ok := ParseKindPoint(p); ok {
			pi.Kind, pi.Operation, pi.Stage = kind, op, stage
		}
		if svc, ok := r.configs[p]; ok {
			pi.Config, pi.Checked = true, svc.Check != nil
			for _, inv := range svc.Invariants {
				pi.Invariants = append(pi.Invariants, inv.Path)
			}
		} else if IsConfigPoint(p) {
			pi.Config = true
		}
		var regs []Registration
		regs = append(regs, r.regs[p]...)
		for _, reg := range r.patterns {
			if MatchPoint(reg.Point, p) {
				regs = append(regs, reg)
			}
		}
		for _, reg := range sortRegs(regs) {
			pi.Hooks = append(pi.Hooks, r.info(reg))
		}
		out = append(out, pi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Point < out[j].Point })
	return out
}

// Hooks lists every registration, in running order within each point,
// those attached by pattern once.
func (r *Registry) Hooks() []HookInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []Registration
	for _, list := range r.regs {
		all = append(all, list...)
	}
	all = append(all, r.patterns...)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Point != all[j].Point {
			return all[i].Point < all[j].Point
		}
		if all[i].Priority != all[j].Priority {
			return all[i].Priority < all[j].Priority
		}
		return all[i].Owner < all[j].Owner
	})
	out := make([]HookInfo, 0, len(all))
	for _, reg := range all {
		out = append(out, r.info(reg))
	}
	return out
}
