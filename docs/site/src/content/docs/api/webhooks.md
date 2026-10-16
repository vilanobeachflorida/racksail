---
title: "Webhooks"
description: "Signed HTTP calls for every event, with retries and delivery logs."
---

A webhook sends Racksail's events to a URL of your choice as they happen: a site that failed, a backup that finished, a login that was refused. Each delivery is signed, so the receiver can check it came from your server, and one that fails is tried again for most of a day. Every attempt is kept in the webhook's delivery log.

Webhooks belong to the whole server, so managing them takes the `core.webhook` permissions, which administrators have.

## Add a webhook

In the panel, open **Webhooks** from the command bar, or **Resources → Panel → Webhooks**, and choose **New webhook**. The form previews the change before it's made, and the webhook's page has its state, its history and its events, with **Send a test** in its **Actions**.

From the API, a webhook needs the URL to send to, the events to send and a signing secret. Make the secret long and random; this makes one in the format [Standard Webhooks](https://www.standardwebhooks.com/) libraries expect:

```sh
echo "whsec_$(openssl rand -base64 32)"
```

Then create the webhook. The secret goes in `secrets`, so it's stored apart from the settings and never shown again:

```sh
curl -X POST https://panel.example.com:8443/api/v1/core/webhooks \
  -H "Authorization: Bearer $RACKSAIL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "spec": {
      "url": "https://hooks.example.com/racksail",
      "events": ["web.site.*", "*.failed"],
      "description": "Alerts for the ops channel"
    },
    "secrets": { "secret": "whsec_…" }
  }'
```

| Setting | What it does |
|---|---|
| `url` | Where events are sent, with `POST`. Use `https`, so nobody can read the events on the way |
| `events` | The event types to send, as patterns: `*` matches any run of characters and `?` one, so `web.site.*` is every event about sites, `*.failed` every failure and `*` everything |
| `minSeverity` | Leaves out events less urgent than this: `info`, `notice`, `warning`, `error` or `critical` |
| `owner` | Only sends events about the resources of one reseller or account, such as `{"level": "account", "id": "acct_01j9…"}` |
| `disabled` | Stops sending events until you turn it back on, including deliveries waiting to be tried again, which are given up |
| `description` | What the webhook is for |

`GET /api/v1/events/types` lists every event type with what it means and the schema of its data. A webhook gets the events that happen after it was created.

To change the secret, update the webhook with the new value in `secrets`. Deliveries signed after that use the new one.

## Where deliveries can go

Deliveries only go to public addresses. A URL that points at the server itself, at its private network or at a cloud's metadata service, such as `http://localhost:8080`, `http://10.0.0.5` or `http://169.254.169.254`, is refused when you save it, and a name that turns out to lead to such an address is refused when a delivery is sent. Otherwise a webhook could read the server's own services, since every answer's start is kept in the delivery log.

To send events to a receiver on your own network, list its network under `[webhooks]` in `racksail.toml`:

```toml
[webhooks]
allow_networks = ["10.0.0.0/8", "127.0.0.1"]
```

Deliveries go straight to the receiver, never through a proxy.

## What a delivery looks like

Each event is one `POST` request with the event as its JSON body:

```http
POST /racksail HTTP/1.1
Host: hooks.example.com
Content-Type: application/json
User-Agent: Racksail/1.0.0
Racksail-Event: web.site.created
webhook-id: dlv_01j9q4g3b2e7xk0m5c8t9v1w2y
webhook-timestamp: 1760520612
webhook-signature: v1,K5oZfzN95Z9UVu1EsfQmfVNQhnkZ2pj9o9NDN/H/pI4=

{
  "id": "evt_01j9q4g3a8f2b6r1n0s7d5h3kz",
  "type": "web.site.created",
  "time": "2026-10-15T09:30:12.412Z",
  "severity": "info",
  "subject": "site_01j9q4g2z1c4v8w6t3p0m9k7hd",
  "kind": "web.site",
  "scope": { "level": "account", "id": "acct_01j9q3x8m2n5p7r9t1v3w5y7za" },
  "actor": { "type": "user", "id": "user_01j9q3x9b4c6d8f0g2h4j6k8mb", "name": "maria", "source": "ui" },
  "changeSet": "chg_01j9q4g2y7h9k1m3p5r7t9v1wc",
  "job": "job_01j9q4g2y8j0l2n4q6s8u0w2yd",
  "message": "Site example.com was created."
}
```

`subject` is the resource the event is about and `scope` who owns it. `actor` says who did it and through what (`ui`, `cli`, `api`, `automation`, `addon`, `assistant` or `system`); addresses are left out, so they don't leave your server. `changeSet` and `job` link to the change that caused the event, and `data` holds what the event type adds.

`webhook-id` is the same on every attempt of a delivery. Deliveries can arrive more than once, for example when the panel restarted while sending one, and not always in order, so use `webhook-id` to skip ones you've seen and the event's `time` to order them.

## Check the signature

`webhook-signature` is `v1,` followed by the base64 HMAC-SHA256 of `<webhook-id>.<webhook-timestamp>.<body>`, keyed with the webhook's secret. A secret in the `whsec_` format is base64-decoded first; any other secret is used as it is. The header can list several signatures separated by spaces; accept the request when any of them matches.

Check every delivery before you act on it: compute the signature from the raw body, exactly as it arrived, compare it in constant time, and refuse timestamps more than five minutes from your clock, so an old delivery can't be replayed. Any Standard Webhooks library does all of this. By hand, in Python:

```python
import base64
import hashlib
import hmac
import time


def verify(secret: str, headers: dict, body: bytes, tolerance: int = 300) -> bool:
    msg_id = headers["webhook-id"]
    timestamp = headers["webhook-timestamp"]
    if abs(time.time() - int(timestamp)) > tolerance:
        return False
    key = secret.encode()
    if secret.startswith("whsec_"):
        key = base64.b64decode(secret[len("whsec_"):])
    signed = f"{msg_id}.{timestamp}.".encode() + body
    expected = "v1," + base64.b64encode(hmac.new(key, signed, hashlib.sha256).digest()).decode()
    return any(hmac.compare_digest(expected, sig) for sig in headers["webhook-signature"].split())
```

And in Node.js, with the body as a `Buffer`:

```js
import { createHmac, timingSafeEqual } from "node:crypto";

export function verify(secret, headers, body, tolerance = 300) {
  const id = headers["webhook-id"];
  const timestamp = headers["webhook-timestamp"];
  if (Math.abs(Date.now() / 1000 - Number(timestamp)) > tolerance) return false;
  const key = secret.startsWith("whsec_") ? Buffer.from(secret.slice(6), "base64") : Buffer.from(secret);
  const expected = Buffer.from(
    "v1," + createHmac("sha256", key).update(`${id}.${timestamp}.`).update(body).digest("base64"),
  );
  return headers["webhook-signature"]
    .split(" ")
    .some((sig) => sig.length === expected.length && timingSafeEqual(Buffer.from(sig), expected));
}
```

## Answers and retries

Answer with any `2xx` status within ten seconds to accept a delivery; Racksail ignores the body. Do the work afterwards, or in the background, if it takes longer.

Any other answer, no answer in time or a connection that fails counts as a failed attempt, and the delivery is tried again after 30 seconds, 2 minutes, 10 minutes, 30 minutes, 1 hour, 3 hours, 6 hours and 12 hours: nine attempts over almost a day. Then it's marked failed. An attempt cut short, because the panel stopped or the test you started was canceled, counts as a failed one too, since the receiver may have seen it. A few answers change that:

- `Retry-After` with a number of seconds makes the next attempt wait at least that long, up to 12 hours.
- `410 Gone` stops trying that delivery at once.
- Redirects aren't followed. A `3xx` answer fails the attempt, and the delivery log shows where it pointed, so you can update the URL.

After five failed attempts in a row the webhook shows as degraded, and the first delivery that succeeds makes it ready again. Its status shows when the last delivery was sent, how the receiver answered and how many attempts in a row failed.

## The delivery log

| Request | What it does |
|---|---|
| `GET /api/v1/core/webhooks/{id}/deliveries` | Lists the webhook's deliveries, newest first, with how many attempts each took and how the last one went; `state=failed` lists only those that gave up |
| `GET /api/v1/core/webhooks/{id}/deliveries/{delivery}` | Shows one delivery with the body that was sent and every attempt: when, how the receiver answered and the start of its answer |
| `POST /api/v1/core/webhooks/{id}/actions/test` | Sends a `core.webhook.ping` event right away and reports how the receiver answered. Tests aren't tried again, and a webhook that is turned off sends nothing |
| `POST /api/v1/core/webhooks/{id}/actions/redeliver` | Sends an earlier delivery's event again, given as `{"delivery": "dlv_…"}`, as a new delivery with the usual retries; the way to send what was given up while the webhook was off |

Deliveries are kept for 30 days; `deliveries` under `[retention]` in `racksail.toml` changes that.
