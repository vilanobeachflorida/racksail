---
title: "API tokens"
description: "Scoped, expiring tokens for scripts and integrations."
---

An API token lets a program, a script, a CI job or the [racksail command](../../api/cli/), use the panel's API as you. Give each program its own token with only what it needs, and an end date, so a token that leaks is worth little and easy to replace.

## Make a token

Make tokens in the panel, under **Settings → API tokens → Make a token**. Programs can't make tokens, not even with a token of their own.

| Setting | What it does |
|---|---|
| Name | What the token is for, such as *Deploys from CI*, so you know which one to revoke |
| What it may do | **Everything you may do**; **Only look**, which is `*.read`; or **only some things**, as permission names and patterns such as `web.site.deploy` or `dns.*` |
| Accounts | Keeps the token to some of the accounts your roles reach. It acts there as an account owner would, never beyond what your roles allow |
| Allowed addresses | IP addresses and networks the token may be used from, such as your CI's; anywhere the sign-in policy allows when empty |
| Expires | When it stops working. The sign-in policy can set the longest a token may last; then every token needs an end date within it |

A token never does more than you may: it's your roles, narrowed to what the token says. Special permissions such as `core.impersonate` stay with you unless the token names one you have.

The panel shows the token once, when it's made. It starts with `rsk_`; the panel only keeps a hash of it, and a few of its first characters to tell it apart from your others. You can have up to 100.

## Use a token

Send it in the `Authorization` header:

```sh
curl -H "Authorization: Bearer $RACKSAIL_TOKEN" https://panel.example.com/api/v1/session
```

Keep it in a secret store or an environment variable, never in a script you share or a repository. To use it with the racksail command, sign in once and the command keeps it in a profile only you can read:

```sh
racksail login --server https://panel.example.com
```

or give it to each command in `RACKSAIL_TOKEN`.

## What tokens can't do

Tokens are for using the panel, not for changing who can use it. Whatever its permissions, a token can't:

- change how anyone signs in: passwords, second steps, passkeys, API tokens, SSH keys, outside sign-ins and where security alerts go;
- change your name, email address or language;
- sign out sessions or forget trusted browsers;
- view the panel as someone else.

These answer `auth.panel_only`; do them in the panel. Tokens also never need to confirm it's you, since they can only do what they were given.

## Keep track of tokens

**Settings → API tokens** lists your tokens with what each may do, when it expires, and when and from where it was last used. A token you don't recognise, or one that's no longer used, is worth revoking: **Revoke** stops it at once, for every program that has it. Making a token also sends you a security alert, so one you didn't make doesn't go unnoticed.

Admins, resellers and account owners see the tokens of the users they manage on each user's page, and can revoke them. Turning a user off stops all their tokens until they're turned back on; helping a user back in can revoke them for good.

What a program does with a token is recorded in the [audit log](../users-and-roles/#the-audit-log) under the token's name and yours, and requests made with a token count against the API's rate limit for that token.

## When a token stops working

A token that was revoked or has expired, or a value that was mistyped, answers `auth.token_invalid` (401). A request from an address the token or the sign-in policy doesn't allow answers `auth.address_not_allowed` (403), and a token of a user who was turned off answers `auth.disabled`.
