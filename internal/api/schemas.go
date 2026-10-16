package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// typeName turns a kind name into the name of its schemas: web.site gives
// WebSite and mail.mailbox_alias gives MailMailboxAlias.
func typeName(kind string) string {
	return camel(strings.FieldsFunc(kind, func(r rune) bool { return r == '.' || r == '_' || r == '-' })...)
}

// camel joins words, each starting with a capital letter.
func camel(words ...string) string {
	var b strings.Builder
	for _, w := range words {
		for _, part := range strings.FieldsFunc(w, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == ' ' }) {
			r := []rune(part)
			r[0] = unicode.ToUpper(r[0])
			b.WriteString(string(r))
		}
	}
	return b.String()
}

// lowerFirst makes the first letter of an operation ID lowercase.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// kindDoc holds the schemas of one kind in the API document.
type kindDoc struct {
	name                         string
	resource, list, create, edit *huma.Schema
	spec, status                 *huma.Schema
	inputs, outputs              map[string]*huma.Schema
}

// register adds a schema the document builds rather than takes from a Go
// type, under name, or name and a suffix when a Go type already took the
// name, and returns a reference to it.
func (s *Server) register(name string, schema *huma.Schema) *huma.Schema {
	reg := s.api.OpenAPI().Components.Schemas
	if _, taken := reg.Map()[name]; taken {
		name += "Resource"
	}
	reg.Map()[name] = schema
	return &huma.Schema{Ref: "#/components/schemas/" + name}
}

// kindSchemas builds the schemas of a kind: its spec and status from their
// Go types, the resource with them, a page of resources, and the bodies of
// create and update requests.
func (s *Server) kindSchemas(k store.Kind) kindDoc {
	info := k.Info()
	name := typeName(info.Name)
	reg := s.api.OpenAPI().Components.Schemas
	d := kindDoc{
		name:    name,
		spec:    reg.Schema(k.SpecType(), true, name+"Spec"),
		status:  reg.Schema(k.StatusType(), true, name+"Status"),
		inputs:  map[string]*huma.Schema{},
		outputs: map[string]*huma.Schema{},
	}
	base := reg.SchemaFromRef(reg.Schema(reflect.TypeFor[store.Resource](), true, "Resource").Ref)
	props := map[string]*huma.Schema{}
	for key, p := range base.Properties {
		props[key] = p
	}
	props["spec"], props["status"] = d.spec, d.status
	d.resource = s.register(name, &huma.Schema{
		Type: huma.TypeObject, Description: info.Title + ": " + info.Description,
		Properties: props, Required: base.Required, AdditionalProperties: false,
	})
	d.list = s.register(name+"List", &huma.Schema{
		Type:        huma.TypeObject,
		Description: "One page of " + plural(info) + ".",
		Properties: map[string]*huma.Schema{
			"items": {Type: huma.TypeArray, Items: d.resource},
			"next":  {Type: huma.TypeString, Description: "The cursor of the next page; empty on the last one"},
		},
		Required:             []string{"items"},
		AdditionalProperties: false,
	})
	labels := &huma.Schema{Type: huma.TypeObject, AdditionalProperties: &huma.Schema{Type: huma.TypeString}, Description: "Labels for grouping and filtering"}
	secrets := &huma.Schema{
		Type: huma.TypeObject, AdditionalProperties: &huma.Schema{Type: huma.TypeString},
		Description: "Values for the secret fields, by field path; they are stored apart from the settings and never shown again",
	}
	if len(store.SensitiveFields(info)) > 0 {
		secrets.Description += ". Secret fields: " + strings.Join(store.SensitiveFields(info), ", ")
	}
	createProps := map[string]*huma.Schema{
		"id":      {Type: huma.TypeString, Description: "An ID chosen up front, starting with " + info.Prefix + "_; one is made when left out"},
		"server":  {Type: huma.TypeString, Description: "The cluster server to place it on; the panel's own server when left out"},
		"labels":  labels,
		"spec":    d.spec,
		"secrets": secrets,
	}
	required := []string{"spec"}
	switch {
	case len(info.Levels) > 0:
		levels := append([]string{string(info.Scope)}, levelNames(info.Levels)...)
		description := "Where it belongs: the " + strings.Join(levels[:len(levels)-1], ", the ") + " or the " + levels[len(levels)-1]
		if info.Scope == store.ScopeServer {
			description += "; the server when left out"
		} else {
			required = append(required, "scope")
		}
		createProps["scope"] = &huma.Schema{AllOf: []*huma.Schema{s.schemaOf(reflect.TypeFor[store.Scope](), "Scope")}, Description: description}
	case info.Scope != store.ScopeServer:
		createProps["scope"] = s.schemaOf(reflect.TypeFor[store.Scope](), "Scope")
		createProps["scope"] = &huma.Schema{AllOf: []*huma.Schema{createProps["scope"]}, Description: "The " + string(info.Scope) + " it belongs to"}
		required = append(required, "scope")
	}
	d.create = s.register(name+"CreateRequest", &huma.Schema{
		Type: huma.TypeObject, Description: "A new " + strings.ToLower(info.Title) + ".",
		Properties: createProps, Required: required, AdditionalProperties: false,
	})
	d.edit = s.register(name+"UpdateRequest", &huma.Schema{
		Type: huma.TypeObject, Description: "New settings for a " + strings.ToLower(info.Title) + ".",
		Properties: map[string]*huma.Schema{
			"spec":     d.spec,
			"labels":   labels,
			"secrets":  secrets,
			"revision": {Type: huma.TypeInteger, Format: "int64", Description: "The revision the settings were read at; when someone changed them since, the update fails with a conflict"},
		},
		Required: []string{"spec"}, AdditionalProperties: false,
	})
	for _, a := range info.Actions {
		if a.Input != nil {
			d.inputs[a.Name] = reg.Schema(reflect.TypeOf(a.Input), true, name+camel(a.Name)+"Input")
		}
		if a.Output != nil {
			d.outputs[a.Name] = reg.Schema(reflect.TypeOf(a.Output), true, name+camel(a.Name)+"Output")
		}
	}
	return d
}

// plural names a kind's resources in sentences: "sites", "mail domains".
func plural(info *store.KindInfo) string { return strings.ReplaceAll(info.Plural, "-", " ") }

var refPattern = regexp.MustCompile(`"#/components/schemas/([^"]+)"`)

// standalone turns a schema of the document into a JSON Schema document of
// its own, with the schemas it refers to under $defs, for forms that build
// themselves from a kind's schema.
func (s *Server) standalone(schema *huma.Schema) (json.RawMessage, error) {
	return standaloneIn(s.api.OpenAPI().Components.Schemas, schema)
}

// standaloneIn is standalone for a schema of any registry.
func standaloneIn(reg huma.Registry, schema *huma.Schema) (json.RawMessage, error) {
	// Forms read the root's properties, so a root that only points at a
	// named schema becomes that schema.
	if schema.Ref != "" {
		if def := reg.SchemaFromRef(schema.Ref); def != nil {
			schema = def
		}
	}
	root, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	defs := map[string]json.RawMessage{}
	queue := []json.RawMessage{root}
	for len(queue) > 0 {
		doc := queue[0]
		queue = queue[1:]
		for _, m := range refPattern.FindAllSubmatch(doc, -1) {
			name := string(m[1])
			if _, done := defs[name]; done {
				continue
			}
			def := reg.Map()[name]
			if def == nil {
				continue
			}
			raw, err := json.Marshal(def)
			if err != nil {
				return nil, err
			}
			defs[name] = raw
			queue = append(queue, raw)
		}
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(root, &top); err != nil {
		return nil, err
	}
	if len(defs) > 0 {
		names := make([]string, 0, len(defs))
		for name := range defs {
			names = append(names, name)
		}
		sort.Strings(names)
		var b bytes.Buffer
		b.WriteByte('{')
		for i, name := range names {
			if i > 0 {
				b.WriteByte(',')
			}
			key, _ := json.Marshal(name)
			b.Write(key)
			b.WriteByte(':')
			b.Write(defs[name])
		}
		b.WriteByte('}')
		top["$defs"] = b.Bytes()
	}
	top["$schema"] = json.RawMessage(`"https://json-schema.org/draft/2020-12/schema"`)
	out, err := json.Marshal(top)
	if err != nil {
		return nil, err
	}
	return bytes.ReplaceAll(out, []byte(`"#/components/schemas/`), []byte(`"#/$defs/`)), nil
}

func levelNames(levels []store.ScopeLevel) []string {
	out := make([]string, len(levels))
	for i, l := range levels {
		out[i] = string(l)
	}
	return out
}
