---
title: "Sign-in and two-factor"
description: "Passwords, passkeys, one-time codes, recovery codes and sessions."
---

Everyone who uses the panel signs in as a user of their own: with a password, a passkey or an outside provider, then a second step when they have one. Programs use [API tokens](../api-tokens/) instead.

## The first admin

A new panel has nobody to sign in as, so it makes its first admin one of two ways:

- **From the installer.** The account in the installer's answers becomes the first admin, with the password and SSH keys it was given there. The installer hands over the password's hash, never the password.
- **With a setup code.** Without an account from the installer, `racksail panel` prints a one-time setup code in its log, and the server's console shows it too:

  ```
  No admin yet. Open the panel and create the first admin with this one-time setup code:

      WHMZ3-WY84P-SM8XB-95KJ9
  ```

  Open the panel, type the code, and choose the admin's username and password. The code works once; it's gone as soon as the first admin exists, from the console too.

Either way the first admin is a user like any other, added through the change pipeline, so the audit log shows where they came from.

## Ways to sign in

| Way | What it takes |
|---|---|
| Password | The username and password. Passwords are hashed with Argon2id; hashes the installer brought from Debian (yescrypt or SHA-512 crypt) keep working and are replaced with Argon2id ones the next time the person signs in |
| Passkey | Nothing to type: the browser offers the person's passkeys for the panel, and their fingerprint, face, device PIN or security key confirms it. A passkey is a second step in itself, so it signs in on its own |
| Outside provider | The provider's own page, such as an OpenID Connect provider's, or a username and password a directory such as LDAP checks. An admin connects providers under **Settings → Sign-in**, from the [LDAP and OpenID Connect add-ons](../outside-providers/) of the official catalog |

A password needs at least 10 characters, more than a few different ones, and nothing close to the username or to the passwords everyone tries first. A few unrelated words make one that's strong and easy to remember.

**Keep me signed in** keeps the session after the browser closes, until it ends by itself. Leave it off on computers other people use.

## Second steps

A second step means a stolen password isn't enough to sign in. People set theirs up under **Settings → Security**:

- **An authenticator app**, such as Aegis, Google Authenticator or 1Password: scan the QR code, then type a code the app shows to turn it on. Codes change every 30 seconds.
- **Passkeys**, up to 20 per person.
- **Second steps add-ons provide**, such as approving a sign-in on a phone, when an admin has connected one.

Setting up the first second step gives the person 10 **recovery codes**. Each signs in once in place of the second step, for when the phone is lost; keep them apart from the phone, and make new ones under **Settings → Security** when they run low.

The sign-in policy can require a second step for some roles. Someone with such a role who has none yet must set one up right after signing in, before anything else in the panel works.

### Trusted browsers

**Trust this browser**, on the second step, skips the second step in that browser for the number of days the sign-in policy says, 30 to start with. Trusted browsers are listed under **Settings → Security**, where you can stop trusting any of them.

## Sessions

A session lives in a cookie the browser keeps for the panel only: it's `HttpOnly`, so scripts can't read it, `Secure` over HTTPS, and `SameSite=Strict`, so other sites can't use it. The panel gives the session a new secret whenever the person signs in, confirms it's them, or starts or stops viewing the panel as someone else, and when their roles change, so a session always acts with what its person may do now. Requests already on their way with the secret before still go through for 30 seconds.

A session ends after an hour without activity, or 12 hours after signing in however active the person is; the sign-in policy changes both. **Settings → Where you're signed in** lists every session with its browser, address and when it was last used, and signs out any of them, or all but the current one. Changing your password can sign you out everywhere else too.

Requests that change something must come from the panel's own pages: the panel refuses those another site's page sends, from what the browser says about where a request comes from. Its pages load nothing from elsewhere, under a strict Content Security Policy.

## Confirming it's you

Sensitive changes ask the person to confirm it's them, with their password, an authenticator code or a passkey, unless they did in the last 10 minutes. They are:

- changes to how someone signs in: second steps, passkeys, API tokens, SSH keys, outside sign-ins and where security alerts go;
- adding, changing and deleting users, and helping them back in;
- viewing the panel as someone else;
- changes the change pipeline rates critical, such as deleting an account.

API tokens never ask: they can only do what they were given, and can't change how anyone signs in at all.

## Lockouts and rate limits

Ten failed sign-ins in a row lock a user out for 15 minutes, and the person gets an alert. Wrong passwords count the same whether the panel checks them or an outside directory does, and a lockout stops the directory's password too. Failed sign-ins at the same moment each count, so guessing in parallel doesn't get more tries. Passkeys still sign a locked-out user in, so someone guessing at a password can't keep its owner out. Each address may also try to sign in, use a sign-in link or confirm it's someone only so often: 20 tries at once, then one every six seconds. An admin can end a lockout early by helping the user back in.

## Alerts

People choose under **Settings → Security** where their security alerts go, such as an email channel: sign-ins from a device they haven't used before (or every sign-in, or none, as the sign-in policy says), lockouts, recovery codes used, and new or changed passwords, second steps, passkeys, API tokens and SSH keys. Each alert says what happened and from where, so a change they didn't make stands out.

## Getting back in

Someone who forgot their password or lost their second step asks an admin, a reseller or their account's owner, who opens their user and chooses **Help them back in**. That can make a **sign-in link** to choose a new password with, sign them out everywhere, remove their second steps, revoke their API tokens and end a lockout. A sign-in link works once, for a day; one made for a new user works for a week. Whoever makes it passes it on, privately.

Whoever may help someone back in can also **set a password** for them on their user's page, which signs them out everywhere, and ask them to choose their own when they next sign in. Until they do, they can't do anything else in the panel. Nobody sets their own password this way: that's **Settings → Profile**, which asks for the current one.

## The sign-in policy

Admins set how everyone signs in under **Settings → Sign-in**:

| Setting | What it does | To start with |
|---|---|---|
| Sign out after inactivity | Minutes without a request before a session ends | 60 |
| Sign out after | Hours after signing in that a session ends, however active | 12 |
| Trust a browser for | Days a trusted browser skips the second step; 0 turns trusting browsers off | 30 |
| Confirming it's you lasts | Minutes before sensitive changes ask again | 10 |
| Failed sign-ins before a lockout | In a row | 10 |
| Lockout lasts | Minutes | 15 |
| Second step required for | Roles whose users must set one up first | none |
| Allowed addresses | Networks people may sign in and use API tokens from; the server itself is always allowed. The panel refuses a list that leaves out where you're connected from | anywhere |
| Sign-in alerts | From a new device, every time, or never | from a new device |
| Longest an API token lasts | Days; 0 lets tokens last until they're revoked | 0 |

Each user can also have addresses of their own to sign in from, under their user's details. [Hardening](../hardening/) shows these settings put together.
