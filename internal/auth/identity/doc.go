// Package identity is who signs in to the panel and how: users and their
// grants, passwords, passkeys, authenticator apps and recovery codes,
// trusted browsers, sessions, API tokens, SSH keys, sign-in links,
// impersonation, the first admin, outside identity providers and security
// alerts. Service implements auth.Authenticator for the API.
//
// Users aren't resources. A user belongs to one scope, their home, which
// says who manages them: admins of the server, a reseller or an account's
// owner, each holding core.user.* there. Their grants never reach outside
// their home, and whoever gives or takes away a grant must be able to use
// it themselves (auth.GrantChecker). Credentials, sessions and tokens are
// kept the same way, in the identity tables of the panel database (SQL) or
// in memory (Memory); every change is recorded in the audit log and
// published as an event, in the user's home scope.
//
// Secrets that sign people in are never stored as they are: sessions,
// trusted browsers, tokens, sign-in links, recovery codes and the setup
// code are kept as SHA-256 hashes of 256-bit secrets, passwords as Argon2id
// hashes, and authenticator app secrets and alert channel settings in the
// secrets store, encrypted.
//
// The sign-in policy (core.signin_policy) and sign-in providers
// (core.signin_provider) are resources, changed through the change pipeline
// like any other; the service follows them as they change.
package identity
