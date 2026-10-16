---
title: "Notifications"
description: "Where the server's alerts go: email, webhooks, Matrix, ntfy, Telegram, Discord and Slack, what each person hears about, quiet hours and the mail relay."
---

Everything that happens on the server is an event in its timeline: a backup that failed, a certificate that couldn't be renewed, a new Racksail release, a hook the circuit breaker switched off. Notifications send the events that matter to where people will see them. Find them in **Settings → Notifications**.

There are two ways to get them:

- **Channels**, which an admin sets up for the server: an ops room on Matrix, a phone with ntfy, an incident tool's webhook.
- **Your notifications**, which everyone chooses for themselves, by email to their own address.

## Channels

| Type | Settings |
|---|---|
| Email | The addresses to send to, through the [mail relay](#the-mail-relay) |
| Webhook | A URL to post to, and an optional signing secret |
| Matrix | The bot account's homeserver, its access token and the room's ID or alias |
| ntfy | The server, ntfy.sh unless you run your own; the topic; and a token or a user name and password for servers that need them |
| Telegram | The bot's token from @BotFather, and the chat's ID, or @name for a public channel |
| Discord | The channel's webhook URL |
| Slack | The incoming webhook's URL |

Modules and add-ons can add more types, through the `provide.notifier` hook point; they are listed with the others.

Each channel chooses what it sends:

- **Events**, as patterns of event types: `*.failed` for every failure, `backups.*` for everything about backups, `*` for everything.
- **At least**: only events at least this urgent, from info, notice and warning to error and critical.
- **Owner**: only events about one reseller's or account's resources, for a channel that belongs to a customer.
- **Quiet hours**, below.

Tokens, passwords, signing secrets and webhook URLs are kept in the secret store, never in the channel's settings or the audit log. **Send a test** sends a test notification at once and says what the other side answered. Adding or changing a channel is a change you preview first.

### Webhooks

A webhook channel posts JSON with the notification's `title`, `body`, `severity` and `url`, and, when it is about an event, the `event` as [API webhooks](../api/webhooks/) send it. A summary of what quiet hours held back carries the notifications in `digest`.

With a signing secret, every post is signed the way [Standard Webhooks](https://www.standardwebhooks.com/) describes, in the `webhook-id`, `webhook-timestamp` and `webhook-signature` headers, so any Standard Webhooks library can check it. `webhook-id` stays the same when a post is tried again, so receivers can ignore repeats.

### Which addresses channels may reach

Webhooks, Matrix homeservers and ntfy servers are addresses someone typed in. So that a channel can't be used to reach the server's own services or the network behind it, channels only connect to public addresses, checked after the name is looked up. To send to a homeserver or an ntfy server on your own network, list its network in `racksail.toml`:

```toml
[webhooks]
allow_networks = ["10.20.0.0/16"]
```

## Your notifications

Under **Your notifications**, everyone with a sign-in chooses:

- whether to get notifications by email at all;
- the events they want, as patterns. Until they choose, it's failures, security events, safe mode, hooks switched off, Racksail's updates and reboots the server schedules for itself;
- how urgent an event has to be, warning and worse unless they choose otherwise;
- events never to send, whatever the patterns say;
- quiet hours of their own.

People only hear about what they may see: a customer gets the events of their own account, never the server's. Email goes to the address on their user, which they change under **Settings → Profile**; a user without one, or turned off, gets none. **Email me a test** checks that email reaches them.

Each person's choices are a resource of their own, `core.notification_preferences`, that only they change. **Save** previews the change before it is made, like every change, and the audit log records it.

## Quiet hours

Quiet hours hold back what can wait. They have a start and an end, such as 22:00 to 07:00, in a time zone, and can start only on some days of the week, such as Friday and Saturday nights.

What arrives during quiet hours is sent as one summary when they end, unless it's urgent enough to go out at once: critical, or as low as you choose. With **Leave out what arrives meanwhile**, held notifications are dropped instead of summed up.

## Delivery

Every notification is kept in the panel's database until it's sent. One that can't be delivered is tried again after 30 seconds, 2 minutes, 10 minutes, 30 minutes, an hour and three hours, and then counts as failed. A channel whose last three attempts failed shows as degraded, with the reason, in **Settings → Notifications**.

**Sent** lists every notification of the last 30 days: when, what, where it went and whether it was sent, held for quiet hours, part of a summary, left out or failed.

## The mail relay

Email goes out through a mail relay: your provider's submission server, or this server's own when the mail module is on.

| Setting | |
|---|---|
| Server and port | The relay's host name; the port is 587 for STARTTLS, 465 for TLS and 25 without encryption, unless set |
| Encryption | STARTTLS, TLS or none; STARTTLS unless set |
| User name and password | If the relay wants a sign-in. The password is only ever sent over an encrypted connection, or to a relay on the server itself |
| Sender address and name | Who email comes from; the name is the panel's brand name unless set |

**Send a test email** sends a short email and says what the relay answered. Emails are laid out with the email template of the panel's theme, with a plain-text version for mail programs that want one; sign-in alerts and messages to customers go through the same relay.

## From the API

```sh
api=https://panel.example.com:8443/api/v1
auth="Authorization: Bearer $RACKSAIL_TOKEN"
curl -H "$auth" "$api/notifications/types"                         # channel types and their settings
curl -H "$auth" -X POST "$api/core/notification-channels?dryRun=true" -d '{
  "spec": {"name": "Ops on ntfy", "type": "ntfy", "settings": {"topic": "acme-ops-7f3k2"},
           "events": ["*.failed", "core.safe_mode.*"], "minSeverity": "warning"}
}'
curl -H "$auth" "$api/notifications?state=failed"                    # what couldn't be delivered
curl -H "$auth" -X PUT "$api/notifications/preferences?dryRun=true" -d '{"email": true, "events": ["*.failed"]}'
```

Changing your preferences answers with its change set; with `dryRun=true` it is the preview, and the same call without it makes the change.

Secret settings go in the change's `secrets`, as `settings.<name>`, so their values never appear in the spec.
