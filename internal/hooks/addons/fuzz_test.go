package addons

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

var (
	fuzzMine   = store.AccountScope("acct_01j9x3k4m5n6p7q8r9s0t1u2v3")
	fuzzTheirs = store.AccountScope("acct_01j9x3k4m5n6p7q8r9s0t1u2v4")
	fuzzGrants = NewGrants(hooks.Permissions{
		Hooks: []hooks.Kind{hooks.Modify},
		Read:  []hooks.DataScope{{Kind: "web.site", Fields: []string{"domains"}}},
		Write: []hooks.DataScope{{Kind: "web.site", Fields: []string{"cdn"}}},
	}, fuzzMine)
)

func fuzzWork() *changeset.ChangeSet {
	return &changeset.ChangeSet{Changes: []changeset.ResourceChange{
		{Op: changeset.OpCreate, Kind: "web.site", ID: "site_01j9x3k4m5n6p7q8r9s0t1u2v3", Scope: fuzzMine, After: json.RawMessage(`{"domains":["mine.example"],"notes":"private","cdn":"off"}`)},
		{Op: changeset.OpCreate, Kind: "web.site", ID: "site_01j9x3k4m5n6p7q8r9s0t1u2v4", Scope: fuzzTheirs, After: json.RawMessage(`{"domains":["theirs.example"],"cdn":"off"}`)},
	}}
}

// What an add-on answers to a modify hook comes from its own process. Its
// answer is taken only into the fields it may change, of the changes in
// its account its point is about, and what it adds stays of kinds it may
// change, in its account.
func FuzzMergeHookReply(f *testing.F) {
	sent, _ := json.Marshal(fuzzGrants.FilterChangeSet(fuzzWork()))
	f.Add(sent)
	f.Add([]byte(`{"changes":[{"op":"create","kind":"web.site","after":{"domains":["x"],"cdn":"on","notes":"gone"}},{}]}`))
	f.Add([]byte(`{"changes":[{},{},{"op":"delete","kind":"core.addon","scope":{"level":"server"}}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var reply changeset.ChangeSet
		if json.Unmarshal(data, &reply) != nil {
			return
		}
		work := fuzzWork()
		before := fuzzWork()
		if err := fuzzGrants.Merge(work, &reply, []int{0}); err != nil {
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
		for _, added := range work.Changes[2:] {
			if added.Kind != "web.site" || added.Scope != fuzzMine {
				t.Fatalf("it added %s in %s", added.Kind, added.Scope)
			}
		}
	})
}

// Answers of the panel's API pass through the gateway on their way to the
// add-on, which leaves only what it may read, whatever they hold.
func FuzzFilterBody(f *testing.F) {
	f.Add([]byte(`{"items":[{"id":"site_1","kind":"web.site","scope":{"level":"account","id":"acct_01j9x3k4m5n6p7q8r9s0t1u2v3"},"spec":{"domains":["a.example"],"notes":"private"}}]}`))
	f.Add([]byte(`[{"kind":"core.addon","spec":{"id":"x"}},1,"a",null]`))
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
// answer: of a resource with a spec, fields beyond domains and cdn, a
// status beyond its phase, or labels.
func unreadable(v any) string {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if what := unreadable(e); what != "" {
				return what
			}
		}
	case map[string]any:
		if spec, ok := x["spec"]; ok {
			if kind, named := x["kind"].(string); named && kind != "web.site" {
				return "the spec of a " + kind
			}
			if m, ok := spec.(map[string]any); ok {
				for k := range m {
					if k != "domains" && k != "cdn" {
						return "the spec's " + k
					}
				}
			} else if spec != nil {
				return "a spec that isn't an object"
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
