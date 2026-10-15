package httpapi

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type plain struct{}

func TestSchemaNamer(t *testing.T) {
	tests := []struct {
		t    reflect.Type
		want string
	}{
		{reflect.TypeFor[plain](), "Plain"},
		{reflect.TypeFor[*plain](), "Plain"},
		{reflect.TypeFor[store.Scope](), "Scope"},
		{reflect.TypeFor[store.Object[plain, store.Status]](), "ObjectPlainStatus"},
		{reflect.TypeFor[string](), "String"},
	}
	for _, tt := range tests {
		if got := SchemaNamer(tt.t, "Hint"); got != tt.want {
			t.Errorf("SchemaNamer(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
	if got := SchemaNamer(reflect.TypeOf(struct{ A int }{}), "Inline"); got != "Inline" {
		t.Errorf("anonymous struct = %q", got)
	}
	if id, ok := moduleOf("github.com/vilanobeachflorida/racksail/modules/web/internal/x"); !ok || id != "web" {
		t.Errorf("moduleOf = %q, %v", id, ok)
	}
}

func TestFromError(t *testing.T) {
	def := i18n.Define("httpapitest.domain_taken", http.StatusConflict, "The domain {domain} is taken.")
	p := FromError(def.New("domain", "example.com"))
	if p.Status != http.StatusConflict || p.Code != "httpapitest.domain_taken" || p.Detail != "The domain example.com is taken." || p.Params["domain"] != "example.com" {
		t.Errorf("problem = %+v", p)
	}
	internal := FromError(errors.New("database is on fire"))
	if internal.Status != 500 || internal.Code != i18n.ErrInternal.Code || internal.Detail == "database is on fire" {
		t.Errorf("internal errors must not leak: %+v", internal)
	}
	fields := FromError(i18n.Invalid(i18n.Field("spec.domains[0]", i18n.ErrRequired)))
	if len(fields.Errors) != 1 || fields.Errors[0].Location != "spec.domains[0]" || fields.Errors[0].Code != i18n.ErrRequired.Code {
		t.Errorf("field problems = %+v", fields.Errors)
	}
	if p.ContentType("application/json") != "application/problem+json" {
		t.Error("problems must use application/problem+json")
	}
}

func TestNewErrorFromHuma(t *testing.T) {
	err := huma.NewError(http.StatusUnprocessableEntity, "validation failed", &huma.ErrorDetail{Location: "body.name", Message: "expected string", Value: 3})
	p, ok := err.(*Problem)
	if !ok {
		t.Fatalf("huma.NewError returned %T", err)
	}
	if p.Code != i18n.ErrInvalid.Code || len(p.Errors) != 1 || p.Errors[0].Location != "body.name" {
		t.Errorf("validation problem = %+v", p)
	}
	unexpected := NewError(http.StatusInternalServerError, "unexpected error occurred", errors.New("secret detail"))
	if up := unexpected.(*Problem); up.Detail != i18n.ErrInternal.Message || len(up.Errors) != 0 {
		t.Errorf("unexpected error leaked: %+v", up)
	}
	coded := NewError(http.StatusInternalServerError, "unexpected error occurred", i18n.ErrNotFound.New("what", "Site"))
	if cp := coded.(*Problem); cp.Status != http.StatusNotFound {
		t.Errorf("coded handler errors keep their status: %+v", cp)
	}
	bad := NewError(http.StatusBadRequest, "unable to parse body")
	if bp := bad.(*Problem); bp.Code != i18n.ErrBadRequest.Code || bp.Params["reason"] != "unable to parse body" {
		t.Errorf("bad request = %+v", bp)
	}
}
