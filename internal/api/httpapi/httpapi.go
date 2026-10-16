package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Problem is an RFC 9457 problem details object carrying Racksail's stable
// error code. Clients translate the code with its parameters; the detail is
// the English text.
type Problem struct {
	Type     string         `json:"type,omitempty" format:"uri" default:"about:blank" doc:"A URI reference for the problem type"`
	Title    string         `json:"title,omitempty" example:"Conflict" doc:"Short summary of the problem type"`
	Status   int            `json:"status,omitempty" example:"409" doc:"HTTP status code"`
	Detail   string         `json:"detail,omitempty" example:"The domain example.com is already used by another site." doc:"English explanation of this occurrence"`
	Instance string         `json:"instance,omitempty" format:"uri" doc:"A URI reference for this occurrence"`
	Code     i18n.Code      `json:"code" example:"web.domain_taken" doc:"Stable error code in the form <module>.<snake_case>"`
	Params   map[string]any `json:"params,omitempty" doc:"Values for the placeholders in the code's message"`
	Errors   []FieldProblem `json:"errors,omitempty" doc:"Problems with individual fields"`
}

// FieldProblem explains what is wrong with one field.
type FieldProblem struct {
	Location string         `json:"location,omitempty" example:"body.spec.domains[0]" doc:"Where the problem is"`
	Code     i18n.Code      `json:"code" doc:"Stable error code"`
	Message  string         `json:"message" doc:"English explanation"`
	Params   map[string]any `json:"params,omitempty" doc:"Values for the placeholders in the code's message"`
	Value    any            `json:"value,omitempty" doc:"The offending value, when it's safe to show"`
}

// Error implements error.
func (p *Problem) Error() string { return p.Detail }

// GetStatus implements huma.StatusError.
func (p *Problem) GetStatus() int { return p.Status }

// ContentType implements huma.ContentTypeFilter.
func (p *Problem) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	return ct
}

// FromError converts any error into a problem. Errors without a code become
// core.internal; their text is logged, never returned.
func FromError(err error) *Problem {
	var p *Problem
	if errors.As(err, &p) {
		return p
	}
	e := i18n.From(err)
	if e.Code == i18n.ErrInternal.Code && err != nil {
		slog.Error("request failed", "error", err)
	}
	prob := &Problem{
		Title:  http.StatusText(e.Status),
		Status: e.Status,
		Detail: e.Message,
		Code:   e.Code,
		Params: e.Params,
	}
	for _, f := range e.Fields {
		prob.Errors = append(prob.Errors, FieldProblem{Location: f.Path, Code: f.Code, Message: f.Message, Params: f.Params})
	}
	return prob
}

// statusCodes maps HTTP statuses Huma produces on its own to codes.
var statusCodes = map[int]*i18n.Def{
	http.StatusBadRequest:            i18n.ErrBadRequest,
	http.StatusUnauthorized:          i18n.ErrUnauthenticated,
	http.StatusForbidden:             i18n.ErrForbidden,
	http.StatusNotFound:              i18n.ErrNotFound,
	http.StatusConflict:              i18n.ErrConflict,
	http.StatusRequestEntityTooLarge: i18n.ErrTooLarge,
	http.StatusUnprocessableEntity:   i18n.ErrInvalid,
	http.StatusTooManyRequests:       i18n.ErrRateLimited,
	http.StatusServiceUnavailable:    i18n.ErrUnavailable,
	http.StatusGatewayTimeout:        i18n.ErrTimeout,
}

// NewError builds the problem Huma returns for errors it detects itself,
// such as malformed requests, and for errors handlers return. Errors with a
// code keep it; everything else gets the code for its status. What Huma
// found wrong with a request's fields becomes field errors of the catalog,
// worded as the panel's own (i18n.SchemaField), never the validator's.
func NewError(status int, msg string, errs ...error) huma.StatusError {
	for _, err := range errs {
		var e *i18n.Error
		var d *i18n.Def
		if errors.As(err, &e) || errors.As(err, &d) {
			return FromError(err)
		}
	}
	def, ok := statusCodes[status]
	if status >= 500 || !ok {
		for _, err := range errs {
			if err != nil {
				slog.Error("request failed", "status", status, "error", err)
			}
		}
		if status == 0 {
			// Huma asks for an example error with status 0 to document the
			// error schema.
			return &Problem{}
		}
		def = i18n.ErrInternal
		if status < 500 {
			def = i18n.ErrBadRequest
		}
	}
	p := &Problem{Title: http.StatusText(status), Status: status, Code: def.Code, Detail: msg}
	var fields []i18n.FieldError
	for _, err := range errs {
		if err == nil {
			continue
		}
		var detail *huma.ErrorDetail
		var value any
		var f i18n.FieldError
		switch {
		case errors.As(err, &detail) && detail.Location == "body" && status == http.StatusBadRequest:
			// The body couldn't be read as JSON at all.
			f = i18n.Field(detail.Location, i18n.ErrBadValue, "reason", "the body isn't valid JSON")
		case detail != nil:
			f, value = i18n.SchemaField(detail.Location, detail.Message), detail.Value
		case def != i18n.ErrInternal:
			f = i18n.SchemaField("", err.Error())
		default:
			continue
		}
		fields = append(fields, f)
		p.Errors = append(p.Errors, FieldProblem{Location: f.Path, Code: f.Code, Message: f.Message, Params: f.Params, Value: value})
	}
	switch def {
	case i18n.ErrInternal:
		p.Detail = i18n.ErrInternal.Message
	case i18n.ErrBadRequest:
		p.Params = map[string]any{"reason": badRequestReason(msg, fields)}
		p.Detail = i18n.Format(def.Message, p.Params)
	case i18n.ErrInvalid:
		p.Detail = def.Message
	case i18n.ErrTooLarge:
		p.Params = map[string]any{"limit": sizeLimit(msg)}
		p.Detail = i18n.Format(def.Message, p.Params)
	}
	return p
}

// badRequestReason says why a request couldn't be read, in plain words:
// what Huma said about the request as a whole, or about its first field
// when it only said that checking it failed.
func badRequestReason(msg string, fields []i18n.FieldError) string {
	switch msg {
	case "request body is required":
		return "the request has no body; send JSON"
	case "request body read timeout":
		return "the body took too long to arrive"
	case "validation failed", "":
		if len(fields) > 0 {
			if reason, ok := fields[0].Params["reason"].(string); ok {
				return reason
			}
			return fields[0].Message
		}
		return "it doesn't have the form the operation takes"
	}
	return msg
}

var sizeLimitPattern = regexp.MustCompile(`limit=(\d+) bytes`)

// sizeLimit says how large a request may be, from what Huma said when one
// was larger.
func sizeLimit(msg string) string {
	m := sizeLimitPattern.FindStringSubmatch(msg)
	if m == nil {
		return "size"
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return "size"
	}
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func init() {
	// Huma reports its own errors through these hooks; route them through
	// NewError so every error response has the same shape and a code.
	huma.NewError = NewError
	huma.NewErrorWithContext = func(_ huma.Context, status int, msg string, errs ...error) huma.StatusError {
		return NewError(status, msg, errs...)
	}
}

// Config returns the Huma configuration for a Racksail API: OpenAPI 3.1,
// schema names that can't collide between modules, no documentation UI
// (it would load scripts from a CDN) and no $schema links in responses.
func Config(title, version string) huma.Config {
	cfg := huma.DefaultConfig(title, version)
	cfg.Components.Schemas = huma.NewMapRegistry("#/components/schemas/", SchemaNamer)
	cfg.OpenAPIPath = ""
	cfg.DocsPath = ""
	cfg.SchemasPath = ""
	cfg.CreateHooks = nil
	return cfg
}

const modulePrefix = "github.com/vilanobeachflorida/racksail/modules/"

// SchemaNamer names schemas after their Go types, like Huma's default, but
// prefixes types from module packages with the module name, so web.SiteSpec
// becomes WebSiteSpec and two modules can use the same type names.
func SchemaNamer(t reflect.Type, hint string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := t.String()
	if t.Name() != "" && t.PkgPath() != "" {
		// Use the full path so module types can be recognized below.
		name = strings.Replace(t.String(), path(t.String()), t.PkgPath(), 1)
	}
	if t.Name() == "" {
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array && t.Kind() != reflect.Map {
			name = hint
		}
	}
	name = strings.ReplaceAll(name, "[]", "List[")
	var b strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '[' || r == ']' || r == '*' || r == ',' || r == ' '
	}) {
		dot := strings.LastIndex(part, ".")
		base := part[dot+1:]
		if base == "" {
			continue
		}
		if dot >= 0 {
			if id, ok := moduleOf(part[:dot]); ok {
				prefix := upperFirst(id)
				if !strings.HasPrefix(base, prefix) {
					base = prefix + base
				}
			}
		}
		b.WriteString(upperFirst(base))
	}
	return b.String()
}

// path returns the package qualifier of a type string such as "web.SiteSpec".
func path(s string) string {
	if i := strings.IndexAny(s, "[."); i >= 0 && s[i] == '.' {
		return s[:i]
	}
	return ""
}

func moduleOf(pkgPath string) (string, bool) {
	rest, ok := strings.CutPrefix(pkgPath, modulePrefix)
	if !ok {
		return "", false
	}
	id, _, _ := strings.Cut(rest, "/")
	return id, id != ""
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
