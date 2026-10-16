---
title: "Users, accounts and roles"
description: "Who can do what: admins, resellers, account owners, collaborators and read-only users."
---

Everyone who manages a Racksail server is a **user** of its panel. What a user may do comes from their **roles**, each given on part of the server: the whole server, a reseller and their customers, or one **account**.

## Accounts

An account is a customer or tenant: the sites, mail, databases and files that belong together, and the people who manage them. On the server, an account is:

- a Linux user and group, named after the account's short name, such as `acme`;
- a home under `/srv/racksail/accounts/acme`, which only the account's user and group can enter;
- a systemd slice, `racksail-accounts-acme.slice`, which holds every process the account runs, so the kernel enforces its limits and its services can't see other accounts'.

Open one under **Accounts → Open an account**, or with the API or the [racksail command](../../api/cli/):

```sh
racksail account open --slug acme --name "Acme Inc." \
  --limits-memory-max 2G --limits-cpu 200 \
  --owner-username ana --owner-email ana@acme.example
```

The short name is also the Linux user's name, so it's 3 to 16 lowercase letters and digits, starts with a letter, can't be changed, and can't be the name of a system user. Opening an account can give it an **owner**, who gets a sign-in link to choose their password.

An account's **limits** are what all its processes may use together:

| Limit | What it does |
|---|---|
| Memory | The most memory its processes use together, such as `2G`. They can't go above it |
| Memory before slowing down | Above this, its processes are slowed down and their memory reclaimed |
| CPU | CPU time as a percentage of one CPU: `200` is two whole CPUs |
| Processes | How many processes and threads it may run at once |
| CPU share, disk share | Its share when others want the same, from 1 to 10000; 100 is the default |

Empty means no limit. Changing the limits, like any change, shows a preview first. Deleting an account removes its Linux user, home and files, so its sites, mail and databases have to go first, and the panel asks you to type the account's short name before it goes ahead.

## Roles

| Role | What it may do | Given on |
|---|---|---|
| Admin | Run the whole server: every module, every account and every setting | The whole server |
| Reseller | Sell hosting from the server: their own plans, customers and accounts, and nothing of the server's own | A reseller and their customers |
| Account owner | Run one account: its sites, mail, databases, files and people, within its plan. Only resellers and admins change the account's limits | One account |
| Collaborator | See what they're given, and do only what their permissions say: on a whole account, or on single resources | Anywhere |
| Read-only | See everything their grant reaches, and change nothing | Anywhere |

A user can have several roles in different places, such as owner of one account and collaborator on another. **Settings → Roles and permissions** shows the permission matrix: every permission, and how far each role reaches with it where the permission applies. One that is only about the whole server, such as reading the panel's metrics or changing webhooks, is out of reach of a reseller's or an owner's grant, however much their role carries.

### Collaborators

A collaborator gets the permissions their grant names, as names or patterns, such as `web.site.read` and `web.site.deploy`, or `mail.*`. Without any they can only look. A grant can also be kept to one resource, such as one site, for someone who only deploys that site. A grant only names what its place carries: what belongs to the whole server, such as the sign-in policy or storage, only a grant on the whole server, and changing an account itself only a grant above it.

### Special permissions

Three permissions are never part of a role, even an admin's `*`, and are given by name:

| Permission | What it lets someone do |
|---|---|
| `core.impersonate` | View the panel as one of their customers' users, to help them |
| `secrets.read` | Read secret values back, such as a stored password |
| `core.override` | Let hooks and rules loosen safety rules; only for admins of the whole server |

### What you can give

Nobody can give more than they have: you can only give a role somewhere if you have all of its permissions there yourself, and taking one away takes the same. A reseller can make their customers' users account owners, but never admins; an account owner can add collaborators to their own account. The panel also keeps one admin who can sign in: the last one can't be turned off, deleted or lose the role, and nobody can turn off or delete themselves.

When someone's roles change, their sessions get a new secret on their next request, carrying what they may do now; nobody keeps acting with roles they lost.

Everyone may change their own name, email address, language and SSH keys under **Settings**, whatever their roles, and nothing else of their own user. Their username, email address, SSH keys and the addresses they may sign in from change only in their own session in the panel: not with an API token of theirs, whatever its permissions, and not by someone viewing the panel as them, since any of those could become a way in that outlasts the token or the viewing. A new email address, theirs or anyone's, also asks whoever gives it in the panel to confirm it's them, as SSH keys do.

## Users

Add users under **Users → Add a user**, or with **Add a user** in the command bar (⌘K), which also reaches every screen on this page. Each user **belongs** somewhere, the whole server, a reseller or an account, which decides who manages them: an account's users are managed by its owner, its reseller and the server's admins. Where a user belongs is set when they're added.

Users are changes like any other: adding one, changing their details or roles, and removing one show a preview first, go through the same checks, hooks and rules as every change, are recorded in the audit log, and can be undone. Only who someone is and what they may do is in such a change. How they sign in, their password, passkeys, second steps, sessions and API tokens, is theirs alone and never part of one, so it doesn't show in previews or history, and undoing a change never brings back an old password.

A new user gets in one of two ways: a **sign-in link** to choose their own password, which you pass on to them, or a password you set, which you can ask them to change the first time they sign in.

The same from the [racksail command](../../api/cli/):

```sh
racksail user create --scope account:acct_01j9… --username ana --name "Ana Silva" --grants owner@acct_01j9…
racksail user link user_01j9…
racksail user password user_01j9… --password - --must-change
```

On a user's page you can change their details and roles, see where they're signed in, their API tokens and SSH keys, set a password for them, and:

- **Turn the user off**: they can't sign in, their sessions end and their API tokens stop working, until you turn them back on;
- **Help them back in**, with a new sign-in link, by signing them out everywhere, removing their second steps, revoking their tokens or ending a lockout (see [Sign-in](../sign-in/#getting-back-in));
- **Delete the user**. What they did stays in the audit log.

## Viewing the panel as someone else

To see what a customer sees, someone with `core.impersonate` opens the customer's user and chooses **View as them**. They need the permission where that user belongs and every role the user has; admins of the whole server can't be viewed as. While it lasts:

- a banner across the top of every screen says whose panel it is, in colors no theme can change, its button included;
- everything done is recorded with both names;
- what the person signs in with, their password, second steps, recovery codes, passkeys, outside sign-ins, tokens, SSH keys and email address, stays out of reach, and so do where their security alerts go, their sessions and their trusted browsers: none of them can be changed, ended or forgotten from there, so their screens show them without offering to, and helping others back in waits until the viewing ends;
- it ends after an hour at most, or with **Stop viewing as** in the banner or the command bar.

## The audit log

**Audit log** lists who did what, when, from where and how it went: every change, every sign-in, and every request that was refused. Each person sees what happened where their roles reach. Filter it by who, what, outcome, the resource it was about and when, and download what the filters show as CSV for spreadsheets, or as JSON lines for other tools:

```sh
curl -H "Authorization: Bearer $RACKSAIL_TOKEN" \
  "https://panel.example.com/api/v1/audit/export?format=csv&outcome=denied&since=2026-10-01T00:00:00Z" \
  -o refused.csv
```
