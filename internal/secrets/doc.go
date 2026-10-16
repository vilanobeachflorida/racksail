// Package secrets keeps passwords, keys and tokens encrypted at rest.
//
// Secrets are addressed by a Ref, "<owner id>/<name>", so a site's database
// password is "site_01.../db-password". Specs store refs, never values; API
// responses, logs and assistant context show Placeholder instead.
//
// Values are encrypted with AES-256-GCM by a Box, and the ref is
// authenticated with every value, so encrypted values can't be swapped
// between secrets. FileStore keeps them in a directory of the panel's
// state, one file per secret. Its key is sealed with systemd-creds (TPM2
// when present, the host key otherwise) and imported by systemd when the
// panel starts; when the key can't be sealed it is kept in a file only the
// panel can read. Open finds the key, or makes and seals one the first
// time. In development the simulated server seals it. Memory is an
// encrypted in-memory Store for tests.
package secrets
