---
title: "The add-on manifest"
description: "Declare an add-on's hooks, permissions, UI extensions and API routes in addon.yaml."
---

Every add-on describes itself in `addon.yaml`: what it is, which hook API versions it speaks, where it attaches, what it provides and every permission it needs. The panel reads nothing else to decide what an add-on may do, so the manifest is also what people approve when they install it. It is checked against [spec/schemas/addon.schema.json](https://github.com/vilanobeachflorida/racksail/blob/main/spec/schemas/addon.schema.json) and the rules below, by the same code the catalog tool uses.

## An example

```yaml
format: 1
id: site-guard
name: Site guard
version: 1.2.0
license: MIT
source: https://github.com/example/site-guard
author: Example Hosting
description: Refuses deploys that contain known web shells, and shows what it found on each site.
apiVersions: [v1]
hooks:
  - point: web.site.deploy.validate
    kind: validate
    deadline: 5s
    failureMode: block
    security: true
  - point: web.site.*.applied
    kind: observe
permissions:
  hooks: [validate, observe]
  read:
    - kind: web.site
      fields: [domains, runtime]
  network: [signatures.example.com]
  resources: {cpu: 10%, memoryMB: 128}
ui:
  - type: tab
    label: Security
    icon: shield
    kind: web.site
    path: site-tab.html
routes:
  - method: GET
    path: /sites/{id}/findings
    summary: List what the guard found on a site
```

## What it says about the add-on

| Field | Required | What it holds |
|---|---|---|
| `format` | yes | Always `1` |
| `id` | yes | Lowercase letters, digits and dashes, 2 to 63 of them, starting with a letter or digit. It names the add-on's service, socket and API routes, and can't change once the add-on is installed |
| `name` | yes | What people see, up to 80 characters |
| `version` | yes | A semantic version, such as `1.2.0` or `2.0.0-beta.1` |
| `license` | yes | The add-on's license as an SPDX expression, such as `MIT` or `Apache-2.0 OR MIT` |
| `source` | yes | Where its source code is, as a URL |
| `author` | yes | Who makes it |
| `description` | yes | What it does, in a sentence or two |
| `apiVersions` | yes | The hook API versions it speaks, such as `[v1]`. The panel only takes an add-on that speaks its own version |
| `hooks` | | Where it attaches, below |
| `provides` | | Capabilities it implements, below |
| `permissions` | yes | What it may see and do, on [Permissions and sandboxing](../permissions/). An add-on that needs nothing has `permissions: {}` |
| `ui` | | Entries it adds to the panel, on [UI extensions](../ui-extensions/) |
| `routes` | | API routes it serves, on [UI extensions](../ui-extensions/#api-routes) |
| `kinds` | | Kinds of resources it adds, below |

Unknown fields are refused rather than ignored, so a misspelt one is reported.

## Hooks

Each entry under `hooks` attaches the add-on to one point, or to every point a pattern names. [Hooks](../hooks/) lists the points and what each kind of hook may do.

| Field | What it holds |
|---|---|
| `point` | A hook point, such as `web.site.deploy.validate`, or a pattern where `*` stands for one part, such as `web.site.*.validate` |
| `kind` | `validate`, `modify`, `observe` or `system`. Providers go under `provides`, and UI entries under `ui` |
| `priority` | Where it runs among the hooks at the same point: lower runs first, then the hooks' owners in alphabetical order. `0` unless set |
| `deadline` | How long the panel waits for an answer, from `1ms` to `10s`, such as `500ms`. `2s` unless set |
| `failureMode` | What an error or a timeout does: `allow` lets the change through, `block` stops it. `allow` unless set, or `block` for a validate hook with `security: true` |
| `security` | Marks a validate hook as a security policy |

Every hook's kind must be one of the levels `permissions.hooks` asks for: a manifest with a modify hook and no `modify` permission is refused.

## Capabilities

`provides` lists the standard interfaces the add-on implements, which the panel calls at `POST /provide/<capability>/<method>` on its socket:

| Capability | Point | For |
|---|---|---|
| `auth-provider` | `auth.provider` | Signing people in with another directory or identity provider, as the [official LDAP and OpenID Connect add-ons](../../security/outside-providers/) do |
| `mfa` | `auth.mfa` | A second step for signing in, such as approving the sign-in on a phone |
| `notifier` | `provide.notifier` | A channel notifications can go to |
| `dns` | `provide.dns` | Records at an outside DNS service |
| `registrar` | `provide.registrar` | Domains at a registrar |
| `backup-target` | `provide.backup-target` | Somewhere to keep backups |
| `ai` | `provide.ai` | A model provider for the assistant |
| `billing` | `provide.billing` | A billing system |
| `importer` | `provide.importer` | A source to migrate from |

Each capability's interface, the methods it has and what they take, belongs to the part of Racksail that uses it; [SDKs](../sdks/#capabilities) has the methods of sign-in providers, second steps and notification channels. What an add-on provides serves the whole server, so only a server admin installs one that provides anything.

## Kinds

`kinds` declares kinds of resources the add-on adds, which it plans and applies itself, as [Hooks](../hooks/#kinds-an-add-on-adds) explains. Adding kinds takes the `system` hook level. Each kind has:

| Field | What it holds |
|---|---|
| `name` | `<group>.<name>` in lowercase, such as `guard.finding`. The add-on's kinds share one group, which no module or other add-on uses, and never `core` |
| `prefix` | The prefix of its resources' IDs, lowercase letters, up to 16 |
| `scope` | What its resources belong to: `server`, `reseller` or `account` |
| `title`, `plural` | Its name for people, such as `Finding`, and its plural, such as `findings`, which also names its screens in the panel, at `/guard/findings` |
| `description` | What its resources are |
| `schema` | The JSON Schema of their settings |
| `status` | The JSON Schema of the status fields the add-on reports when it applies them |
| `actions` | Actions beyond creating, changing and deleting, each with a `name`, a `summary`, a `risk` of `low`, `medium`, `high` or `critical`, and the JSON Schema of its `input` if it takes one |

## The checks

The panel refuses a manifest, with the reason, when:

- it doesn't follow the schema, or has a field the schema doesn't know;
- it doesn't speak the panel's hook API version;
- a hook's kind isn't among the levels `permissions.hooks` asks for, or its deadline is longer than ten seconds;
- a scope under `permissions.read` asks to delete resources or run actions, which takes `write`;
- it adds kinds without the `system` level, kinds of more than one group or of `core`, two kinds with the same name, prefix or plural, a kind without a title or a description, or an action without a risk;
- a tab or a column names no `kind`, or a column's `path` isn't one of its `GET` routes;
- a route's path starts with `/ui/` or `/frames/`, where the panel serves the add-on's [screens](../ui-extensions/#screens);
- its `id` isn't the id the add-on is installed under.

An add-on an account owner installs, a `core.account_addon`, also may not ask for `system` hooks or `override`, provide a capability, or attach to a point outside the change pipeline, such as `core.boot` or `auth.login.validate`; see [Permissions and sandboxing](../permissions/#add-ons-of-an-account).

`go run ./catalog/tool validate` checks the manifests of a catalog's add-ons the same way, and that each one's id, version and license are those of its recipe; see [Publishing add-ons](../publishing/).

## Versions

The manifest's `version` is the add-on's own. When a new version asks for more than the one before, such as another hook level or another host to reach, installing it needs the new permissions approved. The hook API version, `apiVersions`, is the protocol's: the panel speaks `v1`, and when a new version comes, deprecated ones keep working for a published period, so an add-on that lists both keeps working across the change.
