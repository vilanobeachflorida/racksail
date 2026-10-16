# Conventions

How we write code, tests and docs in this repository, and what "done" means.

## Repository layout

```
cmd/racksail/            the single binary and its roles
internal/                the core (not importable from outside the module)
  api/                   API server, middleware, errors, OpenAPI export, SSE, WebSocket
  agent/                 agent server and the typed operation allowlist
  auth/                  users, accounts, roles, sessions, passkeys, TOTP, tokens
  changeset/             change sets: diff, plan, preview, apply, undo
  config/                racksail.toml and runtime settings
  edge/                  embedded Caddy and Racksail's Caddy modules
  events/                event bus, timeline, audit log, webhooks
  hooks/                 hook pipeline, permissions, CEL rules, add-on runtime
  i18n/                  server-side messages and error codes
  installer/             install engine, terminal UI, browser mode, answers file
  jobs/                  job queue with progress and logs
  modules/               module manager, manifests, lifecycle, registry
  notify/                notification channels
  recipes/               recipe engine and catalog client
  reconcile/             reconciler framework
  secrets/               secret storage
  store/                 resource store, migrations, TypeIDs
  system/                system adapter interfaces
    linux/               real implementations (build tag linux)
    sim/                 simulated server for development and tests
  theme/                 theme validation, contrast checks, layering
  updates/               self-update and release channels
  webui/                 embedded UI assets
modules/<id>/            one Go package per module
modules/all/             imports every module
ui/                      the web UI (React, TypeScript)
  src/app/               shell, routing, layout, command bar, assistant dock slot
  src/design/            tokens, themes, components
  src/lib/               generated API client, live data, i18n, forms
  src/modules/<id>/      each module's screens, messages and registry entry
sdk/go, sdk/ts, sdk/python   add-on SDKs
spec/                    openapi.json, addon-protocol.json, agent-protocol.json, schemas/
catalog/                 the official catalog (recipes, templates, models, themes)
docs/architecture/       how Racksail is built
docs/site/               the documentation website
brand/                   logo files and brand assets
iso/                     live-build configuration and boot branding
packaging/               systemd units, install script, cloud images
test/                    end-to-end harness and fixtures
```

## Go

- `gofmt`, `go vet` and `staticcheck` clean. No CGO.
- Linux-only code lives in `internal/system/linux` or behind a `linux` build tag. Everything must also build for darwin so development works on any machine. `make check` builds for darwin, linux/amd64 and linux/arm64.
- Wrap errors with `%w` and context. API errors carry a stable `code` (for example `site.domain_taken`) that the UI translates.
- `context.Context` is the first argument of anything that does I/O.
- No global state other than registries populated in `init()`.
- Never log secrets, tokens, passwords or private keys.
- External programs run only through agent operations, with argument vectors. Never `sh -c`, never string concatenation of commands.
- Render and apply are separate. Rendering (config files, units, rulesets) is pure and has golden-file tests. Applying is thin.
- Every kind, action and reconciler has tests against the `sim` backend.
- Prefer the standard library. New dependencies need an OSI-approved license compatible with AGPL-3.0, and a reason.

## API

- Resource paths are plural nouns: `/api/v1/web/sites`, `/api/v1/dns/zones/{id}/records`.
- Actions are `POST /api/v1/<module>/<kind>/{id}/actions/<action>`.
- Every mutating call accepts `?dryRun=true`, which returns the change set without applying it, and an `Idempotency-Key` header.
- Lists are cursor-paginated (`?cursor=`, `?limit=`) and filterable.
- Every operation has a summary and a description written for a person, because the same text documents the API, labels CLI help and describes assistant tools.
- Long work returns `202 Accepted` with a job id. Jobs stream progress over SSE.

## UI

- TypeScript strict. No `any` in new code.
- Components take colors, spacing, radii and type only from theme tokens. Biome and a custom lint rule reject hard-coded colors.
- Every user-visible string goes through react-intl. Messages live in `ui/src/modules/<id>/messages/en.json` or `ui/src/app/messages/en.json`, and those of the panel's own screens, which load with them, in `ui/src/app/screens/messages/en.json`.
- Use CSS logical properties (`margin-inline-start`, not `margin-left`) so right-to-left works.
- Keyboard first: every action is reachable by keyboard and from the command bar. Visible focus rings everywhere.
- WCAG 2.2 AA. axe runs in CI.
- Loading uses skeletons, not spinners. Optimistic updates where safe. Live data over SSE, never polling.
- Copy follows the voice in the brand guidelines: sentence case, plain words, no "please", errors say what happened and what to do.
- Screens are lazy-loaded per module. Bundle budget: the shell under 250 KB compressed.

## Tests

- Go: table-driven tests, golden files for renderers, the race detector in CI, simulated end-to-end flows through the `sim` backend.
- UI: Vitest and Testing Library for components; Playwright against `racksail dev` for flows.
- End to end on Linux: the QEMU harness boots the ISO, installs unattended, reboots and runs the API and UI suites.
- Fuzz every parser that reads outside input: zone files, .htaccess, backup archives, recipes, manifests, themes, uploads.

## Comments and docs

- Comments explain why, not what. Keep them short. No banners, no emoji.
- Exported Go identifiers have doc comments.
- Docs are written for the person using Racksail: sentence-case headings, plain words, short paragraphs, examples.
- Commit messages: an imperative subject line under 72 characters ("Add DNS zone import"), and a body only when the why isn't obvious. No trailers.

## Naming

- Kinds: `<module>.<name>` in lowercase (`web.site`, `storage.pool`). TypeID prefixes are short and unique (`site`, `pool`, `mbox`).
- Hook points: `<kind>.<operation>.<stage>`.
- Error codes: `<module>.<snake_case>`.
- Events: `<kind>.<past-tense verb>` (`web.site.created`, `backups.run.failed`).

## Definition of done

A feature is done when all of these are true:

1. Its kinds, actions and settings are exposed through the API with clear descriptions.
2. It has a screen (generated or custom) with every string translatable.
3. CLI commands exist (generated from the API).
4. Assistant tools exist with the right risk levels.
5. Hook points exist for every operation and stage, and events are emitted.
6. Data it stores is covered by backups, restore and the portable account archive.
7. Anything that runs has metrics and a health check.
8. Unit tests, golden tests and simulated end-to-end tests pass.
9. Its docs page is written.
10. Privileged code has been reviewed against [security.md](security.md), and idle memory targets still hold.
11. There are no placeholders: no "not implemented", no empty handlers, no TODOs standing in for in-scope behaviour.
