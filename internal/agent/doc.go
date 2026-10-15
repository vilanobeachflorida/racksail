// Package agent is the privileged half of Racksail: a server that runs on
// every machine as root and accepts only typed operations from a fixed
// allowlist.
//
// An operation has a name such as "WriteFile", an input type that validates
// every field (paths stay inside allowed roots, names match strict patterns,
// sizes are bounded), an output type, a pure description of its effects for
// change previews, and a Run function written on top of the system adapters.
// Operations are idempotent: running one whose effect is already in place
// reports that nothing changed. In check mode they report what they would
// change without changing it, which is how previews get exact downloads,
// packages and files.
//
// Operations are registered with Register, normally in package variables, so
// the allowlist is fixed when the binary is built. Modules register the
// operations their reconcilers need in their own packages.
//
// Reconcilers build plans as Steps (Op.Step) and run them through a Client:
// Local runs them in-process (development mode and tests), HTTPClient talks
// to the agent's Unix socket. Server exposes the registry over HTTP+JSON with
// an OpenAPI description (spec/agent-protocol.json).
package agent
