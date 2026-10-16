package i18n

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ruleSchema has a constraint of every kind the schemas in spec/schemas
// use, a property required only for some values of another among them.
const ruleSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["name", "type"],
  "properties": {
    "name": {"type": "string", "minLength": 1, "maxLength": 8},
    "type": {"enum": ["validate", "default"]},
    "kinds": {"type": "array", "uniqueItems": true, "maxItems": 2, "items": {"type": "string", "pattern": "^[a-z]+\\.[a-z]+$"}},
    "message": {"type": "string"},
    "priority": {"type": "integer", "minimum": 0, "maximum": 100},
    "url": {"type": "string", "format": "uri"},
    "when": {"type": "string", "format": "date-time"},
    "defaults": {"type": "array", "items": {"type": "object", "additionalProperties": false, "required": ["field"],
      "properties": {"field": {"type": "string"}, "override": {"type": "boolean"}}}}
  },
  "allOf": [{"if": {"properties": {"type": {"const": "validate"}}}, "then": {"required": ["message"]}}]
}`

func validateRule(t *testing.T, doc string) error {
	t.Helper()
	schema, err := jsonschema.UnmarshalJSON(strings.NewReader(ruleSchema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("racksail:rule.schema.json", schema); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("racksail:rule.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(inst); err != nil {
		// spec.Validate says which document didn't match.
		return fmt.Errorf("the document doesn't match the rule schema: %w", err)
	}
	return nil
}

// What the validator of spec/schemas finds comes out as the catalog's field
// errors at the field's path, worded as SchemaField words the API
// validator's findings.
func TestJSONSchemaFieldsUseTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want []FieldError
	}{
		{`{"name": "Quiet", "type": "validate"}`, []FieldError{Field("spec.message", ErrRequired)}},
		{`{"type": "default"}`, []FieldError{Field("spec.name", ErrRequired)}},
		{`{"name": "x", "type": "default", "colour": "red"}`, []FieldError{Field("spec.colour", ErrUnknownName, "what", "field", "name", "colour")}},
		{`{"name": "much too long", "type": "default"}`, []FieldError{Field("spec.name", ErrTooLong, "max", 8)}},
		{`{"name": "", "type": "default"}`, []FieldError{Field("spec.name", ErrRequired)}},
		{`{"name": "x", "type": "maybe"}`, []FieldError{Field("spec.type", ErrBadValue, "reason", "use validate or default")}},
		{`{"name": 7, "type": "default"}`, []FieldError{Field("spec.name", ErrBadValue, "reason", "use text")}},
		{`{"name": "x", "type": "default", "kinds": ["web.site", "Web.Site"]}`, []FieldError{Field("spec.kinds[1]", ErrBadValue, "reason", "it isn't in the form this field takes")}},
		{`{"name": "x", "type": "default", "kinds": ["web.site", "web.site"]}`, []FieldError{Field("spec.kinds", ErrBadValue, "reason", "give each item only once")}},
		{`{"name": "x", "type": "default", "kinds": ["a.b", "c.d", "e.f"]}`, []FieldError{Field("spec.kinds", ErrBadValue, "reason", "give at most 2 items")}},
		{`{"name": "x", "type": "default", "priority": 101}`, []FieldError{Field("spec.priority", ErrBadValue, "reason", "use 100 or less")}},
		{`{"name": "x", "type": "default", "priority": 1.5}`, []FieldError{Field("spec.priority", ErrBadValue, "reason", "use a whole number")}},
		{`{"name": "x", "type": "default", "url": "hooks.example.com"}`, []FieldError{Field("spec.url", ErrBadValue, "reason", "use a full URL with its scheme, such as https://example.com/")}},
		{`{"name": "x", "type": "default", "when": "tomorrow"}`, []FieldError{Field("spec.when", ErrBadValue, "reason", "use a date and time such as 2026-05-01T09:30:00Z")}},
		{`{"name": "x", "type": "default", "defaults": [{"field": "a"}, {"override": "yes"}]}`, []FieldError{
			Field("spec.defaults[1].field", ErrRequired), Field("spec.defaults[1].override", ErrBadValue, "reason", "use true or false"),
		}},
	} {
		err := validateRule(t, tc.doc)
		if err == nil {
			t.Fatalf("%s is valid", tc.doc)
		}
		got := JSONSchemaFields("spec", err)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.doc, got, tc.want)
		}
		for _, f := range got {
			for _, words := range []string{"expected", "missing", "property", "jsonschema", "doesn't validate"} {
				if strings.Contains(f.Message, words) {
					t.Errorf("%s: the validator's words reach people: %s", tc.doc, f.Message)
				}
			}
		}
	}
	if got := JSONSchemaFields("spec", errors.New("no schema at all")); len(got) != 1 || got[0].Path != "spec" || got[0].Code != ErrBadValue.Code {
		t.Errorf("an error that isn't the validator's = %+v", got)
	}
	if got := JSONSchemaFields("", validateRule(t, `{"name": "Quiet", "type": "validate"}`)); len(got) != 1 || got[0].Path != "message" {
		t.Errorf("without a prefix = %+v", got)
	}
}
