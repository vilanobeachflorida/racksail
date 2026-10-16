package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

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
// misspelt field is reported rather than ignored.
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
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			return i18n.Invalid(i18n.Field("body."+typeErr.Field, i18n.ErrBadValue, "reason", "expected "+typeErr.Type.String()))
		}
		return i18n.ErrBadRequest.New("reason", "the body isn't valid JSON: "+strings.TrimPrefix(err.Error(), "json: "))
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
		return nil, i18n.ErrBadRequest.New("reason", "the body isn't valid JSON")
	}
	return data, nil
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

// changeParamsOf reads dryRun and Idempotency-Key.
func changeParamsOf(ctx huma.Context) (ChangeParams, error) {
	dry, err := paramsOf(ctx).boolean("dryRun")
	if err != nil {
		return ChangeParams{}, err
	}
	key := ctx.Header("Idempotency-Key")
	if len(key) > 128 {
		return ChangeParams{}, i18n.Invalid(i18n.Field("header.Idempotency-Key", i18n.ErrTooLong, "max", 128))
	}
	return ChangeParams{DryRun: dry, IdempotencyKey: key}, nil
}
