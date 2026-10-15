package changeset

import (
	"context"
	"encoding/json"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
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
	// Revision is the revision the change was planned against.
	Revision int64             `json:"revision,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	// Before and After are the specs, kept so the change can be undone.
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Diff   []FieldDiff     `json:"diff,omitempty"`
	Source Source          `json:"source"`
	// Steps are agent operations an action runs once, on top of the
	// resource's reconcile.
	Steps []agent.Step `json:"steps,omitempty"`
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
	// Snapshot says a filesystem snapshot is taken before applying.
	Snapshot bool  `json:"snapshot,omitempty"`
	DryRun   bool  `json:"dryRun,omitempty"`
	State    State `json:"state" enum:"planned,applying,applied,failed,undone"`
	// Job carries out the system changes once the change set is applied.
	Job       store.ID   `json:"job,omitempty"`
	UndoOf    store.ID   `json:"undoOf,omitempty"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	AppliedAt *time.Time `json:"appliedAt,omitempty"`
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

// Request asks for one change.
type Request struct {
	Actor *auth.Actor
	Op    Op
	Kind  string
	// ID names the resource for update, delete and action; on create it may
	// be set to choose the ID.
	ID     store.ID
	Scope  store.Scope
	Server store.ID
	Spec   json.RawMessage
	Labels map[string]string
	// Revision guards against lost updates; zero skips the check.
	Revision int64
	Action   string
	Input    json.RawMessage
	// DryRun returns the change set without applying it.
	DryRun bool
	// IdempotencyKey makes retries of the same request return the first
	// change set instead of applying twice.
	IdempotencyKey string
	Title          string
}

// Pipeline is how anything changes Racksail.
type Pipeline interface {
	// Submit validates and plans a request. Unless it is a dry run, it then
	// applies it: the store is updated and a job runs the system changes.
	Submit(ctx context.Context, req Request) (*ChangeSet, error)
	// Get returns a change set.
	Get(ctx context.Context, id store.ID) (*ChangeSet, error)
	// Undo applies a change set that reverts an applied one.
	Undo(ctx context.Context, actor *auth.Actor, id store.ID) (*ChangeSet, error)
}

// Hooks runs hook points for the pipeline. The hooks package implements it.
type Hooks interface {
	// Validate runs validate hooks for one change. An error denies it.
	Validate(ctx context.Context, point string, cs *ChangeSet, change *ResourceChange) error
	// Modify runs modify hooks, which may add changes and effects; each
	// contribution is recorded and conflicts are reported in cs.
	Modify(ctx context.Context, point string, cs *ChangeSet) error
	// Applied runs observe hooks after a change set has been applied.
	Applied(ctx context.Context, point string, cs *ChangeSet)
}
