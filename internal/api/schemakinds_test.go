package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// schemaNoteKind is a kind described by JSON Schema, as an add-on's are.
type schemaNoteKind struct{ info store.KindInfo }

func (k *schemaNoteKind) Info() *store.KindInfo                                { return &k.info }
func (k *schemaNoteKind) SpecType() reflect.Type                               { return reflect.TypeFor[map[string]any]() }
func (k *schemaNoteKind) StatusType() reflect.Type                             { return reflect.TypeFor[store.Status]() }
func (k *schemaNoteKind) Default(ctx context.Context, r *store.Resource) error { return nil }
func (k *schemaNoteKind) Err() error                                           { return nil }
func (k *schemaNoteKind) SpecSchema() json.RawMessage                          { return json.RawMessage(schemaNoteSpec) }
func (k *schemaNoteKind) StatusSchema() json.RawMessage                        { return json.RawMessage(`{"type":"object"}`) }
func (k *schemaNoteKind) InputSchema(action string) json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (k *schemaNoteKind) Validate(ctx context.Context, r, old *store.Resource) error {
	var spec map[string]any
	if json.Unmarshal(r.Spec, &spec) != nil {
		return i18n.Invalid(i18n.Field("spec", i18n.ErrBadValue, "reason", "not an object"))
	}
	if _, ok := spec["text"].(string); !ok {
		return i18n.Invalid(i18n.Field("spec.text", i18n.ErrRequired))
	}
	return nil
}

const schemaNoteSpec = `{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}`

// Kinds described by JSON Schema, which come and go while the panel runs,
// are served at paths that name them, with the operations every kind has.
func TestSchemaKindOperations(t *testing.T) {
	s := newStack(t, RateLimit{})
	note := &schemaNoteKind{info: store.KindInfo{
		Name: "notes.note", Prefix: "note", Scope: store.ScopeServer, Title: "Note", Plural: "notes", Description: "A note an add-on keeps.",
		Risks:   store.Risks{Create: store.RiskLow, Update: store.RiskLow, Delete: store.RiskHigh},
		Actions: []store.Action{{Name: "pin", Title: "Pin", Summary: "Pin the note", Risk: store.RiskLow, Input: map[string]any{}}},
	}}
	// It arrives after the server was built, as an add-on's does.
	if err := s.kinds.Register(note); err != nil {
		t.Fatal(err)
	}
	base := SchemaKindPath("notes.note")
	var described KindDescription
	if resp := s.call("admin", "GET", "/api/v1/kinds/notes.note", nil, &described); resp.StatusCode != http.StatusOK || described.Path != base || string(described.Spec) != schemaNoteSpec || described.Inputs["pin"] == nil {
		t.Fatalf("the kind = %d %+v", resp.StatusCode, described)
	}
	var kinds struct {
		Kinds []KindDescription `json:"kinds"`
	}
	s.call("admin", "GET", "/api/v1/kinds", nil, &kinds)
	if !slices.ContainsFunc(kinds.Kinds, func(k KindDescription) bool { return k.Name == "notes.note" && k.Path == base }) {
		t.Errorf("the kinds list it with its path: %+v", kinds.Kinds)
	}

	var preview changeset.ChangeSet
	if resp := s.call("admin", "POST", base+"?dryRun=true", map[string]any{"spec": map[string]any{"text": "hi"}}, &preview); resp.StatusCode != http.StatusOK || !preview.DryRun || len(preview.Changes) != 1 {
		t.Fatalf("a preview = %d %+v", resp.StatusCode, preview)
	}
	if resp := s.call("admin", "POST", base, map[string]any{"spec": map[string]any{}}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("settings the kind refuses = %d", resp.StatusCode)
	}
	var created changeset.ChangeSet
	if resp := s.call("admin", "POST", base, map[string]any{"spec": map[string]any{"text": "hi"}}, &created); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("creating = %d", resp.StatusCode)
	}
	if cs := s.finish("admin", &created); cs.State != changeset.StateApplied {
		t.Fatalf("created = %+v", cs)
	}
	id := string(created.Changes[0].ID)
	var page struct {
		Items []store.Resource `json:"items"`
	}
	if resp := s.call("admin", "GET", base, nil, &page); resp.StatusCode != http.StatusOK || len(page.Items) != 1 || string(page.Items[0].ID) != id {
		t.Fatalf("the list = %d %+v", resp.StatusCode, page)
	}
	var got store.Resource
	if resp := s.call("admin", "GET", base+"/"+id, nil, &got); resp.StatusCode != http.StatusOK || !strings.Contains(string(got.Spec), "hi") {
		t.Errorf("get = %d %+v", resp.StatusCode, got)
	}
	var updated changeset.ChangeSet
	if resp := s.call("admin", "PUT", base+"/"+id, map[string]any{"spec": map[string]any{"text": "hello"}}, &updated); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("update = %d", resp.StatusCode)
	}
	s.finish("admin", &updated)
	var history struct {
		Versions []store.Version `json:"versions"`
	}
	if resp := s.call("admin", "GET", base+"/"+id+"/history", nil, &history); resp.StatusCode != http.StatusOK || len(history.Versions) != 2 {
		t.Errorf("history = %d %+v", resp.StatusCode, history)
	}
	if resp := s.call("admin", "POST", base+"/"+id+"/actions/pin?dryRun=true", map[string]any{}, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("an action = %d", resp.StatusCode)
	}
	if resp := s.call("admin", "POST", base+"/"+id+"/actions/shred?dryRun=true", map[string]any{}, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an action the kind doesn't have = %d", resp.StatusCode)
	}

	// Its own permissions decide, as for every kind.
	if resp := s.call("nobody", "GET", base, nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("listing without a grant = %d", resp.StatusCode)
	}
	if resp := s.call("nobody", "GET", base+"/"+id, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("getting without a grant = %d", resp.StatusCode)
	}
	if resp := s.call("nobody", "DELETE", base+"/"+id+"?dryRun=true", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("deleting without a grant = %d", resp.StatusCode)
	}

	// Other kinds are served where their own operations are.
	if resp := s.call("admin", "GET", SchemaKindPath("test.site"), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a kind with operations of its own here = %d", resp.StatusCode)
	}
	if resp := s.call("admin", "GET", SchemaKindPath("nothing.here"), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a kind that doesn't exist = %d", resp.StatusCode)
	}

	// Once the kind is gone, so are its operations.
	var deleted changeset.ChangeSet
	if resp := s.call("admin", "DELETE", base+"/"+id, nil, &deleted); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	s.finish("admin", &deleted)
	s.kinds.Unregister("notes.note")
	if resp := s.call("admin", "GET", base, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a kind that went = %d", resp.StatusCode)
	}
}
