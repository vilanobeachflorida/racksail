package main

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/modules"
)

// A name a kind's namespace could have.
var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// The kinds add-ons add keep out of what the panel's own operations are
// called and of the modules' ids, whose kinds' namespaces they are: every
// path of the API starts with one of them, which no add-on's kinds may
// share.
func TestAddonKindsKeepOutOfThePanelsNames(t *testing.T) {
	out, err := run(t, "openapi")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	ids := modules.Default().IDs()
	if len(ids) == 0 {
		t.Fatal("no modules are registered")
	}
	for path := range doc.Paths {
		rest, ok := strings.CutPrefix(path, "/api/v1/")
		if !ok {
			continue
		}
		first, _, _ := strings.Cut(rest, "/")
		if !namespacePattern.MatchString(first) {
			// No kind can be called that, such as openapi.json.
			continue
		}
		if !slices.Contains(hooks.ReservedNamespaces, first) && !slices.Contains(ids, first) {
			t.Errorf("an add-on's kinds could be called %s.*, where %s is", first, path)
		}
	}
	for _, id := range ids {
		if slices.Contains(hooks.ReservedNamespaces, id) {
			t.Errorf("the module %s has an id the panel keeps for itself", id)
		}
	}
}
