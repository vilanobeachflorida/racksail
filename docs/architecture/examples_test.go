package architecture

// The kind, reconciler and agent operation internals.md shows, exactly as it
// shows them. TestExamplesAreShownAsTheyAre keeps the two the same, and
// TestZoneExampleRuns runs them.

import (
	"context"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// ZoneSpec is what the user wants: the zone's name and settings.
type ZoneSpec struct {
	Name string `json:"name" doc:"The zone's domain name" example:"example.com"`
	// TSIGKey is a reference to the zone's transfer key, never the key itself.
	TSIGKey string `json:"tsigKey,omitempty" doc:"The key secondaries use to transfer the zone"`
}

// ZoneStatus is what the reconciler last saw. Embedding store.Status puts
// the phase, message and conditions at the top level of the status.
type ZoneStatus struct {
	store.Status
	Serial uint32 `json:"serial,omitempty"`
}

var Zone = store.Define[ZoneSpec, ZoneStatus](store.KindInfo{
	Name: "dns.zone", Prefix: "zone", Scope: store.ScopeAccount,
	Title: "Zone", Plural: "zones",
	Description: "A DNS zone served by the authoritative name server.",
	Indexes:     []store.Index{{Name: "name", Path: "name", Unique: true, Fold: true}},
	UI:          store.UIHints{Fields: map[string]store.FieldHint{"tsigKey": {Sensitive: true}}},
})

type zoneReconciler struct{}

func (zoneReconciler) Plan(ctx context.Context, z *store.Object[ZoneSpec, ZoneStatus], req reconcile.Request) (reconcile.Plan, error) {
	file := "/etc/knot/zones/" + z.Spec.Name + ".zone"
	if z.Deleted() {
		return reconcile.Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: file})}}, nil
	}
	return reconcile.Plan{Steps: []agent.Step{
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: "/etc/knot/zones", Mode: 0o750, Parents: true}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: file, Content: renderZone(z.Spec), Mode: 0o640}),
		ReloadZones.Step(ReloadZonesInput{}),
	}}, nil
}

func (zoneReconciler) Apply(ctx context.Context, z *store.Object[ZoneSpec, ZoneStatus], plan reconcile.Plan, ex reconcile.Executor) (ZoneStatus, error) {
	st := z.Status
	err := reconcile.ApplySteps(ctx, z.Generation, plan, ex, &st.Status, time.Now())
	return st, err
}

// ReloadZonesInput takes no arguments, but every input validates itself.
type ReloadZonesInput struct{}

func (ReloadZonesInput) Validate() error { return nil }

var ReloadZones = agent.Register(agent.Def[ReloadZonesInput, agent.Result]{
	Name:        "dns.ReloadZones",
	Summary:     "Reload DNS zones",
	Description: "Asks Knot DNS to load zone files that changed.",
	Run: func(ctx context.Context, env *agent.Env, in ReloadZonesInput) (agent.Result, error) {
		effect := system.Effect{Type: system.EffectService, Action: "reload", Target: "knot.service"}
		if env.Check {
			return agent.Changed(effect), nil
		}
		if _, err := env.System.Exec.Run(ctx, system.Command{Path: "/usr/sbin/knotc", Args: []string{"zone-reload"}}); err != nil {
			return agent.Result{}, err
		}
		return agent.Changed(effect), nil
	},
})
