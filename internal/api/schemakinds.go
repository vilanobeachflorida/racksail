package api

import (
	"net/http"
	"net/url"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

var typeOfScope = reflect.TypeFor[store.Scope]()

// ErrKindElsewhere answers a request at the path of kinds described by JSON
// Schema for a kind whose operations are at a path of its own.
var ErrKindElsewhere = i18n.Define("api.kind_elsewhere", http.StatusNotFound, "Resources of {kind} aren't served here. Use {path} instead.")

// SchemaKindPath is where the operations of a kind described by JSON Schema
// are, such as one an add-on adds: /api/v1/kinds/<name>/resources.
func SchemaKindPath(name string) string {
	return Prefix + "/kinds/" + url.PathEscape(name) + "/resources"
}

// schemaKindsBase is SchemaKindPath with the kind left as a parameter.
const schemaKindsBase = Prefix + "/kinds/{name}/resources"

// schemaKind finds the kind a request names, when it is one described by
// JSON Schema. The operations of other kinds live at their own paths.
func (s *Server) schemaKind(hc huma.Context) (store.Kind, error) {
	name := hc.Param("name")
	k, ok := s.kinds.Get(name)
	if !ok {
		return nil, i18n.ErrNotFound.New("what", "The kind "+name)
	}
	if _, ok := k.(store.SchemaKind); !ok {
		return nil, ErrKindElsewhere.New("kind", name, "path", KindPath(k.Info()))
	}
	return k, nil
}

// registerSchemaKinds adds the operations of the kinds described by JSON
// Schema, such as those add-ons add: the same list, get, create, update,
// delete, history and actions every kind has, at paths that name the kind.
// Kinds come and go with their add-ons while the panel runs, so the
// operations look the kind up each time; each authorizes with the kind's
// own permissions, as the generated ones do, and changes go through the
// change pipeline.
func (s *Server) registerSchemaKinds() {
	tags := []string{"Add-on kinds"}
	nameParam := pathParam("name", "The kind, such as guard.rule; GET /api/v1/kinds lists them with their paths")
	idParam := pathParam("id", "The resource's ID")
	changeSet := s.schemaOf(typeOfChangeSet, "ChangeSet")
	resource := s.schemaOf(typeOfResource, "Resource")
	changeResponses := func(applied string) map[string]*huma.Response {
		return map[string]*huma.Response{
			"200": jsonResponse("The change set without applying it, for a dry run", changeSet),
			"202": jsonResponse(applied, changeSet),
		}
	}
	object := &huma.Schema{Type: huma.TypeObject, Description: "Settings as the kind's JSON Schema, from GET /api/v1/kinds/{name}, describes them", AdditionalProperties: true}
	labels := &huma.Schema{Type: huma.TypeObject, AdditionalProperties: &huma.Schema{Type: huma.TypeString}, Description: "Labels for grouping and filtering"}
	with := func(fn func(k store.Kind) handler) handler {
		return func(hc huma.Context) (int, any, error) {
			k, err := s.schemaKind(hc)
			if err != nil {
				return 0, nil, err
			}
			return fn(k)(hc)
		}
	}
	listParams := []*huma.Param{
		nameParam,
		queryParam("scope", huma.TypeString, "Only those of one owner: server, reseller:<id> or account:<id>"),
		queryParam("server", huma.TypeString, "Only those placed on this cluster server"),
		listParam("label", "Only those with this label, as key=value; repeat it to require several"),
		listParam("phase", "Only those in one of these phases: pending, applying, ready, degraded, failed or deleting"),
		queryParam("q", huma.TypeString, "Only those whose ID contains this text"),
		queryParam("sort", huma.TypeString, "The order: id, createdAt, updatedAt, generation or phase, with - in front for the reverse"),
		queryParam("includeDeleted", huma.TypeBoolean, "Also list those being removed"),
		fieldsParam(),
	}

	op := &huma.Operation{
		OperationID: "listSchemaKindResources", Method: http.MethodGet, Path: schemaKindsBase, Tags: tags, Security: Protected,
		Summary: "List resources of an add-on's kind",
		Description: "Lists the resources you can see of a kind an add-on adds, described by JSON Schema rather than generated with the API, " +
			"oldest first unless sort says otherwise, a page at a time. Its read permission, <kind>.read, decides what you see.",
		Parameters: append(listParams, pageParams()...),
		Responses: map[string]*huma.Response{"200": jsonResponse("A page of resources", &huma.Schema{
			Type: huma.TypeObject, Required: []string{"items"},
			Properties: map[string]*huma.Schema{
				"items": {Type: huma.TypeArray, Items: resource, Description: "The resources on this page"},
				"next":  {Type: huma.TypeString, Description: "The cursor of the next page; empty on the last one"},
			},
		})},
	}
	Describe(op, Info{Operation: "list", Risk: store.RiskRead})
	s.handle(op, with(s.listKind))

	op = &huma.Operation{
		OperationID: "getSchemaKindResource", Method: http.MethodGet, Path: schemaKindsBase + "/{id}", Tags: tags, Security: Protected,
		Summary:     "Get a resource of an add-on's kind",
		Description: "Returns one resource of a kind an add-on adds: its settings, and the status the add-on last reported with the phase and conditions the panel keeps.",
		Parameters:  []*huma.Param{nameParam, idParam, fieldsParam()},
		Responses:   map[string]*huma.Response{"200": jsonResponse("The resource", resource)},
	}
	Describe(op, Info{Operation: "get", Risk: store.RiskRead})
	s.handle(op, with(s.getKind))

	op = &huma.Operation{
		OperationID: "createSchemaKindResource", Method: http.MethodPost, Path: schemaKindsBase, Tags: tags, Security: Protected,
		Summary: "Create a resource of an add-on's kind",
		Description: "Creates a resource of a kind an add-on adds, as a change like any other: the add-on plans it, so with dryRun=true the answer " +
			"is the change set with what it would do; otherwise the change set is applied, and its job runs the plan and has the add-on apply it.",
		Parameters: append([]*huma.Param{nameParam}, changeParams()...),
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: &huma.Schema{
			Type: huma.TypeObject, Required: []string{"spec"}, Description: "A new resource",
			Properties: map[string]*huma.Schema{
				"id":      {Type: huma.TypeString, Description: "An ID chosen up front, starting with the kind's prefix; one is made when left out"},
				"scope":   {AllOf: []*huma.Schema{s.schemaOf(typeOfScope, "Scope")}, Description: "Who it belongs to, at the level the kind says; the server when left out for kinds of the server"},
				"server":  {Type: huma.TypeString, Description: "The cluster server to place it on; the panel's own server when left out"},
				"labels":  labels,
				"spec":    object,
				"secrets": {Type: huma.TypeObject, AdditionalProperties: &huma.Schema{Type: huma.TypeString}, Description: "Values for secret fields, by field path"},
			},
		}}}},
		Responses: changeResponses("The change set, applied to the settings; its job carries out the change"),
	}
	Describe(op, Info{Operation: "create", Risk: store.RiskMedium, DryRun: true, Reversible: true})
	s.handle(op, with(s.createKind))

	op = &huma.Operation{
		OperationID: "updateSchemaKindResource", Method: http.MethodPut, Path: schemaKindsBase + "/{id}", Tags: tags, Security: Protected,
		Summary: "Change a resource of an add-on's kind",
		Description: "Replaces the resource's settings, previewed and applied like any change. Send the revision you read to make sure nobody changed it " +
			"in between; leave labels out to keep them.",
		Parameters: append([]*huma.Param{nameParam, idParam}, changeParams()...),
		RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: &huma.Schema{
			Type: huma.TypeObject, Required: []string{"spec"}, Description: "New settings",
			Properties: map[string]*huma.Schema{
				"spec":     object,
				"labels":   labels,
				"secrets":  {Type: huma.TypeObject, AdditionalProperties: &huma.Schema{Type: huma.TypeString}, Description: "Values for secret fields, by field path"},
				"revision": {Type: huma.TypeInteger, Format: "int64", Description: "The revision the settings were read at; when someone changed them since, the update fails with a conflict"},
			},
		}}}},
		Responses: changeResponses("The change set, applied to the settings; its job carries out the change"),
	}
	Describe(op, Info{Operation: "update", Risk: store.RiskMedium, DryRun: true, Reversible: true})
	s.handle(op, with(s.updateKind))

	op = &huma.Operation{
		OperationID: "deleteSchemaKindResource", Method: http.MethodDelete, Path: schemaKindsBase + "/{id}", Tags: tags, Security: Protected,
		Summary:     "Delete a resource of an add-on's kind",
		Description: "Deletes the resource, previewed and applied like any change: the add-on plans removing what it set up for it, and undoes the rest.",
		Parameters: append([]*huma.Param{nameParam, idParam,
			queryParam("revision", huma.TypeInteger, "The revision you read; when someone changed the resource since, the delete fails with a conflict")}, changeParams()...),
		Responses: changeResponses("The change set; its job removes the resource"),
	}
	Describe(op, Info{Operation: "delete", Risk: store.RiskHigh, DryRun: true, Destructive: true, Reversible: true})
	s.handle(op, with(s.deleteKind))

	op = &huma.Operation{
		OperationID: "getSchemaKindResourceHistory", Method: http.MethodGet, Path: schemaKindsBase + "/{id}/history", Tags: tags, Security: Protected,
		Summary:     "Show the history of a resource of an add-on's kind",
		Description: "Returns every recorded version of the resource's settings, oldest first, with the change set that wrote it and who made the change.",
		Parameters:  []*huma.Param{nameParam, idParam},
		Responses:   map[string]*huma.Response{"200": jsonResponse("The versions", s.schemaOf(typeOfHistory, "History"))},
	}
	Describe(op, Info{Operation: "history", Risk: store.RiskRead})
	s.handle(op, with(s.kindHistory))

	op = &huma.Operation{
		OperationID: "runSchemaKindAction", Method: http.MethodPost, Path: schemaKindsBase + "/{id}/actions/{action}", Tags: tags, Security: Protected,
		Summary: "Run an action of a resource of an add-on's kind",
		Description: "Runs one of the actions the kind declares, which the add-on plans and carries out, as a change like any other. Its input, " +
			"when it takes one, is checked against the action's JSON Schema from GET /api/v1/kinds/{name}. The answer comes when it is done, with what " +
			"it returned in the change set's output; after 30s it is 202 and the job carries on.",
		Parameters: append([]*huma.Param{nameParam, idParam, pathParam("action", "The action, as the kind declares it")}, changeParams()...),
		RequestBody: &huma.RequestBody{Content: map[string]*huma.MediaType{"application/json": {Schema: &huma.Schema{
			Type: huma.TypeObject, AdditionalProperties: true, Description: "The action's input, as its JSON Schema describes it",
		}}}},
		Responses: changeResponses("The change set; its job carries out the action"),
	}
	Describe(op, Info{Operation: "action", Risk: store.RiskMedium, DryRun: true})
	s.handle(op, with(func(k store.Kind) handler {
		return func(hc huma.Context) (int, any, error) {
			a, ok := k.Info().Action(hc.Param("action"))
			if !ok {
				return 0, nil, i18n.ErrNotFound.New("what", "The action "+hc.Param("action")+" of "+k.Info().Title)
			}
			return s.kindAction(k, a)(hc)
		}
	}))
}
