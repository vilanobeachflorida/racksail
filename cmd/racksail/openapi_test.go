package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// Every operation the panel serves says what it does, and every parameter,
// every field of what it takes, a kind's spec fields and an action's input
// among them, and every field of what it answers with says what it is. The
// API reference, the command line's help, the panel's forms and the
// assistant's tools are made of these words, so one that is missing leaves
// a blank there.
func TestEveryOperationIsDescribed(t *testing.T) {
	out, err := run(t, "openapi")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	w := describedSchemas{schemas: doc.Components.Schemas, missing: map[string]bool{}}
	operations := 0
	for path, item := range doc.Paths {
		for method, raw := range item {
			if !slices.Contains([]string{"get", "put", "post", "delete", "patch", "head"}, method) {
				continue
			}
			var op struct {
				ID          string `json:"operationId"`
				Summary     string `json:"summary"`
				Description string `json:"description"`
				Parameters  []struct {
					Name        string `json:"name"`
					In          string `json:"in"`
					Description string `json:"description"`
				} `json:"parameters"`
				RequestBody *struct {
					Content map[string]struct {
						Schema map[string]any `json:"schema"`
					} `json:"content"`
				} `json:"requestBody"`
				Responses map[string]struct {
					Description string `json:"description"`
					Content     map[string]struct {
						Schema map[string]any `json:"schema"`
					} `json:"content"`
				} `json:"responses"`
			}
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatal(err)
			}
			operations++
			name := fmt.Sprintf("%s (%s %s)", op.ID, strings.ToUpper(method), path)
			if strings.TrimSpace(op.Summary) == "" || strings.TrimSpace(op.Description) == "" {
				w.missing[name+" has no summary or no description"] = true
			}
			for _, p := range op.Parameters {
				if strings.TrimSpace(p.Description) == "" {
					w.missing[fmt.Sprintf("%s: the %s parameter %s has no description", name, p.In, p.Name)] = true
				}
			}
			for status, r := range op.Responses {
				if strings.TrimSpace(r.Description) == "" {
					w.missing[fmt.Sprintf("%s: the %s answer has no description", name, status)] = true
				}
				for _, mt := range r.Content {
					w.fields(mt.Schema, fmt.Sprintf("%s: the %s answer's", name, status), "", nil)
				}
			}
			if op.RequestBody != nil {
				for _, mt := range op.RequestBody.Content {
					w.fields(mt.Schema, name+": the body's", "", nil)
				}
			}
		}
	}
	if operations < 100 {
		t.Fatalf("only %d operations in the document", operations)
	}
	if len(w.missing) > 0 {
		list := slices.Sorted(maps.Keys(w.missing))
		t.Errorf("%d things the API document doesn't describe; give each a description, or a doc tag on the field:\n%s", len(list), strings.Join(list, "\n"))
	}
}

// Every operation that takes dryRun answers with what the change would do,
// with 200, with 202 once it is applied, while its job carries out the
// rest, and with problem details when it's refused. The document says all
// three, the first two with the same body, so clients generated from it,
// the command line and the assistant expect the answer they get.
func TestChangesDocumentBothAnswers(t *testing.T) {
	out, err := run(t, "openapi")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		Content map[string]struct {
			Schema map[string]any `json:"schema"`
		} `json:"content"`
	}
	changes := 0
	var wrong []string
	for path, item := range doc.Paths {
		for method, raw := range item {
			var op struct {
				ID        string            `json:"operationId"`
				DryRun    bool              `json:"x-racksail-dry-run"`
				Responses map[string]answer `json:"responses"`
			}
			if json.Unmarshal(raw, &op) != nil || !op.DryRun {
				continue
			}
			changes++
			name := fmt.Sprintf("%s (%s %s)", op.ID, strings.ToUpper(method), path)
			preview, applied := op.Responses["200"].Content["application/json"], op.Responses["202"].Content["application/json"]
			switch {
			case preview.Schema == nil || applied.Schema == nil:
				wrong = append(wrong, name+" doesn't document both 200 and 202")
			case fmt.Sprint(preview.Schema) != fmt.Sprint(applied.Schema):
				wrong = append(wrong, name+" answers 200 and 202 with different bodies")
			case op.Responses["default"].Content["application/problem+json"].Schema == nil:
				wrong = append(wrong, name+" doesn't document the problem details of a refusal")
			}
		}
	}
	if changes < 20 {
		t.Fatalf("only %d operations take dryRun", changes)
	}
	if len(wrong) > 0 {
		slices.Sort(wrong)
		t.Errorf("give these operations Responses, such as api.ChangeSetResponses:\n%s", strings.Join(wrong, "\n"))
	}
}

// describedSchemas walks the schemas of requests and answers for fields
// without a description. A field of a schema in the document's components is
// named by its schema, so each is listed once however many operations use it.
type describedSchemas struct {
	schemas map[string]any
	missing map[string]bool
}

const schemaRef = "#/components/schemas/"

// resolve follows a reference to the schema it names.
func (w describedSchemas) resolve(s map[string]any) map[string]any {
	for s != nil {
		ref, ok := s["$ref"].(string)
		if !ok {
			break
		}
		s, _ = w.schemas[strings.TrimPrefix(ref, schemaRef)].(map[string]any)
	}
	return s
}

// described reports whether a field's schema, or one it is made of, says
// what the field is.
func (w describedSchemas) described(s map[string]any) bool {
	if desc, _ := s["description"].(string); strings.TrimSpace(desc) != "" {
		return true
	}
	if r := w.resolve(s); r != nil {
		if desc, _ := r["description"].(string); strings.TrimSpace(desc) != "" {
			return true
		}
	}
	return false
}

// fields reports the fields under s, at path, that aren't described.
// within holds the schemas the walk is inside of, so recursive ones end.
func (w describedSchemas) fields(s map[string]any, where, path string, within []string) {
	if s == nil {
		return
	}
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, schemaRef)
		if slices.Contains(within, name) {
			return
		}
		next, _ := w.schemas[name].(map[string]any)
		w.fields(next, "the schema "+name+"'s", "", append(slices.Clone(within), name))
		return
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		subs, _ := s[key].([]any)
		for _, sub := range subs {
			m, _ := sub.(map[string]any)
			w.fields(m, where, path, within)
		}
	}
	props, _ := s["properties"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(props)) {
		p, _ := props[name].(map[string]any)
		field := strings.TrimPrefix(path+"."+name, ".")
		if name != "$schema" && !w.described(p) {
			w.missing[fmt.Sprintf("%s field %s has no description", where, field)] = true
		}
		w.fields(p, where, field, within)
	}
	if items, ok := s["items"].(map[string]any); ok {
		w.fields(items, where, path+"[]", within)
	}
	if extra, ok := s["additionalProperties"].(map[string]any); ok {
		w.fields(extra, where, path+".*", within)
	}
}
