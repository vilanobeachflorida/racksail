// Package identity is who signs in to the panel and how: users and their
// grants, passwords, passkeys, authenticator apps and recovery codes,
// trusted browsers, sessions, API tokens, SSH keys, sign-in links,
// impersonation, the first admin, outside identity providers and security
// alerts. Service implements auth.Authenticator for the API.
//
// Users are core.user resources (UserKind), changed through the change
// pipeline like any other, so a change of a user has a preview, hooks,
// rules, history and undo. A user belongs to the server, a reseller or an
// account, which says who manages them: those holding core.user.* there.
// Their grants never reach outside where they belong, and whoever gives or
// takes away a grant must be able to use it themselves (auth.GrantChecker);
// the kind's validator and reconciler hold every change to that, whoever
// makes it. Everyone may change their own profile and SSH keys, through the
// user's actions.
//
// How a user signs in isn't part of their resource. Their credentials,
// sessions and tokens are kept apart, in the identity tables of the panel
// database (SQL) or in memory (Memory), and written one field at a time, so
// a change of someone's settings never touches their password and no change
// set, preview or history ever holds one. What the service does to them is
// recorded in the audit log and published as an event, in the user's scope.
//
// Secrets that sign people in are never stored as they are: sessions,
// trusted browsers, tokens, sign-in links, recovery codes and the setup
// code are kept as SHA-256 hashes of 256-bit secrets, passwords as Argon2id
// hashes, and authenticator app secrets and alert channel settings in the
// secrets store, encrypted.
//
// The sign-in policy (core.signin_policy) and sign-in providers
// (core.signin_provider) are resources too; the service follows them as
// they change.
package identity
