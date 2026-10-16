package i18n

import (
	"fmt"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2/validation"
)

// What the schema validator says comes out as the catalog's field errors,
// in the panel's words: never the validator's own.
func TestSchemaFieldsUseTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		path, message string
		want          FieldError
	}{
		{"spec", fmt.Sprintf(validation.MsgExpectedRequiredProperty, "domains"), Field("spec.domains", ErrRequired)},
		{"", fmt.Sprintf(validation.MsgExpectedRequiredProperty, "name"), Field("name", ErrRequired)},
		{"spec.colour", validation.MsgUnexpectedProperty, Field("spec.colour", ErrUnknownName, "what", "field", "name", "colour")},
		{"spec.note", fmt.Sprintf(validation.MsgExpectedMaxLength, 40), Field("spec.note", ErrTooLong, "max", 40)},
		{"spec.name", fmt.Sprintf(validation.MsgExpectedMinLength, 1), Field("spec.name", ErrRequired)},
		{"spec.slug", fmt.Sprintf(validation.MsgExpectedMinLength, 3), Field("spec.slug", ErrBadValue, "reason", "use at least 3 characters")},
		{"query.format", fmt.Sprintf(validation.MsgExpectedOneOf, "csv, json"), Field("query.format", ErrBadValue, "reason", "use csv or json")},
		{"spec.mode", fmt.Sprintf(validation.MsgExpectedOneOf, "a, b, c"), Field("spec.mode", ErrBadValue, "reason", "use a, b or c")},
		{"spec.limit", fmt.Sprintf(validation.MsgExpectedMaximumNumber, 1e6), Field("spec.limit", ErrBadValue, "reason", "use 1000000 or less")},
		{"query.limit", fmt.Sprintf(validation.MsgExpectedMinimumNumber, 1.0), Field("query.limit", ErrBadValue, "reason", "use 1 or more")},
		{"spec.domains", fmt.Sprintf(validation.MsgExpectedMinItems, 1), Field("spec.domains", ErrBadValue, "reason", "give at least one item")},
		{"spec.domains", fmt.Sprintf(validation.MsgExpectedMaxItems, 10), Field("spec.domains", ErrBadValue, "reason", "give at most 10 items")},
		{"spec.url", validation.MsgExpectedRFC3986AbsoluteURI, Field("spec.url", ErrBadValue, "reason", "use a full URL with its scheme, such as https://example.com/")},
		{"spec.url", fmt.Sprintf(validation.MsgExpectedRFC3986URI, "parse error"), Field("spec.url", ErrBadValue, "reason", "use a URL such as https://example.com/")},
		{"spec.when", validation.MsgExpectedRFC3339DateTime, Field("spec.when", ErrBadValue, "reason", "use a date and time such as 2026-05-01T09:30:00Z")},
		{"spec.slug", fmt.Sprintf(validation.MsgExpectedBePattern, "lowercase letters and digits"), Field("spec.slug", ErrBadValue, "reason", "use lowercase letters and digits")},
		{"spec.slug", fmt.Sprintf(validation.MsgExpectedMatchPattern, "^[a-z]+$"), Field("spec.slug", ErrBadValue, "reason", "it isn't in the form this field takes")},
		{"spec.on", validation.MsgExpectedBoolean, Field("spec.on", ErrBadValue, "reason", "use true or false")},
		{"spec.count", validation.MsgExpectedInteger, Field("spec.count", ErrBadValue, "reason", "use a whole number")},
		{"query.limit", "invalid integer", Field("query.limit", ErrBadValue, "reason", "use a whole number")},
		{"spec.what", "something no validator says", Field("spec.what", ErrBadValue, "reason", "it isn't a value this field takes")},
	} {
		got := SchemaField(tc.path, tc.message)
		if got.Path != tc.want.Path || got.Code != tc.want.Code || got.Message != tc.want.Message || fmt.Sprint(got.Params) != fmt.Sprint(tc.want.Params) {
			t.Errorf("SchemaField(%q, %q) = %+v, want %+v", tc.path, tc.message, got, tc.want)
		}
	}
}

// Every message the validator knows has a finding of its own, so a new one
// in a later release of the validator shows up here rather than as the
// catch-all.
func TestEveryValidatorMessageIsWorded(t *testing.T) {
	for name, format := range map[string]string{
		"UnexpectedProperty": validation.MsgUnexpectedProperty, "RFC3339DateTime": validation.MsgExpectedRFC3339DateTime,
		"RFC1123DateTime": validation.MsgExpectedRFC1123DateTime, "RFC3339Date": validation.MsgExpectedRFC3339Date,
		"RFC3339Time": validation.MsgExpectedRFC3339Time, "RFC5322Email": validation.MsgExpectedRFC5322Email,
		"RFC5322EmailBare": validation.MsgExpectedRFC5322EmailBare, "RFC5890Hostname": validation.MsgExpectedRFC5890Hostname,
		"RFC2673IPv4": validation.MsgExpectedRFC2673IPv4, "RFC2373IPv6": validation.MsgExpectedRFC2373IPv6,
		"RFCIPAddr": validation.MsgExpectedRFCIPAddr, "RFC3986URI": validation.MsgExpectedRFC3986URI,
		"RFC3986AbsoluteURI": validation.MsgExpectedRFC3986AbsoluteURI, "RFC4122UUID": validation.MsgExpectedRFC4122UUID,
		"RFC6570URITemplate": validation.MsgExpectedRFC6570URITemplate, "RFC6901JSONPointer": validation.MsgExpectedRFC6901JSONPointer,
		"RFC6901RelativeJSONPointer": validation.MsgExpectedRFC6901RelativeJSONPointer, "Regexp": validation.MsgExpectedRegexp,
		"MatchAtLeastOneSchema": validation.MsgExpectedMatchAtLeastOneSchema, "MatchExactlyOneSchema": validation.MsgExpectedMatchExactlyOneSchema,
		"NotMatchSchema": validation.MsgExpectedNotMatchSchema, "PropertyNameInObject": validation.MsgExpectedPropertyNameInObject,
		"Boolean": validation.MsgExpectedBoolean, "Duration": validation.MsgExpectedDuration, "Number": validation.MsgExpectedNumber,
		"Integer": validation.MsgExpectedInteger, "String": validation.MsgExpectedString, "Base64String": validation.MsgExpectedBase64String,
		"Array": validation.MsgExpectedArray, "Object": validation.MsgExpectedObject, "ArrayItemsUnique": validation.MsgExpectedArrayItemsUnique,
		"OneOf": validation.MsgExpectedOneOf, "Const": validation.MsgExpectedConst, "MinimumNumber": validation.MsgExpectedMinimumNumber,
		"ExclusiveMinimumNumber": validation.MsgExpectedExclusiveMinimumNumber, "MaximumNumber": validation.MsgExpectedMaximumNumber,
		"ExclusiveMaximumNumber": validation.MsgExpectedExclusiveMaximumNumber, "NumberBeMultipleOf": validation.MsgExpectedNumberBeMultipleOf,
		"MinLength": validation.MsgExpectedMinLength, "MaxLength": validation.MsgExpectedMaxLength, "BePattern": validation.MsgExpectedBePattern,
		"MatchPattern": validation.MsgExpectedMatchPattern, "MinItems": validation.MsgExpectedMinItems, "MaxItems": validation.MsgExpectedMaxItems,
		"MinProperties": validation.MsgExpectedMinProperties, "MaxProperties": validation.MsgExpectedMaxProperties,
		"RequiredProperty": validation.MsgExpectedRequiredProperty, "DependentRequiredProperty": validation.MsgExpectedDependentRequiredProperty,
		"ResolvableSchemaRef": validation.MsgExpectedResolvableSchemaRef,
	} {
		message := strings.NewReplacer("%v", "7", "%d", "7", "%s", "x").Replace(format)
		got := SchemaField("spec.field", message)
		if got.Code == ErrBadValue.Code && got.Params["reason"] == "it isn't a value this field takes" {
			t.Errorf("%s (%q) has no words of its own", name, message)
		}
		if strings.Contains(got.Message, "expected") || strings.Contains(got.Message, "RFC") {
			t.Errorf("%s reads like the validator: %q", name, got.Message)
		}
	}
}
