package i18n

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2/validation"
)

// SchemaField turns what the JSON Schema validator found wrong with the
// field at path into a field error of the catalog, in the words of the
// panel's own errors: a missing property becomes core.required on that
// property, an unknown one core.unknown_name, a string over its maximum
// length core.too_long, and everything else core.bad_value with a reason
// that says what the field takes, such as "use csv or json". The API's
// request checks and the store's checks of specs and action inputs both
// report through it, so the validator's own wording never reaches people.
func SchemaField(path, message string) FieldError {
	for _, f := range schemaFindings {
		if m := f.pattern.FindStringSubmatch(message); m != nil {
			return f.field(path, m[1:])
		}
	}
	return Field(path, ErrBadValue, "reason", "it isn't a value this field takes")
}

// schemaFinding is one kind of message the validator gives, and the field
// error it means.
type schemaFinding struct {
	pattern *regexp.Regexp
	field   func(path string, args []string) FieldError
}

// fieldPath joins a property's name to the path of the object it is in.
func fieldPath(base, name string) string {
	if base == "" {
		return name
	}
	return base + "." + name
}

// lastName is the name of the field a path ends with.
func lastName(path string) string {
	if i := strings.LastIndexAny(path, ".["); i >= 0 && path[i] == '.' {
		return path[i+1:]
	}
	return path
}

// choices lists the values a field takes as people would say them: a, b or c.
func choices(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	}
	return strings.Join(values[:len(values)-1], ", ") + " or " + values[len(values)-1]
}

// count says how many of a thing there are: one item, or 3 items.
func count(n, one, many string) string {
	if n == "1" {
		return "one " + one
	}
	return n + " " + many
}

// number writes a number of a message as people write it, 1000000 rather
// than 1e+06.
func number(text string) string {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return text
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// reason is a finding that is core.bad_value with a reason of its own.
func reason(text func(args []string) string) func(path string, args []string) FieldError {
	return func(path string, args []string) FieldError {
		return Field(path, ErrBadValue, "reason", text(args))
	}
}

// fixed is a reason that doesn't depend on the message.
func fixed(text string) func(path string, args []string) FieldError {
	return reason(func([]string) string { return text })
}

// schemaPattern matches a message of the validator made from format, whose
// %v, %d and %s become what the message says there.
func schemaPattern(format string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(format)
	quoted = strings.NewReplacer("%v", "(.*)", "%d", `(-?\d+)`, "%s", "(.*)").Replace(quoted)
	return regexp.MustCompile("^" + quoted + "$")
}

// schemaFindings are the validator's messages, checked in order: those with
// more fixed text first, so a general one doesn't take the place of a
// narrower one.
var schemaFindings = func() []schemaFinding {
	type entry struct {
		format string
		field  func(path string, args []string) FieldError
	}
	entries := []entry{
		{validation.MsgExpectedRequiredProperty, func(path string, args []string) FieldError {
			return Field(fieldPath(path, args[0]), ErrRequired)
		}},
		{validation.MsgExpectedDependentRequiredProperty, func(path string, args []string) FieldError {
			return Field(fieldPath(path, args[0]), ErrRequired)
		}},
		{validation.MsgUnexpectedProperty, func(path string, _ []string) FieldError {
			return Field(path, ErrUnknownName, "what", "field", "name", lastName(path))
		}},
		{validation.MsgExpectedMaxLength, func(path string, args []string) FieldError {
			n, _ := strconv.Atoi(args[0])
			return Field(path, ErrTooLong, "max", n)
		}},
		{validation.MsgExpectedMinLength, func(path string, args []string) FieldError {
			if args[0] == "1" {
				return Field(path, ErrRequired)
			}
			return Field(path, ErrBadValue, "reason", "use at least "+args[0]+" characters")
		}},
		{validation.MsgExpectedOneOf, reason(func(args []string) string { return "use " + choices(strings.Split(args[0], ", ")) })},
		{validation.MsgExpectedConst, reason(func(args []string) string { return "use " + args[0] })},
		{validation.MsgExpectedMinimumNumber, reason(func(args []string) string { return "use " + number(args[0]) + " or more" })},
		{validation.MsgExpectedExclusiveMinimumNumber, reason(func(args []string) string { return "use a number above " + number(args[0]) })},
		{validation.MsgExpectedMaximumNumber, reason(func(args []string) string { return "use " + number(args[0]) + " or less" })},
		{validation.MsgExpectedExclusiveMaximumNumber, reason(func(args []string) string { return "use a number below " + number(args[0]) })},
		{validation.MsgExpectedNumberBeMultipleOf, reason(func(args []string) string { return "use a multiple of " + number(args[0]) })},
		{validation.MsgExpectedMinItems, reason(func(args []string) string { return "give at least " + count(args[0], "item", "items") })},
		{validation.MsgExpectedMaxItems, reason(func(args []string) string { return "give at most " + count(args[0], "item", "items") })},
		{validation.MsgExpectedArrayItemsUnique, fixed("give each item only once")},
		{validation.MsgExpectedMinProperties, reason(func(args []string) string { return "give at least " + count(args[0], "entry", "entries") })},
		{validation.MsgExpectedMaxProperties, reason(func(args []string) string { return "give at most " + count(args[0], "entry", "entries") })},
		{validation.MsgExpectedMatchPattern, fixed("it isn't in the form this field takes")},
		{validation.MsgExpectedRFC3339DateTime, fixed("use a date and time such as 2026-05-01T09:30:00Z")},
		{validation.MsgExpectedRFC1123DateTime, fixed("use a date and time such as Fri, 01 May 2026 09:30:00 GMT")},
		{validation.MsgExpectedRFC3339Date, fixed("use a date such as 2026-05-01")},
		{validation.MsgExpectedRFC3339Time, fixed("use a time such as 09:30:00")},
		{validation.MsgExpectedRFC5322Email, fixed("use an email address such as ada@example.com")},
		{validation.MsgExpectedRFC5322EmailBare, fixed("use an email address such as ada@example.com, without a name")},
		{validation.MsgExpectedRFC5890Hostname, fixed("use a host name such as example.com")},
		{validation.MsgExpectedRFC2673IPv4, fixed("use an IPv4 address such as 192.0.2.10")},
		{validation.MsgExpectedRFC2373IPv6, fixed("use an IPv6 address such as 2001:db8::10")},
		{validation.MsgExpectedRFCIPAddr, fixed("use an IP address such as 192.0.2.10 or 2001:db8::10")},
		{validation.MsgExpectedRFC3986AbsoluteURI, fixed("use a full URL with its scheme, such as https://example.com/")},
		{validation.MsgExpectedRFC3986URI, fixed("use a URL such as https://example.com/")},
		{validation.MsgExpectedRFC6570URITemplate, fixed("use a URI template such as https://example.com/{id}")},
		{validation.MsgExpectedRFC4122UUID, fixed("use a UUID such as 0b4e7a0e-5e8a-4c3e-9d0f-2f1c7b1e6a5d")},
		{validation.MsgExpectedRFC6901JSONPointer, fixed("use a JSON pointer such as /spec/name")},
		{validation.MsgExpectedRFC6901RelativeJSONPointer, fixed("use a relative JSON pointer such as 0/name")},
		{validation.MsgExpectedRegexp, fixed("use a regular expression that compiles")},
		{validation.MsgExpectedDuration, fixed("use a duration such as 1h30m")},
		{validation.MsgExpectedBase64String, fixed("use base64-encoded data")},
		{validation.MsgExpectedMatchAtLeastOneSchema, fixed("it doesn't have any of the forms this field takes")},
		{validation.MsgExpectedMatchExactlyOneSchema, fixed("it doesn't have exactly one of the forms this field takes")},
		{validation.MsgExpectedNotMatchSchema, fixed("it is a value this field doesn't take")},
		{validation.MsgExpectedPropertyNameInObject, fixed("it doesn't say which of the forms this field takes it has")},
		{validation.MsgExpectedResolvableSchemaRef, fixed("it can't be checked against what this field takes")},
		{validation.MsgExpectedBoolean, fixed("use true or false")},
		{validation.MsgExpectedInteger, fixed("use a whole number")},
		{validation.MsgExpectedNumber, fixed("use a number")},
		{validation.MsgExpectedString, fixed("use text")},
		{validation.MsgExpectedArray, fixed("use a list")},
		{validation.MsgExpectedObject, fixed("use an object with fields")},
		// What the API's request parsing reports about parameters and
		// bodies before the schema is checked.
		{"invalid integer", fixed("use a whole number")},
		{"invalid float", fixed("use a number")},
		{"invalid floating value", fixed("use a number")},
		{"invalid boolean", fixed("use true or false")},
		{"invalid date/time for format %s", fixed("use a date and time such as 2026-05-01T09:30:00Z")},
		{"invalid url.URL value", fixed("use a URL such as https://example.com/")},
		{"invalid value: %s", fixed("it isn't a value this field takes")},
		{"invalid JSON: %s", fixed("it isn't valid JSON")},
		{"expected at most one value, but received multiple values", fixed("give it once")},
	}
	// A pattern's description, when it has one, says what the field takes.
	described := entry{validation.MsgExpectedBePattern, reason(func(args []string) string { return "use " + args[0] })}
	out := make([]schemaFinding, 0, len(entries)+1)
	for _, e := range entries {
		out = append(out, schemaFinding{pattern: schemaPattern(e.format), field: e.field})
	}
	// "expected string to be %s" would also match the formats above, so it
	// comes last.
	return append(out, schemaFinding{pattern: schemaPattern(described.format), field: described.field})
}()
