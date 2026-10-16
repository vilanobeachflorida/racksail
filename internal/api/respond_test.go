package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// bodySample has a field of every shape a body decoded with readJSON has.
type bodySample struct {
	ID       store.ID          `json:"id"`
	Scope    *store.Scope      `json:"scope"`
	Revision int64             `json:"revision"`
	Ratio    float64           `json:"ratio"`
	On       bool              `json:"on"`
	Labels   map[string]string `json:"labels"`
	Spec     json.RawMessage   `json:"spec"`
	Data     []byte            `json:"data"`
	Items    []struct {
		Name string `json:"name"`
	} `json:"items"`
	Groups map[string][]string `json:"groups"`
	Plain  string
}

// What the JSON decoder finds wrong with a body comes back as the schema
// validator's findings do: at the field's path, as the API writes paths,
// with the catalog's message, never the decoder's words.
func TestBodyErrorsSayWhatFieldsTake(t *testing.T) {
	for _, tc := range []struct {
		body           string
		status         int
		code           i18n.Code
		field, message string
	}{
		{`{"scope": {"level": 3}}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.scope.level", "This value isn't valid: use text."},
		{`{"id": 5}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.id", "This value isn't valid: use text."},
		{`{"revision": "7"}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.revision", "This value isn't valid: use a whole number."},
		{`{"revision": 1.5}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.revision", "This value isn't valid: use a whole number."},
		{`{"ratio": "half"}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.ratio", "This value isn't valid: use a number."},
		{`{"on": "yes"}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.on", "This value isn't valid: use true or false."},
		{`{"labels": ["web"]}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.labels", "This value isn't valid: use an object with fields."},
		{`{"labels": {"team": 1}}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.labels.team", "This value isn't valid: use text."},
		{`{"labels": {"app.example.com/name": 1}}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.labels.app.example.com/name", "This value isn't valid: use text."},
		{`{"labels": {"a~b": true}}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.labels.a~b", "This value isn't valid: use text."},
		{`{"groups": {"x/y.z": ["a", 1]}}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.groups.x/y.z[1]", "This value isn't valid: use text."},
		{`{"data": 12}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.data", "This value isn't valid: use base64-encoded data."},
		{`{"items": [{"name": "a"}, {"name": 2}]}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.items[1].name", "This value isn't valid: use text."},
		{`{"groups": {"web": [1]}}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.groups.web[0]", "This value isn't valid: use text."},
		{`{"scope": "account"}`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body.scope", "This value isn't valid: use an object with fields."},
		{`[]`, http.StatusUnprocessableEntity, i18n.ErrBadValue.Code, "body", "This value isn't valid: use an object with fields."},
		{`{"bogus": 1}`, http.StatusUnprocessableEntity, i18n.ErrUnknownName.Code, "body.bogus", "There is no field called bogus."},
		{`{"spec": {"anything": 1}, "scope": {"level": "account", "idd": "x"}}`, http.StatusUnprocessableEntity, i18n.ErrUnknownName.Code, "body.scope.idd", "There is no field called idd."},
		{`{"items": [{"name": "a"}, {"nmae": "b"}]}`, http.StatusUnprocessableEntity, i18n.ErrUnknownName.Code, "body.items[1].nmae", "There is no field called nmae."},
		// The decoder takes names whatever their case, and so does the
		// search for the one it refused.
		{`{"ID": "x", "plain": "y", "Scope": {"Level": "server", "zone": 1}}`, http.StatusUnprocessableEntity, i18n.ErrUnknownName.Code, "body.Scope.zone", "There is no field called zone."},
		{`{"spec": {"domains": ["a.test"]`, http.StatusBadRequest, i18n.ErrBadValue.Code, "body", "This value isn't valid: the body isn't valid JSON."},
		{`{"spec" {}}`, http.StatusBadRequest, i18n.ErrBadValue.Code, "body", "This value isn't valid: the body isn't valid JSON."},
	} {
		dec := json.NewDecoder(bytes.NewReader([]byte(tc.body)))
		dec.DisallowUnknownFields()
		var v bodySample
		err := dec.Decode(&v)
		if err == nil {
			t.Fatalf("%s decodes", tc.body)
		}
		e := i18n.From(bodyError(reflect.TypeOf(&v), []byte(tc.body), err))
		if e.Status != tc.status || len(e.Fields) != 1 {
			t.Errorf("%s = %+v", tc.body, e)
			continue
		}
		if f := e.Fields[0]; f.Path != tc.field || f.Code != tc.code || f.Message != tc.message {
			t.Errorf("%s: %+v, want %s at %s: %s", tc.body, f, tc.code, tc.field, tc.message)
		}
		text := e.Message
		for _, f := range e.Fields {
			text += " " + f.Message + " " + fmt.Sprint(f.Params["reason"])
		}
		for _, words := range []string{"expected", "json:", "store.", "int64", "map[", "unexpected EOF", "Go "} {
			if strings.Contains(text, words) {
				t.Errorf("%s: the decoder's words reach people: %s", tc.body, text)
			}
		}
	}
}
