// Package updates knows which version of Racksail is running and how to get
// the next one.
//
// Build describes the running binary, from values the release build sets
// with -ldflags and from the Go build information. Versions are semantic
// versions; releases are published on GitHub Releases for the stable, beta
// and nightly channels, each file signed keyless with Sigstore by the
// release workflow, with release.json listing them. The core.update_settings
// kind holds the channel, whether releases install themselves and when, and
// mirrors.
//
// The Updater checks the channel daily: it lists releases and fetches the
// newest one's manifest, checked against Sigstore's trusted root as
// packaging/install.sh pins it. Installing backs up the panel's database
// and hands the release to the agent, which downloads the binary and checks
// its signature again itself, snapshots the system, keeps the running
// binary and restarts Racksail onto the new one. The new release confirms
// itself once its health checks pass; a guard, run from the binary it
// replaced, rolls back a release that doesn't.
//
// Debian's major upgrades go through the same settings, with hooks that let
// modules refuse, prepare for and check after the upgrade.
package updates
