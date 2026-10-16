package i18n

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2/validation"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// JSONSchemaFields turns what the validator of the schemas in spec/schemas
// found wrong with a document into field errors of the catalog, worded as
// SchemaField words what the API's validator finds: a missing property is
// core.required on that property, one the schema doesn't have
// core.unknown_name, and so on. Paths start with prefix, such as spec, and
// name the field in the document: spec.message, spec.defaults[0].field.
// Specs that come through the API and are checked against those schemas,
// as rules are, report through it, so the validator's wording never reaches
// people. An error that isn't the validator's is a finding on prefix.
func JSONSchemaFields(prefix string, err error) []FieldError {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []FieldError{SchemaField(prefix, "")}
	}
	var out []FieldError
	collectSchemaFields(prefix, ve, &out)
	if len(out) == 0 {
		out = append(out, SchemaField(locationPath(prefix, ve.InstanceLocation), ""))
	}
	return out
}

// collectSchemaFields adds the findings of e and of the errors it is made
// of, each once.
func collectSchemaFields(prefix string, e *jsonschema.ValidationError, out *[]FieldError) {
	add := func(f FieldError) {
		if !slices.ContainsFunc(*out, func(o FieldError) bool { return o.Path == f.Path && o.Message == f.Message }) {
			*out = append(*out, f)
		}
	}
	path := locationPath(prefix, e.InstanceLocation)
	switch k := e.ErrorKind.(type) {
	case *kind.Schema, *kind.Group, *kind.AllOf, *kind.Reference, *kind.RefCycle:
		// These only gather what their parts found.
		for _, c := range e.Causes {
			collectSchemaFields(prefix, c, out)
		}
		if len(e.Causes) == 0 {
			add(SchemaField(path, ""))
		}
	case *kind.Required:
		for _, name := range k.Missing {
			add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedRequiredProperty, name)))
		}
	case *kind.DependentRequired:
		for _, name := range k.Missing {
			add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedDependentRequiredProperty, name, k.Prop)))
		}
	case *kind.Dependency:
		for _, name := range k.Missing {
			add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedDependentRequiredProperty, name, k.Prop)))
		}
		for _, c := range e.Causes {
			collectSchemaFields(prefix, c, out)
		}
	case *kind.AdditionalProperties:
		for _, name := range k.Properties {
			add(SchemaField(fieldPath(path, name), validation.MsgUnexpectedProperty))
		}
	case *kind.Type:
		add(SchemaField(path, expectedType(k.Want)))
	case *kind.Enum:
		var values []string
		for _, v := range k.Want {
			values = append(values, schemaValue(v))
		}
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedOneOf, strings.Join(values, ", "))))
	case *kind.Const:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedConst, schemaValue(k.Want))))
	case *kind.Format:
		add(formatField(path, k.Want))
	case *kind.MinLength:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMinLength, k.Want)))
	case *kind.MaxLength:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMaxLength, k.Want)))
	case *kind.Pattern:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMatchPattern, k.Want)))
	case *kind.Minimum:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMinimumNumber, ratNumber(k.Want))))
	case *kind.ExclusiveMinimum:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedExclusiveMinimumNumber, ratNumber(k.Want))))
	case *kind.Maximum:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMaximumNumber, ratNumber(k.Want))))
	case *kind.ExclusiveMaximum:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedExclusiveMaximumNumber, ratNumber(k.Want))))
	case *kind.MultipleOf:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedNumberBeMultipleOf, ratNumber(k.Want))))
	case *kind.MinItems:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMinItems, k.Want)))
	case *kind.MaxItems:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMaxItems, k.Want)))
	case *kind.UniqueItems:
		add(SchemaField(path, validation.MsgExpectedArrayItemsUnique))
	case *kind.MinProperties:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMinProperties, k.Want)))
	case *kind.MaxProperties:
		add(SchemaField(path, fmt.Sprintf(validation.MsgExpectedMaxProperties, k.Want)))
	case *kind.AnyOf:
		add(SchemaField(path, validation.MsgExpectedMatchAtLeastOneSchema))
	case *kind.OneOf:
		add(SchemaField(path, validation.MsgExpectedMatchExactlyOneSchema))
	case *kind.Not, *kind.FalseSchema:
		add(SchemaField(path, validation.MsgExpectedNotMatchSchema))
	case *kind.InvalidJsonValue:
		add(SchemaField(path, "invalid JSON: "+fmt.Sprint(k.Value)))
	default:
		add(SchemaField(path, ""))
	}
}

// locationPath writes where in a document a value is, its names and indexes
// from the top, as field errors do: names after dots, indexes in brackets.
func locationPath(prefix string, location []string) string {
	path := prefix
	for _, token := range location {
		if _, err := strconv.Atoi(token); err == nil {
			path += "[" + token + "]"
			continue
		}
		path = fieldPath(path, token)
	}
	return path
}

// expectedType is what the API's validator says of a value that isn't of
// one of the JSON types want lists.
func expectedType(want []string) string {
	want = slices.DeleteFunc(slices.Clone(want), func(t string) bool { return t == "null" })
	if len(want) != 1 {
		return ""
	}
	switch want[0] {
	case "boolean":
		return validation.MsgExpectedBoolean
	case "integer":
		return validation.MsgExpectedInteger
	case "number":
		return validation.MsgExpectedNumber
	case "string":
		return validation.MsgExpectedString
	case "array":
		return validation.MsgExpectedArray
	case "object":
		return validation.MsgExpectedObject
	}
	return ""
}

// formatField says what a string of a JSON Schema format looks like, in the
// words of the API's validator for the same format.
func formatField(path, format string) FieldError {
	switch format {
	case "date-time":
		return SchemaField(path, validation.MsgExpectedRFC3339DateTime)
	case "date":
		return SchemaField(path, validation.MsgExpectedRFC3339Date)
	case "time":
		return SchemaField(path, validation.MsgExpectedRFC3339Time)
	case "email", "idn-email":
		return SchemaField(path, fmt.Sprintf(validation.MsgExpectedRFC5322Email, format))
	case "hostname", "idn-hostname":
		return SchemaField(path, validation.MsgExpectedRFC5890Hostname)
	case "ipv4":
		return SchemaField(path, validation.MsgExpectedRFC2673IPv4)
	case "ipv6":
		return SchemaField(path, validation.MsgExpectedRFC2373IPv6)
	case "uri", "iri":
		return SchemaField(path, validation.MsgExpectedRFC3986AbsoluteURI)
	case "uri-reference", "iri-reference":
		return SchemaField(path, fmt.Sprintf(validation.MsgExpectedRFC3986URI, format))
	case "uri-template":
		return SchemaField(path, validation.MsgExpectedRFC6570URITemplate)
	case "uuid":
		return SchemaField(path, fmt.Sprintf(validation.MsgExpectedRFC4122UUID, format))
	case "json-pointer":
		return SchemaField(path, validation.MsgExpectedRFC6901JSONPointer)
	case "relative-json-pointer":
		return SchemaField(path, validation.MsgExpectedRFC6901RelativeJSONPointer)
	case "regex":
		return SchemaField(path, fmt.Sprintf(validation.MsgExpectedRegexp, format))
	case "duration":
		// JSON Schema's durations are ISO 8601's, not Go's.
		return Field(path, ErrBadValue, "reason", "use a duration such as PT1H30M")
	}
	return SchemaField(path, "")
}

// schemaValue writes a value a schema allows as people would type it.
func schemaValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// ratNumber is a bound of a schema as a number.
func ratNumber(r *big.Rat) float64 {
	if r == nil {
		return 0
	}
	f, _ := r.Float64()
	return f
}
