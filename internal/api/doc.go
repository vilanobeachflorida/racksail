// Package api is the panel's HTTP API: REST under /api/v1, built code-first
// with Huma on net/http, with RFC 9457 problem details carrying stable error
// codes and an OpenAPI 3.1 document exported to spec/openapi.json.
//
// Server wires the middleware every request goes through (request IDs,
// recovery, access logs, authentication), the public health and version
// operations, the operations modules add through modules.APIProvider and
// the embedded web UI. Operations that need a signed-in actor declare
// Protected as their security requirement; everything else about access is
// decided by the authorizer in the change pipeline.
package api
