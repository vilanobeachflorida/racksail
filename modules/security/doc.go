// Package security finds trouble: ClamAV scanning on a schedule and on upload
// with quarantine, web-shell rules for PHP, and system file integrity
// monitoring. Findings show up in the attention inbox and as validate hooks
// that block deploys containing known web shells.
//
// Its screens live in ui/src/modules/security and its documentation in
// docs/site/src/content/docs/modules/security.md.
package security
