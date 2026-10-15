package hooks

import (
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
)

// The add-on protocol is HTTP and JSON over the add-on's Unix socket. The
// panel calls the add-on for its hooks with the types below; the add-on
// calls the panel's API with a token limited to its permissions.
// ProtocolDocument describes the add-on's side as OpenAPI, committed as
// spec/addon-protocol.json for the SDKs.

// HookCall is what the panel sends an add-on to run one of its hooks, as
// POST /hooks/{point}. The add-on runtime leaves out whatever the add-on's
// permissions don't grant.
type HookCall struct {
	Point     Point                `json:"point" doc:"The hook point, such as web.site.create.validate"`
	Kind      Kind                 `json:"kind" enum:"observe,validate,modify,system" doc:"What the hook may do"`
	Actor     *auth.Actor          `json:"actor,omitempty" doc:"Who is making the change, and through what"`
	ChangeSet *changeset.ChangeSet `json:"changeSet,omitempty" doc:"The change set, at points of the change pipeline"`
	// Change and Changes are indexes into ChangeSet.Changes.
	Change  *int         `json:"change,omitempty" doc:"Validate hooks: the index of the change to decide on"`
	Changes []int        `json:"changes,omitempty" doc:"Modify and observe hooks: the indexes of the changes the point is about"`
	Config  *ConfigPatch `json:"config,omitempty" doc:"System hooks: the configuration to patch"`
	Data    any          `json:"data,omitempty" doc:"What happened, at lifecycle and identity points"`
}

// HookReply is an add-on's answer to a HookCall.
type HookReply struct {
	Deny      bool                 `json:"deny,omitempty" doc:"Validate hooks: refuse the change"`
	Reason    string               `json:"reason,omitempty" doc:"Why the change is refused, shown to the person making it"`
	ChangeSet *changeset.ChangeSet `json:"changeSet,omitempty" doc:"Modify hooks: the change set with the hook's changes, which may be new specs for the changes the point is about, added changes and added effects. A reply that changes anything else counts as a failure of the hook"`
	Config    map[string]any       `json:"config,omitempty" doc:"System hooks: the patched configuration"`
}

// AddonHealth is an add-on's answer to GET /health.
type AddonHealth struct {
	Status     string `json:"status" enum:"ok,degraded" doc:"ok when the add-on can take hook calls"`
	Version    string `json:"version" doc:"The add-on's version"`
	APIVersion string `json:"apiVersion" doc:"The hook API version the add-on speaks, such as v1"`
}

// ProtocolDocument returns the OpenAPI document of the add-on protocol.
func ProtocolDocument() ([]byte, error) {
	cfg := httpapi.Config("Racksail add-on protocol", APIVersion)
	doc := cfg.OpenAPI
	doc.Info.Description = "What an add-on serves on its Unix socket for the panel: its health, and its hooks. " +
		"Hooks answer within their deadline; an error or a timeout counts as a failure, which blocks the change " +
		"only for hooks whose failure mode is block. The panel never sends data the add-on's permissions don't grant."
	schemas := doc.Components.Schemas
	jsonBody := func(t reflect.Type, name string) map[string]*huma.MediaType {
		return map[string]*huma.MediaType{"application/json": {Schema: schemas.Schema(t, true, name)}}
	}
	problem := &huma.Response{
		Description: "The hook failed",
		Content:     map[string]*huma.MediaType{"application/problem+json": {Schema: schemas.Schema(reflect.TypeFor[httpapi.Problem](), true, "Problem")}},
	}
	doc.AddOperation(&huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Check that the add-on is running",
		Description: "The panel calls it after starting the add-on and from its health checks.",
		Tags:        []string{"Add-on"},
		Responses:   map[string]*huma.Response{"200": {Description: "The add-on is running", Content: jsonBody(reflect.TypeFor[AddonHealth](), "AddonHealth")}},
	})
	doc.AddOperation(&huma.Operation{
		OperationID: "runHook",
		Method:      http.MethodPost,
		Path:        "/hooks/{point}",
		Summary:     "Run one of the add-on's hooks",
		Description: "Validate hooks allow or deny one change, modify hooks return the change set with their " +
			"changes, observe hooks only take note, and system hooks return the patched configuration.",
		Tags: []string{"Add-on"},
		Parameters: []*huma.Param{{
			Name: "point", In: "path", Required: true, Description: "The hook point, as in the call",
			Schema: &huma.Schema{Type: "string", Pattern: `^[a-z][a-z0-9-]*(\.[a-z0-9][a-z0-9_-]*)+$`},
		}},
		RequestBody: &huma.RequestBody{Required: true, Content: jsonBody(reflect.TypeFor[HookCall](), "HookCall")},
		Responses: map[string]*huma.Response{
			"200":     {Description: "The hook's answer", Content: jsonBody(reflect.TypeFor[HookReply](), "HookReply")},
			"default": problem,
		},
	})
	return json.MarshalIndent(doc, "", "  ")
}
