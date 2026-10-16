package api

import (
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// corePackages are the parts of Racksail whose types the core's own
// operations take and answer with. The schemas of other parts' types, such
// as modules' and the catalog's, only come with the whole binary, whose
// TestEverySchemaFieldIsDescribed holds them to the same.
var corePackages = []string{
	"internal/api", "internal/api/httpapi", "internal/auth", "internal/auth/account", "internal/auth/identity",
	"internal/changeset", "internal/events", "internal/events/webhook", "internal/jobs", "internal/store", "internal/system",
}

// Every field of the schemas of the core's operations has a description:
// the same text documents the API, labels the racksail command's help and
// describes the assistant's tools, which otherwise show bare field names.
// That includes the schemas the API builds itself, those of every kind's
// resources and requests.
func TestCoreSchemasDescribeEveryField(t *testing.T) {
	reg := New(Options{}).api.OpenAPI().Components.Schemas
	core := func(typ reflect.Type) bool {
		if typ == nil || typ.PkgPath() == "" {
			// Built by the API, or an operation's own body.
			return true
		}
		return slices.ContainsFunc(corePackages, func(p string) bool {
			return strings.HasSuffix(typ.PkgPath(), "/racksail/"+p)
		})
	}
	var missing []string
	for name, schema := range reg.Map() {
		if !core(reg.TypeFromRef("#/components/schemas/" + name)) {
			continue
		}
		for prop, p := range schema.Properties {
			if p.Description == "" && prop != "$schema" {
				missing = append(missing, name+"."+prop)
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("%s has no description", m)
	}
}
