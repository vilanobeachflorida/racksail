package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/api"
)

// Document is an OpenAPI document as the commands need it: its operations,
// with what Racksail's extensions say about them, and the schemas they
// refer to.
type Document struct {
	Operations []*Operation
	schemas    map[string]*Schema
}

// Operation is one operation of the API.
type Operation struct {
	ID          string
	Method      string
	Path        string
	Summary     string
	Description string
	Tags        []string
	Params      []*Param
	// Body is the schema of the JSON request body; nil when it takes none.
	Body *Schema
	// Responses are the documented answers by status code, such as "200".
	Responses map[string]*Response

	// What the operations catalog says about the operation.
	Kind        string
	Operation   string
	Permission  string
	Risk        string
	DryRun      bool
	Long        bool
	Destructive bool
	Stream      bool
	StepUp      bool
	Session     bool
	Public      bool
}

// Param is a path, query or header parameter.
type Param struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description"`
	Required    bool    `json:"required"`
	Schema      *Schema `json:"schema"`
}

// Response is one documented answer: its schema by media type.
type Response struct {
	Description string
	Content     map[string]*Schema
}

// Mutating reports whether the operation changes something.
func (op *Operation) Mutating() bool { return op.Method != http.MethodGet }

// PathParams are the operation's path parameters, in the order the path
// names them.
func (op *Operation) PathParams() []*Param {
	var out []*Param
	rest := op.Path
	for {
		i := strings.Index(rest, "{")
		if i < 0 {
			return out
		}
		j := strings.Index(rest[i:], "}")
		if j < 0 {
			return out
		}
		name := rest[i+1 : i+j]
		rest = rest[i+j+1:]
		for _, p := range op.Params {
			if p.In == "path" && p.Name == name {
				out = append(out, p)
			}
		}
	}
}

// Param returns the parameter with the given name and location.
func (op *Operation) Param(in, name string) *Param {
	for _, p := range op.Params {
		if p.In == in && p.Name == name {
			return p
		}
	}
	return nil
}

// Success returns the schema and media type of the operation's main
// successful answer: 200 before 201 and 202, JSON before anything else.
func (op *Operation) Success() (*Schema, string) {
	for _, code := range []string{"200", "201", "202"} {
		r := op.Responses[code]
		if r == nil {
			continue
		}
		if s, ok := r.Content["application/json"]; ok {
			return s, "application/json"
		}
		types := make([]string, 0, len(r.Content))
		for t := range r.Content {
			types = append(types, t)
		}
		sort.Strings(types)
		if len(types) > 0 {
			return r.Content[types[0]], types[0]
		}
	}
	return nil, ""
}

// Schema is the part of JSON Schema the commands use to make flags and
// print answers.
type Schema struct {
	Ref         string             `json:"$ref,omitempty"`
	Types       Types              `json:"type,omitempty"`
	Format      string             `json:"format,omitempty"`
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	Enum        []any              `json:"enum,omitempty"`
	Examples    []any              `json:"examples,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	AllOf       []*Schema          `json:"allOf,omitempty"`
	OneOf       []*Schema          `json:"oneOf,omitempty"`
	AnyOf       []*Schema          `json:"anyOf,omitempty"`
	ReadOnly    bool               `json:"readOnly,omitempty"`
	// AdditionalProperties is the schema of a map's values; nil when the
	// object has none.
	AdditionalProperties *Schema `json:"-"`

	// Name is the component a resolved schema came from.
	Name string `json:"-"`
}

// UnmarshalJSON reads additionalProperties, which is a schema or a
// boolean.
func (s *Schema) UnmarshalJSON(raw []byte) error {
	type plain Schema
	var aux struct {
		plain
		Additional json.RawMessage `json:"additionalProperties"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return err
	}
	*s = Schema(aux.plain)
	if len(aux.Additional) > 0 && aux.Additional[0] == '{' {
		s.AdditionalProperties = &Schema{}
		if err := json.Unmarshal(aux.Additional, s.AdditionalProperties); err != nil {
			return err
		}
	}
	return nil
}

// IsRequired reports whether the object requires the property.
func (s *Schema) IsRequired(name string) bool { return slices.Contains(s.Required, name) }

// Types are a schema's types: one, or several such as ["string", "null"].
type Types []string

// UnmarshalJSON reads a type or a list of them.
func (t *Types) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		var one string
		if err := json.Unmarshal(raw, &one); err != nil {
			return err
		}
		*t = Types{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return err
	}
	*t = many
	return nil
}

// Main is the type that isn't null.
func (t Types) Main() string {
	for _, typ := range t {
		if typ != "null" {
			return typ
		}
	}
	return ""
}

// Is reports whether the schema allows the type.
func (t Types) Is(typ string) bool { return slices.Contains(t, typ) }

const refPrefix = "#/components/schemas/"

// Resolve follows a schema's reference, and a lone allOf, to the schema it
// stands for. It returns nil for nil.
func (d *Document) Resolve(s *Schema) *Schema {
	for range 16 {
		switch {
		case s == nil:
			return nil
		case s.Ref != "":
			name := strings.TrimPrefix(s.Ref, refPrefix)
			target, ok := d.schemas[name]
			if !ok {
				return &Schema{Description: s.Description}
			}
			s = target
		case len(s.AllOf) == 1 && len(s.Properties) == 0 && s.Types.Main() == "":
			s = s.AllOf[0]
		default:
			return s
		}
	}
	return s
}

// Schema returns the component schema with the given name.
func (d *Document) Schema(name string) (*Schema, bool) {
	s, ok := d.schemas[name]
	return s, ok
}

// Find returns the operation with the given ID.
func (d *Document) Find(id string) *Operation {
	for _, op := range d.Operations {
		if op.ID == id {
			return op
		}
	}
	return nil
}

// At returns the operation of a method on a path.
func (d *Document) At(method, path string) *Operation {
	for _, op := range d.Operations {
		if op.Method == method && op.Path == path {
			return op
		}
	}
	return nil
}

type rawMedia struct {
	Schema *Schema `json:"schema"`
}

type rawOperation struct {
	OperationID string   `json:"operationId"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Parameters  []*Param `json:"parameters"`
	RequestBody *struct {
		Content map[string]rawMedia `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Description string              `json:"description"`
		Content     map[string]rawMedia `json:"content"`
	} `json:"responses"`
	Security []map[string][]string `json:"security"`
}

var methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// Parse reads an OpenAPI 3.1 document.
func Parse(raw []byte) (*Document, error) {
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]*Schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("reading the API document: %w", err)
	}
	d := &Document{schemas: doc.Components.Schemas}
	if d.schemas == nil {
		d.schemas = map[string]*Schema{}
	}
	for name, s := range d.schemas {
		s.Name = name
	}
	for path, item := range doc.Paths {
		for _, method := range methods {
			body, ok := item[strings.ToLower(method)]
			if !ok {
				continue
			}
			op, err := parseOperation(method, path, body)
			if err != nil {
				return nil, fmt.Errorf("reading %s %s: %w", method, path, err)
			}
			d.Operations = append(d.Operations, op)
		}
	}
	sort.Slice(d.Operations, func(i, j int) bool {
		a, b := d.Operations[i], d.Operations[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return slices.Index(methods, a.Method) < slices.Index(methods, b.Method)
	})
	return d, nil
}

func parseOperation(method, path string, raw json.RawMessage) (*Operation, error) {
	var r rawOperation
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	var ext map[string]any
	if err := json.Unmarshal(raw, &ext); err != nil {
		return nil, err
	}
	text := func(key string) string { s, _ := ext[key].(string); return s }
	flag := func(key string) bool { b, _ := ext[key].(bool); return b }
	op := &Operation{
		ID: r.OperationID, Method: method, Path: path, Summary: r.Summary, Description: r.Description, Tags: r.Tags,
		Params: r.Parameters, Responses: map[string]*Response{},
		Kind: text(api.ExtKind), Operation: text(api.ExtOperation), Permission: text(api.ExtPermission), Risk: text(api.ExtRisk),
		DryRun: flag(api.ExtDryRun), Long: flag(api.ExtLong), Destructive: flag(api.ExtDestructive), Stream: flag(api.ExtStream),
		StepUp: flag(api.ExtStepUp), Session: flag(api.ExtSession), Public: len(r.Security) == 0,
	}
	if r.RequestBody != nil {
		if m, ok := r.RequestBody.Content["application/json"]; ok {
			op.Body = m.Schema
			if op.Body == nil {
				op.Body = &Schema{}
			}
		}
	}
	for code, resp := range r.Responses {
		out := &Response{Description: resp.Description, Content: map[string]*Schema{}}
		for media, m := range resp.Content {
			out.Content[media] = m.Schema
		}
		op.Responses[code] = out
	}
	return op, nil
}
