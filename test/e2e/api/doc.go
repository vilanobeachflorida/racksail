// Package api holds the end-to-end tests of the panel's API. They run
// against a panel that is already up: racksail dev for make e2e-local, or a
// server the QEMU harness installed. Without RACKSAIL_E2E_URL they are
// skipped, so go test ./... stays fast.
//
//	RACKSAIL_E2E_URL=http://127.0.0.1:7170 go test ./test/e2e/api
//
// RACKSAIL_E2E_INSECURE=1 accepts the panel's self-signed certificate,
// RACKSAIL_E2E_VERSION names the version the panel must report, and
// RACKSAIL_E2E_DEV=1 says the panel is racksail dev, which runs anywhere
// against a simulated server, rather than an installed one.
package api
