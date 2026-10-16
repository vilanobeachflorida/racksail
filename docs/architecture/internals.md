# Internals

How the code fits together, for people working on Racksail. The other documents here say what Racksail is and why; this one says where things live and how to add to them. It describes the code as it is, so update it in the same pull request when that changes.

## Running things

```sh
make dev                     # racksail dev on 127.0.0.1:7070 plus the UI dev server on 127.0.0.1:5173
make check                   # what a pull request must pass; CI also runs make test-race and make docs
go run ./cmd/racksail dev    # just the panel, agent and edge, serving the embedded UI if it's built
go test ./internal/store     # one package
go test ./internal/system -update   # rewrite golden files after an intended change
```

`racksail dev` puts its directories under `.dev/` (or `--data`): `etc/`, `state/`, `run/`, where the simulated agent and the edge have their sockets, and `machine/`, where the simulated server is kept between runs (`machine.json`, with a copy of its files under `machine/root/` that the edge serves from). The panel keeps its state in `state/state.db`, so resources, change sets, jobs and the timeline are still there after a restart, and its secrets and the edge's certificates are kept under `state/` too. The simulated server takes as long as a small real one to install packages or start services; `--fresh` starts over with a newly installed one and no saved state. Everyone who reaches it is signed in as the developer, an admin, which the log says when it starts and with every request, and which is why it refuses to listen on anything but loopback addresses. Its webhooks may send to receivers on the same machine, which a panel only does when `webhooks.allow_networks` says so. The developer isn't rate-limited, since their browser tabs and the test suites all come as that one person; callers without credentials still are. The panel listens on `--addr` (127.0.0.1:7070 by default), and the edge on `--edge-http` and `--edge-https` (127.0.0.1:7080 and 127.0.0.1:7443) once a module adds a route, with certificates from a local CA. The simulated agent and the edge also answer on their Unix sockets when the path is short enough for one.

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
| `racksail theme validate THEME` | Checks a theme as the panel would: a `theme.yaml` or JSON file, a package folder, a zip archive of one, or a built-in theme's ID |
| `racksail theme css [THEME]` | Prints the token stylesheet the UI loads; `--layer` prints only a theme's own layer, as the panel applies server and reseller themes |
| `racksail theme export THEME` | Prints a theme as `theme.yaml`, or JSON with `--format json` |
| `racksail theme spec` | Prints the built-in themes and the rules themes are checked against, as the UI's `themes.json` |
| `racksail theme list`, `racksail theme mark [THEME]` | List the built-in themes; draw the mark in the terminal in a theme's console colors |
| `go run ./brand/gen` | Writes every brand file from the artwork in `brand/gen`; `-check` writes nothing and lists stale files |
| `racksail install --answers FILE --dry-run` | Shows the steps a convert-mode answers file would take on this server; `--convert` plans the same without a file, and `--sim` carries them out on a simulated server. Install and restore modes erase disks, so they only run from the installer image |
| `racksail agent --sim` | Runs a standalone agent against a simulated server, without root. It listens in a directory of your own under `$XDG_RUNTIME_DIR` or the temporary directory, and `--socket` picks another path |
| `racksail agent join HUB TOKEN --fingerprint FP` | Enrolls a server with a panel's hub; `--cluster-file` keeps the certificate somewhere other than `/var/lib/racksail-agent`, for trying it without root |
| `racksail edge --state DIR --socket PATH` | Runs an edge by hand, keeping its certificates and routing table in `DIR`; without a socket from systemd it creates its admin socket at `PATH`, for its own user only |

## What the panel keeps

The panel keeps its state in one SQLite database (decision T3), `state.db` in the state directory (`config.Paths.Database`). `store.OpenDB` opens it in WAL mode with a single writing connection, whose transactions start `IMMEDIATE` so they never fail halfway for want of the lock, and a pool of read-only connections, which never wait for the writer. The driver, `modernc.org/sqlite`, is pure Go, so the binary still builds without CGO.

Each part that keeps state brings its own tables as embedded migrations named `NNNN_description.sql`. `DB.Migrate` applies them in order, each once and in a transaction of its own, and records them per owner in `schema_migrations`:

| Owner | What | Kept |
|---|---|---|
| `core` | Resources and their index entries (`store.NewSQLite`), and every version of their specs | Until the resource is purged; its spec versions stay for undo |
| `core.events` | The timeline and the audit log (`events.NewSQL`) | `retention.events`, 90 days, and `retention.audit`, 400 days |
| `core.jobs` | Jobs, their logs and the idempotency keys of retries (`jobs.NewSQL`) | `retention.jobs`, 30 days once finished; keys for a day |
| `core.changesets` | Change sets, previews and idempotency keys (`changeset.NewSQLStorage`) | `retention.change_sets`, 30 days once finished; previews for an hour |
| `core.webhooks` | Webhook deliveries and their attempts (`webhook.NewSQL`) | `retention.deliveries`, 30 days |
| `core.inbox` | Who dismissed or snoozed which items of their attention inbox (`inbox.NewSQL`) | Until the snooze ends; otherwise 180 days |
| `core.notifications` | Notifications sent and waiting, with their messages, and where routing is in the timeline (`notify.NewSQL`) | `retention.deliveries`, 30 days once finished |
| `core.hooks` | The hooks the circuit breaker switched off (`hooks.NewSQLBreakers`) | Until an admin switches them back on |
| `core.addons` | The hashes of add-ons' API tokens (`addons.NewSQLTokens`) | While the add-on is installed |
| `<module id>` | A module's own tables, from `MigrationProvider` | As the module decides |

Each also caps how many it keeps, apart from the inbox's dismissals, one per person and item. A migration that has run must never change: `Migrate` refuses to go on when one did, and when the database has migrations the binary doesn't know, as after going back to an older release. Add a new migration instead, as `0002_record_history_scope.sql` added owners to the history. When existing rows need values only Go can work out, the part fills them in when it opens, as `store.NewSQLite` does for the sort keys `0003_index_sort_keys.sql` added. Every durable implementation has an in-memory twin (`store.NewMemory`, `events.NewMemory`, `jobs.NewMemory`, `changeset.NewMemoryStorage`, `webhook.NewMemory`) that passes the same tests, for tests and tools.

Writes of different parts can commit together. `DB.Atomic` runs a function in one transaction, and every write made with the context it passes, through `DB.Write` and `DB.Exec`, joins that transaction as a savepoint, so a write that fails is undone on its own and its caller decides whether to go on. Work that others must only see once it is committed waits for the commit with `DB.AfterCommit`: the store tells watchers about writes that way, and the job queue starts jobs queued in a transaction once it commits. The change pipeline applies a change set this way. The transaction holds the only writing connection, so everything inside it must write with its context, and nothing inside it may publish events.

Secrets are kept on disk as well, each in a file of its own rather than in the database (see [Where secrets are kept](#where-secrets-are-kept)). Module states are resources like any other, one `core.module` for every module the binary has, which the module manager creates the first time the panel starts with a module; see [Modules](#modules). So are the rest of the panel's settings that people change: rules, add-ons, catalogs and what was installed from them, notification channels, the mail relay, people's notification preferences and the update settings.

## The binary

`cmd/racksail` is the only binary. `main.go` runs the cobra root command from `root.go`, which groups the commands:

- **Run Racksail:** `panel`, `agent` (with `agent join`), `edge`, `wake-proxy`, `dev`, `install`, `safe-mode` and `update`. `wake-proxy` is what the units of sleeping applications run; it lives in `edge.go`. `safe-mode` and `update` (`maintenance.go`) are for when the panel can't help: turning safe mode on or off from the console, and what the agent runs around a self-update.
- **Manage a server:** commands that call a panel's API. `internal/cli` builds one for every operation API tokens may use from the binary's own OpenAPI document (`cli.AddCommands`), named from the operations catalog: a kind's operations are `racksail <kind> <operation>`, with a module's kinds under the module (`racksail web site create`), the core's other operations are named in `internal/cli/names.go`, as are those that act on a kind beside its own, such as installing from a catalog (`racksail catalog install`) or turning a module on with what it needs (`racksail module on`), and anything else a module adds is `racksail <module> <operation id>`. Path parameters are arguments; query parameters, and the request body's fields, a kind's spec fields among them, are flags; `--file` and `--set` reach the rest. Updates start from what the panel has, with its revision. Commands that remove data, and critical ones, ask to have their argument typed first, or take `--yes`; fields marked as passwords take `-` or `@file`, never their value. Without `--server`, a profile or `RACKSAIL_SERVER`, commands call the panel of the server they run on, where `racksail.toml` says it listens (`cli.DefaultServer` to start with). Hand-written commands such as `status` are built from `clientCommands` in `client.go` and get the same shared flags (`--profile`, `--server`, `--ca-file`, `--output`); commands that change something are wrapped in `mutating(cmd)`, which gives them `--dry-run`. `status` reads the health report a failing check answers with, 503, as a report (`cli.Request.Answers`), lists the checks with their states lined up after the longest name, and exits with 2 when one fails. `TestEveryOperationHasACommand` fails when an operation has none, and two commands with one name stop the build of the tree. The roles that run Racksail skip building these commands (`newRootCommandFor`).
- **Tools:** `openapi`, `version` and `theme`.

`core.go` wires the panel. It opens the panel database and brings every part's tables up to date, modules' included, then builds the store, the timeline and audit log, the job queue, the change set storage, the secrets store, the webhook dispatcher, the hook registry and runner, the reconciler registry and runner and the change pipeline, which modules reach through `modules.Services`, plus the module manager, the rule engine, notifications, the add-on runtime, the updater, the catalog client with the recipe engine (`extend.go` builds the add-on runtime, the catalogs and the recipes), the attention inbox (`newInbox` in `inbox.go`) and the identity service, whose users are the people notifications reach (`notify.Users`) and whose security alerts go out through channels built as notifications build theirs. It hands the authorizer which accounts are whose (`account.Ownership`, from the accounts themselves and what the module that keeps resellers says, `OwnershipProvider`), and the add-on runtime what the module that keeps accounts says about where their add-ons run (`AccountProvider`), attaches the enabled rules, starts the modules that are on and notifies `core.boot`. Then it starts the job queue, which picks up the jobs a previous run left behind, the reconcile queue (`reconcile.Runner.Run`), webhook deliveries, notifications, the timeline's pruning, the identity service's watch on the sign-in policy with its cleanup of what expired, the inbox's checks, the module manager's watch on what uses each module, the add-on runtime with the gateway add-ons call the API through, the updater, which first finishes an update the panel restarted onto, and the catalogs' daily fetch with the installations' health checks. It publishes `core.panel.started`, and follows what the agent reports on its own (see [Agent operations](#agent-operations)). Modules that didn't start are reported through the `modules` health check rather than keeping the panel down, so they can still be turned off, and the health report also runs the checks modules declare (`HealthProvider`) while they're on, and a `database` check.

Stopping goes the other way round (`core.stop`): the HTTP server's `Shutdown` first calls `api.Server.Close`, registered with `RegisterOnShutdown`, which ends live streams and waits for actions, so clients that stay connected don't hold it up. Then the background work stops and is waited for, the job queue with it (`jobs.Manager.Stop`, which leaves the jobs it stops for the next start), the modules stop, and only then does the database close. The health report has `jobs`, `reconcile` and `webhooks` checks for that background work, and `core.metrics` gives `GET /api/v1/metrics` its numbers.

Both `panel` and `dev` build the same core; they differ in the agent client (the agent's Unix socket, or a simulated machine in the same process), in how they listen, and in who may connect. The panel only accepts requests from `panel.allow_from` in `racksail.toml` when it is set. Both sign people in with the identity service (`internal/auth/identity`): a session cookie from the panel, or an API token in the `Authorization` header. `dev` also signs every request that carries neither in as its developer, an admin, and logs that it does; `dev --sign-in` doesn't. Until the panel has an admin, `panel` (and `dev --sign-in`) makes the first one from the installer's answers or prints a one-time setup code (see [Sign-in](#sign-in-and-sessions)).

On a server, `racksail install` writes the units `system.PlatformUnits` renders, which `packaging/systemd` keeps a copy of for `/usr/bin/racksail`, through `InstallPlatformUnits` in its own process (only the installer and the self-update code may run it): `racksail.slice` and `racksail-accounts.slice`, the agent (as root, of type `notify`) and the panel (as `racksail`, importing the `racksail-secrets-key` credential) as services, and the edge as `racksail-edge.service` behind `racksail-edge.socket`. It enables and starts the agent, the panel and both sockets. systemd creates the edge's admin socket, `/run/racksail/edge.sock`, for the `racksail` group, so the panel can reach it without the edge belonging to that group (which would let it reach the agent's socket too), and starts the edge the first time the panel sends it routes. It likewise creates the panel's edge-facing socket, `/run/racksail/panel.sock` (`racksail-panel.socket`), for the `racksail-edge` group, so the edge reaches the panel through it; the panel believes a forwarded client address only on that socket, never from a loopback peer, so a site that reaches the panel's own port over loopback can't forge it. The panel still binds its own port (8443) itself. `racksail edge` takes the socket systemd passes in, and creates one for its own user when started by hand (`--socket`); started by systemd, it exits once it has had nothing to serve for five minutes (`edge.Options.IdleExit`).

`dev.go` builds `racksail dev` from `newDevServer`, which the one-process test in `dev_test.go` also uses: the simulated server, one agent runtime shared by the panel and the agent socket, and the edge serving the same `edge.Table` the panel's core writes to. The panel answers the edge's questions in the same process there. `racksail panel` reaches the edge through an `edge.Link`, the `edge.Router` its core gets: since any request to the socket starts the edge, the link holds changes back until a module sends a route that isn't passive, or the agent reports the edge running. With that first route it sends what waited, asks the edge to serve the panel under `panel.hostname`, answers the edge's questions over its socket with `edge.AnswerQuestions` and has the agent enable `racksail-edge.service`, so sites come back at boot without the panel; when the last such route goes, it undoes all of that.

The binary refuses to start if any registry recorded a problem while packages initialized (`checkRegistries`): an error code defined twice, a malformed agent operation, a kind with problems, an event or job type registered twice, a bad manifest. The same check runs in the tests, so these mistakes never reach a release.

## Packages

| Package | What it holds |
|---|---|
| `internal/store` | Resources, kinds, TypeIDs, the `Store` interface with its SQLite and in-memory implementations, the panel database and its migrations, and the `Overlay` the pipeline plans against |
| `internal/changeset` | The change pipeline: requests, diffs, plans, previews, apply jobs, undo, retention, secret values and where change sets are kept |
| `internal/reconcile` | The reconciler interface, the typed adapter, the runner with its health checks, and the reconcile queue: retries, concurrency limits, drift and the periodic resync |
| `internal/agent` | The typed operation allowlist, the agent's HTTP server, its runtime (audit trail, drift ledger, network rollback), mutual TLS and the hub agents connect to, and the clients that call it |
| `internal/system` | Adapter interfaces for everything the agent touches, and the pure renderers: units, slices, sandbox profiles, the platform units, networkd, nftables, apt sources and pins, kernel defaults, zram and unattended upgrades |
| `internal/system/linux` | The real adapters, for Debian 13 |
| `internal/system/sim` | A simulated Debian 13 server implementing every adapter in memory |
| `internal/modules` | Manifests, the `Module` interface and its optional capabilities, the registry, and the module manager: `core.module` and its reconciler, transitions as change sets with their previews, profiles, install on first use, exports and the gate that holds back the reconcilers of modules that are off |
| `internal/modules/modulesapi` | The module manager's operations: the modules, what they install, turning them on with what they need, off and removing them, exporting their resources, and profiles |
| `internal/modules/modulestest` | A whole panel in memory for modules' tests, with the on, use, off and remove lifecycle every module passes, and accounts for what they make to belong to (`Env.Account`) |
| `internal/api` | The HTTP API: middleware (request IDs, access logs, rate limits), health, version and metrics, the operations generated for every kind, change sets, jobs, the timeline, the audit log, live streams, the operations catalog, module operations, read authorization, the change parameters, the OpenAPI export |
| `internal/api/httpapi` | Problem details, error mapping and schema naming shared by the panel API, the agent protocol and the add-on protocol |
| `internal/hooks` | Hook points, registrations, providers and extensions, the hook runner with ordering, deadlines, failure modes, conflicts and the circuit breaker, configuration patches with their checks, invariants and `core.override`, the audit records of what hooks contributed, the add-on manifest and protocol, and the rule spec |
| `internal/hooks/rules` | CEL rules: `core.policy`, `core.reseller_policy` and `core.account_policy`, compiled with a cost limit and attached as hooks, and the rule tester |
| `internal/hooks/addons` | The add-on runtime: `core.addon` and `core.account_addon`, their sandboxed services and sockets, what they are sent and may send back, scoped tokens, the gateway and safe mode |
| `internal/hooks/hooksapi` | The extension system's operations: hook points and hooks, switching a hook back on, the rule tester, safe mode, add-ons' UI entries, routes and screens |
| `internal/auth` | Actors, grants, roles, the permission matrix and the central authorizer, and the credentials: password hashing, TOTP, secrets, SSH keys, and the interfaces of sign-in providers and second steps |
| `internal/auth/identity` | Users, sign-in, sessions, API tokens, passkeys, impersonation, the first admin and the sign-in policy, with their own tables |
| `internal/auth/account` | The `core.account` kind, its reconciler (Linux user, home and slice) and the ownership resellers have of accounts |
| `internal/cli` | The client commands built from the API document, profiles, and printing answers, change sets and jobs |
| `internal/events` | The event bus, the timeline and the audit log, with followers for live streams, and the catalog of event types |
| `internal/events/webhook` | Outgoing webhooks: the `core.webhook` kind, signing, the delivery queue with its retries, and the delivery log |
| `internal/inbox` | The attention inbox: its sources (failed jobs, resources that fail, alerts, updates, certificates, module suggestions), the service that keeps what the slower ones found, and dismissals |
| `internal/jobs` | The job queue with progress, logs, cancel, retry and history |
| `internal/i18n` | Error codes and their English messages, and the catalog's words for what the JSON Schema validators find: the API's (`SchemaField`), and that of `spec/schemas`, which checks rules (`JSONSchemaFields`) |
| `internal/edge` | The routing table and the embedded Caddy that serves it, certificates, the edge's admin API and the wake proxy |
| `internal/config` | `racksail.toml`, which refuses settings nothing would honour: `[paths]`, since the agent's roots, the installer and the units only know the standard locations, and `agent.socket` and `edge.socket`, since the units and the add-ons' sandboxes only know the sockets in `/run/racksail` |
| `internal/secrets` | Secret storage, encrypted at rest with a key sealed by systemd-creds |
| `internal/theme` | Themes: parsing and checks (schema, WCAG contrast in both palettes and the console palette, status hues, translucency, fonts), packages, layering, CSS rendering, templates, custom CSS and the terminal mark. Templates are read with a spec-following HTML tokenizer, SVG files as namespaced XML against an allowlist of drawing elements, and every style with a spec-following CSS tokenizer, so checks see what browsers see |
| `internal/notify` | Notifications: channel types (email through the mail relay, webhooks, Matrix, ntfy, Telegram, Discord and Slack), `core.notification_channel`, `core.smtp_relay` and `core.notification_preferences`, routing events to channels and people, quiet hours, retries and the delivery log, and messages laid out with the theme's email template |
| `internal/notify/notifyapi` | Notifications' operations: channel types, the delivery log and each person's own preferences |
| `internal/recipes` | Recipes, their inputs, the recipe engine that installs, upgrades and uninstalls them as change sets, upstream verification, `core.catalog` and the catalog client, and installations |
| `internal/recipes/recipesapi` | The catalog's operations: items, installing, upgrading and uninstalling |
| `internal/installer` | Answers files and the install, convert and restore plans |
| `internal/updates` | Build information, release channels and versions, `core.update_settings`, the updater with the agent operations that install, confirm and roll back a release, Debian's package updates and its upgrade to the next release |
| `internal/updates/sigstore` | Verifying Sigstore bundles against a pinned trusted root: certificates, transparency log entries, inclusion proofs, checkpoints and timestamps |
| `internal/webui` | The embedded UI and the page shown when a binary was built without it |
| `spec` | The JSON Schemas in `spec/schemas/` and the code that validates documents against them |
| `modules/<id>` | One package per module |
| `modules/all` | Imports every module and lists their ids |
| `brand/gen` | The brand files' generator: the mark's and wordmark's outlines, lockups, icons, rasters and the terminal mark |

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
	Indexes:     []store.Index{{Name: "name", Path: "name", Unique: true, Fold: true}},
	UI:          store.UIHints{Fields: map[string]store.FieldHint{"tsigKey": {Sensitive: true}}},
})
```

- The name is `<module>.<name>` and the prefix is short and unique; `Define` records problems, and registration refuses a kind that has any. Some prefixes are reserved for IDs that aren't resources (change sets, jobs, events and so on). Registration also refuses a kind whose API path (`KindInfo.Path`, `<module>/<plural>`) or the ID of one of the operations generated for it (`KindInfo.OperationIDs`) is another kind's, such as two kinds of a module with one plural, or `web.site_history` beside `web.site`, whose history is `getWebSiteHistory`; the API refuses one whose operation IDs a module's own operations have.
- `KindInfo` also declares actions (with risk levels, inputs, outputs and whether they're long, destructive or reversible), unique indexes, UI hints and backup coverage. Everything generated for the kind comes from it, so descriptions are written for people: they document the API, label CLI help and describe assistant tools.
- An action's event is `<kind>.<past tense>`, derived from its name: `deploy` gives `web.site.deployed`, `reset_password` gives `web.site.password_reset` and `spin_down` gives `storage.disk.spun_down`. Set `Past` when the derived form is wrong; event names are a public contract that webhooks and automations rely on.
- Fields that hold secrets are marked `Sensitive` in the UI hints. Their paths are camelCase fields joined by dots, and their value is always a reference to one of the resource's own secrets; see [Secrets](#secrets).
- `WithDefaults` and `WithValidator` add typed defaulting and validation. Validation errors use `i18n.Invalid` with field errors, so the UI can highlight fields. What the check of a spec against its JSON Schema finds is said in the catalog's words too (`i18n.SchemaField`): a missing field is `core.required`, an unknown one `core.unknown_name`, and anything else `core.bad_value` with a reason that says what the field takes, such as "use csv or json", never the validator's own wording. In the change pipeline a validator's context also carries the actor, the authorizer and, with `store.ReaderFrom`, the store as the change set would leave it, for rules about the whole, such as `core.user` keeping an admin who can sign in.
- `Scope` is the level a kind's resources belong to; `Levels` adds the lower levels they may belong to as well, as `core.user` is the server's, a reseller's or an account's (`KindInfo.OwnedAt`). A reseller's or an account's resources belong to a resource of the level's owner kind (`KindRegistry.OwnerKind`): `core.account` for accounts, and for resellers `core.reseller` or the kind named `reseller` of the module that keeps resellers, the same rule the screens choose owners by. An owner belongs to itself, as an account's scope is the account. A kind with `Confirm` asks a person in the panel to confirm it's them before any change of it, whatever its risk, and its actions above low risk.
- `Zone.Decode`, `Zone.Encode`, `Zone.Get` and `Zone.List` convert between the untyped `store.Resource` that generic code passes around and the typed `store.Object[ZoneSpec, ZoneStatus]`. `Zone.Get` reports a resource of another kind as not found.

The store keeps a revision per resource for optimistic concurrency (status writes don't change it), bumps the generation whenever the desired state changes, soft-deletes resources until their reconciler has removed what they created, and keeps every spec version for history and undo, with the resource's kind and owner. Lists sort by an indexed field through sort keys kept next to its values, which order numbers by their value, before text, and booleans last; a resource with several values sorts by its lowest, and filters still compare the values as written. A purged resource's ID never goes to a new resource, which would inherit its history and the secrets its change sets stored: `Create` refuses it with `store.id_used`, unless the transaction's `WriteMeta.UndoOf` is the change set whose delete it brings back, as the same kind with the same owner (`store.CheckReuse`).

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

For each change in turn, `Engine.Submit` fills in the kind's defaults, asks the authorizer, takes the change's secret values, runs the kind's validation, makes sure the account or reseller a new resource belongs to exists, stored and not being deleted or created by an earlier change of the request (a field error on `scope.id` otherwise, so nothing ends up outside every account, for an account made later with that ID to inherit, and one on `scope.level` while no kind keeps the level's owners, as resellers until the module that keeps them is built in), runs the validate hooks, then asks the resource's reconciler for a plan and runs its steps in the agent's check mode, so the preview lists the exact downloads, packages, files and ports. A change the authorizer refuses on a resource the actor may not even read answers `core.not_found`, as reading it would. Reconcilers plan against the overlay too, so the site's reconciler can read the database it uses before either exists. Modify hooks then run once per plan point, with the indexes of the changes the point is about. They may rewrite the specs of those changes and add changes and effects, and nothing else: no hook can turn an update into a delete, move a change to another resource or scope, or drop one. What they add is labelled with its source, and two of them changing the same field is reported as a conflict. Every change a hook rewrote or added is authorized for the actor, validated and run past the validate hooks again. The requested changes are planned again from the first one a hook rewrote, each against the state the change set leaves at that point, and added changes, which can only be creates, updates and deletes, are planned too, so the preview shows what the change set as the hooks left it does to the machine; the effects hooks added stay labelled as theirs. Added changes don't go through modify hooks themselves.

A change set with a destructive effect gets `Snapshot`, and its preview lists the snapshots applying it takes. A dry run then makes the store writes in a transaction it throws away, so the preview also reports a name that's already taken, and returns the change set. Otherwise the pipeline first takes read-only Btrfs snapshots of the subvolumes that hold what the change set removes (`agent.SnapshotPaths`; data that isn't on Btrfs goes without, and if taking them fails nothing changes). Then, in one transaction of the panel database (`Options.DB` with `DB.Atomic`), it writes every change, keeps the change set, queues its `changeset.apply` job, settles the idempotency key, marks the change set an undo undoes and records each change in the audit log, so a crash leaves all of it or none. `NewEngine` takes the database from a `store.SQLite` whose database the change set storage shares; with parts kept in memory each write commits on its own. Once the writes start they finish, even if the client goes away. The job starts once the transaction commits, runs the plans with live progress and verifies the result: the runner runs each plan's health checks, and the job runs an action's `Checks` after its steps. A check that doesn't pass fails the change set, which can then be undone. Events follow. When a request has several changes, field errors say which change they belong to: `changes[1].spec.domains`.

The audit log has every change as `success`. Attempts that don't go through are there too: `denied` when the authorizer refuses a change or a validate hook stops it, previews included, and `failed` when a change that was allowed couldn't be made, because its settings aren't valid, a modify hook that blocks failed, or writing it failed. Failed previews are left out, since they change nothing. Refusals are bounded per actor (`events.Denials`), so a caller who keeps being refused, at whatever rate the API lets them, can't push everyone else's records out before their time: the log counts the actor's refusals in windows of an hour, and one the same as a refusal already recorded in the window, by the same actor from the same address for the same reason, only adds to that record's `repeats` and `lastTime`. Past a hundred different refusals in the window, the rest count together on one record of `core.refusals`. A repeat whose record is gone, pruned or rolled back with the transaction that wrote it, is recorded again, and repeats that come while their record is still being written count once it is, so none of them waits for another's write. To count repeats the log keeps a hash of each refusal it recorded, a quarter of a million at most across actors, after which every window starts over. The export has the two as its last columns.

The snapshots are recorded in `ChangeSet.Snapshots` and deleted when the change set is no longer kept. `Engine.Run`, which the panel starts with its other background work, also deletes the snapshots no kept change set lists, when the panel starts and daily (`CollectSnapshots`): those of change sets dropped by a panel that stopped before it deleted them, of a change whose writes never committed and of a preview whose apply stopped in between. It finds them by their label, the change set's ID, with `ListMounts` and `ListSubvolumes`, and leaves alone the snapshots of a change set whose record isn't written yet and those of anything else, such as updates. Undo puts back specs; bringing back data from a snapshot is a restore, which starts from that list.

The apply job marks each change `started` before it runs and `done` once it's in place (`ResourceChange.State`), and records how the change set ended with a context that outlives the job's, so a canceled job or one out of time still ends its change set as failed. Handlers learn why their context ended from `jobs.Stopped`. When the panel stops under the job, the job returns without recording anything; the next start reports it as interrupted, and the job type's `Ended` marks the change set failed, as it does for a job canceled before it started. The job type doesn't set `Resume`, because an action's steps may not be safe to run twice. A retry of the job (`jobs.Queue.Retry`, asked first through the type's `Retryable`) claims the change set only when it failed, carries on from the first change that isn't done, and refuses with `changeset.action_not_repeated` when that change is an action that may already have done what it does once (steps of its own, or `ActionApplier` work in the panel), since a retry doesn't go through authorization and validate hooks again. A change set that was undone or applied is never claimed again (`changeset.not_retryable`).

- `Undo(ctx, changeset.UndoRequest{Actor, ChangeSet, DryRun, IdempotencyKey})` turns the recorded previous specs, labels and placement into a new request (creates become deletes, deletes become creates with the same ID) and sends it through the same stages, so an undo is authorized, validated, hooked and previewed like any change. Applied and failed change sets can be undone; an action that changed no settings can't. An undo puts back the settings from before its change set whatever happened to the resources since, so it replaces later changes of them too, by design: its preview's diff goes from the settings now to the ones it puts back, and `ChangeSet.ChangedSince` lists the resources whose settings or labels aren't the ones the change set left. It is planned at the revisions it read, so a change that lands after its preview makes applying the preview fail with `core.conflict` instead of being replaced unseen.
- `Apply(ctx, changeset.ApplyRequest{Actor, ChangeSet, DryRun, IdempotencyKey})` applies a preview as it was shown: the request it came from is planned again for the actor, and applied only if the plan is still the one the preview showed; otherwise the answer is `changeset.preview_stale`, naming a fresh preview. The preview becomes the applied change set, keeping its ID. Previews don't keep secret values, so one that carried them has to be sent again (`changeset.preview_has_secrets`). Applies of one preview are taken one at a time: another that arrives meanwhile gets `changeset.applying`, and one after it `changeset.not_preview`. A dry run plans the preview again and applies nothing: it returns the preview while applying it would still do what it shows.
- A request, an undo or the apply of a preview may carry a `Confirm`, which is asked about the change set as planned, with a copy of it, just before anything is written, and whose error stops it; dry runs don't ask it. The writes are made only at the revisions the plan was made from, so what it agreed to is what gets written: a change that lands after it has a request that names no revision planned and asked about again, from what landed, and makes the others fail with `core.conflict`.
- An undo marks its original as undone in the transaction of its own writes, and only an applied or failed change set becomes undone, so nothing is undone twice.
- A retry with the same `IdempotencyKey` by the same actor returns the first change set for 24 hours, as long as it's kept. A second request with the key while the first is still running gets `changeset.duplicate_request`. `Submit`, `Undo` and `Apply` reserve the key before they read or check anything else, so a retry gets the first answer even when the first request did away with what it was about: an undo's change set is undone by then, a deleted resource purged. `Replay` looks a key up without claiming it, for callers that read something before they reach the pipeline, as the API's undo and apply do, and every operation that builds its request from what is there (`api.SubmitFunc`).
- `List` returns the kept change sets newest first, filtered by state, resource or actor, or the previews that can still be applied. `ListQuery.Visible` narrows it to what someone may see, in the query, so pages stay full.
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

### Where secrets are kept

`secrets.Open` keeps every secret in its own file under the panel's state (`/var/lib/racksail/secrets`), encrypted with AES-256-GCM. The key comes, in order, from the credential systemd imported for the panel (`$CREDENTIALS_DIRECTORY/racksail-secrets-key`), from the sealed credential opened through the agent (`UnsealCredential`), when the panel runs outside systemd, or from the key file `/var/lib/racksail/secrets.key`. The first start makes a key and seals it with `SealCredential`, which runs `systemd-creds` with the TPM2 chip when there is one and the host key always, and keeps it in `/etc/credstore.encrypted`; when that isn't possible it writes the key file instead. A `key-check` file lets a start tell a wrong key from a missing one, and no new key is made while there are secrets the missing one would open. In development the simulated server seals the key and keeps it in `.dev/machine`.

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
```

The module returns `reconcile.Typed(Zone, zoneReconciler{})` from `Reconcilers()`. Each step does one thing: `WriteFile` writes into a directory that exists, so the plan makes sure of the directory first, and `Parents` creates what's missing above it. `docs/architecture/internals_test.go` runs this example, kind and agent operation included, on the simulated server. Plans must be idempotent: every operation reports `Changed: false` when its effect is already in place, so running a plan twice changes nothing. The panel relies on that: `reconcile.Runner.Run` reconciles every resource when the panel starts, unfinished ones first, and again every 15 minutes (`reconcile.ResyncInterval`), which puts back what drifted on the machine and carries out changes whose apply job never ran. When the plan of a resource that was ready still changed something, the machine had drifted: the `Drifted` condition says what was put back and `<kind>.drifted` is published. `Runner.ReportDrift` takes a report of drift noticed outside the resync: it marks the resource and publishes `<kind>.drifted` at once, and the queue puts it back as soon as there is room. `Runner.Enqueue` asks for a reconcile when something a resource depends on changed. A reconcile whose context ends part way, as when its job is canceled, still records that in the status, with the reason `Stopped`, so the resource never looks busy with work that stopped. When a reconcile the queue started fails, `<kind>.failed` is published for the first failure in a row, and it's retried after five seconds, then twice as long each time up to ten minutes, eight times at most; after that the resource waits for its next change or the resync. A reconcile a change set's job ran isn't retried by the queue, failed or canceled: the change set reports it, and the resource waits the same way. At most eight reconciles run at once, and two of each kind unless its reconciler implements `reconcile.Limiter`. The runner runs one reconcile per resource at a time, while different resources reconcile side by side, so the queue and an apply job never work on the same resource at once; a reconcile waiting for another of the same resource stops waiting when its context ends, as when its job is canceled. When a resource is deleted, the plan removes what earlier plans created; the runner purges the resource once that has worked. A delete's apply job that finds its resource purged already, as when the resync got to it first, counts the delete as done. Reconcilers of kinds with actions also implement `reconcile.ActionPlanner`.

A plan's `Checks` verify the result once its steps have run:

```go
return reconcile.Plan{Steps: steps, Checks: []reconcile.HealthCheck{
	{Name: "knot", Type: reconcile.CheckUnit, Target: "knot.service"},
	{Name: "answers", Type: reconcile.CheckDNS, Target: z.Spec.Name},
}}, nil
```

Checks are `http` (a status below 400, or `Expect`), `tcp`, `tls` (a certificate valid for the host, or for `Expect`), `dns` and `unit` (active, or `Expect`). Each is tried until it passes or its timeout, 30 seconds unless it sets one, runs out, since a service that was just started may need a moment. Unit checks ask the agent; the others go out from the panel, as visitors' requests would. When one doesn't pass, the resource's `Ready` condition turns false with the reason `VerifyFailed`.

`req.Read` reads other resources: the store, or during a preview the change set's planned state. `req.Facts` describes the machine the resource is placed on, and `req.Secrets` resolves secret references.

`internal/reconcile/reconcile_test.go` is a complete small example, run against the simulated machine, and `internal/changeset/changeset_test.go` shows kinds with secrets and change sets with several changes.

## Events, jobs and webhooks

The change pipeline and the reconcilers publish the events every kind has: `<kind>.created`, `.updated`, `.deleted`, `.failed`, `.drifted` and one per action, plus the core's own (`core.changeset.applied`, `core.hook.disabled` and so on). A module publishes anything else through `svc.Events`, after declaring the type with `EventProvider`, so webhooks, automations and `GET /api/v1/events/types` know it:

```go
err := svc.Events.Publish(ctx, events.Event{
	Type: "backups.run.finished", Severity: events.SeverityInfo,
	Subject: policy.ID, Kind: "backups.policy", Scope: policy.Scope, Actor: actor,
	Message: "The nightly backup finished.",
	Data:    data,
})
```

Publishing stores the event in the timeline, which gives it a sequence number; subscribers, live streams and the webhook dispatcher follow the timeline by that number, so a slow one catches up instead of missing events, and a stream a client reconnects to resumes from `Last-Event-ID`. What someone did, and every attempt that was refused, goes to `svc.Audit.Record` with the actor; the pipeline records every change itself, `api.Authorize` and `api.AuthorizeRead` record the requests they refuse, and records made during an API request carry its ID.

Work that takes a while runs as a job. Register its type from `init` and queue it with arguments; the queue keeps it in the database, runs four at a time, retries it as its type says, and streams its progress and log to whoever follows it:

```go
jobs.RegisterType(jobs.Type{
	Name: "dns.check", Timeout: 10 * time.Minute, Retries: 2,
	Run: func(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
		r.Progress(1, 3, "Asking the parent zone")
		r.Log(slog.LevelInfo, "delegation looks right", "servers", 2)
		return result, nil
	},
})

job, err := svc.Jobs.Enqueue(ctx, "dns.check", input, jobs.Options{Title: "Check example.com", Actor: actor, Scope: scope})
```

A job that was running when the panel stopped is marked failed with `jobs.interrupted` when it starts again, unless its type sets `Resume`, which runs it again from the start; only set that for work that is safe to repeat. A handler whose context ends asks `jobs.Stopped` why: `core.canceled`, `core.timeout`, or `jobs.interrupted` when the panel is stopping, when it should return without recording anything of its own. A type's `Ended` hears how every job of the type ended, once, also one that never ran, before `Wait` returns the job, and `Retryable` can refuse a retry. Jobs that need the panel's services, such as the change pipeline's `changeset.apply`, are registered on the queue with `Handle` instead. `Manager.Stop` stops the queue and waits for the jobs running here, and `Stats` and `Check` are the queue's metrics and health check.

A job moves from one state to the next only if it is still in the state it was read in, so a cancel that races the dispatcher either keeps the job from starting or asks it to stop, never both. `Cancel` reports whether it did either; canceling a job that ended, or asking one to stop again, does nothing. A job is retried once, by hand or on its own: `Retry` refuses another with `jobs.already_retried`, naming the retry, unless the request repeats the first one's idempotency key (`RetryOptions`), which answers with the same job for a day, and `Retried` finds that job without reading the one it retried. `RetryOptions.DryRun` checks a retry and queues nothing. `Watch` hands a job's updates to a buffer; a watcher that falls behind gets one update with `Missed` set in place of what didn't fit, and reads the lines it missed back with `LogsAfter`, which always has them, since each line is stored before it is sent.

Webhooks are resources of the core kind `core.webhook`. The dispatcher in `internal/events/webhook` follows the timeline, turns each event into a delivery for every webhook that wants it, signs it the way Standard Webhooks does, and retries failures on `webhook.Schedule`, recording every attempt. An attempt is recorded even when its context ends during it, so no delivery stays pending with nothing to send it again, and deliveries of a webhook that was turned off are given up rather than sent. Deliveries only connect to public addresses, checked once the receiver's name is resolved, plus the networks `webhooks.allow_networks` in `racksail.toml` lists (`Options.AllowNetworks`); the webhook's reconciler refuses a URL written with an address that isn't allowed, so the preview says so. `webhook.Verify` checks a signature the way receivers do, for tests and add-ons.

## The attention inbox

The home screen starts with what needs the person's attention, which `GET /api/v1/inbox` answers from the sources in `internal/inbox`: jobs that failed in the last week and weren't run again or undone, resources whose status says they fail or work only in part, alerts (events of warning or worse from the last three days, one item per type and subject, leaving out what the other sources show), the server's package updates, a new kernel and a restart that would finish installing them, certificates of the edge or installed for other services that couldn't be obtained, expired or expire within two weeks, and modules that are off but would help, such as backups once something worth keeping exists. Sources that find the same for everyone and take a while, the updates and certificates, set `Registration.Keep`: the service checks them when the panel starts and again in the background once what they found is that old, so nobody waits for apt. Every item carries an `Access` (a resource, a job, or a permission on a scope) that the API checks before showing it, the way reads of what it is about are checked, so an account's owner only sees their own.

A module adds items with `InboxProvider`, read while the module is on:

```go
func (m *Module) InboxSources(svc *modules.Services) []inbox.Registration {
	return []inbox.Registration{{Name: "backups", Source: inbox.SourceFunc(func(ctx context.Context, q inbox.Query) ([]inbox.Item, error) {
		return failedRuns(ctx, svc, q)
	})}}
}
```

Items have a `Code` the UI translates as `inbox.<code>` with the item's `Params`, and English `Title` and `Message` it falls back on; a source that couldn't be read is reported with its error's code and params the same way. People dismiss items, or snooze them, with `POST /api/v1/inbox/{id}/dismiss`, and bring them back with `DELETE`, which says whether there was a dismissal to take back; a dismissal holds while the item's `Version` stays the same, so the same failure happening again shows again. Dismissals are what one person sees, not what Racksail manages, so they don't go through the change pipeline: `dryRun=true` checks the request without making it, and doing either again leaves the inbox as the first request did, so a repeat is safe with or without an `Idempotency-Key`. `GET /api/v1/inbox/stream` says when to read the inbox again, and why: `checked` when a check found something new, `event` when an event it shows arrives, `resource` when a resource starts or stops failing, and `dismissal` when the person dismissed or brought back an item, here or in another tab. Each reason settles on its own, so a burst of one is told once and none hides another. Checks reach every stream, and dismissals only the person's own (`Service.Changes`).

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
- A path the caller chose is reached through `beneath(env, path, read)`, a `system.Files` view of its managed root that follows no symbolic link below it (`Files.Beneath`). Operations that hand a path to a program instead, such as btrfs, check it with `checkNoSymlinks` first.
- Unit files go through `system.CheckUnitFile`, which parses them as systemd does and allows only the directives the renderers use, and holds an account's services to the sandbox of security.md; `EnsureUnit` then checks the service's user against the machine, and a racksail-* user must really be one of Racksail's system users, since the checks spare those the account sandbox. Racksail's own units are never written this way: `InstallPlatformUnits` renders them in the agent, and it is a local operation (`Def.Local`), which runs only for a caller in the agent's own process, the installer and the self-update code; `Server.Handler` refuses it on the socket and to other servers before reading its input, and records the refusal. A unit whose service has to run as root belongs in an operation of its own that renders it from typed input. The agent's own unit can't be disabled (`DisableUnits`), since nothing else would start it again.
- Drop-ins and resource limits (`EnsureDropIn`, `SetUnitProperties`) reach only units the panel may adjust (`checkAdjustableUnit`): never Racksail's own, the add-on services the agent renders, the root system services or a dash prefix such as `racksail-.service`, whose drop-ins reach every unit under it. A drop-in's sections are those of its unit's type. `SetUnitProperties` reports no change only when the limits are in effect and, unless `runtime` is set, also kept: `Units.ResourceControl` reads the live values from systemd and the kept ones from the unit's configuration as systemd loads it after a reboot (`system.LoadUnitConfig`).
- Users and groups (`EnsureUser`, `EnsureGroup`, `DeleteUser`, `DeleteGroup`) change only accounts, whose IDs are in the account range, and racksail-* system users and groups: never root, an administrator's login user, a system group or the platform's own users. What a user may be given follows from who it is on the machine, never from the request: an account's home is its own directory, its subordinate IDs its own range (`system.AccountSubIDs`), and asking for a system user where there is an account, or the other way round, is a conflict. No new account or account group takes the name of a privileged group or of a user or group the system or its packages use (`agent.ReservedName`, which `account.Reserved` asks too), since a package installed later adopts the group it finds with whoever is in it.
- Disk operations install the tools Debian 13 doesn't ship before they run: gdisk for `PartitionDisk`; xfsprogs, dosfstools and btrfs-progs for `FormatDevice`; and btrfs-progs with the first `EnsureMounted` of a Btrfs disk brought from elsewhere.
- Mounts take data disks only (`checkDataDevice`), since the mount points are also where file operations write: a disk or partition named by its link under `/dev/disk`, never the system disk, one with RAID or LVM built on it, or a filesystem mounted anywhere else, at `/mnt/racksail/<name>` or over `/srv/racksail`, where no operation can put a symbolic link. Mount units written with `EnsureUnit` are held to the same.
- Snapshots of the system's own subvolumes, such as the root filesystem before an upgrade, go to `/.snapshots/racksail-system`, which no operation reads, sends or copies and `DeleteSubvolume` removes; snapshots of what Racksail manages go to `/.snapshots/racksail`. The agent's state, its audit trail and the system's snapshots are out of reach of every operation's path (`agentOnly` in `policy.go`), whatever roots modules add.
- Received Btrfs streams lose set-ID bits, device nodes, file capabilities and security labels on the way to `btrfs receive` (`system.FilterSendStream` on Linux), and may only build on, or copy from, subvolumes Racksail reads in full (`receiveSources`), since btrfs looks those up anywhere on the filesystem.
- Firewall rulesets may only be `table <family> racksail* { ... }` blocks, with no `include` or `define` (`system.CheckRuleset`), and networkd files only what `NetworkConfig.Render` writes (`system.CheckNetworkdFile`).
- In check mode (`env.Check`) an operation reports what it would change without changing it. `Effects` describes a step without asking the machine, for previews where check mode can't run; `ReadOnly` marks operations that never change anything.
- Inputs and outputs travel as JSON, up to about 86 MiB on the socket: enough for the largest file `WriteFile` writes, base64-encoded. Keep bulk data such as archives out of inputs; operations download it themselves. Streams aren't held to that limit; each bounds what it reads itself.
- What file, unit and package operations may touch is fixed when the binary starts. Modules extend it from `init` functions: `agent.AllowWrite("/etc/knot")`, `agent.AllowRead(...)`, `agent.AllowUnit("knot.service")`, `agent.RegisterAptSource(...)` for vendor repositories and `agent.RegisterSigningKey(...)` for the keys vendor archives are signed with, which the panel can then only name.
- `Sensitive` marks operations that take or return secrets, such as `SealCredential`; the audit trail keeps no hash of their input.

Every call is recorded in the audit trail, including refusals and calls that never reached an operation, callers whose certificate may not call the agent and peers refused during the TLS handshake: who called, from where when it was refused, for which resource, what it touched (paths, units, names), a hash of the input, how long it took and how it ended. A server keeps it in `/var/log/racksail-agent/audit.jsonl`, which only root reads and no operation reaches, never opened through a symbolic link and started again if it is moved away; `ReadAudit` reads it back. The runtime (`agent.Runtime`) also keeps what must outlive single calls in `/var/lib/racksail-agent`, through the backend so the simulated server keeps it too: a ledger of the files, units and drop-ins written for each resource, one small file per target in `ledger/`, which `CheckDrift` and a periodic check compare with the machine (files by size and modification time, plus a few read in full each check); network changes applied with `confirmWithin`, which roll back on their own unless `ConfirmNetworkConfig` comes in time, even across a restart and trying again while putting the files back fails, and how the last one ended, so a late confirmation is a conflict; account IDs handed out by `AllocateAccountID` but not used yet; and the snapshot files `SaveSnapshot` wrote, the only ones `RestoreSnapshot` receives. `GET /v1/events` streams drift, once per change and starting with what the last check found, and network changes to the panel as they happen. The panel follows it (`followAgent` in `cmd/racksail/agentevents.go`, through `agent.FollowEvents`). Drift on a resource waits two seconds for more of the same, as when a directory is edited file by file, and then goes to `reconcile.Runner.ReportDrift` once per resource, which marks the resource, publishes `<kind>.drifted` and has the reconcile queue put it back. What no resource puts back, such as Racksail's own units or the files of a resource that is gone, is recorded as `core.drift.found`, and network changes as `core.network.rolled_back` and `core.network.confirmed`. Streams under `/v1/streams/` move what is too large for JSON, such as Btrfs send streams and uploads, and are validated and audited like operations.

Other servers are reached with mutual TLS. The panel keeps a CA (`agent.NewCA`) and every certificate names its holder in a URI, `racksail://panel` or `racksail://agent/<server>`, which both sides check instead of host names. A server joins with `racksail agent join`, which trades a one-time token for a certificate at the panel's hub (`agent.Hub`, port 8444 by default), pinning the CA's fingerprint. The hub asks the panel's `MemberChecker` before it takes an agent's connection or renews its certificate, keeps a few idle connections per server, and `Hub.Remove` cuts off a server the panel removed. Its agent then connects out to the hub and serves the panel over those connections (`agent.Connector`), so it needs no open management port, and renews its certificate before it expires; `--listen-tls` also accepts the panel directly.

Operations registered anywhere in the binary are in the agent's allowlist, because `cmd/racksail` imports every module through `modules/all`. After adding or changing one, run `make gen` to update `spec/agent-protocol.json`; a test fails until you do.

## System adapters

`system.Backend` is a struct of interfaces, one per area: facts, users, files, units, packages, Btrfs, mounts, disks, firewall, network, sysctl, processes, certificates, the journal, metrics and program execution.

- `internal/system/linux` implements them for Debian 13. Code that only builds on Linux, such as D-Bus access to systemd, sits behind the `linux` build tag; everything else takes injected dependencies (a command runner, a root directory, a systemd connection), so its tests run on any machine with fakes.
- `internal/system/sim` implements them in memory. A new `sim.Machine` looks like a freshly installed server with a few empty disks, network interfaces, CPUs with their flags and a TPM, and every operation changes it the way it would change a real one, including its journal, metrics and SMART data. `sim.Open` keeps one in a directory between runs, with a copy of its files on disk (`HostPath`), and `Options.Realistic` makes operations take as long as on a small real server. It simulates `timedatectl` and `systemd-creds`; modules teach it about their software with `sim.RegisterPackage` (what a package installs) and `sim.RegisterProgram` (what a program does when run), from `init` functions in their simulation code. It has no network, so the files it downloads, vendor archives and what `Packages.Fetch` is asked for, are the ones `sim.RegisterDownload` or `Machine.SetDownload` gave it. Tests inspect `Calls()`, the latest ten thousand adapter calls, make the next operation fail with `FailNext`, offer upgrades with `OfferUpgrade` and reboot with `Reboot`.
- Rendering is separate from applying. Configuration files are produced by pure functions such as `system.ServiceSpec.Render`, `system.PlatformUnits`, `system.WakeSpec.Units`, `system.NetworkConfig.Render`, `system.FirewallPolicy.Render`, `system.AptSource.Render` and `system.KernelDefaults`, with golden files in `testdata/`; `go test -update` rewrites them after an intended change, and also `packaging/systemd`.
- Archives are packed and unpacked inside a directory opened as a `system.Tree`, which never follows a symbolic link, so neither names in an archive nor links in an account's directory can reach outside it. On Linux trees and `Files.Beneath` views open each directory relative to the one before with `O_NOFOLLOW`, which works the same in the tests on any machine.
- `system.ParseUnitFile` reads unit files as systemd does and `system.CheckUnitFile` and `system.CheckDropIn` hold them to the directives Racksail renders; a fuzz test keeps the parser honest. `system.LoadUnitConfig` finds what systemd applies to a unit from its search path (`UnitSearchPath`, or `PersistentUnitSearchPath` for what survives a reboot): the unit file of the highest priority, or its template's, then its drop-ins sorted by name, whichever directory they are in, those of its template, dash prefixes, aliases and type included, each hiding its namesakes of lower priority; `UnitResourceLimits` reads the limits they leave. Both backends read persistent limits this way, and the simulated server keeps what `SetProperties` sets where systemd does, one drop-in per property under `system.control`.

## The edge

`internal/edge` embeds Caddy. Caddy's configuration names no sites: Racksail's router module (`http.handlers.racksail`) serves the `edge.Table`, compiling each route into Caddy handlers in a context of its own and replacing them as the table changes, so adding or changing a site's routing doesn't reload Caddy. Caddy starts with the first route that isn't passive and stops with the last; `Edge.Loads` counts its configuration loads, which the edge's own settings change, and so does dropping the certificates of names no longer served: when an automatic-certificate subject stops being served — a route removed, its hosts changed, or its TLS mode moved off automatic — the keeper reports its cache stale a couple of minutes later and `reloadCaddy` purges it.

Certificates all go through one on-demand automation policy. Its permission module allows the names routes serve and asks the panel about names on routes with `TLS.Ask`; its certificate manager hands out uploaded certificates; and its issuers each take one kind of subject: IP addresses (an ACME profile for short-lived certificates), wildcards (DNS-01, with records the panel publishes), other public names (HTTP-01 and TLS-ALPN-01) and private names (the local CA, which issues everything in development). The edge obtains the certificates of auto routes itself before anyone visits, renews them whether or not anyone does, and lists them with their expiry; `GET /certificates/{subject}/export` hands one to services such as mail.

The edge never calls the panel. It asks its questions (may this name have a certificate, publish this challenge record) through a `Responder`: directly in `racksail dev`, and through `GET /questions` and `POST /answers` on its admin socket otherwise, where `edge.AnswerQuestions` polls. Modules plug their answers into `edge.DefaultAnswers` with `ApproveNames` and `SetDNSChallenger`. `internal/edge/acme_test.go` runs the whole ACME flow against Pebble, Let's Encrypt's test CA, in the test process.

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
| `MigrationProvider` | `Migrations() fs.FS` | Tables of its own in the panel database, migrated under the module's id when the panel starts |
| `RecipeProvider` | `Recipes() fs.FS` | Recipes the module ships |
| `BackupProvider` | `Backups() []Backup` | What data belongs to each kind and how to quiesce it |
| `HealthProvider` | `HealthChecks() []HealthCheck` | Health checks for what the module runs, in the panel's health report while the module is on |
| `OwnershipProvider` | `Ownership(svc *Services) auth.Ownership` | Which reseller each account belongs to, so reseller grants cover their customers. Only the module that keeps resellers implements it |
| `AccountProvider` | `Accounts(svc *Services) addons.Accounts` | Where each account's add-ons run: the account's slug, which names its slice, and its plan's limits. Only the module that keeps accounts implements it; until one does, accounts' add-ons don't run |
| `HintProvider` | `Hints() []Hint` | Descriptions and examples that help the assistant |
| `InboxProvider` | `InboxSources(svc *Services) []inbox.Registration` | What the module finds needs attention, such as failed backups, in the attention inbox while the module is on |
| `Starter`, `Stopper` | `Start`, `Stop` | Work to start when the module is turned on and to stop when it's turned off |

A few rules that aren't obvious from the types:

- `RegisterAPI` runs when `racksail openapi` exports the document, with empty services. Register operations and nothing else there; use `svc` only inside handlers.
- Schema names of module types get the module's name in front (`dns.ZoneSpec` becomes `DnsZoneSpec`), so two modules can use the same type names.
- Errors are package variables: `var ErrZoneTaken = i18n.Define("dns.zone_taken", http.StatusConflict, "{zone} is already hosted on this server.")`, returned as `ErrZoneTaken.New("zone", name)`. Every code the binary can return needs a message with the same English text in the UI, named `error.<code>`, in `ui/src/modules/<id>/messages/en.json` (the shell's catalog for core codes). `TestErrorCodesHaveUIMessages` in `cmd/racksail` checks this.
- Job types are registered with `jobs.RegisterType(jobs.Type{Name: "dns.check", Run: ...})` from `init`.
- Hook points for every operation and stage of every kind exist automatically (`dns.zone.create.validate`, `dns.zone.delete.applied` and so on). A module's manifest declares any others it adds under `hooks`: `system.<service>.<part>` points for configuration it renders, and `provide.<name>` points for capabilities others implement for it.
- `Start` runs when the module is turned on, and when the panel starts while the module is on or active; `Stop` runs when it's turned off and when the panel stops. A module that fails to start while being turned on stays off. After a module is turned on or off, the manager notifies `modules.<id>.on` or `modules.<id>.off` with a `modules.Transition`.

Module states are `core.module` resources, whose `enabled` and `removed` say what is wanted and whose status says where the module is: `off`, `on`, `active` while resources use it, `stopping` while its services stop and `removing` while its software is uninstalled. Every transition is a change set through the pipeline. `Manager.TurnOnRequest` turns a module on with the modules it needs, dependencies first, `TurnOffRequest` turns one off, and `RemoveRequest` removes one, with the deletes of its resources in the same change set unless the data is kept; the kind's own actions change one module at a time. The reconciler of `core.module` checks hardware, dependencies and dependents as it plans, so the preview says why a change can't be made, and plans what the transition does: starting the module, with what it would download from where, install, run, open and take once something uses it (effects whose actions, such as `download_on_first_use`, `Later` tells apart), stopping its services, and uninstalling its software, from `UninstallPlan` and the module's `TransitionPlanner`. What it downloads and how large comes from `Manager.FirstUse`, which asks the agent about the packages the manifest names and which the request builders call before they submit, since plans don't do I/O; it keeps what it found in the module's status (`ModuleStatus.Software`), where plans read it, so a preview and the apply that follows it plan the same however long apart. Turning a module off or removing it leaves alone what other modules still use, those that are on or keep resources they were turned off with: `UninstallPlan` and the stopping effects skip their packages, repositories, runtime directories, units and ports and list them as kept (`ActionKeep`), and `TransitionRequest.Shared` tells a `TransitionPlanner` the same. `Manager.Export` lists a module's resources for keeping before it is removed with its data. Modules install their software the first time a resource needs it, with `modules.InstallSteps` in front of their reconcilers' own steps. `modulestest.Lifecycle` is the on, use, off and remove test every module passes: nothing it installed, wrote or started may be left afterwards but the data the test keeps.

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

The recipe engine (`recipes.Engine`) builds those change sets for the marketplace and anything else that installs from a catalog or a module. `InstallRequest` puts the installation first, a `core.installation` or `core.server_installation` that records where the recipe came from, then the recipe's resources, and last the installation's `check` action with `wait`. Applying the installation checks the upstream files the recipe pins, by checksum and signature (`recipes.Verifier`), before any resource applies, so a file that changed refuses the install with nothing on the server; the `check` action gives each health check its whole timeout and fails the change set when one doesn't pass. `UpgradeRequest` and `UninstallRequest` work the same way, and operations on an installation authorize the caller before they read anything for it. Since the panel works from what an installation records, the kinds' validation checks a spec against the recipe it carries, and the engine's own validate hooks (`Engine.Hooks`, owned by `core:recipes`) hold every create and update, whoever makes it, to its source: the recipe's checksum must be the one the catalog's signed index lists, from the index the panel checked last, or the module's shipped recipe, and the resources it lists that exist must be of their kind and in its scope. A spec the installation had before, as an undo writes, passes, since it was checked when it was written; and a health check reads only resources in the installation's scope. `Catalogs.AddonItem` returns an add-on item with its recipe and manifest, checked against the index, for the add-on runtime. `recipes.Catalogs` fetches catalogs' signed indexes, checks every file it fetches against them and keeps them under the panel's state, only while the marketplace module is on.

## Updates

The updater (`internal/updates`) keeps `core.update_settings`, the server's single update settings, whose actions check for releases, install one, roll it back, upgrade Debian's packages and upgrade Debian to its next release. It only offers a release whose `release.json` the release workflow signed for its tag, checked with `internal/updates/sigstore` against the trusted root Racksail pins. Installing one backs up the panel's database and asks the agent's `InstallRelease`, which downloads the binary through the system's packages adapter (`Packages.Fetch`, which the simulated server answers from its registered downloads) and checks its signature again itself, against the trusted roots the binary pins (`updates.TrustRoot` adds one from an init function), takes the panel's backup into its own state, where the panel can't reach, reading it without following links, snapshots the system, keeps the running binary and arms a guard: a transient unit that runs the binary it replaced fifteen minutes later and rolls the release back, database and all, unless the new release confirmed it works (`Updater.Confirm`, once the core's own health checks pass). Debian's next release is moved to with `SetDebianRelease` and an upgrade of every package, after `core.os_upgrade.check` lets it.

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
	// point is about. The hook may rewrite their specs and add changes and
	// effects; what it does is labelled as its own, and changing anything
	// else counts as a failure.
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
- Every hook works on its own copy of what it's given, the actor included. Validate and observe hooks can't change anything; a modify hook's copy is checked against the original, and if it changed more than it may, the hook has failed (`hooks.overstep`) and its work is thrown away. System hooks patch a copy of the configuration, which only replaces it when the hook succeeds in time. An add-on's modify hook answers with `HookReply.ChangeSet`, which takes the place of its copy, so it goes through the same checks.
- Points outside the change pipeline are run by the code that reaches them: `svc.Hooks.Check(ctx, point, actor, data)` runs validate hooks, as for `auth.login.validate`, and `svc.Hooks.Notify(ctx, point, actor, data)` runs observe hooks, as for `backups.run.after`. Hooks find what happened in `c.Data`. The core notifies `core.boot`, the module manager `modules.<id>.on` and `.off`, and the updater `core.update.before` and `.after`, `core.os_upgrade.before` and `.after`, after checking `core.os_upgrade.check`.
- The add-on runtime (`internal/hooks/addons`) attaches what an installed add-on's manifest declares and calls the add-on over its socket with only what its approved permissions let it read, taking back from a modify hook's answer only the fields it may write (`Grants.Merge`). An add-on an account owner installed, a `core.account_addon`, attaches only to points of the change pipeline (`hooks.AccountPoint`) and never provides a capability; it is never called with what other points describe, and a change set that reaches beyond its account comes to it with the other accounts' changes, and what the change set says as a whole, left out. It comes from a catalog, and runs in its account's slice with memory and CPU capped by the account's plan, as `addons.Accounts` says; without one, it doesn't run (`addons.no_account`). Add-ons call the API through the gateway with a token of their own, as an actor whose grants are their permissions.
- The runtime's own validate hooks (`core:addons`) refuse what only a server admin may approve, `core.override`, `secrets.read` and system hooks, to anyone else, as well as a new program for an add-on that holds one; and an add-on that names a catalog, as every account's add-on does, unless its manifest is the one the catalog's signed index lists and its program one the item's recipe pins (`Options.Catalogs`). Changes that keep the program, and a program the add-on ran before, as an undo puts back, pass.

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

The consumer of a capability also defines its interface: in `internal/hooks/provide_<capability>.go` for the provide points modules share, so the DNS module and the home module's dynamic DNS see the same `provide.dns` interface; in `internal/auth` for `auth.provider` and `auth.mfa`; and `notify.Channel` for `provide.notifier`. Channel types built into the core register with `notify.RegisterType` from `init` functions, and are offered next to the `provide.notifier` providers: email through the mail relay, webhooks, Matrix, ntfy, Telegram, Discord and Slack. Provider settings are a kind's spec like any other, so secret fields in them hold references.

### Extensions

Extend hooks are how add-ons add to the panel declaratively. The add-on runtime registers one per UI entry in the add-on's manifest, at `ui.nav`, `ui.tab`, `ui.card`, `ui.command`, `ui.settings` or `ui.column`, and one at `api.routes` with a handler for the routes it serves under `/api/v1/addons/<id>/`. `Registry.Extensions(point)` lists them for the API and the UI. Modules don't use extend hooks: they add operations through `APIProvider` and screens through their UI entry.

## The API

`internal/api` builds the API on Huma with the standard library's router. Errors become RFC 9457 problem details with the error's code and parameters, whatever produced them: `httpapi.NewError` replaces Huma's error constructor, and errors without a code become `core.internal`, logged and never shown. What Huma finds wrong with a request's parameters and body becomes field errors of the catalog (`i18n.SchemaField`), with their messages, as a spec's check does. Paths under `/api` that no operation matches answer `404` with `core.not_found`, or `405` with `core.method_not_allowed` and an `Allow` header when the path has operations for other methods.

Every request is authenticated, but credentials that aren't valid, such as an expired session cookie, only matter to protected operations, which answer with the authenticator's error. Everything else, the UI and its sign-in screen included, treats the request as anonymous. Without an authenticator (`api.Options.Auth`), protected operations answer `api.no_sign_in`, unless `Options.Dev` signs every request in as the developer. When `panel.allow_from` is set, requests from other addresses are refused with `api.address_not_allowed` before anything else happens.

`Server.Handler` also gives every request an ID (the caller's `X-Request-Id` when it's sensible, otherwise a new `req_` TypeID), which it sends back, logs with the method, path, status, duration, actor and address, and passes on through the context to audit records (`events.WithRequestID`). Requests under `/api` are rate-limited with a token bucket per actor, or per address without credentials (per /64 for IPv6), from `api.rate_limit` and `api.anonymous_rate_limit` in `racksail.toml`. A bucket holds a quarter of a minute's requests and ten more, and answers describe it: `RateLimit-Limit` is what it holds, `RateLimit-Remaining` what is left, and `RateLimit-Reset` the seconds until it is full. A caller over the limit gets `core.rate_limited` with `Retry-After`, and so does any other answer of 429 whose problem says `retryAfter`, such as those of the limits on signing in and of lockouts (`retryAfterHeader`). The health check is never limited. `GET /api/v1/metrics` reports the API's own counts and what `Options.Metrics` adds, in the Prometheus text format, to callers with `core.metrics.read`.

### Generated operations

Every registered kind gets its operations without code of its own, under `/api/v1/<module>/<plural>` (`api.KindPath`): list, get, create, update, delete and history, and `POST .../{id}/actions/<action>` for each action. Their IDs are `listDnsZones`, `getDnsZone`, `createDnsZone`, `getDnsZoneHistory`, `checkDnsZone` and so on; their schemas are the kind's spec and status types, and `DnsZone`, `DnsZoneList`, `DnsZoneCreateRequest` and `DnsZoneUpdateRequest` built around them; and their summaries and descriptions come from `KindInfo`, which is why those are written for people. Lists take the owner, labels, phase, search text, sort, field selection and every index of the kind as parameters, and pages with cursors. Changes go through the change pipeline. An action that isn't `Long` answers when it's done, with its output in the change set, or with `202` after 30 seconds while its job carries on. `GET /api/v1/kinds/{name}` describes a kind with standalone JSON Schemas of its spec, status and action inputs and outputs, for forms. `APIProvider` is for operations beyond the generated ones; kinds' operations are registered after modules' own, so a schema a kind builds never takes a name a Go type needs.

The core's own operations sit next to them:

| Path | What |
|---|---|
| `/changesets` | List change sets and previews, get one, submit several changes as one, apply a preview by ID, undo |
| `/jobs` | List jobs, get one, read and follow its log, cancel and retry |
| `/events`, `/events/stream`, `/events/types` | The timeline, live, and the catalog of event types |
| `/audit`, `/audit/export` | The audit log, and its export as CSV or JSON lines |
| `/resources/{id}`, `/resources/stream` | Any resource by its ID, and live notices of changes to resources |
| `/core/webhooks/{id}/deliveries` | A webhook's delivery log |
| `/kinds`, `/operations` | The kinds and the operations catalog |
| `/search` | Resources of every kind whose ID, title, subtitle or indexed fields contain some text, a few of each kind |
| `/inbox`, `/inbox/stream`, `/inbox/{id}/dismiss` | The attention inbox, live, and dismissing or snoozing its items |

Live streams are Server-Sent Events with IDs a client sends back as `Last-Event-ID` to resume, and a comment every 20 seconds to keep proxies from closing them. `Server.Close` ends them all, and so does the client going away; a stream never writes after its handler has returned. Every one of these operations authorizes what it returns, as below. Lists of jobs and change sets ask their storage for what the actor may see (`jobs.Filter.Visible`, `changeset.ListQuery.Visible`) and check each item again, reading on until a page is full. Anyone signed in may read and act on the jobs and change sets they started, so the catalog lists no permission for those operations; `core.job.read`, `core.job.cancel`, `core.job.retry` and `core.changeset.read` reach other people's.

Every operation records what it does for the operations catalog with `api.Describe`: the kind and operation, the permission it needs, the risk, and whether it takes `dryRun`, runs as a job, destroys data, can be undone or streams. `GET /api/v1/operations` and the `x-racksail-` extensions in the OpenAPI document come from it, and the CLI and the assistant's tools are built from those, so call `Describe` on every operation a module adds; without it, a GET counts as a read and anything else as medium risk. The fields of what operations take and answer with are described too, with `doc` tags, which become their schemas' descriptions, and the schemas the API builds itself for every kind describe theirs (`described` in `schemas.go`); `TestCoreSchemasDescribeEveryField` fails when a field of the core's schemas has none, and `TestEverySchemaFieldIsDescribed` in `cmd/racksail` when one of the whole document does, those of modules, the extension system, notifications, updates and the catalog included.

### Access

`api.Protected` as an operation's security requirement only establishes who is calling: without a signed-in actor, the request is refused with 401. What the caller may do is decided by the central authorizer, `svc.Authorizer`, every time. Changes are authorized change by change in the pipeline, but reads never go through it, so every handler that returns data authorizes it first. `api.Authorize` and `api.AuthorizeRead` are for an operation's own decision, and record each refusal in the audit log; `api.Readable` filters what a list or stream shows and records nothing, and so does asking `svc.Authorizer` directly:

```go
type delegationInput struct {
	ID store.ID `path:"id" doc:"The zone's ID"`
}

func (m *Module) RegisterAPI(a huma.API, svc *modules.Services) {
	op := huma.Operation{
		OperationID: "checkDnsDelegation",
		Method:      http.MethodGet,
		Path:        api.Prefix + "/dns/zones/{id}/delegation",
		Summary:     "Check a zone's delegation",
		Description: "Asks the parent zone which name servers it delegates to, and compares them with this server's.",
		Tags:        []string{"DNS"},
		Security:    api.Protected,
	}
	api.Describe(&op, api.Info{Kind: "dns.zone", Operation: "delegation", Permission: "dns.zone.read", Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *delegationInput) (*delegationOutput, error) {
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

A list reads the store through `api.List`, which authorizes it:

```go
page, err := api.List(ctx, svc.Authorizer, svc.Store, store.Query{
	Kind: "dns.zone", Scope: &scope, Cursor: in.Cursor, Limit: in.Limit,
})
```

When a grant covers the whole scope listed (or every scope, when `Scope` is nil), the store's page is used as it is. A list of every scope by an actor whose grants cover only some, such as an account's owner, lists the scopes those grants cover, a reseller's customers' accounts included when the authorizer can list them (`auth.ScopeAuthorizer`, which `RoleAuthorizer` implements with an `Ownership` that is also an `auth.AccountLister`). The timeline and the audit log reach the same scopes. Otherwise the list falls back to the resources the actor holds grants on single resources for (`auth.ObjectAuthorizer`), so a collaborator granted one zone sees that zone. The query names those scopes or resources, so pages stay full. With none of these, the list answers `core.forbidden`. Each resource on the page is authorized again before it's returned. Anything else an operation does is checked the same way, with the permission it needs.

### Changes

An operation that changes something embeds `api.ChangeParams`, which adds the `dryRun` query parameter and the `Idempotency-Key` header every mutating operation accepts, and answers through `api.Submit`: 200 with the preview for a dry run, and otherwise 202 with the change set, whose `job` carries out the system changes. Before an operation reads anything for a change, it leaves the reading to the pipeline, which looks up the key first, or asks `Pipeline.Replay`: the generated updates, deletes and actions only parse the ID in their path, and undoing and applying a preview replay the key before they read the change set. An operation that builds its request from what is there, or checks against it first, answers through `api.SubmitFunc`, which runs the function that builds the request only when the key isn't a retry's (`api.Replayed`): turning a module on reads which modules it needs are off and refuses one that is on, uninstalling reads the installation the first uninstall removed, and removing an SSH key reads the user, and a retry gets the first change set from each. One whose answer holds more than the change set, such as turning a profile on with the modules it left out, or opening an account with its owner's sign-in link, asks `api.Replayed` itself and builds that answer around the first change set. A job's cancel and retry change no settings, so their `dryRun` checks the request and answers with the job, queuing or stopping nothing, as the inbox's dismissals do.

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
op := huma.Operation{
	OperationID: "importDnsZone",
	Method:      http.MethodPost,
	Path:        api.Prefix + "/dns/zones/import",
	Summary:     "Import a zone file",
	Description: "Creates a zone and its records from a zone file, as one change you can preview and undo.",
	Tags:        []string{"DNS"},
	Security:    api.Protected,
}
api.Describe(&op, api.Info{
	Kind: "dns.zone", Operation: "import", Permission: "dns.zone.create", Risk: store.RiskLow,
	DryRun: true, Reversible: true,
})
huma.Register(a, op, func(ctx context.Context, in *importInput) (*api.ChangeSetOutput, error) {
	changes, err := zoneFileChanges(store.AccountScope(in.Body.Account), in.Body.Name, in.Body.File)
	if err != nil {
		return nil, ErrBadZoneFile.New("reason", err.Error())
	}
	return api.Submit(ctx, svc.Pipeline, in.ChangeParams, changeset.Request{
		Title: "Import " + in.Body.Name, Changes: changes,
	})
})
```

`api.Submit` fills in the signed-in actor, and has the pipeline ask a person in the panel to confirm it's them first when what the changes do needs it, as a new user or a user's new email address does (see [Sign-in and sessions](#sign-in-and-sessions)); the pipeline authorizes each change for it. The OpenAPI document is generated from the code and committed as `spec/openapi.json`. Change an operation, run `make gen`, and commit both. Every operation needs a summary and a description, and every parameter, every field of a request body, a kind's spec fields and an action's input among them, and every field of what it answers with a description, which a field takes from its `doc` tag: the API reference, the command line's help, the panel's forms and the assistant's tools are made of them, and `TestEveryOperationIsDescribed` in `cmd/racksail` fails, naming each one, when one is missing, a field of a schema among the document's components by that schema, so the answers of streams and the fields of bodies that aren't components are held to it too.

The core's operations that live outside `internal/api`, those of the extension system, the module manager, notifications and the catalog, register with `api.RegisterExtension` from `init` functions in their packages (`hooksapi`, `modulesapi`, `notifyapi`, `recipesapi`), since those import `api`. `TestEveryPanelOperationIsAuthorized` in `cmd/racksail` builds the whole panel and calls every operation it serves without signing in and signed in without a grant: every one must answer as the authorizer would, forbidden or not found, unless the test's table says it only needs a sign-in, and an operation missing from the table fails it. Add a new operation to that table, with a body its handler takes. The operations generated for kinds are called for every kind the binary registers, a create with an empty spec where the kind's resources belong and the rest on a resource that doesn't exist; `TestEveryOperationIsAuthorized` in `internal/api` calls them on resources that exist, with a module's operation among the core's, and holds anything that answers without signing in to `api.PublicOperations`.

Every update or action is written only at the revision it was planned against, so two changes made at the same moment never undo each other silently. A request that names a revision fails with `core.conflict` when the resource changed since. One that names none asks for the change whatever is there, so when another change of the resource lands between its planning and its writes, `Engine.Submit` plans it again from what landed, three times at most, the way it would have been planned a moment later: it neither undoes the other change nor fails for it, unless changes keep landing first. Undoing and applying a preview plan once.

## Sign-in and sessions

`internal/auth` holds what every part needs to decide who may do what: actors, grants and roles, the permission names and the matrix, and `RoleAuthorizer`. A grant gives a role on a scope, optionally kept to one resource (`Object`), with named permissions for collaborators or special permissions for the other roles, and narrowed by `Limit` for API tokens. Special permissions (`core.impersonate`, `secrets.read`, `core.override`) are never matched by a pattern. What only grants on the whole server carry (`auth.ServerOnly`, such as `core.signin_policy.*`), and changing an account itself (`auth.AboveAccount`) on an account's grants, a grant elsewhere doesn't carry even by name: `Grant.Validate` refuses to name it, the authorizer doesn't count it, and `CanGrant` doesn't let such a grant pass it on. `auth.GrantChecker` decides whether someone may give a grant: only when they could use every permission of it themselves there, which the identity service checks for users and the account validator for accounts opened by resellers. `TestEveryOperationIsInThePermissionMatrix` builds the whole binary's API and fails when an operation names no permission and isn't listed in `api.PublicOperations` or `api.SignedInOperations` with a reason. The matrix counts only where each permission is checked: a kind's levels for its operations, and the levels `RegisterPermission` was given for the rest, such as the server for `core.metrics.read`, which grants on a reseller or an account never reach.

`internal/auth/identity` is the identity service (see its package documentation). Users are `core.user` resources (`identity.UserKind`): username, name, email, language, grants, allowed addresses, SSH keys and whether they're turned off, owned by the server, a reseller or an account, and changed only through the change pipeline, so every change of a user has a preview, hooks at `core.user.*`, rules, history, backups and undo. Its validator keeps grants inside where the user belongs and lets nobody give or take a grant they couldn't use, nobody turn themselves off, and the server keep an admin who can sign in; its reconciler signs out a user who is turned off, removes everything of a user who is deleted, alerts the person about SSH keys someone added, and keeps the user's status, how they sign in, current. Everyone may read their own user and take the `update_profile`, `add_ssh_key` and `remove_ssh_key` actions on it (`auth.SelfPermissions`, `auth.SelfAllowed`), which the `/me` operations submit for them; an API token narrowed to permissions that leave users out, or to some accounts (`Actor.Limit`, `Actor.Accounts`), gets no more of its own user than its grants give, and `Service.ReadOwn` holds the `/me` reads to that too. A change of one's own username, email address, allowed addresses or SSH keys is refused unless it comes from one's own session in the panel, not a token and not someone viewing the panel as one (`ownSignIn`), since each could become a way in that outlasts them. How someone signs in is their credentials, kept apart in the service's own tables (migrations under `identity`, with an in-memory twin) and written column by column, never part of a change set: the password hash, second steps, failed sign-ins and lockout, sessions, devices, API tokens, passkeys, recovery codes, sign-in links and links to outside identities.

The service implements `auth.Authenticator`: a request carries a session cookie (`racksail_session`, `HttpOnly`, `SameSite=Strict`, `Secure` behind HTTPS) or an API token in `Authorization: Bearer`, and becomes an `auth.Actor` with the person's grants, a token's narrowed to what it says, and the names the audit log shows: a token's person (`UserName`) and whoever is viewing the panel as someone (`ImpersonatorName`). The secrets of sessions, trusted browsers, tokens and sign-in links are 256-bit values kept as SHA-256 hashes, and so is the setup code, of about 98 bits; recovery codes, of about 49, are kept as HMAC-SHA-256 with a key of the user's in the secrets store (`auth.KeyedCodeHash`), so the database alone can't be guessed against. A session gets a new secret on every sign-in, step-up and change of impersonation, and when its person's grants change (`Session.GrantsHash`), which `api.Server.Handler` sets as the cookie of the answer. After a change of grants, the one before keeps working for 30 seconds (`rotationGrace`) for requests already on their way; after a step-up it stops at once, so a copy of the old cookie gains nothing. `Store.RotateSession` writes a new secret only over the one it replaces, so of requests racing to rotate a session one wins and the others go on with the secret before, and recording a session's use (`Store.TouchSession`) writes nothing but when and from where. `api.Server.Handler` also works out the client's address (believing `X-Forwarded-For` only on a request that came in on the panel's edge-facing Unix socket, `/run/racksail/panel.sock`, which the edge proxies through and account sandboxes can't reach, and from loopback only in development, where the edge runs in the same process) and the browser's origin for passkeys.

Two middlewares keep sessions honest. `gate` keeps a session whose person must first set up a second step or choose a password (`Actor.Pending`) to the operations that let them, and asks for a recent confirmation (`identity.Service.RequireStepUp`, within the sign-in policy's minutes) before operations marked `Info.StepUp` and critical changes. Only a preview spares the confirmation: a request for a dry run of an operation that takes `dryRun` (`Info.DryRun`), read the same way by Huma and by `url.Values` (`previewOnly`); other operations ignore the parameter and go ahead, so it never spares them. Opening an account with an owner asks for it too, as adding a user does. Changes are also judged by what they do, however they're sent: `api.Submit`, which every operation that changes something answers through, modules' and the extension system's included, and the operations that apply a preview and undo a change set give the pipeline `confirmPlan` (in `security.go`) as their `Confirm`, which it asks about the change set as planned, just before writing it. A critical change set needs a recent confirmation, and so does every change of a kind with `Confirm` but its low-risk actions, and anything that gives a user another username or email address than the one it was planned from (`identity.SignInMatchChanged`), their own or someone else's, since a provider that matches people by either the first time they sign in could make it a way in. Addresses are compared trimmed and in lowercase (`identity.NormalizeEmail`), as the users' email index keeps them and outside sign-in looks them up, not with Unicode's case folding, which would count an address with a long s as the same. What hooks added is judged with the rest, an undo by the changes it makes, and whoever makes the change needs no permission to read what it changes for it to be judged. The pipeline writes the change set only at the revisions it was planned against, so a change landing after the judgment has the request planned and judged again from what landed, or the apply of a preview fail with `core.conflict`, rather than make a new name put back an old address. Operations marked `Info.Session`, which are signing in and changing how the caller signs in, refuse API tokens with `auth.panel_only`; the service refuses them itself as well (`requirePerson`). The racksail command leaves these out. Cross-site requests that change something are refused (`http.CrossOriginProtection`), and API answers carry a Content Security Policy that loads nothing.

Sign-in steps live in `signin.go`, `passkeys.go` (WebAuthn through `go-webauthn`, with the relying party worked out from the origin the panel was opened at), `totp.go` in `internal/auth`, and `providers.go`, which reaches outside identity providers and add-ons' second steps through the `auth.provider` and `auth.mfa` provide points; `auth.login.validate` lets policies refuse a sign-in. A provider whose `Open` returns an `auth.Caller` rather than the capability's interface, as an add-on's does, is reached through `auth.RemoteIdentityProvider` and `auth.RemoteMFAMethod`, which speak the methods of `internal/auth/remote.go`; the official LDAP and OpenID Connect add-ons in `addons/` answer them. An outside identity signs in as the user it is linked to, or matches one the first time by username or email, never a user with a grant on the whole server; people link identities themselves from their security settings (`LinkMine`, `BeginLinkMine`). Per-address attempt limits (`limits.go`) and per-user lockouts slow down guessing, both saying how long to wait in `retryAfter`, which answers carry as `Retry-After`, and a wrong password for a name nobody has, or for someone without a password, takes as long as checking one (`auth.WasteTime`); `Store.RecordFailure` counts failures in one write, so attempts at the same moment each count, and failures at outside directories count the same. The sign-in policy (`core.signin_policy`, a singleton) and providers (`core.signin_provider`) are kinds whose reconcilers hand the service the current settings. A provider names what an add-on or a module provides at `auth.provider` or `auth.mfa`: its reconciler, the service's own (`Service.ProviderReconciler`), refuses one that names anything else in the change pipeline, where the context carries who makes the change (`auth.provider_missing`, pointing at `spec.provider`), and outside it marks one whose provider went, as when its add-on was removed, degraded with the same code; the service's `Run` has every provider reconciled again when what add-ons provide changes, since add-ons attach theirs as they're reconciled, after the panel starts. Security alerts go to the channel each person chooses (`alerts.go`), of the notification channel types registered with `notify.RegisterType` and those `provide.notifier` add-ons offer, built with the notification service's client and mail relay (`Options.Channels`). A person's channel settings are kept encrypted whole, so their secret settings hold the values themselves and never name one of the panel's secrets. A sign-in through a provider's own page keeps the browser's trusted-device cookie from its start, on the panel's own page, since the cookie is `SameSite=Strict` and the way back from the provider's site doesn't carry it.

The first admin comes from `bootstrap.json`, which the installer leaves with the answers' account, or from a one-time setup code `racksail panel` prints and keeps in `state/setup-code` for the console until someone uses it, when the service removes it (`Options.SetupFile`; `cmd/racksail/bootstrap.go`). Either way the admin is added through the change pipeline, as the panel itself holding the first admin's grants, after their password hash is kept: credentials of a user whose resource never came are removed an hour later. `racksail dev` signs requests without credentials in as its developer through `identity.DevAuthenticator`, recording each with the address it came from and the surface it came through (`auth.SourceOf`: the panel's pages, which say where their requests come from in `Sec-Fetch-Site`, the racksail command by its User-Agent, or any other program as the API); `dev --sign-in` behaves as a server does.

Opening an account with an owner (`openCoreAccount`) is one change set with both, so a taken username leaves no account behind; the owner then gets a sign-in link. People who may help a user back in set a password for them, or ask them to choose a new one, with `setUserPassword`, which isn't a change of the user's settings.

Impersonation (`impersonate.go`) starts a session of the viewed user marked with who is viewing; everything done in it is recorded with both, it lasts an hour at most, and what the user signs in with is out of its reach (`auth.ErrImpersonating`), their sessions, trusted browsers, passkeys' names and where their alerts go included, as is helping others back in. The identity screens leave out what would change any of it while someone views the panel as the person (`useViewing` in `ui/src/app/identity/session.tsx`), rather than offer what is refused. Accounts are the `core.account` kind in `internal/auth/account`: its reconciler makes the Linux user and group, the home and the slice through typed agent operations, and `account.Ownership` tells the authorizer which reseller each account belongs to, including accounts a reseller is opening that the store doesn't have yet (`Ownership.Expect`).

## The web UI

The UI lives in `ui/`: React 19, TypeScript, Vite, TanStack Router and Query, Tailwind CSS v4 and shadcn/ui on Radix. `npm run build` writes it to `internal/webui/dist/app`, which the binary embeds.

- `src/app/` is the shell: providers, the router, the frame with the icon rail and top bar, the home screen and error screens (`errors.tsx`; every route catches what its screen throws, so a screen that fails shows what went wrong in the shell, with Try again and the technical details folded away), the change tracker (`changes/`), page titles (`page-title.tsx`) and the general preferences (`general.tsx`). `src/app/screens/` holds the panel's own screens, which load when they're first shown: the generic screens of every kind (`resources/`), the jobs, events, changes and search screens, and the attention inbox.
  - `identity/` is signing in and everything about people: the sign-in screens, the first admin's setup and sign-in links (`sign-in.tsx`), what someone must do before the panel opens (`pending.tsx`), confirming it's you (`step-up.tsx`, whose `guard` asks and runs an action again, and which also asks when any change fails with `auth.step_up_required`), the impersonation banner, the user menu and the command bar's entries for people and access (`commands.tsx`, whose additions open a screen's form with `?new=1`, `asked.ts`), and the screens under `account/` (your profile, security, sessions, tokens and SSH keys) and `people/` (users, roles, accounts, the audit log and the sign-in settings). `app.tsx` shows the sign-in screens instead of the shell when the session answers 401, and reads the session again when any request finds the person signed out. Its messages are in `identity/messages/`, loaded with its screens rather than with the shell, except the few the shell shows itself and the entries of the settings index and the command bar, which are in the screens' catalog.
- `src/design/` holds `theme.css` (generated by `racksail theme css`), `globals.css`, which maps the tokens onto Tailwind and shadcn names, `tokens.ts`, the theme engine and the components.
  - `theme/` is the browser's half of `internal/theme`: the same checks, CSS renderer and layering, driven by `themes.json` (generated by `racksail theme spec`), whose rules and rendered layers the tests compare against. `provider.tsx` applies the server and reseller themes as a style element and personal preferences as attributes on the root element.
  - `components/ui/` are the shadcn/ui primitives on Radix; next to them are the data table, charts on uPlot, the CodeMirror editor, the xterm.js terminal, the change preview, the jobs drawer and the form generated from a JSON Schema.
- `src/lib/` holds the API client (`api/schema.d.ts` is generated from `spec/openapi.json` by `npm run gen:api`), live data, i18n and helpers.
  - `api/operations.ts` has the calls the shell and several screens share, those of the jobs, the event timeline and the session, on the same `openapi-fetch` client, so the generated types check them; the paths of their live streams are checked against the document's too. Every operation the UI calls is in `spec/openapi.json`; the UI declares none of its own.
  - `live.tsx` keeps TanStack Query data current over Server-Sent Events, one connection per stream. A reconnect can't resume where the stream left off, so after one, and on a `resync` event, live queries load again. Data the panel answers 404 for isn't streamed.
  - `i18n.tsx` loads the shell's and each module's messages per locale; `pseudo.ts` makes the en-XA and ar-XB pseudo-locales, and `src/test/pseudo.ts` finds text that skipped the catalogs.
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

Each module's messages live in `src/modules/<id>/messages/en.json`, with ids starting with the module's id (or `error.<id>.` for its error codes); `messageIds` turns a catalog's ids into typed descriptors. The shell's own messages are in `src/app/messages/en.json`. The panel's own screens have a catalog of their own, `src/app/screens/messages/en.json`, with the words of the settings screens, the theme editor and the command bar's entries for people and your own account too, which `ScreenMessages` (`screens/catalog.tsx`) loads the first time a screen needs it and shows beside the shell's, so the shell stays within its budget: `screen()` wraps a route's lazy screen in it, and `ScreenOutlet` does the same for every route below one. The attention inbox, which the home screen shows first, has a small catalog of its own in `screens/inbox/messages/`. The words of the core's kinds are in one of their own, `screens/kinds/messages/`, which loads with the screens' catalog, and which the inbox and the home screen's quick actions load alone (`catalog="kinds"`) when they name one of the core's kinds, so the home screen stays light. With `optional`, `ScreenMessages` shows its fallback rather than fail when a catalog can't load. No id is in two of these catalogs, which a test checks.

Before the shell, the app reads `GET /api/v1/session`: who is signed in, with their profile's language, their permission patterns (as roles grant them, such as `*` or `*.read`), each module's state and the person's preferences, which it keeps with `PUT /api/v1/session/preferences` when they change, display and general preferences in one document, so a change of either sends both. The UI also applies the server's and reseller's themes, with where their package files are served, the brand name and the custom CSS URL, when the session names them, which the panel's doesn't yet. `src/lib/api/session.ts` has the document's shape. A panel that answers 404 runs the UI without a session, with every module on and nothing hidden for lack of a permission; the server checks every request either way.

`npm run build` holds the shell to its bundle budget: what the browser loads before it can show the panel, which is the entry chunk with everything it imports and its styles, each module's entry file, the English messages and the theme engine, has to stay under 250 KB compressed (`scripts/bundle-budget.mjs`). Screens, the command bar, the jobs drawer, toasts and pseudo-locales load when they're first needed.

Components take colors, spacing, radii and type only from tokens. Tailwind's default palette and type scale are removed, and `scripts/check-tokens.mjs`, part of `npm run lint`, reports hard-coded colors and text sizes off the scale. Tests use Vitest and Testing Library; `src/app/app.test.tsx` shows how to render the shell or a module's contributions against a fake API, and runs the shell's screens under the pseudo-locale to catch text that skipped the catalogs.

### The panel's own screens

Every kind whose UI hints don't hide it gets generic screens at `/<module>/<plural>`, the path of its API: the list (`screens/resources/list.tsx`), with the columns its hints name, filters, sorting, saved views, and bulk delete and actions, those that take input with the same input for each; `new` and `<id>/edit`, with the form generated from the kind's spec schema and hints, an owner for kinds that belong to accounts or resellers, and labels; and `<id>`, with its settings, details, status and conditions, the history of its settings with what each version changed and undo, its events and its actions, which `?action=<name>` (or `delete`) also opens, as the command bar does. The owner is chosen from the resources of `core.account` or `core.reseller`, or a module's kind of that name, and a kind whose resources own themselves, as accounts do, gets its new resource's ID chosen by the form (`ids.ts`) and given as its owner too. A field with the resource widget lists the resources of the kind its hint names (`FieldHint.Kind`), or takes an ID when it names none. `<id>/edit` makes the change against the settings, labels and revision it opened with, so the panel refuses it with `core.conflict` when someone else changed the resource meanwhile rather than quietly undo their change; the form says what they changed, from the live resource or the refusal, and offers to start again from the latest settings. A module's own routes are static, so they win over the generic ones where they name the same path, and a module without a first screen of its own gets the list of its kinds at `/<module>`. A kind's words are in its module's catalog, or `screens/kinds/messages/` for the core's kinds, under `<module>.kinds.<name>.`: `title` and `plural` for headings, `noun` and `nouns` inside sentences, `description`, `fields.<path>`, `status.<path>`, `values.<path>.<value>` for a field's choices, `groups.<name>`, and `actions.<name>.title` and `.description`. The panel's English is the fallback, so a kind without them still reads well in English.

`/jobs` and `/jobs/<id>` list jobs and show one with its whole log, live while it runs; `/events` is the timeline, with its filters in the address; `/changes` and `/changes/<id>` list change sets, previews that weren't applied included, and show one with undo; `/search` finds resources of every kind (`GET /api/v1/search`); `/resources` lists every kind by module. Screens name their page in the breadcrumb and the browser tab with `staticData.title`, or with `usePageTitle` (`page-title.tsx`) when the name is data, such as a resource's.

Every change a screen makes goes through `ChangeDialog` and `useChangeFlow` (`screens/changes/`): a dry run for the preview, then apply. A preview applies by its ID (`POST /api/v1/changesets/{id}/apply`) unless the change carries secret values, which previews don't keep, and is then sent again; when the change would now do something else, the API answers `changeset.preview_stale` and the dialog shows the fresh preview instead. `apply` never fails: it resolves with the state it left the flow in. A change the toasts follow closes the dialog once it's applied; one someone waits for, an action on one resource that answers when it's done, stays in the dialog with its progress, log and result, and undoing it from there leaves the toast that would offer the same out. The changes screen applies a preview kept from before through the same flow, without the request that made it: a stale one opens the dialog on the fresh preview, and one that carried secret values says it can't be applied and links to the form that makes the change again. Applied change sets go to the change tracker (`changes/tracker.tsx`, with the code that follows them in `changes/follow.ts`, which loads with the first change), which follows their job, shows its progress and outcome in a toast with **Undo**, and loads the resources and change sets they touched again. Undo, there and on every screen, is only offered to those who may do what it takes (`undoPermissions`): deleting what the change created, putting back the settings it changed and creating again what it deleted. An undo is previewed first: one that deletes something, is risky, has conflicting contributions or replaces what changed since (`changedSince`) shows its preview to apply; any other is applied at once. The command bar offers to undo the last change made in the tab.

The command bar adds to the shell's navigation and module commands: resources found by the search once typing settles, every kind's list, a new resource of every kind the person may create (from the operations catalog, `GET /api/v1/operations`), leaving out kinds of modules that are off and those the entries for people and access stand for (`peopleKinds` in `identity/commands.tsx`), which offer their own screens instead, the actions of the resource on screen, what was opened recently (kept in the browser, which the general settings can turn off) and undo. The home screen's quick actions offer new resources the same way after the modules' own commands, for kinds whose module offers no commands (`screens/resources/quick.tsx`). The general settings (`general.tsx`) keep the person's time zone, clock, single-key shortcuts and whether recent items are remembered; like the display preferences, they're kept in the browser and with the account, whose preferences replace the browser's once the session arrives, so they follow the person to every browser they sign in on. What was opened recently stays in each browser. The panel's language is the one chosen in the browser, which the general settings set and saving the profile clears, or else the profile's (`user.locale` in the session), or else the best match for the browser's languages.

## Generated files

| File | Generated by | Checked by |
|---|---|---|
| `spec/openapi.json` | `racksail openapi` | `TestOpenAPIMatchesTheCommittedDocuments` |
| `spec/agent-protocol.json` | `racksail openapi --agent` | the same test |
| `spec/addon-protocol.json` | `racksail openapi --addon` | the same test |
| `ui/src/design/theme.css` | `racksail theme css` | `TestThemeCSSMatchesTheUI` |
| `ui/src/design/themes.json` | `racksail theme spec` | `TestUISpecMatchesTheUI` |
| `catalog/themes/<id>/theme.yaml` | `racksail theme export <id>` for each built-in theme | `TestCatalogThemesMatchTheBuiltins` |
| `brand/logo/`, `brand/icons/`, `brand/terminal/`, the icons in `ui/public/`, `docs/site/src/assets/mark-*.svg` and `docs/site/public/favicon.svg` | `go run ./brand/gen` | `TestGeneratedFilesAreCurrent` in `brand/gen` |
| `ui/src/design/brand.ts`, `internal/theme/mark_grid.go` | `go run ./brand/gen` | the same test |
| `ui/src/lib/api/schema.d.ts` | `npm run gen:api` | `npm run check:api`, part of `make check` |

`make gen` regenerates all of them.
