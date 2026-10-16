# Official add-ons

This directory holds the source of the add-ons the official catalog offers in [catalog/addons/](../catalog/addons/). It is a Go module of its own, `github.com/vilanobeachflorida/racksail/addons`, which depends on nothing but the standard library, and is licensed under AGPL-3.0-or-later like the rest of Racksail.

| Directory | Add-on |
|---|---|
| [ldap-sign-in/](ldap-sign-in/) | Signs people in with the username and password they have in an LDAP directory or Active Directory |
| [oidc-sign-in/](oidc-sign-in/) | Signs people in on an OpenID Connect provider's own page |
| [internal/serve/](internal/serve/) | Serves the add-on protocol on the socket systemd hands an add-on |
| [internal/pack/](internal/pack/) | Writes an add-on's release archive |

Both sign-in add-ons provide the `auth-provider` capability. The panel calls them at `POST /provide/auth-provider/<method>` on their socket, as [spec/addon-protocol.json](../spec/addon-protocol.json) describes, with the settings of the sign-in provider an admin connected and, as `secret` among them, the provider's secret. They keep nothing of their own: what a sign-in in progress needs, the panel keeps and hands back. The methods are those of `internal/auth/remote.go`:

| Method | Takes | Answers |
|---|---|---|
| `describe` | nothing | `{"method": "password"}` or `{"method": "redirect"}` |
| `check-password` | `{"username", "password"}` | `{"identity": {...}}`, or `{"refused": true, "reason"}` |
| `begin` | `{"callback"}` | `{"redirect", "state"}` |
| `complete` | `{"state", "callback", "query"}` | `{"identity": {...}}`, or `{"refused": true, "reason"}` |

An identity is `{"subject", "username", "name", "email", "groups"}`, where the subject never changes for the person at the provider. A refusal is a wrong password, someone the provider doesn't know or turned down; an error, answered as a problem, is a provider that couldn't answer or settings that can't work.

## Building and testing

```sh
make addons-check     # vet and staticcheck for darwin and Linux, and the tests
make addons           # dist/addons/<id>-<version>-linux-<arch>.tar.gz and SHA256SUMS
```

`make check` runs `make addons-check`. The tests run each add-on against a directory or provider of the tests' own, from `ldap-sign-in/ldap/ldaptest` and `oidc-sign-in/oidc/oidctest`, so they need no network. An archive holds the add-on's program alone, called `addon`, and the same source and Go version always give the same archive. Its version is the one in the add-on's `addon.yaml` in the catalog; change both together. `make release` builds the archives too and publishes them with Racksail's release, where the catalog's recipes pin them.

Run an add-on by hand on a socket of your choosing, and call it as the panel does:

```sh
cd addons && go run ./ldap-sign-in --socket /tmp/ldap.sock
curl --unix-socket /tmp/ldap.sock http://addon/health
```
