// Package secrets keeps passwords, keys and tokens encrypted at rest.
//
// Secrets are addressed by a Ref, "<owner id>/<name>", so a site's database
// password is "site_01.../db-password". Specs store refs, never values; API
// responses, logs and assistant context show Placeholder instead.
//
// Values are encrypted with AES-256-GCM by a Box whose key is sealed by
// systemd-creds on a server (TPM2 when present, the host key otherwise). The
// ref is authenticated with every value, so encrypted values can't be
// swapped between secrets. Memory is an encrypted in-memory Store for
// development and tests.
package secrets
