package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

var (
	fuzzMine   = store.AccountScope("acct_01j9x3k4m5n6p7q8r9s0t1v2w3")
	fuzzTheirs = store.AccountScope("acct_01j9x3k4m5n6p7q8r9s0t1v2w4")
	fuzzGrants = NewGrants(hooks.Permissions{
		Hooks: []hooks.Kind{hooks.Modify},
		Read:  []hooks.DataScope{{Kind: "web.site", Fields: []string{"domains"}}},
		Write: []hooks.DataScope{{Kind: "web.site", Fields: []string{"cdn"}}},
	}, fuzzMine)
	// Sites already stored, one in the add-on's account with labels and
	// one in another account.
	fuzzStoredMine   = store.ID("site_01j9x3k4m5n6p7q8r9s0t1v2w5")
	fuzzStoredTheirs = store.ID("site_01j9x3k4m5n6p7q8r9s0t1v2w6")
)

func fuzzWork() *changeset.ChangeSet {
	return &changeset.ChangeSet{Changes: []changeset.ResourceChange{
		{Op: changeset.OpCreate, Kind: "web.site", ID: "site_01j9x3k4m5n6p7q8r9s0t1v2w3", Scope: fuzzMine, After: json.RawMessage(`{"domains":["mine.example"],"notes":"private","cdn":"off"}`), Source: changeset.RequestSource},
		{Op: changeset.OpCreate, Kind: "web.site", ID: "site_01j9x3k4m5n6p7q8r9s0t1v2w4", Scope: fuzzTheirs, After: json.RawMessage(`{"domains":["theirs.example"],"cdn":"off"}`), Source: changeset.RequestSource},
	}}
}

// fuzzStore holds the stored sites.
func fuzzStore(f *testing.F) *store.Memory {
	kinds := store.NewKindRegistry()
	if err := kinds.Register(siteKind); err != nil {
		f.Fatal(err)
	}
	st := store.NewMemory(kinds)
	ctx := context.Background()
	if err := st.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		if err := tx.Create(ctx, &store.Resource{
			Meta: store.Meta{ID: fuzzStoredMine, Kind: "web.site", Scope: fuzzMine, Labels: map[string]string{"racksail.dev/installation": "inst_real"}},
			Spec: json.RawMessage(`{"domains":["stored.example"],"notes":"stored"}`),
		}); err != nil {
			return err
		}
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: fuzzStoredTheirs, Kind: "web.site", Scope: fuzzTheirs}, Spec: json.RawMessage(`{"domains":["theirs.example"]}`)})
	}); err != nil {
		f.Fatal(err)
	}
	return st
}

// What an add-on answers to a modify hook comes from its own process. Its
// answer is taken only into the fields it may change, of the changes in
// its account its point is about, and what it adds stays of kinds it may
// change, in its account, whatever scope it claims: it creates sites with
// only the field it may write and no labels, and changes only that field
// of sites in its account, as the change set leaves them.
func FuzzMergeHookReply(f *testing.F) {
	st := fuzzStore(f)
	sent, _ := json.Marshal(fuzzGrants.FilterChangeSet(fuzzWork()))
	f.Add(sent)
	f.Add([]byte(`{"changes":[{"op":"create","kind":"web.site","after":{"domains":["x"],"cdn":"on","notes":"gone"}},{}]}`))
	f.Add([]byte(`{"changes":[{},{},{"op":"delete","kind":"core.addon","scope":{"level":"server"}}]}`))
	f.Add([]byte(`{"changes":[{},{},{"op":"delete","kind":"web.site","id":"site_01j9x3k4m5n6p7q8r9s0t1v2w6","scope":{"level":"account","id":"acct_01j9x3k4m5n6p7q8r9s0t1v2w3"}}]}`))
	f.Add([]byte(`{"changes":[{},{},{"op":"update","kind":"web.site","id":"site_01j9x3k4m5n6p7q8r9s0t1v2w5","labels":{}}]}`))
	f.Add([]byte(`{"changes":[{},{},{"op":"update","kind":"web.site","id":"site_01j9x3k4m5n6p7q8r9s0t1v2w5","after":{"domains":["stored.example"],"cdn":"on"}}]}`))
	f.Add([]byte(`{"changes":[{},{},{"op":"update","kind":"web.site","id":"site_01j9x3k4m5n6p7q8r9s0t1v2w3","after":{"cdn":"on"}},{"op":"create","kind":"web.site","id":"site_01j9x3k4m5n6p7q8r9s0t1v2w7","scope":{"level":"account","id":"acct_01j9x3k4m5n6p7q8r9s0t1v2w3"},"after":{"cdn":"on"}}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var reply changeset.ChangeSet
		if json.Unmarshal(data, &reply) != nil {
			return
		}
		work := fuzzWork()
		before := fuzzWork()
		if err := fuzzGrants.Merge(context.Background(), st, work, &reply, []int{0}); err != nil {
			return
		}
		if !bytes.Equal(work.Changes[1].After, before.Changes[1].After) {
			t.Fatalf("another account's change was rewritten: %s", work.Changes[1].After)
		}
		var mine map[string]any
		if err := json.Unmarshal(work.Changes[0].After, &mine); err != nil {
			t.Fatalf("its own change isn't JSON any more: %s", work.Changes[0].After)
		}
		if mine["notes"] != "private" {
			t.Fatalf("a field it may not change was: %s", work.Changes[0].After)
		}
		if d, ok := mine["domains"].([]any); !ok || len(d) != 1 || d[0] != "mine.example" {
			t.Fatalf("a field it may only read was changed: %s", work.Changes[0].After)
		}
		// What each site it may change was before the answer: the change
		// set's own site in its account, the stored one, and those it
		// created itself.
		was := map[store.ID]fuzzSite{
			before.Changes[0].ID: {spec: before.Changes[0].After},
			fuzzStoredMine:       {spec: json.RawMessage(`{"domains":["stored.example"],"notes":"stored"}`), labels: map[string]string{"racksail.dev/installation": "inst_real"}},
		}
		for _, added := range work.Changes[2:] {
			if added.Kind != "web.site" || added.Scope != fuzzMine {
				t.Fatalf("it added %s in %s", added.Kind, added.Scope)
			}
			switch added.Op {
			case changeset.OpCreate:
				if len(added.Labels) > 0 || !onlyCDN(added.After, nil) {
					t.Fatalf("it created a site with %s and labels %v", added.After, added.Labels)
				}
				if added.ID != "" {
					if _, taken := was[added.ID]; taken || added.ID == before.Changes[1].ID || added.ID == fuzzStoredTheirs {
						t.Fatalf("it created %s, which exists", added.ID)
					}
					was[added.ID] = fuzzSite{spec: added.After}
				}
			case changeset.OpUpdate:
				site, ok := was[added.ID]
				if !ok {
					t.Fatalf("it changed %s, which isn't a site of its account", added.ID)
				}
				if added.Labels != nil && !maps.Equal(added.Labels, site.labels) {
					t.Fatalf("it changed the labels of %s to %v", added.ID, added.Labels)
				}
				if added.After != nil && !onlyCDN(added.After, site.spec) {
					t.Fatalf("it changed %s from %s to %s", added.ID, site.spec, added.After)
				}
			default:
				t.Fatalf("it added a %s, which it may not", added.Op)
			}
		}
	})
}

// fuzzSite is a site an add-on may change, as it was.
type fuzzSite struct {
	spec   json.RawMessage
	labels map[string]string
}

// onlyCDN reports whether a spec differs from was, when there is one, in
// cdn alone, and otherwise holds nothing else.
func onlyCDN(spec, was json.RawMessage) bool {
	var now, then map[string]any
	if json.Unmarshal(spec, &now) != nil {
		return len(spec) == 0
	}
	if len(was) > 0 && json.Unmarshal(was, &then) != nil {
		return false
	}
	for k, v := range now {
		if k == "cdn" {
			continue
		}
		if old, ok := then[k]; !ok || !reflect.DeepEqual(old, v) {
			return false
		}
	}
	for k := range then {
		if _, ok := now[k]; !ok && k != "cdn" {
			return false
		}
	}
	return true
}

// Answers of the panel's API pass through the gateway on their way to the
// add-on, which leaves only what it may read, whatever they hold.
func FuzzFilterBody(f *testing.F) {
	f.Add([]byte(`{"items":[{"id":"site_1","kind":"web.site","scope":{"level":"account","id":"acct_01j9x3k4m5n6p7q8r9s0t1v2w3"},"spec":{"domains":["a.example"],"notes":"private"}}]}`))
	f.Add([]byte(`[{"kind":"core.addon","spec":{"id":"x"}},1,"a",null]`))
	f.Add([]byte(`{"items":[{"id":"site_1","kind":"web.site","labels":{"team":"red"},"status":{"phase":"ready","message":"private"}},{"id":"zone_1","kind":"dns.zone","status":{"serial":7}}]}`))
	f.Add([]byte(`{"last":{"op":"update","kind":"web.site","id":"site_1","source":{"type":"request"},"before":{"notes":"private"},"labels":{"team":"red"},"diff":[{"path":"notes"}]}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		out := filterBody(data, fuzzGrants, "web.site")
		if json.Valid(data) && (out == nil || !json.Valid(out)) {
			t.Fatalf("filtering %s gave %s", data, out)
		}
		var v any
		if json.Unmarshal(out, &v) == nil {
			if what := unreadable(v); what != "" {
				t.Fatalf("filtering %s kept %s: %s", data, what, out)
			}
		}
	})
}

// unreadable finds what fuzzGrants don't let the add-on see in a filtered
// answer. Of a resource, an object with a spec or one that names its kind
// and its ID, as one cut down to some fields does: fields beyond domains
// and cdn, a status beyond its phase, or labels, and of a resource of
// another kind, any of them. Of a change, one that also says its operation
// and source: settings beyond those fields, of another kind any, and its
// labels, input, output, steps and checks.
func unreadable(v any) string {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if what := unreadable(e); what != "" {
				return what
			}
		}
	case map[string]any:
		kind, named := x["kind"].(string)
		_, hasSpec := x["spec"]
		_, hasID := x["id"].(string)
		_, hasOp := x["op"].(string)
		_, hasSource := x["source"]
		if !named {
			kind = "web.site"
		}
		switch {
		case named && hasID && hasOp && hasSource && !hasSpec:
			for _, key := range []string{"labels", "labelsBefore", "input", "output", "steps", "checks"} {
				if _, ok := x[key]; ok {
					return "the " + key + " of a change"
				}
			}
			for _, key := range []string{"before", "after"} {
				doc, present := x[key]
				if !present || doc == nil {
					continue
				}
				m, ok := doc.(map[string]any)
				if kind != "web.site" || !ok {
					return "the settings of a change to a " + kind
				}
				for k := range m {
					if k != "domains" && k != "cdn" {
						return "the settings' " + k + " in a change"
					}
				}
			}
			if diff, ok := x["diff"].([]any); ok {
				for _, d := range diff {
					entry, _ := d.(map[string]any)
					path, _ := entry["path"].(string)
					if kind != "web.site" || !pathAllowed(path, []string{"domains", "cdn"}) {
						return "the change of " + path
					}
				}
			} else if _, ok := x["diff"]; ok {
				return "a diff that isn't a list"
			}
			return ""
		case hasSpec || (named && hasID):
			if kind != "web.site" {
				for _, key := range []string{"spec", "status", "labels"} {
					if _, ok := x[key]; ok {
						return "the " + key + " of a " + kind
					}
				}
			}
			if spec, ok := x["spec"]; ok {
				if m, ok := spec.(map[string]any); ok {
					for k := range m {
						if k != "domains" && k != "cdn" {
							return "the spec's " + k
						}
					}
				} else if spec != nil {
					return "a spec that isn't an object"
				}
			}
			if st, ok := x["status"].(map[string]any); ok {
				for k := range st {
					if k != "phase" {
						return "the status's " + k
					}
				}
			} else if _, ok := x["status"]; ok {
				return "a status that isn't an object"
			}
			if _, ok := x["labels"]; ok {
				return "labels"
			}
		}
		if _, ok := x["changes"].([]any); ok {
			if _, ok := x["effects"]; ok {
				// Change sets are filtered by FilterChangeSet, which
				// FuzzMerge and the tests cover.
				return ""
			}
		}
		for k, child := range x {
			if k == "spec" {
				continue
			}
			if what := unreadable(child); what != "" {
				return what
			}
		}
	}
	return ""
}
