---
title: "Signing in with LDAP or OpenID Connect"
description: "Connect a company directory or a single sign-on provider, and link people's accounts there to their users."
---

People can sign in to the panel with an account they already have elsewhere: their company's **LDAP directory** or **Active Directory**, with the username and password they have there, or an **OpenID Connect** provider such as Keycloak, Authentik, Microsoft Entra ID, Google or Okta, on its own sign-in page. Two add-ons of the official catalog provide them, **LDAP sign-in** and **OpenID Connect sign-in**. Both are free software, built from `addons/` in Racksail's repository.

## Connecting a provider

1. Install the add-on from **Marketplace → Add-ons**. Its permissions ask to reach any address, since which directory or provider it talks to is yours to choose next; it only ever gets the settings below and the sign-ins it checks.
2. Under **Settings → Sign-in**, add a sign-in provider: the add-on's provider, by the name the add-on provides it under, such as `ldap-sign-in`, the title the sign-in screen shows, the settings as JSON, and the one **secret** it needs, the directory's bind password or the client secret, which is stored encrypted and sent to the add-on only with each call. A name nothing on the panel provides is refused, and a provider whose add-on is removed later shows as not available, and can't be changed, until the add-on is back; it can still be removed.
3. Choose how someone signing in for the first time finds their user:

| Match | What happens |
|---|---|
| None | Nobody is matched. People link the account from their own security settings, signed in another way first |
| Username | The user whose username is what the provider says, such as `ada` |
| Email | The one user with the email address the provider says. Two users with it match neither |

Users with a role on the whole server are never matched, whatever the setting: usernames and email addresses are often the person's own choice at the provider, so nobody becomes an admin by picking one. They link their account themselves. Since a username or an email address can decide whom someone signs in as, giving any user another one asks whoever does it in the panel to confirm it's them. **Add users who match nobody** adds a user, with the roles you give, for anyone the provider signs in who isn't linked or matched; you can only give roles you have yourself, and never an admin's.

Connecting, changing and removing a provider are changes like any other, with a preview, the audit log and undo.

## Linking your account

Under **Settings → Security → Outside sign-ins**, **Link** a provider:

- for a directory, type your username and password there. The panel checks them with the directory once and keeps neither;
- for an OpenID Connect provider, sign in on its page, which sends you back to your security settings.

From then on, signing in with that account signs you in as your user. An account at a provider signs in as one user only. Linking or unlinking asks you to confirm it's you, and you get an alert when an account is linked to your user. You can't unlink the only way you have to sign in.

## LDAP settings

| Setting | What it is | To start with |
|---|---|---|
| `url` | The directory: `ldaps://ldap.example.com`, or `ldap://` with `startTLS`. Plain `ldap://` without it would send passwords in the clear, so it's only taken for a directory on the server itself | required |
| `startTLS` | Switch an `ldap://` connection to TLS before anything is sent | `false` |
| `caCertificate` | The PEM certificate of a private authority the directory's certificate comes from | the system's |
| `bindDN` | The account that searches for people, for directories that don't let anyone search; its password is the provider's secret | none: search anonymously |
| `baseDN`, `scope` | Where people are searched for, and whether below it (`sub`) or only its children (`one`) | required, `sub` |
| `userFilter` | How to find someone by what they typed, which goes where `{username}` is, escaped | `(uid={username})` |
| `userDN` | Bind as the person directly, without searching, for directories whose DNs follow from usernames, such as `uid={username},ou=people,dc=example,dc=com` | none |
| `subjectAttribute` | What identifies the person for good, surviving renames; binary values such as `objectGUID` are read as hex | `entryUUID`, or the DN |
| `usernameAttribute`, `nameAttribute`, `emailAttribute`, `groupAttribute` | Where their username, name, email address and groups are | `uid`, `cn`, `mail`, `memberOf` |
| `requiredGroups` | Groups people must be in to sign in, by DN or name, such as `admins` for `cn=admins,ou=groups,dc=example,dc=com` | anyone |
| `timeoutSeconds` | How long each step may take, 1 to 60 | 10 |

For Active Directory:

```json
{
  "url": "ldaps://dc1.corp.example.com",
  "bindDN": "CN=racksail,OU=Service accounts,DC=corp,DC=example,DC=com",
  "baseDN": "OU=People,DC=corp,DC=example,DC=com",
  "userFilter": "(&(objectClass=user)(sAMAccountName={username}))",
  "usernameAttribute": "sAMAccountName",
  "subjectAttribute": "objectGUID",
  "nameAttribute": "displayName",
  "requiredGroups": ["Racksail users"]
}
```

A wrong password, a username the directory doesn't know and someone outside the required groups all look the same on the sign-in screen, and count toward a [lockout](../sign-in/#lockouts-and-rate-limits) like a wrong password here. A directory that can't answer, or settings that can't work, are logged with the reason and leave the other ways to sign in working.

## OpenID Connect settings

Register the panel as a client at the provider, with `https://<the panel's name>/api/v1/auth/providers/<the provider's ID>/callback` as its redirect URI; the provider's ID is on its page under **Settings → Sign-in**.

| Setting | What it is | To start with |
|---|---|---|
| `issuer` | The provider's issuer, exactly as it publishes it, such as `https://id.example.com/realms/staff`; `https` unless the provider is on the server itself | required |
| `clientId` | The client the provider registered; its secret is the provider's secret, and without one the panel is a public client | required |
| `scopes` | What to ask for; `openid` is always among them | `openid profile email` |
| `prompt` | `login` to sign in again every time, `consent`, or `select_account` | the provider's choice |
| `usernameClaim`, `nameClaim`, `groupsClaim` | Which claims say the username, the name and the groups | `preferred_username`, `name`, `groups` |
| `allowedGroups` | Groups people must be in to sign in | anyone |
| `allowedDomains` | Domains their verified email address must be at, such as `example.com` | anyone |
| `acceptUnverifiedEmail` | Pass on email addresses the provider hasn't verified; off, they're left out, since matching by email trusts them | `false` |
| `caCertificate`, `timeoutSeconds` | A private authority to trust, and how long each call may take | the system's, 10 |

The add-on uses the authorization code flow with PKCE, checks the state and nonce of every sign-in, and checks each ID token's signature with the provider's published keys, which it fetches again when the provider rotates them. Tokens signed with the client secret, or not signed at all, are refused, as are tokens for another client, from another issuer or past their expiry. Providers that name themselves in their answers (RFC 9207) must name the right one.

## Writing a provider

An add-on can provide any outside sign-in, or a second step such as approving a sign-in on a phone, by declaring `auth-provider` or `mfa` under `provides` in its [manifest](../../addons/manifest/). The panel calls it at `POST /provide/<capability>/<method>` on its socket, with `{"input": {"settings": ..., "input": ...}}`: the provider's settings, its secret among them as `secret`, and what the method takes. The add-on keeps nothing between calls; what a sign-in in progress needs, it hands the panel as `state`, which the panel keeps and hands back.

| Capability | Method | Takes | Answers |
|---|---|---|---|
| `auth-provider` | `describe` | nothing | `{"method": "password"}` for a username and password typed on the panel's screen, or `{"method": "redirect"}` for the provider's own page |
| `auth-provider` | `check-password` | `{"username", "password"}` | `{"identity": {...}}`, or `{"refused": true, "reason"}` |
| `auth-provider` | `begin` | `{"callback"}`, where the browser comes back to | `{"redirect", "state"}`: where to send the browser |
| `auth-provider` | `complete` | `{"state", "callback", "query"}`, the query the browser came back with | `{"identity": {...}}`, or `{"refused": true, "reason"}` |
| `mfa` | `challenge` | `{"user"}` | `{"prompt", "input", "state"}`: what to ask, and whether the person types an answer |
| `mfa` | `verify` | `{"user", "state", "response"}` | `{"result"}`: `passed`, `pending` while the person hasn't answered, or `failed` |

An identity is `{"subject", "username", "name", "email", "groups"}`, where `subject` identifies the person at the provider for good. A refusal is the person's credentials or choice; an error, answered as a problem, is a provider that couldn't answer, which the panel logs without blocking other ways in. The LDAP and OpenID Connect add-ons in `addons/` are complete examples.
