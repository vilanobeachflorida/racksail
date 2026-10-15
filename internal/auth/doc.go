// Package auth identifies who is acting and decides what they may do.
//
// Every request becomes an Actor: a person, an API token, an automation, an
// add-on, the assistant or the system itself, together with the person
// responsible for it and the surface it came from (UI, CLI, API and so on).
// An Authenticator turns an HTTP request into an Actor; an Authorizer
// decides whether the Actor may use a Permission on a Target.
//
// Permissions are named after kinds and operations, "<kind>.<operation>",
// such as web.site.create or web.site.deploy, plus a few special ones like
// core.override. Roles (admin, reseller, account owner, collaborator,
// read-only) are sets of permission patterns, and grants give a role on a
// scope: the whole server, a reseller, an account or a single resource.
//
// Users, accounts, sessions, passkeys, TOTP, recovery codes and API tokens
// are built on these types.
package auth
