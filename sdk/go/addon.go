// Package addon is the Go SDK for Racksail add-ons.
//
// An add-on runs as its own sandboxed systemd service and talks to the panel
// over a Unix socket: the panel calls the add-on's hooks, and the add-on
// calls the panel's API with a token limited to the permissions it was
// granted. The protocol is spec/addon-protocol.json in the Racksail
// repository. This module depends on nothing but the standard library, so
// add-ons build it without the rest of Racksail.
package addon

// APIVersions are the hook API versions this SDK speaks, newest first. An
// add-on lists the versions it supports under apiVersions in its addon.yaml,
// and the panel only installs add-ons that share a version with it.
var APIVersions = []string{"v1"}

// Supports reports whether the SDK speaks a hook API version.
func Supports(version string) bool {
	for _, v := range APIVersions {
		if v == version {
			return true
		}
	}
	return false
}
