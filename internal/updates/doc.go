// Package updates knows which version of Racksail is running and how to get
// the next one.
//
// Build describes the running binary, from values the release build sets
// with -ldflags and from the Go build information. Versions are semantic
// versions; releases are published on GitHub Releases for the stable, beta
// and nightly channels, signed with Sigstore and shipped with SBOMs and
// checksums. A Source lists releases, a Verifier checks their signatures,
// and the updater downloads, verifies and applies them, taking a snapshot
// first and rolling back on failure.
package updates
