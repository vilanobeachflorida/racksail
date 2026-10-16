---
title: "Hardening"
description: "Settings that make a server stricter, such as an IP allowlist for the panel."
---

Racksail starts with safe settings: passwords hashed with Argon2id, lockouts after repeated failures, short sessions in cookies other sites can't use, and confirmation before sensitive changes. These settings make a server stricter still, for when you know who uses it and from where.

## Only let your networks reach the panel

The strictest setting keeps everyone else from reaching the panel at all. List the addresses and networks allowed in `racksail.toml`:

```toml
[panel]
allow_from = ["198.51.100.0/24", "2001:db8:10::/48"]
```

Requests from anywhere else are refused before anything else happens, signing in included, with `api.address_not_allowed`. Restart the panel after changing it. Sites, mail and the other services are unaffected; this is about the panel only.

Under **Settings → Sign-in**, **Allowed addresses** does the same for signing in and API tokens, in the panel itself, without a restart. The panel refuses a list that leaves out the address you're connected from, so you can't lock yourself out by mistake, and the server itself is always allowed, so you can get back in from its console.

Each user, and each API token, can also have addresses of their own: give the people who manage the whole server, and the tokens your CI uses, only the networks they work from.

## Require a second step

Under **Settings → Sign-in**, tick the roles that need a **second step**. Their users then set up an authenticator app or a passkey the next time they sign in, before anything else works. Requiring one for admins and resellers is a good start; their passwords open every account they manage.

Passkeys are the strongest second step, since they can't be phished or typed into the wrong site. Encourage people to add two, on different devices, and to keep their recovery codes apart from their phone.

## Sign in with your organisation's accounts

When people already have accounts in a company directory or a single sign-on provider, connect it with the [LDAP or OpenID Connect add-on](../outside-providers/), so leaving the company, a password reset or a second step required there applies to the panel too. Keep **Match** at *None* unless the provider's usernames or verified email addresses are yours to give out; people then link their accounts themselves, and admins of the whole server always do.

## Shorter sessions and stricter lockouts

| Setting | Stricter value | Why |
|---|---|---|
| Sign out after inactivity | 15 minutes | A forgotten browser signs itself out soon |
| Sign out after | 8 hours | Everyone signs in again each working day |
| Trust a browser for | 0 days | Every sign-in asks for the second step |
| Confirming it's you lasts | 5 minutes | Sensitive changes ask again sooner |
| Failed sign-ins before a lockout | 5 | Guessing passwords gets fewer tries |
| Longest an API token lasts | 90 days | Every token gets an end date |
| Sign-in alerts | Every time | People hear about every sign-in to their account |

## API tokens

Give each program its own token, with only the permissions and accounts it needs, an end date and the addresses it runs from, and revoke the ones nobody uses any more. **Settings → API tokens** shows when each was last used. See [API tokens](../api-tokens/).

## Watch the audit log

The **Audit log** shows every sign-in and every change, and every request that was refused. Filter it by the outcome *Refused* to see what someone tried and wasn't allowed to do, and export it regularly to keep it elsewhere. Security alerts tell each person about sign-ins from new devices and changes to how they sign in, so make sure everyone has chosen where theirs go, under **Settings → Security**.
