// Package api is the panel's HTTP API: REST under /api/v1, built code-first
// with Huma on net/http, with RFC 9457 problem details carrying stable error
// codes and an OpenAPI 3.1 document exported to spec/openapi.json.
//
// Server wires the middleware every request goes through (request IDs,
// recovery, access logs, the panel.allow_from allowlist, authentication), the
// public health and version operations, the operations modules add through
// modules.APIProvider and the embedded web UI. Paths under /api that no
// operation matches answer with a problem: 405 with an Allow header when the
// path exists for other methods, 404 otherwise.
//
// Operations that need a signed-in actor declare Protected as their security
// requirement. That only establishes who is calling: every operation also
// asks the central authorizer what the caller may do. Reads never go through
// the change pipeline, so their handlers authorize them with Authorize,
// AuthorizeRead and List, which also honours grants on single resources.
// Changes go through Submit, which carries the dryRun and Idempotency-Key
// parameters every mutating operation accepts (ChangeParams) into the
// pipeline, where each change is authorized again.
//
// Close ends the live streams and the waits for actions when the panel
// shuts down, and GET /api/v1/metrics reports the API's own counts with
// those Options.Metrics adds.
package api
