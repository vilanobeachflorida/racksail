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

`racksail dev` keeps everything under `.dev/` (or `--data`): configuration in `etc/`, state in `state/`, sockets in `run/`. Everyone who reaches it is signed in as the admin, which is why it refuses to listen on anything but a loopback address. The simulated agent and the edge also answer on their Unix sockets when the path is short enough for one.

Other commands that help during development:

| Command | What it does |
|---|---|
| `racksail status --server http://127.0.0.1:7070` | Asks a panel for its health and version |
| `racksail openapi` | Prints the API's OpenAPI document; `--agent` prints the agent protocol |
| `racksail theme css [FILE]` | Renders a theme as CSS custom properties; `theme validate FILE` checks one |
| `racksail install --answers FILE --dry-run` | Shows the install plan for an answers file; `--sim` carries it out on a simulated server |
| `racksail agent --sim` | Runs a standalone agent against a simulated server |

## The binary

`cmd/racksail` is the only binary. `main.go` runs the cobra root command from `root.go`, which groups the commands:

- **Run Racksail:** `panel`, `agent`, `edge`, `dev` and `install`, one file each.
- **Manage a server:** commands that call a panel's API. They're built from `clientCommands` in `client.go`, a list of builder functions that get the shared flags (`--server`, `--ca-file`, `--output`, `--dry-run`). Hand-written commands such as `status` sit next to the commands generated from the OpenAPI document, which append themselves to the list from `init` functions.
- **Tools:** `openapi`, `version` and `theme`.

`core.go` wires the panel: the store, the event bus, the job queue, the hook registry and runner, the reconciler registry and runner, the change pipeline and the secrets store, which modules reach through `modules.Services`, and the module manager. Both `panel` and `dev` build the same core; they differ in the agent client (the agent's Unix socket, or a simulated machine in the same process) and in how they listen.

The binary refuses to start if any registry recorded a problem while packages initialized (`checkRegistries`): an error code defined twice, a malformed agent operation, a duplicate job type, a bad manifest. The same check runs in the tests, so these mistakes never reach a release.

## Packages

| Package | What it holds |
|---|---|
| `internal/store` | Resources, kinds, TypeIDs, the `Store` interface and its in-memory implementation |
| `internal/changeset` | The change pipeline: requests, diffs, plans, previews, apply jobs and undo |
| `internal/reconcile` | The reconciler interface, the typed adapter and the runner |
| `internal/agent` | The typed operation allowlist, the agent's HTTP server and the clients that call it |
| `internal/system` | Adapter interfaces for everything the agent touches, and the pure renderers for unit files and apt sources |
| `internal/system/linux` | The real adapters, for Debian 13 |
| `internal/system/sim` | A simulated Debian 13 server implementing every adapter in memory |
| `internal/modules` | Manifests, the `Module` interface and its optional capabilities, the registry and the module manager |
| `internal/api` | The HTTP API: middleware, health and version, module operations, the OpenAPI export |
| `internal/api/httpapi` | Problem details, error mapping and schema naming shared by the panel API and the agent protocol |
| `internal/hooks` | Hook points, registrations, the hook runner, the add-on manifest and the rule spec |
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

## Resources and kinds

Everything Racksail manages is a resource of a kind. A kind is defined once, in a package variable, with Go types for its spec and status:

```go
// ZoneSpec is what the user wants: the zone's name and settings.
type ZoneSpec struct {
	Name string `json:"name" doc:"The zone's domain name" example:"example.com"`
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
})
```

- The name is `<module>.<name>` and the prefix is short and unique; `Define` records problems, and registration refuses a kind that has any. Some prefixes are reserved for IDs that aren't resources (change sets, jobs, events and so on).
- `KindInfo` also declares actions (with risk levels, inputs, outputs and whether they're long, destructive or reversible), unique indexes, UI hints and backup coverage. Everything generated for the kind comes from it, so descriptions are written for people: they document the API, label CLI help and describe assistant tools.
- `WithDefaults` and `WithValidator` add typed defaulting and validation. Validation errors use `i18n.Invalid` with field errors, so the UI can highlight fields.
- `Zone.Decode`, `Zone.Encode`, `Zone.Get` and `Zone.List` convert between the untyped `store.Resource` that generic code passes around and the typed `store.Object[ZoneSpec, ZoneStatus]`.

The store keeps a revision per resource for optimistic concurrency (status writes don't change it), bumps the generation whenever the desired state changes, soft-deletes resources until their reconciler has removed what they created, and keeps every spec version for history and undo. `store.Memory` is the only `Store` implementation so far; `racksail dev` and the tests use it. The production store is SQLite (decision T3) behind the same interface.

## The change pipeline

Nothing writes to the store directly. The API, the CLI, recipes, automations, add-ons and the assistant all call `changeset.Pipeline`:

```go
cs, err := svc.Pipeline.Submit(ctx, changeset.Request{
	Actor:  actor,
	Op:     changeset.OpCreate,
	Kind:   "dns.zone",
	Scope:  scope,
	Spec:   spec,
	DryRun: true,
})
```

`Engine.Submit` fills in the kind's defaults, asks the authorizer, runs the kind's validation and the validate hooks, then asks the resource's reconciler for a plan and runs its steps in the agent's check mode, so the preview lists the exact downloads, packages, files and ports. Modify hooks then add to the change set, labelled with their source, and two of them changing the same field is reported as a conflict. A dry run stops there and returns the change set; otherwise the changes are written in one store transaction and a `changeset.apply` job runs the plans with live progress. Events and audit records follow, including for denied attempts. `Undo` builds a new change set from the recorded previous specs.

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

`internal/reconcile/reconcile_test.go` is a complete small example, run against the simulated machine.

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
| `APIProvider` | `RegisterAPI(api huma.API, svc *Services)` | Operations beyond the generated ones, under `/api/v1/<id>/` |
| `HookProvider` | `Hooks(svc *Services) []hooks.Registration` | Hooks, such as patches to other services' configuration |
| `EventProvider` | `Events() []events.Type` | Event types beyond the generated kind events |
| `MigrationProvider` | `Migrations() fs.FS` | Database migrations |
| `RecipeProvider` | `Recipes() fs.FS` | Recipes the module ships |
| `BackupProvider` | `Backups() []Backup` | What data belongs to each kind and how to quiesce it |
| `HealthProvider` | `HealthChecks() []HealthCheck` | Health checks for what the module runs |
| `HintProvider` | `Hints() []Hint` | Descriptions and examples that help the assistant |
| `Starter`, `Stopper` | `Start`, `Stop` | Work when the module is turned on or off |

A few rules that aren't obvious from the types:

- `RegisterAPI` runs when `racksail openapi` exports the document, with empty services. Register operations and nothing else there; use `svc` only inside handlers.
- Schema names of module types get the module's name in front (`dns.ZoneSpec` becomes `DnsZoneSpec`), so two modules can use the same type names.
- Errors are package variables: `var ErrZoneTaken = i18n.Define("dns.zone_taken", http.StatusConflict, "{zone} is already hosted on this server.")`, returned as `ErrZoneTaken.New("zone", name)`. Every code the binary can return needs a message with the same English text in the UI, named `error.<code>`, in `ui/src/modules/<id>/messages/en.json` (the shell's catalog for core codes). `TestErrorCodesHaveUIMessages` in `cmd/racksail` checks this.
- Job types are registered with `jobs.RegisterType(jobs.Type{Name: "dns.check", Run: ...})` from `init`.
- Hook points for every operation and stage of every kind exist automatically (`dns.zone.create.validate`, `dns.zone.delete.applied` and so on). A module's manifest declares any others it adds.

## The API

`internal/api` builds the API on Huma with the standard library's router. Errors become RFC 9457 problem details with the error's code and parameters, whatever produced them: `httpapi.NewError` replaces Huma's error constructor, and errors without a code become `core.internal`, logged and never shown. Operations that need a signed-in user declare `api.Protected` as their security requirement; everything else about access is decided by the authorizer in the change pipeline.

The OpenAPI document is generated from the code and committed as `spec/openapi.json`. Change an operation, run `make gen`, and commit both.

## The web UI

The UI lives in `ui/`: React 19, TypeScript, Vite, TanStack Router and Query, Tailwind CSS v4 and shadcn/ui on Radix. `npm run build` writes it to `internal/webui/dist/app`, which the binary embeds.

- `src/app/` is the shell: providers, the router, the frame with the icon rail and top bar, the home screen and error screens.
- `src/design/` holds `theme.css` (generated by `racksail theme css`), `globals.css`, which maps the tokens onto Tailwind and shadcn names, and the components.
- `src/lib/` holds the API client (`api/schema.d.ts` is generated from `spec/openapi.json` by `npm run gen:api`), i18n and helpers.
- `src/modules/registry.ts` lists every module with a lazy import of its entry file, `src/modules/<id>/index.ts`. An entry is a `ModuleUI` (see `src/modules/types.ts`): the module's name, description and icon, and optionally navigation entries, routes, home cards, settings pages and command-bar actions. Screens, cards and pages are loaded the first time they're shown.

Each module's messages live in `src/modules/<id>/messages/en.json`, with ids starting with the module's id (or `error.<id>.` for its error codes); `messageIds` turns a catalog's ids into typed descriptors. The shell's own messages are in `src/app/messages/en.json`.

Components take colors, spacing, radii and type only from tokens. Tailwind's default palette is removed, and `scripts/check-tokens.mjs`, part of `npm run lint`, reports hard-coded colors. Tests use Vitest and Testing Library; `src/app/app.test.tsx` shows how to render the shell or a module's contributions against a fake API.

## Generated files

| File | Generated by | Checked by |
|---|---|---|
| `spec/openapi.json` | `racksail openapi` | `TestOpenAPIMatchesTheCommittedDocuments` |
| `spec/agent-protocol.json` | `racksail openapi --agent` | the same test |
| `ui/src/design/theme.css` | `racksail theme css` | `TestThemeCSSMatchesTheUI` |
| `ui/src/lib/api/schema.d.ts` | `npm run gen:api` | `npm run check:api`, part of `make check` |

`make gen` regenerates all of them.
