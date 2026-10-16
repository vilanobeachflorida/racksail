---
title: "Rules"
description: "Short CEL policies that refuse changes or fill in fields of new resources, who they apply to and how to try one first."
---

A rule is a policy of a line or two, written in [CEL](https://cel.dev), the Common Expression Language. It runs inside the panel on every change it applies to, before anything happens, so it needs no add-on, no process of its own and no network access.

There are two types:

- A **validate** rule refuses a change when its expression is true, and tells the person making the change why, in the rule's own message.
- A **default** rule fills in fields of a resource as it is created, or overrides them.

Rules take part in the [hook pipeline](../hooks/) like everything else: a validate rule is a validate hook at the `validate` points of the kinds and operations it names, and a default rule is a modify hook at their `create.plan` points, owned by `rule:<id>`. What a default rule fills in is labelled with the rule's name in the preview, and the audit log records it.

## Examples

Only install open source software from catalogs:

```yaml
name: Open source only
type: validate
kinds: [core.installation, core.server_installation]
operations: [create]
expression: resource.spec.licenseClass != "open-source"
message: This server only installs open source software.
enabled: true
```

Refuse sites on `.invalid` domains:

```yaml
name: No .invalid domains
type: validate
kinds: [web.site]
expression: resource.spec.domains.exists(d, d.endsWith(".invalid"))
message: Sites can't use .invalid domains.
enabled: true
```

Have new notification channels send warnings and worse, unless whoever adds one chooses otherwise:

```yaml
name: Quiet channels
type: default
kinds: [core.notification_channel]
defaults:
  - {field: minSeverity, expression: '"warning"'}
enabled: true
```

## What expressions see

| Variable | What it is |
|---|---|
| `request` | The change: `operation` (`create`, `update`, `delete` or an action's name), `kind`, `source` (`ui`, `api`, `cli` and so on) and `actor` |
| `resource` | The resource as the change leaves it: `id`, `kind`, `scope`, `labels` and `spec` |
| `oldResource` | The resource before the change; `null` for a create |
| `account` | The account the resource belongs to, as a resource; `null` for what belongs to the server or a reseller |
| `plan` | The account's hosting plan, as a resource; `null` when it has none |
| `user` | Who makes the change: `type`, `id`, `name`, `user`, `source` and `roles` |
| `server` | The machine: `hostname`, `fqdn`, `arch`, `memoryBytes`, `cpus`, `kernel`, `os` and `virtualization` |

Specs are JSON documents, so every variable is dynamic: `resource.spec.domains` is whatever list the kind keeps there. The [API reference](../../api/reference/) shows what each kind's spec holds. Besides CEL's standard functions and macros (`exists`, `all`, `filter`, `map` and the rest), expressions can use CEL's string, list, set and math extensions, and optional values: `resource.spec.?quota.orValue(0)`.

A validate rule's expression must give `true` or `false`. A default rule's expressions give the value of their field; `field` is a dotted path into the spec, and the objects on the way are created. Unless `override` is set, a field the request already gave a value keeps it.

## Who a rule applies to

`kinds` and `operations` narrow a rule; left empty they mean every kind and every operation. Default rules only ever apply to creates.

Rules belong to whoever keeps them, and apply only to what that owner owns:

| Rules | Kind | Apply to |
|---|---|---|
| Server rules | `core.policy` | Every change on the server |
| Reseller rules | `core.reseller_policy` | The reseller's own resources and its customers' accounts |
| Account rules | `core.account_policy` | Changes in that account |

Rules never judge rules, so a rule can't stop its own repair. When several rules apply, they run in order of their `priority`, lowest first, then by owner, like any other hooks.

## Limits

- An expression is at most 4,096 characters long.
- One evaluation of one expression may cost at most 100,000 of CEL's cost units: roughly one per operation, plus one for every element a macro such as `exists` visits. That's plenty for checking a spec, and it stops a runaway comprehension over a large list.
- A rule that can't be evaluated, because it runs over its cost or reads a field the wrong way, counts as a failure of its hook: the change goes through, and a rule that fails five times in a row is switched off by the [circuit breaker](../hooks/#the-circuit-breaker) until an admin switches it back on.
- Rules can only refuse and fill in. They never hold `core.override`, so they can't loosen the settings hooks may only make stricter.

A rule that doesn't compile can't be saved: the panel says which part of the expression is wrong.

## Trying a rule

**Settings → Rules → Try a rule** runs a rule on a change you describe, as the hook pipeline would, without saving the rule or making the change. It shows whether the rule applies, what a validate rule decides and the message it would show, what a default rule fills in, how much of the cost limit the evaluation used and the hook points the rule would be attached to. **Save as a server rule** saves it, after a preview.

The tester is in the API too:

```sh
curl -H "Authorization: Bearer $RACKSAIL_TOKEN" -X POST https://panel.example.com:8443/api/v1/rules/test -d '{
  "rule": {"name": "No .invalid domains", "type": "validate", "kinds": ["web.site"],
           "expression": "resource.spec.domains.exists(d, d.endsWith(\".invalid\"))",
           "message": "Sites can'\''t use .invalid domains."},
  "owner": {"level": "server"},
  "operation": "create", "kind": "web.site", "scope": {"level": "account", "id": "acct_..."},
  "spec": {"domains": ["shop.example.com", "shop.invalid"]}
}'
```

```json
{"applies": true, "refused": true, "message": "Sites can't use .invalid domains.",
 "cost": 16, "costLimit": 100000, "points": ["web.site.*.validate"]}
```

## Managing rules

Server rules are listed in **Settings → Rules**, where switching one off or on is a change you preview first. Rules are resources, so the API manages them like any other: `/api/v1/core/policies`, `/api/v1/core/reseller-policies` and `/api/v1/core/account-policies`. A rule's status lists the hook points it is attached to, and says when something it names, such as a kind, doesn't exist.

Changing a rule takes effect for the next change: rules are compiled when they are saved, and again as the panel starts.
