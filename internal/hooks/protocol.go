package hooks

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
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

// ProvideCall is what the panel sends an add-on to call a method of a
// capability it provides, as POST /provide/{capability}/{method}. The
// capability's interface says what each method takes and returns.
type ProvideCall struct {
	Input any `json:"input,omitempty" doc:"What the method takes, as the capability's interface describes it"`
}

// KindResource is a resource of a kind the add-on adds, as the panel sends
// it to be planned and applied.
type KindResource struct {
	ID         store.ID          `json:"id" doc:"The resource's ID"`
	Kind       string            `json:"kind" doc:"Its kind, one the add-on adds"`
	Scope      store.Scope       `json:"scope" doc:"Who it belongs to"`
	Labels     map[string]string `json:"labels,omitempty" doc:"Its labels"`
	Generation int64             `json:"generation" doc:"Its generation, which goes up whenever its settings change"`
	Spec       json.RawMessage   `json:"spec" doc:"Its settings, as the kind's JSON Schema describes them"`
	Status     json.RawMessage   `json:"status,omitempty" doc:"Its status: what the add-on last reported, with the phase and conditions the panel keeps"`
	Deleted    bool              `json:"deleted,omitempty" doc:"Set while it is deleted: the plan removes what earlier plans wrote, and applying undoes what the add-on did for it"`
}

// KindAction is an action of a kind the add-on adds, to plan or apply.
type KindAction struct {
	Name  string          `json:"name" doc:"The action, as the kind declares it"`
	Input json.RawMessage `json:"input,omitempty" doc:"Its input, checked against the action's JSON Schema"`
}

// KindCall is what the panel sends an add-on to plan or apply a resource of
// a kind it adds, as POST /kinds/{kind}/plan and POST /kinds/{kind}/apply.
type KindCall struct {
	Resource KindResource `json:"resource"`
	Action   *KindAction  `json:"action,omitempty" doc:"Set to plan or apply an action of the resource rather than its settings"`
	// Dir is the add-on's own directory for its kinds, the only place the
	// steps of its plans may write, which its process can read.
	Dir string `json:"dir" doc:"The only directory the plan's steps may touch, which the add-on can read"`
	// Results are what the plan's steps returned, sent with apply.
	Results []agent.StepResult `json:"results,omitempty" doc:"Apply: what each step of the plan returned"`
}

// KindPlan is an add-on's plan of a resource: the steps the agent runs, and
// what applying does beyond them, for the preview. Planning must not change
// anything: the same call always gets the same plan.
type KindPlan struct {
	Steps   []agent.Step    `json:"steps,omitempty" doc:"Agent operations to run on the server: WriteFile, EnsureDir, RemovePath, ReadFile, StatPath and ListDir, of paths in dir"`
	Effects []system.Effect `json:"effects,omitempty" doc:"What applying does beyond the steps, such as records it makes with an outside service, for the preview"`
	Spec    json.RawMessage `json:"spec,omitempty" doc:"Actions: the resource's new settings, when the action changes them"`
}

// KindApplied is an add-on's answer once the steps of its plan have run:
// what it did itself, such as calls to an outside service, is done, and the
// status says how the resource is. An error answer fails the change.
type KindApplied struct {
	Status map[string]any  `json:"status,omitempty" doc:"The kind's own status fields, as its status schema describes them"`
	Output json.RawMessage `json:"output,omitempty" doc:"Actions: what the action returned"`
}

// ProtocolDocument returns the OpenAPI document of the add-on protocol.
func ProtocolDocument() ([]byte, error) {
	cfg := httpapi.Config("Racksail add-on protocol", APIVersion)
	doc := cfg.OpenAPI
	doc.Info.Description = "What an add-on serves on its Unix socket for the panel: its health, its hooks, the capabilities it provides, " +
		"the plans and applies of the kinds it adds, and its routes and screens. " +
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
	segment := func(name, description string) *huma.Param {
		return &huma.Param{Name: name, In: "path", Required: true, Description: description, Schema: &huma.Schema{Type: "string", Pattern: `^[a-z][a-z0-9_-]*$`}}
	}
	doc.AddOperation(&huma.Operation{
		OperationID: "provide",
		Method:      http.MethodPost,
		Path:        "/provide/{capability}/{method}",
		Summary:     "Call a method of a capability the add-on provides",
		Description: "The panel calls it when it uses a capability the add-on declares under provides, such as sending a notification " +
			"for notifier or creating a record for dns. The capability's interface says what each method takes and returns.",
		Tags:        []string{"Add-on"},
		Parameters:  []*huma.Param{segment("capability", "The capability, as under provides"), segment("method", "The method, such as send")},
		RequestBody: &huma.RequestBody{Required: true, Content: jsonBody(reflect.TypeFor[ProvideCall](), "ProvideCall")},
		Responses: map[string]*huma.Response{
			"200":     {Description: "What the method returns", Content: map[string]*huma.MediaType{"application/json": {Schema: &huma.Schema{}}}},
			"default": problem,
		},
	})
	kindParam := &huma.Param{
		Name: "kind", In: "path", Required: true, Description: "The kind, one the add-on's manifest adds, such as guard.rule",
		Schema: &huma.Schema{Type: "string", Pattern: `^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$`},
	}
	doc.AddOperation(&huma.Operation{
		OperationID: "planKind",
		Method:      http.MethodPost,
		Path:        "/kinds/{kind}/plan",
		Summary:     "Plan a resource of a kind the add-on adds",
		Description: "The panel calls it as it plans a change of the resource, or one of its actions, for the preview and again to apply it, " +
			"and when it puts back what drifted. The plan is the steps the agent runs, only of files in dir, and what applying does beyond them. " +
			"Planning changes nothing, and the same call always gets the same plan.",
		Tags:        []string{"Add-on"},
		Parameters:  []*huma.Param{kindParam},
		RequestBody: &huma.RequestBody{Required: true, Content: jsonBody(reflect.TypeFor[KindCall](), "KindCall")},
		Responses: map[string]*huma.Response{
			"200":     {Description: "The plan", Content: jsonBody(reflect.TypeFor[KindPlan](), "KindPlan")},
			"default": problem,
		},
	})
	doc.AddOperation(&huma.Operation{
		OperationID: "applyKind",
		Method:      http.MethodPost,
		Path:        "/kinds/{kind}/apply",
		Summary:     "Apply a resource of a kind the add-on adds",
		Description: "The panel calls it once the plan's steps have run, with what they returned. The add-on does what the plan said it " +
			"would besides, and answers with the resource's status, or with a problem, which fails the change. For a resource being deleted, " +
			"it undoes what it did for it.",
		Tags:        []string{"Add-on"},
		Parameters:  []*huma.Param{kindParam},
		RequestBody: &huma.RequestBody{Required: true, Content: jsonBody(reflect.TypeFor[KindCall](), "KindCall")},
		Responses: map[string]*huma.Response{
			"200":     {Description: "The resource's status, and an action's output", Content: jsonBody(reflect.TypeFor[KindApplied](), "KindApplied")},
			"default": problem,
		},
	})
	forwarded := []*huma.Param{
		{Name: "path", In: "path", Required: true, Description: "The rest of the path", Schema: &huma.Schema{Type: "string"}},
		{Name: "Racksail-Actor", In: "header", Description: "Who is asking, as JSON: type, id, name, user and source. The panel signed them in and checked they may use the add-on", Schema: &huma.Schema{Type: "string"}},
		{Name: "Racksail-Request-Id", In: "header", Description: "The ID of the panel request this one comes from, for the add-on's logs", Schema: &huma.Schema{Type: "string"}},
	}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		doc.AddOperation(&huma.Operation{
			OperationID: "route" + strings.ToUpper(m[:1]) + strings.ToLower(m[1:]),
			Method:      m,
			Path:        "/routes/{path}",
			Summary:     "Serve one of the add-on's API routes",
			Description: "The panel passes requests for /api/v1/addons/<id>/<path> on to the add-on, for the routes its manifest declares. " +
				"Cookies and credentials stay with the panel; the add-on learns who asks from Racksail-Actor.",
			Tags:       []string{"Add-on"},
			Parameters: forwarded,
			Responses:  map[string]*huma.Response{"default": {Description: "Whatever the route answers"}},
		})
	}
	doc.AddOperation(&huma.Operation{
		OperationID: "screen",
		Method:      http.MethodGet,
		Path:        "/ui/{path}",
		Summary:     "Serve one of the add-on's screens",
		Description: "Pages of the add-on's UI extensions that need more than the panel's declarative entries. The panel shows them in a " +
			"sandboxed frame of an origin of their own, so they can never read the panel's session.",
		Tags:       []string{"Add-on"},
		Parameters: forwarded,
		Responses:  map[string]*huma.Response{"200": {Description: "The page", Content: map[string]*huma.MediaType{"text/html": {Schema: &huma.Schema{Type: "string"}}}}},
	})
	return json.MarshalIndent(doc, "", "  ")
}
