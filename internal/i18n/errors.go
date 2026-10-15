package i18n

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Code is a stable, translatable identifier for one kind of failure, in the
// form <module>.<snake_case>.
type Code string

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)

// Valid reports whether c follows the <module>.<snake_case> pattern.
func (c Code) Valid() bool { return codePattern.MatchString(string(c)) }

// Module returns the part of the code before the first dot.
func (c Code) Module() string {
	module, _, _ := strings.Cut(string(c), ".")
	return module
}

// Def declares one kind of error: its code, the HTTP status the API answers
// with, and an English message that may contain {placeholders}.
type Def struct {
	Code    Code
	Status  int
	Message string
}

// Error returns the English message with unfilled placeholders. Prefer New,
// which fills them in; this exists so a Def can be the target of errors.Is.
func (d *Def) Error() string { return Format(d.Message, nil) }

// New returns an error of this kind. Params are key/value pairs that fill the
// message placeholders, for example New("domain", "example.com").
func (d *Def) New(params ...any) *Error {
	p := pairs(params)
	return &Error{
		Code:    d.Code,
		Status:  d.Status,
		Message: Format(d.Message, p),
		Params:  p,
	}
}

// Wrap returns an error of this kind that keeps cause for logs and errors.Is.
// The cause is never shown to users.
func (d *Def) Wrap(cause error, params ...any) *Error {
	e := d.New(params...)
	e.cause = cause
	return e
}

// Is reports whether err, or any error it wraps, is of this kind.
func (d *Def) Is(err error) bool { return errors.Is(err, d) }

// Error is an error with a stable code. The API turns it into problem details
// and the UI translates the code with the parameters.
type Error struct {
	Code    Code           `json:"code"`
	Status  int            `json:"status"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
	Fields  []FieldError   `json:"fields,omitempty"`
	cause   error
}

// Error returns the English message followed by the cause, for logs.
func (e *Error) Error() string {
	if e.cause == nil {
		return e.Message
	}
	return e.Message + ": " + e.cause.Error()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.cause }

// Is matches errors of the same code, whether the target is a Def or an Error.
func (e *Error) Is(target error) bool {
	switch t := target.(type) {
	case *Def:
		return t.Code == e.Code
	case *Error:
		return t.Code == e.Code
	}
	return false
}

// WithFields attaches field errors and returns e.
func (e *Error) WithFields(fields ...FieldError) *Error {
	e.Fields = append(e.Fields, fields...)
	return e
}

// FieldError explains what is wrong with one field of a request.
type FieldError struct {
	// Path locates the field with dots and indexes, for example "spec.domains[0]".
	Path    string         `json:"path"`
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
}

// Field builds a field error of the kind d.
func Field(path string, d *Def, params ...any) FieldError {
	p := pairs(params)
	return FieldError{Path: path, Code: d.Code, Message: Format(d.Message, p), Params: p}
}

// Invalid returns a core.invalid error carrying the given field errors.
func Invalid(fields ...FieldError) *Error {
	return ErrInvalid.New().WithFields(fields...)
}

// From returns err as an *Error. Errors without a code become core.internal
// with err kept as the cause, so nothing internal reaches users by accident.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var d *Def
	if errors.As(err, &d) {
		return d.Wrap(err)
	}
	return ErrInternal.Wrap(err)
}

// CodeOf returns the code carried by err, or "" when it has none.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	var d *Def
	if errors.As(err, &d) {
		return d.Code
	}
	return ""
}

func pairs(params []any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	m := make(map[string]any, len(params)/2+1)
	for i := 0; i < len(params); i += 2 {
		key := fmt.Sprint(params[i])
		if i+1 < len(params) {
			m[key] = params[i+1]
		} else {
			m[key] = nil
		}
	}
	return m
}

// The registry of declared codes. It is filled while packages initialize.
var registry = struct {
	sync.RWMutex
	defs map[Code]*Def
	errs []error
}{defs: map[Code]*Def{}}

// Define declares an error code. It is meant for package-level variables:
//
//	var ErrDomainTaken = i18n.Define("web.domain_taken", http.StatusConflict,
//		"The domain {domain} is already used by another site.")
//
// An invalid or duplicate code is recorded and reported by Err, which every
// role checks at startup and the test suite checks on every run.
func Define(code Code, status int, message string) *Def {
	d := &Def{Code: code, Status: status, Message: message}
	registry.Lock()
	defer registry.Unlock()
	switch {
	case !code.Valid():
		registry.errs = append(registry.errs, fmt.Errorf("i18n: invalid error code %q", code))
	case status < 400 || status > 599:
		registry.errs = append(registry.errs, fmt.Errorf("i18n: error code %q has HTTP status %d, want 4xx or 5xx", code, status))
	case strings.TrimSpace(message) == "":
		registry.errs = append(registry.errs, fmt.Errorf("i18n: error code %q has no message", code))
	case registry.defs[code] != nil:
		registry.errs = append(registry.errs, fmt.Errorf("i18n: error code %q is defined twice", code))
	default:
		registry.defs[code] = d
	}
	return d
}

// Lookup returns the declaration of code.
func Lookup(code Code) (*Def, bool) {
	registry.RLock()
	defer registry.RUnlock()
	d, ok := registry.defs[code]
	return d, ok
}

// Defs returns every declared code, sorted by code.
func Defs() []*Def {
	registry.RLock()
	defer registry.RUnlock()
	out := make([]*Def, 0, len(registry.defs))
	for _, d := range registry.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Err reports problems found while codes were declared.
func Err() error {
	registry.RLock()
	defer registry.RUnlock()
	return errors.Join(registry.errs...)
}
