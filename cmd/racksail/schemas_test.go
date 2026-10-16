package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// Every field of every schema in the API document says what it is: the
// core's, the modules', the extension system's and the catalog's, and those
// of what operations answer with as much as of what they take. The API
// reference, the racksail command's help, the panel's forms and the
// assistant's tools are made of these words, so one that is missing leaves
// a bare field name there.
func TestEverySchemaFieldIsDescribed(t *testing.T) {
	out, err := run(t, "openapi")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if n := len(doc.Components.Schemas); n < 100 {
		t.Fatalf("only %d schemas in the document", n)
	}
	var missing []string
	for name, schema := range doc.Components.Schemas {
		for field, p := range schema.Properties {
			if field != "$schema" && strings.TrimSpace(p.Description) == "" {
				missing = append(missing, name+"."+field)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d fields of the API document's schemas have no description; give each a doc tag:\n%s", len(missing), strings.Join(missing, "\n"))
	}
}
