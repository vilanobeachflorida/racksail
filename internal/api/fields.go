package api

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Top-level fields of a resource, which field selection checks names
// against. Deeper paths into specs, statuses and labels aren't checked: a
// path to a field a resource doesn't have selects nothing.
var resourceFields = []string{"id", "kind", "scope", "server", "labels", "generation", "revision", "createdAt", "updatedAt", "deletedAt", "spec", "status"}

var fieldPathPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\.[A-Za-z0-9_-]+)*$`)

// fieldSelection is the parsed fields parameter: dotted paths such as
// spec.domains or status.phase.
type fieldSelection [][]string

func parseFields(p params) (fieldSelection, error) {
	var out fieldSelection
	for _, f := range p.list("fields") {
		if !fieldPathPattern.MatchString(f) {
			return nil, badParam("fields", "list fields as dotted paths such as spec.domains or status.phase, separated by commas")
		}
		path := strings.Split(f, ".")
		if !slices.Contains(resourceFields, path[0]) {
			return nil, badParam("fields", path[0]+" isn't a field of resources; use "+strings.Join(resourceFields, ", "))
		}
		out = append(out, path)
	}
	return out, nil
}

// apply cuts a resource down to the selected fields; its id and kind always
// stay. Without a selection it returns the resource as it is.
func (sel fieldSelection) apply(r *store.Resource) (any, error) {
	if len(sel) == 0 {
		return r, nil
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, err
	}
	out := map[string]any{"id": full["id"], "kind": full["kind"]}
	for _, path := range sel {
		copyPath(full, out, path)
	}
	return out, nil
}

// copyPath copies the value at path from src to dst, creating the objects
// on the way.
func copyPath(src, dst map[string]any, path []string) {
	v, ok := src[path[0]]
	if !ok {
		return
	}
	if len(path) == 1 {
		dst[path[0]] = v
		return
	}
	child, ok := v.(map[string]any)
	if !ok {
		return
	}
	next, ok := dst[path[0]].(map[string]any)
	if !ok {
		next = map[string]any{}
		dst[path[0]] = next
	}
	copyPath(child, next, path[1:])
}
