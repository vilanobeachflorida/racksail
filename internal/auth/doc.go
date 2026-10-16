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
// RoleAuthorizer is the Authorizer the panel uses. It also lists the
// resources grants on single resources reach (ObjectAuthorizer) and the
// scopes grants on whole scopes reach (ScopeAuthorizer), and learns which
// accounts belong to which reseller from the Ownership the module that keeps
// resellers provides; when that Ownership can also list a reseller's
// accounts (AccountLister), lists reach those accounts too.
//
// Users, accounts, sessions, passkeys, TOTP, recovery codes and API tokens
// are built on these types.
package auth
