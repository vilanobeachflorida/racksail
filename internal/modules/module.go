package modules

import (
	"context"
	"io/fs"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/hooks/rules"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Module is a bundled module. Only the manifest is required; every other
// capability is an optional interface below.
type Module interface {
	Manifest() *Manifest
}

// Services are the parts of the core a module works with.
type Services struct {
	Config   config.Config
	Store    store.Store
	Kinds    *store.KindRegistry
	Pipeline changeset.Pipeline
	Jobs     jobs.Queue
	Events   events.Bus
	Audit    events.AuditLog
	Agent    agent.Client
	Edge     edge.Router
	Secrets  secrets.Store
	// Hooks runs hook points outside the change pipeline, such as
	// backups.run.before, and its Registry lists providers and extensions.
	Hooks      *hooks.Runner
	Authorizer auth.Authorizer
	// Modules says which modules are on and turns them on and off.
	Modules *Manager
	// Rules runs the panel's CEL rules and tries rules out.
	Rules *rules.Engine
	// Addons runs installed add-ons and keeps safe mode.
	Addons *addons.Runtime
	// Notify sends notifications: alerts through the channels and
	// preferences people set, and messages for one person or address,
	// such as sign-in alerts and customer emails.
	Notify *notify.Service
	// Catalogs fetches and checks catalogs, and Recipes installs,
	// upgrades and uninstalls what they offer.
	Catalogs *recipes.Catalogs
	Recipes  *recipes.Engine
	Log      *slog.Logger
}

// KindProvider declares a module's kinds. Kind names must start with the
// module id.
type KindProvider interface {
	Kinds() []store.Kind
}

// ReconcilerProvider declares the reconcilers of a module's kinds.
type ReconcilerProvider interface {
	Reconcilers() []reconcile.Reconciler
}

// APIProvider adds API operations beyond the generated kind operations.
// Paths go under /api/v1/<module id>/.
type APIProvider interface {
	RegisterAPI(api huma.API, svc *Services)
}

// HookProvider registers a module's hooks: validate, modify and observe
// hooks on any point, system patches to the configuration of other modules'
// services, and providers of capabilities such as provide.dns.
type HookProvider interface {
	Hooks(svc *Services) []hooks.Registration
}

// EventProvider declares events beyond the generated kind events.
type EventProvider interface {
	Events() []events.Type
}

// MigrationProvider returns the module's database migrations, files named
// NNNN_description.sql.
type MigrationProvider interface {
	Migrations() fs.FS
}

// RecipeProvider returns the recipes a module ships, embedded from
// modules/<id>/recipes/, one <recipe id>.yaml each. The manifest lists their
// ids under recipes. The official catalog's recipes live in catalog/ instead.
type RecipeProvider interface {
	Recipes() fs.FS
}

// Backup says what data belongs to resources of a kind and how to quiesce
// it, for backups, restores and the portable account archive.
type Backup struct {
	Kind string
	// Paths returns the directories and files that hold a resource's data.
	Paths func(ctx context.Context, r *store.Resource) ([]string, error)
	// Quiesce and Resume return steps that pause and resume writes around a
	// snapshot, such as locking a database's tables. Either may be nil.
	Quiesce func(ctx context.Context, r *store.Resource) ([]agent.Step, error)
	Resume  func(ctx context.Context, r *store.Resource) ([]agent.Step, error)
	// Dump returns steps that write a consistent dump under dir, for data
	// that isn't plain files, such as databases. It may be nil.
	Dump func(ctx context.Context, r *store.Resource, dir string) ([]agent.Step, error)
}

// BackupProvider declares backup coverage of a module's kinds.
type BackupProvider interface {
	Backups() []Backup
}

// HealthCheck checks something a module runs.
type HealthCheck struct {
	// Name is short and unique within the module, such as "knot"; the
	// panel's health report shows it as "<module id>.<name>".
	Name  string
	Check func(ctx context.Context, svc *Services) error
}

// HealthProvider declares health checks. The panel's health report runs
// them while the module is on or active, side by side with the core's own.
type HealthProvider interface {
	HealthChecks() []HealthCheck
}

// OwnershipProvider says which reseller each account belongs to. The
// authorizer asks it when a reseller's grant is checked against one of their
// customers' accounts, so the module that keeps resellers implements it, and
// no other module may.
type OwnershipProvider interface {
	Ownership(svc *Services) auth.Ownership
}

// AccountProvider says where each account's add-ons run: in the account's
// slice, within what its plan allows. The module that keeps accounts
// implements it, and no other module may; until one does, accounts'
// add-ons don't run.
type AccountProvider interface {
	Accounts(svc *Services) addons.Accounts
}

// Hint helps the assistant use a module's tools: a short description and
// examples of requests, for a kind or one of its actions.
type Hint struct {
	Kind        string
	Action      string
	Description string
	Examples    []string
}

// HintProvider declares assistant hints.
type HintProvider interface {
	Hints() []Hint
}

// Starter is implemented by modules with work to start: Start runs when the
// module is turned on, and when the panel starts while the module is on or
// active. A module that fails to start stays off when it was being turned
// on, and is reported by the panel's health checks at boot.
type Starter interface {
	Start(ctx context.Context, svc *Services) error
}

// Stopper is implemented by modules with work to stop: Stop runs when the
// module is turned off, and when the panel stops. Turning a module off
// keeps its data and settings.
type Stopper interface {
	Stop(ctx context.Context) error
}
