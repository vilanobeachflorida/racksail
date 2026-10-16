---
title: "Using the API"
description: "Tokens, errors, dry runs, idempotency keys, pagination and live updates."
---

Everything the panel does goes through its API, so anything you can do on a screen you can also do from a script, the `racksail` command or another program. This page covers what every operation has in common. The [API reference](../reference/) lists the operations themselves.

## Where it is

The API lives under `/api/v1` on the panel's address, which is port 8443 unless you changed it:

```sh
curl https://panel.example.com:8443/api/v1/health
```

The panel describes its own API in an OpenAPI 3.1 document at `/api/v1/openapi.json`. It's generated from the code that serves the API, so it's always right for the version you run. The same document is in the source repository as `spec/openapi.json`.

## Signing in

In the browser, the panel's session cookie signs requests in. Scripts and other programs send an [API token](../../security/api-tokens/) instead:

```sh
curl -H "Authorization: Bearer $RACKSAIL_TOKEN" https://panel.example.com:8443/api/v1/core/webhooks
```

A token can do what its permissions and accounts allow, and nothing else. Operations that need no sign-in, such as `GET /api/v1/health` and `GET /api/v1/version`, say so in the reference.

What you're allowed to see shapes every answer. A list only holds what you may read, and a resource of another account answers `404`, as if it didn't exist, whether you read it or try to change it. The audit log keeps every request that was refused, so an account's owner can see who tried.

## Errors

Errors are [problem details](https://www.rfc-editor.org/rfc/rfc9457) with a stable `code` you can rely on, and `params` that fill in its message:

```json
{
  "title": "Conflict",
  "status": 409,
  "detail": "example.com is already used by another site.",
  "code": "web.domain_taken",
  "params": { "value": "example.com" }
}
```

Match on `code`, never on `detail`: the English text can be reworded, while codes stay. When some values aren't valid, the answer is `422` with `core.invalid`, and `errors` says what is wrong with each field and where:

```json
{
  "status": 422,
  "code": "core.invalid",
  "detail": "Some values aren't valid. Fix the highlighted fields and try again.",
  "errors": [
    { "location": "spec.domains[0]", "code": "core.bad_value", "message": "…" }
  ]
}
```

A `location` starting with `spec.` points into a resource's settings, `secrets.` at a secret value and `input.` into an action's input. When a request makes several changes, it starts with the change, as in `changes[1].spec.domains[0]`. The request itself is `body.`, `query.` or `header.`, as in `body.scope` or `query.limit`.

A few codes you'll meet anywhere:

| Code | Status | Means |
|---|---|---|
| `core.unauthenticated` | 401 | The request isn't signed in, or its token or session has expired |
| `core.forbidden` | 403 | You're signed in but may not do this |
| `core.not_found` | 404 | It doesn't exist, or you may not see it |
| `core.conflict` | 409 | Someone changed it since you read it; read it again and retry |
| `core.invalid` | 422 | Some values aren't valid; see `errors` |
| `core.rate_limited` | 429 | Too many requests; wait as `Retry-After` says |

## Resources

Everything Racksail manages is a resource of a kind: a site is a `web.site`, a DNS zone a `dns.zone`, a webhook a `core.webhook`. Every kind has the same operations, under the module's name and the kind's plural:

| Operation | Request |
|---|---|
| List | `GET /api/v1/dns/zones` |
| Get | `GET /api/v1/dns/zones/{id}` |
| Create | `POST /api/v1/dns/zones` |
| Change | `PUT /api/v1/dns/zones/{id}` |
| Delete | `DELETE /api/v1/dns/zones/{id}` |
| History | `GET /api/v1/dns/zones/{id}/history` |
| Actions | `POST /api/v1/dns/zones/{id}/actions/{action}` |

A resource has an ID that starts with its kind's prefix, such as `zone_01j9…`, an owner (`scope`: the server, a reseller or an account), its settings (`spec`) and what Racksail last saw of it (`status`). `GET /api/v1/kinds` describes every kind, and `GET /api/v1/kinds/{name}` gives the JSON Schemas of its settings and of its actions' inputs and outputs. `GET /api/v1/resources/{id}` finds any resource by its ID alone.

Each resource has a `revision`. Send the revision you read when you change or delete something, and the request fails with `core.conflict` if someone changed it in between, instead of overwriting their work.

Settings that hold secrets, such as passwords and keys, never appear in answers. Their fields hold a reference, and you send new values in `secrets`, next to `spec`:

```json
{
  "spec": { "url": "https://hooks.example.com/racksail", "events": ["*.failed"] },
  "secrets": { "secret": "<a long random value>" }
}
```

## Changes

Every change, from any screen, script or automation, is a change set: one preview, one apply, one entry in the history and one undo. Creates, changes, deletes and actions all answer with the change set.

Add `dryRun=true` to see what a change would do without doing it:

```sh
curl -X DELETE "https://panel.example.com:8443/api/v1/dns/zones/zone_01j9…?dryRun=true" \
  -H "Authorization: Bearer $RACKSAIL_TOKEN"
```

The preview answers `200` with the field-by-field differences, what it would do to the server (packages to install, files to write, ports to open, services to restart, disk and memory it needs), what hooks and add-ons would add, and how risky it is. Nothing changes.

To go ahead, send the request again without `dryRun`, or apply the preview itself with `POST /api/v1/changesets/{id}/apply`. Applying a preview carries out exactly what it showed: if things changed since and the change would now do something else, the answer is `changeset.preview_stale` with a new preview to look at. A preview is applied once; applying it again while that is under way answers `changeset.applying`. Previews can be applied for an hour.

Without `dryRun`, the settings change at once and the answer is `202 Accepted` with the change set. Its `job` carries out the change on the server; follow it to see the progress (see [Live updates](#live-updates)). Each change in the change set has a `state`: `started` while it runs, `done` once it is in place. Actions that finish quickly answer `200` once they're done, with what they returned in the change set's `output`.

If the job fails, or you cancel it with `POST /api/v1/jobs/{id}/cancel`, the change set ends `failed` with the reason in `error`, and you can undo it. The settings it stored stay, and the panel carries them out at its next resync unless you undo it. `POST /api/v1/jobs/{id}/retry` carries on from the change that didn't finish. It never runs an action that may already have run a second time: run that action again instead. A job is retried once: asking again answers `jobs.already_retried`, naming the job that retries it, so follow or retry that one.

`POST /api/v1/changesets` makes several changes at once, as one change set: a database and the site that uses it, say. The changes are planned in order, and each sees what the ones before it create, so give new resources an `id` up front to refer to them. An ID that belonged to a resource that was deleted can't be used again (`store.id_used`), so its history and secrets stay its own; undoing the delete is the only way to bring it back:

```json
{
  "title": "Create example.com with a database",
  "changes": [
    { "op": "create", "kind": "databases.database", "id": "db_01j9…", "scope": { "level": "account", "id": "acct_01j9…" }, "spec": { "name": "example" } },
    { "op": "create", "kind": "web.site", "scope": { "level": "account", "id": "acct_01j9…" }, "spec": { "domains": ["example.com"], "database": "db_01j9…" } }
  ]
}
```

`POST /api/v1/changesets/{id}/undo` undoes a change set: it puts back the settings it replaced, through a new change set you can preview first like any other. `GET /api/v1/changesets` lists past changes, and `GET /api/v1/dns/zones/{id}/history` every version of one resource's settings.

### Idempotency keys

Networks fail halfway. To retry a change safely, send an `Idempotency-Key` header with any unique string, such as a UUID. A retry with the same key, from the same token or user, returns the first change set instead of making the change twice, for 24 hours. While the first request is still running, a retry gets `changeset.duplicate_request`; wait a moment and try again. Retrying a job takes a key the same way, and a repeat answers with the job the first request queued. Canceling a job accepts one too, though canceling twice changes nothing anyway.

```sh
curl -X POST https://panel.example.com:8443/api/v1/core/webhooks \
  -H "Authorization: Bearer $RACKSAIL_TOKEN" \
  -H "Idempotency-Key: 6f1c2e2a-7d0f-4f7e-9a4e-0f6b1c1e9b0d" \
  -H "Content-Type: application/json" \
  -d @webhook.json
```

## Lists

Lists come a page at a time. An answer holds `items` and, when there is more, `next`; pass it as `cursor` to get the following page. `limit` sets the page size, 50 unless you say otherwise and at most 1000.

```sh
curl "https://panel.example.com:8443/api/v1/web/sites?limit=100&cursor=$NEXT"
```

Lists of resources also take:

| Parameter | Example | Lists |
|---|---|---|
| `scope` | `account:acct_01j9…` | The resources of one owner |
| `label` | `env=production` | Those with a label; repeat it to require several |
| `phase` | `failed` | Those in a phase: `pending`, `applying`, `ready`, `degraded`, `failed` or `deleting` |
| `q` | `shop` | Those whose ID, indexed fields, title or subtitle contain the text |
| `sort` | `-createdAt` | In another order; `-` reverses it |
| `fields` | `spec.domains,status.phase` | Only these fields of each, plus `id` and `kind` |
| `includeDeleted` | `true` | Also those still being removed |

Kinds with indexed fields take those as parameters too: `GET /api/v1/web/sites?domain=example.com`, and `sort` takes their names. Numbers sort by their value and come before text, which sorts alphabetically; a resource with several values sorts by its lowest. The reference lists the parameters of every list.

## Live updates

Racksail sends live updates as [Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html), which browsers read with `EventSource` and other programs as a plain HTTP response:

| Stream | Sends |
|---|---|
| `GET /api/v1/events/stream` | Events as they happen; `type=web.site.*` picks which |
| `GET /api/v1/jobs/{id}/stream` | A job's progress and every line of its log, until it ends |
| `GET /api/v1/resources/stream` | A notice whenever a resource you can see changes; `kind=` picks which |

```sh
curl -N -H "Authorization: Bearer $RACKSAIL_TOKEN" \
  "https://panel.example.com:8443/api/v1/events/stream?type=*.failed"
```

Events and log lines carry an ID. A client that reconnects with a `Last-Event-ID` header gets what it missed while it was away. Resource notices don't: after a reconnect, or a `resync` event saying you read too slowly to be told about every change, read your lists again. Quiet streams send a comment every 20 seconds, so proxies keep them open. When the panel stops or restarts, it ends every stream; reconnect, as `EventSource` does by itself.

## Limits

Each user and each token may make 600 requests a minute, and each address without credentials 120. `rate_limit` and `anonymous_rate_limit` under `[api]` in `racksail.toml` change them. Requests come out of a bucket that holds a quarter of a minute's requests and ten more, 160 for a user, and refills at the steady rate, so short bursts are fine. Every answer says where you stand:

```
RateLimit-Limit: 160
RateLimit-Remaining: 157
RateLimit-Reset: 1
```

`RateLimit-Limit` is how many requests the bucket holds, `RateLimit-Remaining` how many you can make right away, and `RateLimit-Reset` how many seconds until the bucket is full again. Past the limit, the answer is `429` with `core.rate_limited` and a `Retry-After` header saying how many seconds to wait. The health check is never limited, so monitors and load balancers can call it as often as they like.

## Metrics

`GET /api/v1/metrics` reports how the panel's own machinery is doing, in the Prometheus text format: jobs waiting and running, the reconcile queue and its failures, webhook deliveries, and requests refused by the limits. Reading it takes the `core.metrics.read` permission, so give the token your monitoring scrapes with just that one.

## Request IDs

Every answer carries an `X-Request-Id` header. The panel's log records it with every request, and audit records name the request each action came through, so quote it when you report a problem. Send your own `X-Request-Id`, 8 to 128 letters, digits, dots, dashes or underscores, to follow a request from your own logs into the panel's.

## Operations and risk

`GET /api/v1/operations` lists every operation with the permission it needs and how risky it is: `read` changes nothing, `low` is reversible and limited to one resource, `medium` is reversible but visible (a restart, say), `high` deletes data or affects many resources, and `critical` can lock people out or take the server down. The panel asks for confirmation according to the risk, and the `racksail` command and the assistant build their commands and tools from the same list. The OpenAPI document carries the same details as `x-racksail-` extensions on each operation.
