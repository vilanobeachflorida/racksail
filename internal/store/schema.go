package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// The JSON Schemas of specs and action inputs are generated from their Go
// types with Huma, the way the API documents them, so what the API calls a
// valid spec is exactly what the change pipeline accepts. The registry here
// is only used for validation; the API keeps its own, with the names its
// document shows.
var schemas = struct {
	sync.RWMutex
	reg huma.Registry
}{reg: huma.NewMapRegistry("#/components/schemas/", qualifiedSchemaName)}

// qualifiedSchemaName names a schema after the full path of its Go type, so
// two packages using the same type name never share a schema.
func qualifiedSchemaName(t reflect.Type, hint string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := hint
	if t.Name() != "" {
		name = t.PkgPath() + "." + t.Name()
	}
	return strings.NewReplacer("/", "_", "[", "(", "]", ")", "*", "", " ", "").Replace(name)
}

// generateSchema returns the schema of t, turning the panic Huma raises for
// a malformed struct tag into an error, so a broken kind is refused rather
// than crashing the binary.
func generateSchema(t reflect.Type, hint string) (s *huma.Schema, err error) {
	schemas.Lock()
	defer schemas.Unlock()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("generating the JSON Schema of %s: %v", t, p)
		}
	}()
	return schemas.reg.Schema(t, true, hint), nil
}

var requiredProperty = regexp.MustCompile(`^expected required property (\S+) to be present$`)

// validateDocument checks a JSON document against a schema. Problems come
// back as field errors whose paths start with prefix, such as
// spec.domains[0]; a missing required property is reported on the property
// itself, so forms can highlight it.
func validateDocument(s *huma.Schema, doc json.RawMessage, prefix string) []i18n.FieldError {
	if s == nil {
		return nil
	}
	var v any
	if len(bytes.TrimSpace(doc)) > 0 {
		if err := json.Unmarshal(doc, &v); err != nil {
			return []i18n.FieldError{i18n.Field(prefix, i18n.ErrBadValue, "reason", "it isn't valid JSON")}
		}
	}
	pb := huma.NewPathBuffer([]byte(prefix), len(prefix))
	res := &huma.ValidateResult{}
	schemas.RLock()
	huma.Validate(schemas.reg, s, pb, huma.ModeWriteToServer, v, res)
	schemas.RUnlock()
	var out []i18n.FieldError
	for _, e := range res.Errors {
		var detail *huma.ErrorDetail
		if d, ok := e.(*huma.ErrorDetail); ok {
			detail = d
		} else {
			out = append(out, i18n.Field(prefix, i18n.ErrBadValue, "reason", e.Error()))
			continue
		}
		if m := requiredProperty.FindStringSubmatch(detail.Message); m != nil {
			out = append(out, i18n.Field(joinPath(detail.Location, m[1]), i18n.ErrRequired))
			continue
		}
		out = append(out, i18n.Field(detail.Location, i18n.ErrBadValue, "reason", detail.Message))
	}
	return out
}

func joinPath(base, field string) string {
	if base == "" {
		return field
	}
	return base + "." + field
}

// ValidateActionInput checks an action's input against the JSON Schema of
// its input type, then decodes it, rejecting unknown fields, and runs the
// input's own Validate method when it has one. An action without an input
// type only takes an empty input.
func ValidateActionInput(a Action, input json.RawMessage) error {
	trimmed := bytes.TrimSpace(input)
	if a.Input == nil {
		if len(trimmed) == 0 || string(trimmed) == "{}" || string(trimmed) == "null" {
			return nil
		}
		return i18n.Invalid(i18n.Field("input", i18n.ErrBadValue, "reason", "this action takes no input"))
	}
	if len(trimmed) == 0 || string(trimmed) == "null" {
		trimmed = []byte("{}")
	}
	t := reflect.TypeOf(a.Input)
	s, err := generateSchema(t, a.Name+"Input")
	if err != nil {
		return err
	}
	if fields := validateDocument(s, trimmed, "input"); len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	v := reflect.New(t).Interface()
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return i18n.Invalid(i18n.Field("input", i18n.ErrBadValue, "reason", err.Error()))
	}
	validator, ok := v.(interface{ Validate() error })
	if !ok {
		return nil
	}
	if err := validator.Validate(); err != nil {
		if i18n.CodeOf(err) != "" {
			return err
		}
		return i18n.Invalid(i18n.Field("input", i18n.ErrBadValue, "reason", err.Error()))
	}
	return nil
}
