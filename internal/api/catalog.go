package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Extensions every operation in the document carries, which the operations
// catalog is read from. Clients that generate commands and tools from the
// API, such as the CLI and the assistant, read them too.
const (
	ExtKind        = "x-racksail-kind"
	ExtOperation   = "x-racksail-operation"
	ExtPermission  = "x-racksail-permission"
	ExtRisk        = "x-racksail-risk"
	ExtDryRun      = "x-racksail-dry-run"
	ExtLong        = "x-racksail-long"
	ExtDestructive = "x-racksail-destructive"
	ExtReversible  = "x-racksail-reversible"
	ExtStream      = "x-racksail-stream"
	// ExtOutput is the schema of what an action returns, which arrives in
	// the change set's output rather than as the response body.
	ExtOutput = "x-racksail-output"
)

// Info describes what an operation does, for the catalog: the kind and the
// operation on it, the permission it needs, how risky it is and how it
// behaves.
type Info struct {
	Kind      string
	Operation string
	// Permission is what the authorizer checks; empty for operations that
	// only need a signed-in user, or none.
	Permission string
	Risk       store.Risk
	// DryRun says the operation takes dryRun and answers with a change set.
	DryRun      bool
	Long        bool
	Destructive bool
	Reversible  bool
	// Stream says the operation answers with Server-Sent Events.
	Stream bool
	// StepUp says a person in the panel must have confirmed it's them
	// recently, as for changing someone's roles.
	StepUp bool
	// Session says only a person in the panel may use the operation, not
	// an API token: signing in, and changing how one signs in. The
	// racksail command leaves these out.
	Session bool
}

// Describe records what an operation does in its extensions, before it is
// registered. Modules call it for the operations they add, so the catalog,
// the CLI and the assistant know their risk.
func Describe(op *huma.Operation, info Info) {
	if op.Extensions == nil {
		op.Extensions = map[string]any{}
	}
	set := func(key string, v any, keep bool) {
		if keep {
			op.Extensions[key] = v
		}
	}
	risk := info.Risk
	if !risk.Valid() {
		risk = store.RiskRead
		if op.Method != http.MethodGet && op.Method != http.MethodHead {
			risk = store.RiskMedium
		}
	}
	set(ExtRisk, string(risk), true)
	set(ExtKind, info.Kind, info.Kind != "")
	set(ExtOperation, info.Operation, info.Operation != "")
	set(ExtPermission, info.Permission, info.Permission != "")
	set(ExtDryRun, true, info.DryRun)
	set(ExtLong, true, info.Long)
	set(ExtDestructive, true, info.Destructive)
	set(ExtReversible, true, info.Reversible)
	set(ExtStream, true, info.Stream)
	set(ExtStepUp, true, info.StepUp)
	set(ExtSession, true, info.Session)
}

// Operation is one entry of the operations catalog.
type Operation struct {
	ID          string     `json:"id" doc:"The operation's ID in the API document"`
	Method      string     `json:"method"`
	Path        string     `json:"path"`
	Summary     string     `json:"summary"`
	Description string     `json:"description,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Kind        string     `json:"kind,omitempty" doc:"The kind the operation is about"`
	Operation   string     `json:"operation,omitempty" doc:"list, get, create, update, delete, history, or an action's name"`
	Permission  string     `json:"permission,omitempty" doc:"The permission the operation needs"`
	Risk        store.Risk `json:"risk" enum:"read,low,medium,high,critical" doc:"How much harm the operation can do"`
	Mutating    bool       `json:"mutating" doc:"Whether the operation changes anything"`
	DryRun      bool       `json:"dryRun,omitempty" doc:"Whether the operation takes dryRun and answers with a change set"`
	Long        bool       `json:"long,omitempty" doc:"Whether the work runs as a job the answer names"`
	Destructive bool       `json:"destructive,omitempty" doc:"Whether it removes data, and so needs a typed confirmation"`
	Reversible  bool       `json:"reversible,omitempty" doc:"Whether it can be undone"`
	Stream      bool       `json:"stream,omitempty" doc:"Whether it answers with Server-Sent Events"`
	StepUp      bool       `json:"stepUp,omitempty" doc:"Whether it needs you to have confirmed it's you recently, when you use it in the panel"`
	Session     bool       `json:"session,omitempty" doc:"Whether only a person in the panel may use it, not an API token"`
	Public      bool       `json:"public,omitempty" doc:"Whether it works without signing in"`
	// Request and Response name the schemas of the body and of the answer
	// in the document's components, and Output the schema of what an action
	// returns in its change set's output.
	Request  string `json:"request,omitempty"`
	Response string `json:"response,omitempty"`
	Output   string `json:"output,omitempty"`
}

// Catalog lists every operation of an API document, sorted by path and
// method, from the extensions Describe records. Operations without them are
// taken to be reads when they are GETs and of medium risk otherwise.
func Catalog(doc *huma.OpenAPI) []Operation {
	var out []Operation
	for path, item := range doc.Paths {
		for method, op := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put,
			http.MethodPatch: item.Patch, http.MethodDelete: item.Delete,
		} {
			if op == nil {
				continue
			}
			ext := op.Extensions
			text := func(key string) string { s, _ := ext[key].(string); return s }
			flag := func(key string) bool { b, _ := ext[key].(bool); return b }
			o := Operation{
				ID: op.OperationID, Method: method, Path: path, Summary: op.Summary, Description: op.Description, Tags: op.Tags,
				Kind: text(ExtKind), Operation: text(ExtOperation), Permission: text(ExtPermission), Risk: store.Risk(text(ExtRisk)),
				Mutating: method != http.MethodGet, DryRun: flag(ExtDryRun), Long: flag(ExtLong), Destructive: flag(ExtDestructive),
				Reversible: flag(ExtReversible), Stream: flag(ExtStream), StepUp: flag(ExtStepUp), Session: flag(ExtSession), Public: len(op.Security) == 0,
			}
			if !o.Risk.Valid() {
				o.Risk = store.RiskRead
				if o.Mutating {
					o.Risk = store.RiskMedium
				}
			}
			if op.RequestBody != nil {
				if mt := op.RequestBody.Content["application/json"]; mt != nil && mt.Schema != nil {
					o.Request = strings.TrimPrefix(mt.Schema.Ref, "#/components/schemas/")
				}
			}
			for _, code := range []string{"200", "202", "201"} {
				if r := op.Responses[code]; r != nil {
					if mt := r.Content["application/json"]; mt != nil && mt.Schema != nil && mt.Schema.Ref != "" {
						o.Response = strings.TrimPrefix(mt.Schema.Ref, "#/components/schemas/")
						break
					}
				}
			}
			if out, ok := ext[ExtOutput].(*huma.Schema); ok && out != nil {
				o.Output = strings.TrimPrefix(out.Ref, "#/components/schemas/")
			}
			out = append(out, o)
		}
	}
	order := map[string]int{http.MethodGet: 0, http.MethodPost: 1, http.MethodPut: 2, http.MethodPatch: 3, http.MethodDelete: 4}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return order[out[i].Method] < order[out[j].Method]
	})
	return out
}

// Catalog lists the server's operations.
func (s *Server) Catalog() []Operation { return Catalog(s.api.OpenAPI()) }

type catalogOutput struct {
	Body struct {
		Operations []Operation `json:"operations"`
	}
}

func (s *Server) registerCatalog() {
	op := huma.Operation{
		OperationID: "listOperations",
		Method:      http.MethodGet,
		Path:        Prefix + "/operations",
		Summary:     "List the API's operations",
		Description: "Lists every operation of the API with the kind it is about, the permission it needs, how risky it is and how it behaves. " +
			"The racksail command and the assistant build their commands and tools from it; the same details are in the API document as x-racksail extensions.",
		Tags:     []string{"System"},
		Security: Protected,
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*catalogOutput, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		out := &catalogOutput{}
		out.Body.Operations = s.Catalog()
		return out, nil
	})
}
