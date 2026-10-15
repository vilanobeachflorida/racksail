package changeset

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Op is what a resource change does.
type Op string

// Operations.
const (
	OpCreate Op = "create"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
	OpAction Op = "action"
)

// Stage is a step of the pipeline that hooks attach to.
type Stage string

// Hookable stages.
const (
	StageValidate Stage = "validate"
	StagePlan     Stage = "plan"
	StageApplied  Stage = "applied"
)

// HookPoint returns the hook point of an operation on a kind at a stage, such
// as web.site.create.validate. For actions, operation is the action name.
func HookPoint(kind, operation string, stage Stage) string {
	return kind + "." + operation + "." + string(stage)
}

// Source says who contributed part of a change set.
type Source struct {
	// Type is request, hook, rule, addon or module.
	Type string `json:"type" enum:"request,hook,rule,addon,module"`
	// Name identifies the contributor, such as an add-on id and version.
	Name string `json:"name,omitempty"`
	// Hook is the hook point the contribution came through.
	Hook string `json:"hook,omitempty"`
}

// RequestSource is the source of changes the requester asked for.
var RequestSource = Source{Type: "request"}

// DiffOp is what happened to a field.
type DiffOp string

// Field changes.
const (
	DiffAdd     DiffOp = "add"
	DiffRemove  DiffOp = "remove"
	DiffReplace DiffOp = "replace"
)

// FieldDiff is one changed field of a spec.
type FieldDiff struct {
	// Path locates the field with dots and indexes, such as domains[0].
	Path   string          `json:"path"`
	Op     DiffOp          `json:"op" enum:"add,remove,replace"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
	// Sensitive values are left out of the diff.
	Sensitive bool `json:"sensitive,omitempty"`
}

// ResourceChange is one change to one resource.
type ResourceChange struct {
	Op Op `json:"op" enum:"create,update,delete,action"`
	// Action is the action name when Op is action.
	Action string      `json:"action,omitempty"`
	Kind   string      `json:"kind"`
	ID     store.ID    `json:"id"`
	Scope  store.Scope `json:"scope"`
	// Server is the cluster server the resource is placed on; empty for the
	// server running the panel.
	Server store.ID `json:"server,omitempty"`
	// Revision is the revision the change was planned against.
	Revision int64             `json:"revision,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	// LabelsBefore are the labels before an update or action, so undo can
	// put them back.
	LabelsBefore map[string]string `json:"labelsBefore,omitempty"`
	// Before and After are the specs, kept so the change can be undone.
	// Sensitive fields in them only ever hold references to secrets.
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
	// Input is an action's input. It is recorded with the change set, so
	// actions never take secrets as input.
	Input  json.RawMessage `json:"input,omitempty"`
	Diff   []FieldDiff     `json:"diff,omitempty"`
	Source Source          `json:"source"`
	// Steps are agent operations an action runs once, on top of the
	// resource's reconcile.
	Steps []agent.Step `json:"steps,omitempty"`
	// Checks confirm that an action's steps worked. They run after the steps
	// and the resource's reconcile, whose own plan has checks of its own.
	Checks []reconcile.HealthCheck `json:"checks,omitempty"`
	// Output is what an action returned once it was applied.
	Output json.RawMessage `json:"output,omitempty" doc:"What the action returned, once it was applied"`
}

// Clone returns a copy of rc whose maps, documents and lists can be changed
// without affecting rc. Action steps keep sharing their inputs.
func (rc ResourceChange) Clone() ResourceChange {
	c := rc
	c.Labels = maps.Clone(rc.Labels)
	c.LabelsBefore = maps.Clone(rc.LabelsBefore)
	c.Before = bytes.Clone(rc.Before)
	c.After = bytes.Clone(rc.After)
	c.Input = bytes.Clone(rc.Input)
	c.Output = bytes.Clone(rc.Output)
	if rc.Diff != nil {
		c.Diff = make([]FieldDiff, len(rc.Diff))
		for i, d := range rc.Diff {
			d.Before, d.After = bytes.Clone(d.Before), bytes.Clone(d.After)
			c.Diff[i] = d
		}
	}
	c.Steps = slices.Clone(rc.Steps)
	c.Checks = slices.Clone(rc.Checks)
	return c
}

// Contribution labels what a hook or add-on added.
type Contribution struct {
	Source  Source `json:"source"`
	Summary string `json:"summary"`
	// Changes and Effects index into the change set's lists.
	Changes []int `json:"changes,omitempty"`
	Effects []int `json:"effects,omitempty"`
}

// Conflict is two contributors changing the same field. A change set with
// conflicts can't be applied until they are resolved.
type Conflict struct {
	Resource store.ID `json:"resource"`
	Path     string   `json:"path"`
	Sources  []Source `json:"sources"`
	Message  string   `json:"message"`
}

// Estimate totals what applying a change set costs.
type Estimate struct {
	DownloadBytes int64 `json:"downloadBytes"`
	DiskBytes     int64 `json:"diskBytes"`
	MemoryBytes   int64 `json:"memoryBytes"`
}

// State is where a change set is in the pipeline.
type State string

// Change set states.
const (
	StatePlanned  State = "planned"
	StateApplying State = "applying"
	StateApplied  State = "applied"
	StateFailed   State = "failed"
	StateUndone   State = "undone"
)

// ChangeSet is a validated, planned set of changes and their consequences.
type ChangeSet struct {
	ID            store.ID         `json:"id"`
	Title         string           `json:"title"`
	Actor         *auth.Actor      `json:"actor,omitempty"`
	Changes       []ResourceChange `json:"changes"`
	Effects       []system.Effect  `json:"effects"`
	Contributions []Contribution   `json:"contributions,omitempty"`
	Conflicts     []Conflict       `json:"conflicts,omitempty"`
	Risk          store.Risk       `json:"risk" enum:"read,low,medium,high,critical"`
	Estimate      Estimate         `json:"estimate"`
	// Snapshot says the change set removes data, so before it is applied,
	// read-only snapshots are taken of the Btrfs subvolumes that hold what it
	// removes.
	Snapshot bool `json:"snapshot,omitempty"`
	// Snapshots are the snapshots taken. A restore can bring back what the
	// change set removed from them; they are deleted when the change set is
	// no longer kept.
	Snapshots []agent.Snapshot `json:"snapshots,omitempty"`
	DryRun    bool             `json:"dryRun,omitempty"`
	State     State            `json:"state" enum:"planned,applying,applied,failed,undone"`
	// Job carries out the system changes once the change set is applied.
	Job       store.ID   `json:"job,omitempty"`
	UndoOf    store.ID   `json:"undoOf,omitempty"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	AppliedAt *time.Time `json:"appliedAt,omitempty"`
}

// Clone returns a copy of cs whose actor, lists, maps and documents can be
// changed without affecting cs. Action steps keep sharing their inputs.
func (cs *ChangeSet) Clone() *ChangeSet {
	if cs == nil {
		return nil
	}
	c := *cs
	c.Actor = cs.Actor.Clone()
	if cs.Changes != nil {
		c.Changes = make([]ResourceChange, len(cs.Changes))
		for i, rc := range cs.Changes {
			c.Changes[i] = rc.Clone()
		}
	}
	c.Effects = slices.Clone(cs.Effects)
	c.Snapshots = slices.Clone(cs.Snapshots)
	if cs.Contributions != nil {
		c.Contributions = make([]Contribution, len(cs.Contributions))
		for i, con := range cs.Contributions {
			con.Changes, con.Effects = slices.Clone(con.Changes), slices.Clone(con.Effects)
			c.Contributions[i] = con
		}
	}
	if cs.Conflicts != nil {
		c.Conflicts = make([]Conflict, len(cs.Conflicts))
		for i, con := range cs.Conflicts {
			con.Sources = slices.Clone(con.Sources)
			c.Conflicts[i] = con
		}
	}
	if cs.AppliedAt != nil {
		t := *cs.AppliedAt
		c.AppliedAt = &t
	}
	return &c
}

// Summarize recomputes the estimate from the effects.
func (cs *ChangeSet) Summarize() {
	var e Estimate
	for _, eff := range cs.Effects {
		e.DownloadBytes += eff.DownloadBytes
		e.DiskBytes += eff.DiskBytes
		e.MemoryBytes += eff.MemoryBytes
	}
	cs.Estimate = e
}

// Change asks for one change to one resource.
type Change struct {
	Op   Op     `json:"op" enum:"create,update,delete,action" doc:"What to do"`
	Kind string `json:"kind" doc:"The kind of the resource, such as web.site"`
	// ID names the resource for update, delete and action. On create it may
	// be set to choose the ID, so later changes in the same request can
	// refer to the new resource.
	ID    store.ID    `json:"id,omitempty" doc:"The resource; on create, an ID chosen up front so later changes can refer to it"`
	Scope store.Scope `json:"scope,omitzero" doc:"The owner of a new resource"`
	// Server places a new resource on a cluster server; empty means the
	// server running the panel. Updates keep the placement.
	Server store.ID        `json:"server,omitempty" doc:"The cluster server a new resource is placed on; empty for the panel's own server"`
	Spec   json.RawMessage `json:"spec,omitempty" doc:"The desired state, for create and update"`
	// Labels replace the resource's labels; nil keeps them on update.
	Labels map[string]string `json:"labels,omitempty" doc:"Labels that replace the resource's own; left out, an update keeps them"`
	// Revision guards against lost updates; zero skips the check.
	Revision int64           `json:"revision,omitempty" doc:"The revision the change was made against; a newer one makes the change fail with a conflict"`
	Action   string          `json:"action,omitempty" doc:"The action to run, for action changes"`
	Input    json.RawMessage `json:"input,omitempty" doc:"The action's input"`
	// Secrets are values for the kind's sensitive fields, by field path.
	// They never enter the spec: when the change set is applied each value
	// goes to the secrets store, and the field gets a reference to it. A
	// dry run stores nothing. An empty value clears the field.
	Secrets map[string]string `json:"secrets,omitempty" doc:"Values for the kind's secret fields, by field path; they are stored apart and never shown again"`
}

func (c Change) clone() Change {
	out := c
	out.Spec = bytes.Clone(c.Spec)
	out.Labels = maps.Clone(c.Labels)
	out.Input = bytes.Clone(c.Input)
	out.Secrets = maps.Clone(c.Secrets)
	return out
}

// Request asks for a change set: one or more changes that are validated,
// previewed, applied and undone together. Changes are planned in order and
// each sees the resources the earlier ones create, update or delete, so a
// recipe can create a database and then a site that uses it.
type Request struct {
	Actor   *auth.Actor
	Changes []Change
	// Title describes the change set; when empty it is derived from the
	// changes.
	Title string
	// DryRun returns the change set without applying it.
	DryRun bool
	// IdempotencyKey makes retries of the same request return the first
	// change set instead of applying twice.
	IdempotencyKey string
}

// UndoRequest asks for a change set that reverts an applied one.
type UndoRequest struct {
	Actor *auth.Actor
	// ChangeSet is the change set to revert.
	ChangeSet      store.ID
	DryRun         bool
	IdempotencyKey string
}

// ApplyRequest asks for a preview to be applied as it was shown.
type ApplyRequest struct {
	Actor *auth.Actor
	// ChangeSet is the preview, a dry run.
	ChangeSet      store.ID
	IdempotencyKey string
}

// Pipeline is how anything changes Racksail.
type Pipeline interface {
	// Submit validates and plans a request. Unless it is a dry run, it then
	// applies it: the store is updated in one transaction and a job runs the
	// system changes.
	Submit(ctx context.Context, req Request) (*ChangeSet, error)
	// Get returns a change set.
	Get(ctx context.Context, id store.ID) (*ChangeSet, error)
	// Undo plans a change set that reverts an applied or failed one, built
	// from the recorded previous specs, and applies it unless it is a dry
	// run. It goes through the same stages as any other change.
	Undo(ctx context.Context, req UndoRequest) (*ChangeSet, error)
	// Apply applies a preview as it was shown: the request it came from is
	// planned again for the actor, and applied only when the plan is still
	// the one the preview showed. The preview becomes the applied change
	// set, keeping its ID.
	Apply(ctx context.Context, req ApplyRequest) (*ChangeSet, error)
	// List returns kept change sets, newest first, and the cursor of the
	// next page.
	List(ctx context.Context, q ListQuery) ([]*ChangeSet, string, error)
}

// Hooks runs hook points for the pipeline. The hooks package implements it.
type Hooks interface {
	// Validate runs validate hooks for one change. An error denies it.
	Validate(ctx context.Context, point string, cs *ChangeSet, change *ResourceChange) error
	// Modify runs the modify hooks of a plan point. changes are the indexes
	// in cs.Changes of the changes the point is about. Hooks may change the
	// specs of those, add changes and add effects, and nothing else; each
	// contribution is recorded and conflicts are reported in cs. The engine
	// authorizes, validates and diffs whatever they changed or added again.
	Modify(ctx context.Context, point string, cs *ChangeSet, changes []int) error
	// Applied runs observe hooks after a change set has been applied, for
	// the changes at the given indexes.
	Applied(ctx context.Context, point string, cs *ChangeSet, changes []int)
}
