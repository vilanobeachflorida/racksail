// Package i18n holds server-side messages and the stable error codes that
// every layer of Racksail returns.
//
// An error code names one kind of failure, such as "web.domain_taken". Codes
// are declared once with Define, together with the HTTP status the API uses
// and an English message with {placeholders}. The API returns the code and
// its parameters in RFC 9457 problem details, and the UI translates them
// through its ICU message catalogs, so the English text here is a fallback
// for logs, the CLI and clients that don't translate.
//
// Codes follow the pattern <module>.<snake_case>. Core codes use the "core"
// module name and are declared in this package.
//
// What the JSON Schema validators find wrong with a request becomes field
// errors of these codes, in the panel's own words: SchemaField words the
// findings of the API's validator, and JSONSchemaFields those of the
// validator of the schemas in spec/schemas, which rules are checked with.
//
// Messages that are not errors (CLI output, notification text, installer
// screens) go through a Catalog, which looks up a key for a locale and falls
// back to English.
package i18n
