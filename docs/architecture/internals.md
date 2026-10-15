# Internals

How the code fits together, for people working on Racksail. The other documents here say what Racksail is and why; this one says where things live and how to add to them. It describes the code as it is, so update it in the same pull request when that changes.

## Running things

```sh
make dev                     # racksail dev on 127.0.0.1:7070 plus the UI dev server on 127.0.0.1:5173
make check                   # everything CI runs
go run ./cmd/racksail dev    # just the panel, agent and edge, serving the embedded UI if it's built
go test ./internal/store     # one package
go test ./internal/system -update   # rewrite golden files after an intended change
```

`racksail dev` keeps everything under `.dev/` (or `--data`): configuration in `etc/`, state in `state/`, sockets in `run/`. Everyone who reaches it is signed in as the admin, which is why it refuses to listen on anything but loopback addresses. The panel listens on `--addr` (127.0.0.1:7070 by default), and the edge on `--edge-http` and `--edge-https` (127.0.0.1:7080 and 127.0.0.1:7443) once a module adds a route. The simulated agent and the edge also answer on their Unix sockets when the path is short enough for one.

To run several instances side by side, as separate checkouts do, give each its own addresses and data directory. Port 0 lets the edge pick free ports, which it logs when it starts:

```sh
make dev DEV_ADDR=127.0.0.1:7170 DEV_EDGE_HTTP=127.0.0.1:0 DEV_EDGE_HTTPS=127.0.0.1:0 DEV_DATA=.dev-2
```

The UI dev server moves to the next free port by itself when 5173 is taken, and proxies `/api` to `DEV_ADDR`.

Other commands that help during development:

| Command | What it does |
|---|---|
| `racksail status --server http://127.0.0.1:7070` | Asks a panel for its health and version |
| `racksail openapi` | Prints the API's OpenAPI document; `--agent` prints the agent protocol and `--addon` the add-on protocol |
| `racksail theme css [FILE]` | Renders a theme as CSS custom properties; `theme validate FILE` checks one |
| `racksail install --answers FILE --dry-run` | Shows the steps a convert-mode answers file would take on this server; `--sim` carries them out on a simulated server. Install and restore modes erase disks, so they only run from the installer image |
| `racksail agent --sim` | Runs a standalone agent against a simulated server, without root. It listens in a directory of your own under `$XDG_RUNTIME_DIR` or the temporary directory, and `--socket` picks another path |

## What isn't kept yet

Everything the panel holds lives in memory for now, in `racksail panel` as much as in `racksail dev`, and the panel logs a warning about it when it starts:

- Resources and their history live in `store.Memory`. The production store is SQLite (decision T3) behind the same `store.Store` interface.
- Secrets live in `secrets.Memory`, encrypted with a key made at every start. On a server the key will be sealed with systemd-creds (decision T12) and kept.
- Change sets live in the change pipeline's engine, within its `Retention`: dry runs for an hour, at most 200, and finished change sets for seven days, at most 1000. `Get` and `Undo` only reach those, and none survive a restart.
- Jobs and their logs live in `jobs.Memory`, within its `Retention`: finished jobs for seven days, at most 2000.
- Module states live in the module manager.

Code written against the interfaces won't change when these are kept on disk, so don't add another way to keep something in the meantime.

## The binary

`cmd/racksail` is the only binary. `main.go` runs the cobra root command from `root.go`, which groups the commands:

- **Run Racksail:** `panel`, `agent`, `edge`, `dev` and `install`, one file each.
- **Manage a server:** commands that call a panel's API. They're built from `clientCommands` in `client.go`, a list of builder functions that get the shared flags (`--server`, `--ca-file`, `--output`). Commands that change something are wrapped in `mutating(cmd)`, which also gives them `--dry-run`. `status` is the only client command so far; the commands generated from the OpenAPI document are to append their builders to the list from `init` functions, wrapping every operation that isn't a GET in `mutating`.
- **Tools:** `openapi`, `version` and `theme`.

`core.go` wires the panel: the store, the event bus, the job queue, the secrets store, the hook registry and runner, the reconciler registry and runner and the change pipeline, which modules reach through `modules.Services`, plus the module manager. It starts the modules that are on, notifies `core.boot`, and reports modules that didn't start through the `modules` health check rather than refusing to run, so they can still be turned off. Both `panel` and `dev` build the same core; they differ in the agent client (the agent's Unix socket, or a simulated machine in the same process) and in how they listen.

The binary refuses to start if any registry recorded a problem while packages initialized (`checkRegistries`): an error code defined twice, a malformed agent operation, a duplicate job type, a bad manifest. The same check runs in the tests, so these mistakes never reach a release.

## Packages

| Package | What it holds |
|---|---|
| `internal/store` | Resources, kinds, TypeIDs, the `Store` interface, its in-memory implementation and the `Overlay` the pipeline plans against |
| `internal/changeset` | The change pipeline: requests, diffs, plans, previews, apply jobs, undo, retention and secret values |
| `internal/reconcile` | The reconciler interface, the typed adapter and the runner |
| `internal/agent` | The typed operation allowlist, the agent's HTTP server and the clients that call it |
| `internal/system` | Adapter interfaces for everything the agent touches, and the pure renderers for unit files and apt sources |
| `internal/system/linux` | The real adapters, for Debian 13 |
| `internal/system/sim` | A simulated Debian 13 server implementing every adapter in memory |
| `internal/modules` | Manifests, the `Module` interface and its optional capabilities, the registry and the module manager |
| `internal/api` | The HTTP API: middleware, health and version, module operations, read authorization, the change parameters, the OpenAPI export |
| `internal/api/httpapi` | Problem details, error mapping and schema naming shared by the panel API, the agent protocol and the add-on protocol |
| `internal/hooks` | Hook points, registrations, providers and extensions, the hook runner, the add-on manifest and protocol, and the rule spec |
| `internal/auth` | Actors, grants, roles and the central authorizer |
| `internal/events` | The event bus, the timeline and the audit log |
| `internal/jobs` | The job queue with progress, logs, cancel and retry |
| `internal/i18n` | Error codes and their English messages |
| `internal/edge` | The routing table and the server that answers from it |
| `internal/config` | `racksail.toml` |
| `internal/secrets` | Secret storage, encrypted at rest |
| `internal/theme` | Theme parsing, validation, contrast checks and CSS rendering |
| `internal/notify` | Notification channels |
| `internal/recipes` | Recipes, their inputs and catalog indexes |
| `internal/installer` | Answers files and the install, convert and restore plans |
| `internal/updates` | Build information, release channels and versions |
| `internal/webui` | The embedded UI and the page shown when a binary was built without it |
| `spec` | The JSON Schemas in `spec/schemas/` and the code that validates documents against them |
| `modules/<id>` | One package per module |
| `modules/all` | Imports every module and lists their ids |

Dependencies point one way: `system` knows nothing of Racksail; `agent` builds on `system`; `reconcile` and `changeset` build on `agent` and `store`; `modules` sees all of them; `api` and `cmd/racksail` sit on top. Modules never import each other. When one module needs another's data, it reads that module's resources through the store.

The other top-level directories of [conventions.md](conventions.md), `catalog/`, `iso/`, `packaging/`, `test/` and `sdk/`, each have a README saying what goes in them.

## Resources and kinds

Everything Racksail manages is a resource of a kind. A kind is defined once, in a package variable, with Go types for its spec and status:

```go
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
	Indexes: []store.Index{{Name: "name", Path: "name", Unique: true, Fold: true}},
	UI: store.UIHints{Fields: map[string]store.FieldHint{"tsigKey": {Sensitive: true}}},
})
```

- The name is `<module>.<name>` and the prefix is short and unique; `Define` records problems, and registration refuses a kind that has any. Some prefixes are reserved for IDs that aren't resources (change sets, jobs, events and so on).
- `KindInfo` also declares actions (with risk levels, inputs, outputs and whether they're long, destructive or reversible), unique indexes, UI hints and backup coverage. Everything generated for the kind comes from it, so descriptions are written for people: they document the API, label CLI help and describe assistant tools.
- An action's event is `<kind>.<past tense>`, derived from its name: `deploy` gives `web.site.deployed`, `reset_password` gives `web.site.password_reset` and `spin_down` gives `storage.disk.spun_down`. Set `Past` when the derived form is wrong; event names are a public contract that webhooks and automations rely on.
- Fields that hold secrets are marked `Sensitive` in the UI hints. Their paths are camelCase fields joined by dots, and their value is always a reference to one of the resource's own secrets; see [Secrets](#secrets).
- `WithDefaults` and `WithValidator` add typed defaulting and validation. Validation errors use `i18n.Invalid` with field errors, so the UI can highlight fields.
- `Zone.Decode`, `Zone.Encode`, `Zone.Get` and `Zone.List` convert between the untyped `store.Resource` that generic code passes around and the typed `store.Object[ZoneSpec, ZoneStatus]`. `Zone.Get` reports a resource of another kind as not found.

The store keeps a revision per resource for optimistic concurrency (status writes don't change it), bumps the generation whenever the desired state changes, soft-deletes resources until their reconciler has removed what they created, and keeps every spec version for history and undo.

## The change pipeline

Nothing writes to the store directly. The API, the CLI, recipes, automations, add-ons and the assistant all call `changeset.Pipeline`. A request holds one or more changes, and they become one change set: one preview, one store transaction, one apply job and one undo.

```go
cs, err := svc.Pipeline.Submit(ctx, changeset.Request{
	Actor:  actor,
	DryRun: true,
	Changes: []changeset.Change{{
		Op: changeset.OpCreate, Kind: "dns.zone", Scope: scope, Spec: spec,
	}},
})
```

The changes of a request are planned in order against a `store.Overlay`, so each one sees what the earlier ones create, update or delete. Choose IDs up front to refer from one change to another:

```go
dbID := store.NewID("db")
cs, err := svc.Pipeline.Submit(ctx, changeset.Request{
	Actor: actor,
	Title: "Create example.com with a database",
	Changes: []changeset.Change{
		{
			Op: changeset.OpCreate, Kind: "databases.database", ID: dbID, Scope: scope,
			Spec:    json.RawMessage(`{"name":"example"}`),
			Secrets: map[string]string{"owner.password": password},
		},
		{
			Op: changeset.OpCreate, Kind: "web.site", Scope: scope,
			Spec: json.RawMessage(`{"domains":["example.com"],"database":"` + string(dbID) + `"}`),
		},
	},
})
```

For each change in turn, `Engine.Submit` fills in the kind's defaults, asks the authorizer, takes the change's secret values, runs the kind's validation and the validate hooks, then asks the resource's reconciler for a plan and runs its steps in the agent's check mode, so the preview lists the exact downloads, packages, files and ports. Reconcilers plan against the overlay too, so the site's reconciler can read the database it uses before either exists. Modify hooks then run once per plan point, with the indexes of the changes the point is about. What they add is labelled with its source, two of them changing the same field is reported as a conflict, and whatever they changed is validated again.

A dry run then makes the store writes in a transaction it throws away, so the preview also reports a name that's already taken, and returns the change set. Otherwise every change is written in one store transaction and a `changeset.apply` job runs the plans with live progress. Events and audit records follow, including for denied attempts. When a request has several changes, field errors say which change they belong to: `changes[1].spec.domains`.

- `Undo(ctx, changeset.UndoRequest{Actor, ChangeSet, DryRun, IdempotencyKey})` turns the recorded previous specs, labels and placement into a new request (creates become deletes, deletes become creates with the same ID) and sends it through the same stages, so an undo is authorized, validated, hooked and previewed like any change. Applied and failed change sets can be undone; an action that changed no settings can't.
- A retry with the same `IdempotencyKey` by the same actor returns the first change set for 24 hours, as long as it's kept. A second request with the key while the first is still running gets `changeset.duplicate_request`.
- `Change.Server` places a new resource on a cluster server. Updates keep the placement, and undoing a delete puts the resource back where it was.

## Secrets

Secret values never enter specs, so they never reach change sets, history, events, hooks or previews. A sensitive field holds a reference to one of the resource's own secrets, `<resource id>/<name>`, and the pipeline refuses anything else there, including references to another resource's secrets. Values come in with a change:

```go
changeset.Change{
	Op: changeset.OpUpdate, Kind: "mail.mailbox", ID: id, Spec: spec,
	Secrets: map[string]string{"password": newPassword},
}
```

When the change set is applied (never in a dry run), the pipeline stores each value in `svc.Secrets` under a new name, such as `mbox_01j9.../password.chg_01j9...`, and puts that reference in the field. Because each change set gets new names, undo puts the previous reference back and the previous value is still there. Versions nothing refers to any more are deleted once the change sets that used them are no longer kept.

- Reconcilers resolve references at the moment they need a value, with `req.Secrets.Get(ctx, secrets.Ref(spec.Password))`. During a preview, that also finds the values the change set brings.
- Action inputs are recorded in the change set, so actions never take secrets as input. Setting a secret is an update with the value in `Secrets`.
- A resource that needs another resource's secret, such as a site that needs its database user's password, reads that resource's spec through `req.Read` and resolves the reference from there.
- Recipe inputs of type `password` and `secret` can only be used in sensitive fields, and `Recipe.Request` moves their values into `Secrets`.
- Secrets a module manages itself, such as generated DKIM keys, go straight to `svc.Secrets` under names of its own. The pipeline only deletes the versions it made.

## Reconcilers

A reconciler makes the machine match one kind's resources. Plan is pure: it reads the request and returns agent steps. Apply runs them; `reconcile.ApplySteps` does that and sets the phase and the `Ready` condition.

```go
type zoneReconciler struct{}

func (zoneReconciler) Plan(ctx context.Context, z *store.Object[ZoneSpec, ZoneStatus], req reconcile.Request) (reconcile.Plan, error) {
	file := "/etc/knot/zones/" + z.Spec.Name + ".zone"
	if z.Deleted() {
		return reconcile.Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: file})}}, nil
	}
	return reconcile.Plan{Steps: []agent.Step{
		agent.WriteFile.Step(agent.WriteFileInput{Path: file, Content: renderZone(z.Spec), Mode: 0o640}),
		ReloadZones.Step(ReloadZonesInput{}),
	}}, nil
}

func (zoneReconciler) Apply(ctx context.Context, z *store.Object[ZoneSpec, ZoneStatus], plan reconcile.Plan, ex reconcile.Executor) (ZoneStatus, error) {
	st := z.Status
	err := reconcile.ApplySteps(ctx, z.Generation, plan, ex, &st.Status, time.Now())
	return st, err
}
```

The module returns `reconcile.Typed(Zone, zoneReconciler{})` from `Reconcilers()`. Plans must be idempotent: every operation reports `Changed: false` when its effect is already in place, so running a plan twice changes nothing, and the runner can resync everything on a timer. When a resource is deleted, the plan removes what earlier plans created; the runner purges the resource once that has worked. Reconcilers of kinds with actions also implement `reconcile.ActionPlanner`.

`req.Read` reads other resources: the store, or during a preview the change set's planned state. `req.Facts` describes the machine the resource is placed on, and `req.Secrets` resolves secret references.

`internal/reconcile/reconcile_test.go` is a complete small example, run against the simulated machine, and `internal/changeset/changeset_test.go` shows kinds with secrets and change sets with several changes.

## Agent operations

The agent runs as root and only runs operations from its allowlist. Each is registered in a package variable:

```go
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
```

- Names are CamelCase. Core operations have bare names (`WriteFile`); a module's operations start with its id (`dns.ReloadZones`).
- `Validate` checks every field before anything runs: paths absolute, clean and inside allowed roots, names matching strict patterns, sizes bounded.
- `Run` works through `env.System`, never through `os` or `exec` directly, so it runs the same against the simulated machine. External programs get a fixed path and an argument vector; there is no shell and no operation that runs an arbitrary command.
- In check mode (`env.Check`) an operation reports what it would change without changing it. `Effects` describes a step without asking the machine, for previews where check mode can't run; `ReadOnly` marks operations that never change anything.
- Inputs and outputs travel as JSON, up to about 86 MiB on the socket: enough for the largest file `WriteFile` writes, base64-encoded. Keep bulk data such as archives out of inputs; operations download it themselves.
- What file, unit and package operations may touch is fixed when the binary starts. Modules extend it from `init` functions: `agent.AllowWrite("/etc/knot")`, `agent.AllowRead(...)`, `agent.AllowUnit("knot.service")`, `agent.AllowRootUnit(...)` for platform services only, and `agent.RegisterAptSource(...)` for vendor repositories, which the panel can then only name.

Operations registered anywhere in the binary are in the agent's allowlist, because `cmd/racksail` imports every module through `modules/all`. After adding or changing one, run `make gen` to update `spec/agent-protocol.json`; a test fails until you do.

## System adapters

`system.Backend` is a struct of interfaces, one per area: facts, users, files, units, packages, Btrfs, mounts, disks, firewall, network, sysctl, processes, certificates and program execution.

- `internal/system/linux` implements them for Debian 13. Code that only builds on Linux, such as D-Bus access to systemd, sits behind the `linux` build tag; everything else takes injected dependencies (a command runner, a root directory, a systemd connection), so its tests run on any machine with fakes.
- `internal/system/sim` implements them in memory. A new `sim.Machine` looks like a freshly installed server with a few empty disks and a network interface, and every operation changes it the way it would change a real one. Modules teach it about their software with `sim.RegisterPackage` (what a package installs) and `sim.RegisterProgram` (what a program does when run), from `init` functions in their simulation code. Tests inspect `Calls()` and make the next operation fail with `FailNext`.
- Rendering is separate from applying. Configuration files are produced by pure functions such as `system.UnitFile.Render` and `system.AptSource.Render`, with golden files in `testdata/`; `go test -update` rewrites them after an intended change.

## Modules

A module is a package in `modules/<id>` with an embedded manifest:

```go
//go:embed manifest.yaml
var manifest []byte

// Module is the DNS module.
type Module struct {
	modules.Base
}

func init() {
	modules.Register(&Module{Base: modules.NewBase(manifest)})
}
```

`manifest.yaml` is validated by `spec/schemas/module.schema.json`, and tests check it against the profiles and dependencies in [modules.md](modules.md). Everything beyond the manifest is an optional interface the module implements:

| Interface | Method | Adds |
|---|---|---|
| `KindProvider` | `Kinds() []store.Kind` | Kinds, registered with the store when the module registers |
| `ReconcilerProvider` | `Reconcilers() []reconcile.Reconciler` | Reconcilers for those kinds |
| `APIProvider` | `RegisterAPI(api huma.API, svc *Services)` | Operations beyond the generic kind operations, under `/api/v1/<id>/` |
| `HookProvider` | `Hooks(svc *Services) []hooks.Registration` | Hooks: validate, modify and observe hooks, patches to other services' configuration, and providers of capabilities |
| `EventProvider` | `Events() []events.Type` | Event types beyond the generated kind events |
| `MigrationProvider` | `Migrations() fs.FS` | Database migrations |
| `RecipeProvider` | `Recipes() fs.FS` | Recipes the module ships |
| `BackupProvider` | `Backups() []Backup` | What data belongs to each kind and how to quiesce it |
| `HealthProvider` | `HealthChecks() []HealthCheck` | Health checks for what the module runs |
| `HintProvider` | `Hints() []Hint` | Descriptions and examples that help the assistant |
| `Starter`, `Stopper` | `Start`, `Stop` | Work to start when the module is turned on and to stop when it's turned off |

A few rules that aren't obvious from the types:

- `RegisterAPI` runs when `racksail openapi` exports the document, with empty services. Register operations and nothing else there; use `svc` only inside handlers.
- Schema names of module types get the module's name in front (`dns.ZoneSpec` becomes `DnsZoneSpec`), so two modules can use the same type names.
- Errors are package variables: `var ErrZoneTaken = i18n.Define("dns.zone_taken", http.StatusConflict, "{zone} is already hosted on this server.")`, returned as `ErrZoneTaken.New("zone", name)`. Every code the binary can return needs a message with the same English text in the UI, named `error.<code>`, in `ui/src/modules/<id>/messages/en.json` (the shell's catalog for core codes). `TestErrorCodesHaveUIMessages` in `cmd/racksail` checks this.
- Job types are registered with `jobs.RegisterType(jobs.Type{Name: "dns.check", Run: ...})` from `init`.
- Hook points for every operation and stage of every kind exist automatically (`dns.zone.create.validate`, `dns.zone.delete.applied` and so on). A module's manifest declares any others it adds under `hooks`: `system.<service>.<part>` points for configuration it renders, and `provide.<name>` points for capabilities others implement for it.
- `Start` runs when the module is turned on, and when the panel starts while the module is on or active; `Stop` runs when it's turned off and when the panel stops. A module that fails to start while being turned on stays off. After a module is turned on or off, the manager notifies `modules.<id>.on` or `modules.<id>.off` with a `modules.Transition`.

## Recipes

The official catalog's recipes live in `catalog/`. A module can also ship recipes of its own, such as presets it always offers: they go in `modules/<id>/recipes/<recipe id>.yaml`, embedded and returned from `Recipes()`, and the manifest lists their ids under `recipes`. `TestShippedRecipesMatchTheManifests` checks that the two agree.

Whatever installs a recipe (the marketplace, the setup wizard, an automation) checks the file, checks the person's inputs, turns the recipe into one change set request and submits it as the person:

```go
rec, err := recipes.Parse(data)
if err != nil {
	return nil, err
}
values, err := rec.Values(inputs)
if err != nil {
	return nil, err
}
req, err := rec.Request(values, store.AccountScope(account), svc.Kinds)
if err != nil {
	return nil, err
}
req.Actor, req.DryRun = actor, dryRun
return svc.Pipeline.Submit(ctx, req)
```

`Recipe.Request` chooses the resources' IDs up front, so `${resources.db.id}` in one resource's spec refers to another, and orders the resources by `dependsOn`. The whole install is then one change set, previewed, applied and undone as a whole.

## Hooks

Hooks attach to points. A module returns its hooks from `Hooks(svc)`; the add-on runtime registers an add-on's hooks from its manifest and calls the add-on over its socket with the same data, described in `spec/addon-protocol.json` (`racksail openapi --addon`).

```go
// One hook of each kind, as four modules would return them from Hooks.
var examples = []hooks.Registration{{
	// Allow or deny one change. Returning a coded error denies it, with that
	// message; a security hook also denies it when it fails or times out,
	// because its failure mode is block.
	Point: hooks.KindPoint("web.site", "deploy", changeset.StageValidate),
	Kind:  hooks.Validate, Owner: "module:security",
	Security: true, Failure: hooks.Block, Deadline: 5 * time.Second,
	OnValidate: func(ctx context.Context, c *hooks.Context) error {
		if shell := findWebShell(ctx, c.Change); shell != "" {
			return ErrWebShell.New("file", shell)
		}
		return nil
	},
}, {
	// Change the plan before the preview. c.Changes are the changes the
	// point is about; what the hook changes is labelled as its own.
	Point: hooks.KindPoint("backups.policy", "create", changeset.StagePlan),
	Kind:  hooks.Modify, Owner: "module:cluster",
	Priority: 10, Failure: hooks.Allow, Deadline: time.Second,
	OnModify: func(ctx context.Context, c *hooks.Context) error {
		for _, i := range c.Changes {
			after, err := addStandbyDestination(c.ChangeSet.Changes[i].After)
			if err != nil {
				return err
			}
			c.ChangeSet.Changes[i].After = after
		}
		return nil
	},
}, {
	// React once the change set is applied, in the background.
	Point: hooks.KindPoint("web.site", "deploy", changeset.StageApplied),
	Kind:  hooks.Observe, Owner: "module:monitoring",
	OnObserve: func(ctx context.Context, c *hooks.Context) {
		for _, i := range c.Changes {
			markDeploy(c.ChangeSet.Changes[i].ID, c.ChangeSet.AppliedAt)
		}
	},
}, {
	// Patch another service's generated configuration at plan time.
	Point: "system.caddy.config", Kind: hooks.System, Owner: "module:performance",
	OnConfig: func(ctx context.Context, p *hooks.ConfigPatch) error {
		p.Config["encode"] = []string{"zstd", "gzip"}
		return nil
	},
}}
```

- Hooks run in a fixed order, by priority and then owner. Synchronous hooks get `Deadline` (two seconds by default, ten at most). When one fails or times out, its `Failure` mode decides: `hooks.Allow` lets the change through and is the default, `hooks.Block` stops it and is the default for `Security` validate hooks. A hook that fails five times in a row is switched off and `core.hook.disabled` is published.
- Points outside the change pipeline are run by the code that reaches them: `svc.Hooks.Check(ctx, point, actor, data)` runs validate hooks, as for `auth.login.validate`, and `svc.Hooks.Notify(ctx, point, actor, data)` runs observe hooks, as for `backups.run.after`. Hooks find what happened in `c.Data`. The core notifies `core.boot`, and the module manager `modules.<id>.on` and `.off`.

### Providers

A provide hook registers one implementation of a capability: `provide.dns`, `provide.registrar`, `provide.backup-target`, `provide.notifier`, `provide.ai`, `provide.billing`, `provide.importer`, `auth.provider` and `auth.mfa`, and the `provide.<name>` points modules declare. Modules register their built-in providers from `Hooks(svc)`; the add-on runtime registers the ones add-ons declare under `provides` when they're installed, and removes them with `Registry.Unregister`.

```go
{
	Point: hooks.ProvideNotifier, Kind: hooks.Provide, Owner: "module:home",
	Provider: &hooks.Provider{
		Name: "gotify", Title: "Gotify", Description: "Push notifications through your own Gotify server.",
		Settings: GotifySettings{},
		Open: func(ctx context.Context, settings json.RawMessage) (any, error) {
			return newGotify(settings, svc.Secrets)
		},
	},
}
```

The code that needs the capability lists providers with `svc.Hooks.Registry.Providers(point)`, for people to choose from, and connects the chosen one with its saved settings:

```go
ch, err := hooks.Open[notify.Channel](ctx, svc.Hooks.Registry, hooks.ProvideNotifier, "gotify", settings)
```

The consumer of a capability also defines its interface: in `internal/hooks/provide_<capability>.go` for the provide points modules share, so the DNS module and the home module's dynamic DNS see the same `provide.dns` interface; in `internal/auth` for `auth.provider` and `auth.mfa`; and `notify.Channel` for `provide.notifier`, offered next to the channel types the notify package registers itself. Provider settings are a kind's spec like any other, so secret fields in them hold references.

### Extensions

Extend hooks are how add-ons add to the panel declaratively. The add-on runtime registers one per UI entry in the add-on's manifest, at `ui.nav`, `ui.tab`, `ui.card`, `ui.command`, `ui.settings` or `ui.column`, and one at `api.routes` with a handler for the routes it serves under `/api/v1/addons/<id>/`. `Registry.Extensions(point)` lists them for the API and the UI. Modules don't use extend hooks: they add operations through `APIProvider` and screens through their UI entry.

## The API

`internal/api` builds the API on Huma with the standard library's router. Errors become RFC 9457 problem details with the error's code and parameters, whatever produced them: `httpapi.NewError` replaces Huma's error constructor, and errors without a code become `core.internal`, logged and never shown. Paths under `/api` that no operation matches answer `404` with `core.not_found`, or `405` with `core.method_not_allowed` and an `Allow` header when the path has operations for other methods.

The API server doesn't generate operations for kinds yet. The generic list, get, create, update, delete and action operations, under `/api/v1/<module>/<plural>` as [conventions.md](conventions.md) describes, are meant to be built once for every kind from the kind registry, with the helpers below, so modules don't write them by hand. `APIProvider` is for operations beyond them.

### Access

`api.Protected` as an operation's security requirement only establishes who is calling: without a signed-in actor, the request is refused with 401. What the caller may do is decided by the central authorizer, `svc.Authorizer`, every time. Changes are authorized change by change in the pipeline, but reads never go through it, so every handler that returns data authorizes it first:

```go
type delegationInput struct {
	ID store.ID `path:"id" doc:"The zone's ID"`
}

func (m *Module) RegisterAPI(a huma.API, svc *modules.Services) {
	huma.Register(a, huma.Operation{
		OperationID: "checkDnsDelegation",
		Method:      http.MethodGet,
		Path:        api.Prefix + "/dns/zones/{id}/delegation",
		Summary:     "Check a zone's delegation",
		Description: "Asks the parent zone which name servers it delegates to, and compares them with this server's.",
		Tags:        []string{"DNS"},
		Security:    api.Protected,
	}, func(ctx context.Context, in *delegationInput) (*delegationOutput, error) {
		zone, err := Zone.Get(ctx, svc.Store, in.ID)
		if err != nil {
			return nil, err
		}
		// A zone of another account is reported as not found.
		if err := api.AuthorizeRead(ctx, svc.Authorizer, zone.Meta); err != nil {
			return nil, err
		}
		return checkDelegation(ctx, zone)
	})
}
```

A list authorizes the scope it lists with `api.Authorize(ctx, svc.Authorizer, api.ReadPermission(kind), auth.Target{Scope: scope})`, queries the store for that scope only, so pages stay full, and passes the page through `api.Readable`, which also covers grants on single resources. Anything else an operation does is checked the same way, with the permission it needs.

### Changes

An operation that changes something embeds `api.ChangeParams`, which adds the `dryRun` query parameter and the `Idempotency-Key` header every mutating operation accepts, and answers through `api.Submit`: 200 with the preview for a dry run, and otherwise 202 with the change set, whose `job` carries out the system changes.

```go
var ErrBadZoneFile = i18n.Define("dns.bad_zone_file", http.StatusUnprocessableEntity, "The zone file can't be read: {reason}.")

type importInput struct {
	api.ChangeParams
	Body struct {
		Account store.ID `json:"account" doc:"The account the zone belongs to"`
		Name    string   `json:"name" doc:"The zone's domain name" example:"example.com"`
		File    string   `json:"file" doc:"The zone file, in the format of RFC 1035"`
	}
}

// In RegisterAPI:
huma.Register(a, huma.Operation{
	OperationID: "importDnsZone",
	Method:      http.MethodPost,
	Path:        api.Prefix + "/dns/zones/import",
	Summary:     "Import a zone file",
	Description: "Creates a zone and its records from a zone file, as one change you can preview and undo.",
	Tags:        []string{"DNS"},
	Security:    api.Protected,
}, func(ctx context.Context, in *importInput) (*api.ChangeSetOutput, error) {
	changes, err := zoneFileChanges(store.AccountScope(in.Body.Account), in.Body.Name, in.Body.File)
	if err != nil {
		return nil, ErrBadZoneFile.New("reason", err.Error())
	}
	return api.Submit(ctx, svc.Pipeline, in.ChangeParams, changeset.Request{
		Title: "Import " + in.Body.Name, Changes: changes,
	})
})
```

`api.Submit` fills in the signed-in actor; the pipeline authorizes each change for it. The OpenAPI document is generated from the code and committed as `spec/openapi.json`. Change an operation, run `make gen`, and commit both.

## The web UI

The UI lives in `ui/`: React 19, TypeScript, Vite, TanStack Router and Query, Tailwind CSS v4 and shadcn/ui on Radix. `npm run build` writes it to `internal/webui/dist/app`, which the binary embeds.

- `src/app/` is the shell: providers, the router, the frame with the icon rail and top bar, the home screen and error screens.
- `src/design/` holds `theme.css` (generated by `racksail theme css`), `globals.css`, which maps the tokens onto Tailwind and shadcn names, and the components.
- `src/lib/` holds the API client (`api/schema.d.ts` is generated from `spec/openapi.json` by `npm run gen:api`), i18n and helpers.
- `src/modules/registry.ts` lists every module with a lazy import of its entry file, `src/modules/<id>/index.ts`. An entry is a `ModuleUI` (see `src/modules/types.ts`): the module's name, description and icon, and optionally navigation entries, routes, home cards, settings pages and command-bar actions. Screens, cards and pages are loaded the first time they're shown.

```ts
import { Globe, Plus } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "web",
  name: m("web.name"),
  description: m("web.description"),
  icon: Globe,
  nav: [{ id: "sites", label: m("web.nav.sites"), icon: Globe, to: "/sites", permission: "web.site.read" }],
  routes: [
    { path: "/sites", title: m("web.sites.title"), screen: () => import("./screens/sites") },
    { path: "/sites/$siteId", title: m("web.site.title"), screen: () => import("./screens/site") },
  ],
  homeCards: [{ id: "traffic", title: m("web.cards.traffic"), card: () => import("./cards/traffic"), size: "medium" }],
  settings: [{ id: "php", title: m("web.settings.php"), page: () => import("./settings/php") }],
  commands: [
    {
      id: "new-site",
      label: m("web.commands.newSite"),
      icon: Plus,
      keywords: ["website", "domain"],
      permission: "web.site.create",
      run: ({ navigate }) => navigate("/web/sites?new=1"),
    },
  ],
});
```

Navigation entries and routes use paths relative to the module, so `/sites` is `/web/sites`; settings pages are mounted at `/settings/<module id>/<page id>`, and commands navigate to full paths. Every lazily imported file default-exports its component.

Each module's messages live in `src/modules/<id>/messages/en.json`, with ids starting with the module's id (or `error.<id>.` for its error codes); `messageIds` turns a catalog's ids into typed descriptors. The shell's own messages are in `src/app/messages/en.json`.

Components take colors, spacing, radii and type only from tokens. Tailwind's default palette is removed, and `scripts/check-tokens.mjs`, part of `npm run lint`, reports hard-coded colors. Tests use Vitest and Testing Library; `src/app/app.test.tsx` shows how to render the shell or a module's contributions against a fake API.

## Generated files

| File | Generated by | Checked by |
|---|---|---|
| `spec/openapi.json` | `racksail openapi` | `TestOpenAPIMatchesTheCommittedDocuments` |
| `spec/agent-protocol.json` | `racksail openapi --agent` | the same test |
| `spec/addon-protocol.json` | `racksail openapi --addon` | the same test |
| `ui/src/design/theme.css` | `racksail theme css` | `TestThemeCSSMatchesTheUI` |
| `ui/src/lib/api/schema.d.ts` | `npm run gen:api` | `npm run check:api`, part of `make check` |

`make gen` regenerates all of them.
