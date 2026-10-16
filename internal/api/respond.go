package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/validation"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// maxBody bounds request bodies. Specs describe settings; anything bulky,
// such as a site's files, goes through its own operations.
const maxBody = 4 << 20

// handler serves an operation built while the server starts, such as those
// generated for kinds: it reads the request itself and returns the status
// and body of the answer, or an error that becomes a problem.
type handler func(ctx huma.Context) (status int, body any, err error)

// handle registers an operation whose parameters and schemas are only known
// when the server is built. It goes through the same middleware as every
// other operation.
func (s *Server) handle(op *huma.Operation, fn handler) {
	s.withProblems(op)
	s.api.OpenAPI().AddOperation(op)
	s.api.Adapter().Handle(op, s.api.Middlewares().Handler(func(ctx huma.Context) {
		status, body, err := fn(ctx)
		if err != nil {
			writeProblemTo(ctx, err)
			return
		}
		ctx.SetHeader("Content-Type", "application/json")
		ctx.SetStatus(status)
		if body == nil {
			return
		}
		if err := json.NewEncoder(ctx.BodyWriter()).Encode(body); err != nil {
			s.opts.Log.Warn("writing an answer", "operation", op.OperationID, "error", err)
		}
	}))
}

// withProblems documents the problem details every operation can answer
// with.
func (s *Server) withProblems(op *huma.Operation) {
	if op.Responses == nil {
		op.Responses = map[string]*huma.Response{}
	}
	if op.Responses["default"] == nil {
		op.Responses["default"] = &huma.Response{
			Description: "A problem; its code says what went wrong",
			Content:     map[string]*huma.MediaType{"application/problem+json": {Schema: s.schemaOf(reflect.TypeFor[httpapi.Problem](), "Problem")}},
		}
	}
}

// schemaOf returns a reference to the schema of a Go type in the document.
func (s *Server) schemaOf(t reflect.Type, hint string) *huma.Schema {
	return s.api.OpenAPI().Components.Schemas.Schema(t, true, hint)
}

// jsonResponse documents a JSON answer.
func jsonResponse(description string, schema *huma.Schema) *huma.Response {
	return &huma.Response{Description: description, Content: map[string]*huma.MediaType{"application/json": {Schema: schema}}}
}

func writeProblemTo(ctx huma.Context, err error) {
	p := httpapi.FromError(err)
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(p.Status)
	json.NewEncoder(ctx.BodyWriter()).Encode(p)
}

// readJSON decodes the request body into v, refusing unknown fields, so a
// misspelt field is reported rather than ignored. What the decoder finds
// wrong comes back as the schema validator's findings do for operations
// with typed inputs, in the catalog's words (bodyError).
func readJSON(ctx huma.Context, v any) error {
	data, err := io.ReadAll(io.LimitReader(ctx.BodyReader(), maxBody+1))
	if err != nil {
		return i18n.ErrBadRequest.Wrap(err, "reason", "the body couldn't be read")
	}
	if len(data) > maxBody {
		return i18n.ErrTooLarge.New("limit", "4 MiB")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return i18n.ErrBadRequest.New("reason", "the request has no body; send JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return bodyError(reflect.TypeOf(v), data, err)
	}
	if dec.More() {
		return i18n.ErrBadRequest.New("reason", "the body has more than one JSON value")
	}
	return nil
}

// readRaw returns the request body as JSON, or nil when there is none.
func readRaw(ctx huma.Context) (json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(ctx.BodyReader(), maxBody+1))
	if err != nil {
		return nil, i18n.ErrBadRequest.Wrap(err, "reason", "the body couldn't be read")
	}
	if len(data) > maxBody {
		return nil, i18n.ErrTooLarge.New("limit", "4 MiB")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if !json.Valid(data) {
		return nil, notJSON()
	}
	return data, nil
}

// notJSON is the answer to a body that isn't JSON at all, the one operations
// with typed inputs give.
func notJSON() error {
	const reason = "the body isn't valid JSON"
	return i18n.ErrBadRequest.New("reason", reason).WithFields(i18n.Field("body", i18n.ErrBadValue, "reason", reason))
}

// bodyError says what the JSON decoder found wrong with data, a body for a
// value of type t, as the schema validator says it for operations with
// typed inputs (i18n.SchemaField): a field of the wrong type is
// core.bad_value with what the field takes, such as "use a whole number",
// a field t doesn't have is core.unknown_name, and a body that isn't JSON,
// one cut short included, is core.bad_request. The decoder's own words,
// which name Go types, never reach people.
func bodyError(t reflect.Type, data []byte, err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		path := "body"
		if typeErr.Field != "" {
			path += "." + decodedPath(data, typeErr.Field)
		}
		return i18n.Invalid(i18n.SchemaField(path, expected(typeErr.Type)))
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		path, found := unknownField(t, data)
		if !found {
			path, _ = strconv.Unquote(name)
		}
		return i18n.Invalid(i18n.SchemaField("body."+path, validation.MsgUnexpectedProperty))
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return notJSON()
	}
	// A field's own decoding refused its value.
	return i18n.Invalid(i18n.Field("body", i18n.ErrBadValue, "reason", "it doesn't have the form the operation takes"))
}

// expected is what the schema validator says when a value isn't of the JSON
// type a value of t is written as.
func expected(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return validation.MsgExpectedBoolean
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return validation.MsgExpectedInteger
	case reflect.Float32, reflect.Float64:
		return validation.MsgExpectedNumber
	case reflect.String:
		return validation.MsgExpectedString
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			// Bytes are written as base64 text.
			return validation.MsgExpectedBase64String
		}
		return validation.MsgExpectedArray
	case reflect.Map, reflect.Struct:
		return validation.MsgExpectedObject
	}
	return ""
}

// decodedPath turns the path of a field of the JSON document data, as the
// decoder writes it, the names and indexes from the top joined by dots, into
// the API's, where an index is in brackets: items.1.name becomes
// items[1].name. The decoder escapes names as a JSON pointer does, ~1 for /
// and ~0 for ~, but not their dots, so a name is matched with the document:
// labels.app.example.com is the key app.example.com of labels when labels
// has that key.
func decodedPath(data []byte, dotted string) string {
	parts := strings.Split(dotted, ".")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	var b strings.Builder
	node := bytes.TrimSpace(data)
	i := 0
walk:
	for i < len(parts) {
		switch {
		case len(node) > 0 && node[0] == '{':
			var members map[string]json.RawMessage
			if json.Unmarshal(node, &members) != nil {
				break walk
			}
			for k := i + 1; k <= len(parts); k++ {
				name := strings.Join(parts[i:k], ".")
				if value, ok := members[name]; ok {
					b.WriteString("." + name)
					node, i = bytes.TrimSpace(value), k
					continue walk
				}
			}
		case len(node) > 0 && node[0] == '[':
			var items []json.RawMessage
			n, err := strconv.Atoi(parts[i])
			if json.Unmarshal(node, &items) != nil || err != nil || n < 0 || n >= len(items) {
				break walk
			}
			b.WriteString("[" + parts[i] + "]")
			node, i = bytes.TrimSpace(items[n]), i+1
			continue walk
		}
		break
	}
	if i < len(parts) {
		b.WriteString("." + strings.Join(parts[i:], "."))
	}
	return strings.TrimPrefix(b.String(), ".")
}

var (
	typeOfRawMessage  = reflect.TypeFor[json.RawMessage]()
	typeOfUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
)

// unknownField finds the first field of the JSON document data that a value
// of type t has no place for, the one the decoder refused, and returns its
// path, such as scope.idd. Values t decodes itself, and raw JSON, take
// anything.
func unknownField(t reflect.Type, data []byte) (string, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == typeOfRawMessage || t.Implements(typeOfUnmarshaler) || reflect.PointerTo(t).Implements(typeOfUnmarshaler) {
		return "", false
	}
	switch t.Kind() {
	case reflect.Struct:
		dec := json.NewDecoder(bytes.NewReader(data))
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			return "", false
		}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return "", false
			}
			name, _ := tok.(string)
			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				return "", false
			}
			f, ok := jsonField(t, name)
			if !ok {
				return name, true
			}
			if path, ok := unknownField(f.Type, value); ok {
				return joinPath(name, path), true
			}
		}
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			return "", false
		}
		for i, item := range items {
			if path, ok := unknownField(t.Elem(), item); ok {
				return joinPath("["+strconv.Itoa(i)+"]", path), true
			}
		}
	case reflect.Map:
		var entries map[string]json.RawMessage
		if json.Unmarshal(data, &entries) != nil {
			return "", false
		}
		for _, key := range slices.Sorted(maps.Keys(entries)) {
			if path, ok := unknownField(t.Elem(), entries[key]); ok {
				return joinPath(key, path), true
			}
		}
	}
	return "", false
}

// joinPath puts the path of a field inside another after that one's.
func joinPath(outer, inner string) string {
	if strings.HasPrefix(inner, "[") {
		return outer + inner
	}
	return outer + "." + inner
}

// jsonField finds the field of struct type t that the decoder fills from
// the JSON field name: the one with that name, or failing that the one whose
// name matches it but for case, as the decoder does, those of embedded
// structs included.
func jsonField(t reflect.Type, name string) (reflect.StructField, bool) {
	var folded *reflect.StructField
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || (f.Anonymous && f.Tag.Get("json") == "") {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if tag == "" {
			tag = f.Name
		}
		if tag == name {
			return f, true
		}
		if folded == nil && strings.EqualFold(tag, name) {
			folded = &f
		}
	}
	if folded != nil {
		return *folded, true
	}
	return reflect.StructField{}, false
}

func badParam(name, reason string) error {
	return i18n.Invalid(i18n.Field("query."+name, i18n.ErrBadValue, "reason", reason))
}

// params reads the query parameters of a request.
type params url.Values

func paramsOf(ctx huma.Context) params {
	u := ctx.URL()
	return params(u.Query())
}

func (p params) get(name string) string { return url.Values(p).Get(name) }

// list returns every value of a parameter, given repeated or separated by
// commas.
func (p params) list(name string) []string {
	var out []string
	for _, v := range p[name] {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func (p params) integer(name string, def, low, high int) (int, error) {
	v := p.get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < low || n > high {
		return 0, badParam(name, fmt.Sprintf("use a whole number from %d to %d", low, high))
	}
	return n, nil
}

func (p params) boolean(name string) (bool, error) {
	v := p.get(name)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, badParam(name, "use true or false")
	}
	return b, nil
}

func (p params) id(name string) (store.ID, error) {
	v := p.get(name)
	if v == "" {
		return "", nil
	}
	id, err := store.ParseID(v)
	if err != nil {
		return "", badParam(name, "not an ID")
	}
	return id, nil
}

func (p params) scope(name string) (*store.Scope, error) {
	v := p.get(name)
	if v == "" {
		return nil, nil
	}
	s, err := store.ParseScope(v)
	if err != nil {
		return nil, badParam(name, "use server, reseller:<id> or account:<id>")
	}
	return &s, nil
}

// limit reads the page size of a list.
func (p params) limit() (int, error) {
	return p.integer("limit", store.DefaultLimit, 1, store.MaxLimit)
}

// queryParam documents a query parameter.
func queryParam(name, typ, description string) *huma.Param {
	return &huma.Param{Name: name, In: "query", Description: description, Schema: &huma.Schema{Type: typ}}
}

// listParam documents a query parameter that may be repeated.
func listParam(name, description string) *huma.Param {
	return &huma.Param{
		Name: name, In: "query", Description: description, Explode: boolPtr(true),
		Schema: &huma.Schema{Type: huma.TypeArray, Items: &huma.Schema{Type: huma.TypeString}},
	}
}

func pathParam(name, description string) *huma.Param {
	return &huma.Param{Name: name, In: "path", Required: true, Description: description, Schema: &huma.Schema{Type: huma.TypeString}}
}

func boolPtr(b bool) *bool { return &b }

// pageParams documents cursor and limit.
func pageParams() []*huma.Param {
	limit := queryParam("limit", huma.TypeInteger, fmt.Sprintf("How many to return, from 1 to %d; %d when left out", store.MaxLimit, store.DefaultLimit))
	limit.Schema.Minimum, limit.Schema.Maximum = floatPtr(1), floatPtr(store.MaxLimit)
	return []*huma.Param{
		queryParam("cursor", huma.TypeString, "Continues a list where the page before it ended: the next of that page"),
		limit,
	}
}

func floatPtr(f float64) *float64 { return &f }

// changeParams documents the parameters every operation that changes
// something takes, as ChangeParams does for operations with typed inputs.
func changeParams() []*huma.Param {
	key := &huma.Param{
		Name: "Idempotency-Key", In: "header",
		Description: "Any unique string; a retry with the same key returns the first change set instead of applying the change again",
		Schema:      &huma.Schema{Type: huma.TypeString, MaxLength: intPtr(128)},
	}
	return []*huma.Param{queryParam("dryRun", huma.TypeBoolean, "Plan the change and return its change set without applying it"), key}
}

func intPtr(i int) *int { return &i }

// changeParamsOf reads dryRun and Idempotency-Key. dryRun is read the way
// Huma reads it into ChangeParams, so it means the same to every operation.
func changeParamsOf(ctx huma.Context) (ChangeParams, error) {
	var dry bool
	if v := ctx.Query("dryRun"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return ChangeParams{}, badParam("dryRun", "use true or false")
		}
		dry = b
	}
	key := ctx.Header("Idempotency-Key")
	if len(key) > 128 {
		return ChangeParams{}, i18n.Invalid(i18n.Field("header.Idempotency-Key", i18n.ErrTooLong, "max", 128))
	}
	return ChangeParams{DryRun: dry, IdempotencyKey: key}, nil
}
