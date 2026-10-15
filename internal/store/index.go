package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Helpers every Store shares, so they index, check and compare resources the
// same way.

func findIndex(info *KindInfo, name string) *Index {
	for i := range info.Indexes {
		if info.Indexes[i].Name == name {
			return &info.Indexes[i]
		}
	}
	return nil
}

// uniqueKey is one value a resource holds in a unique index.
type uniqueKey struct {
	kind, scope, index, value string
}

func (k uniqueKey) String() string {
	return strings.Join([]string{k.kind, k.scope, k.index, k.value}, "\x00")
}

// uniqueKeys lists the values r holds in the kind's unique indexes. A value
// repeated within one resource is a mistake in the spec.
func uniqueKeys(info *KindInfo, r *Resource) ([]uniqueKey, error) {
	var keys []uniqueKey
	for i := range info.Indexes {
		ix := &info.Indexes[i]
		if !ix.Unique {
			continue
		}
		values, err := indexValues(r.Spec, ix)
		if err != nil {
			return nil, err
		}
		scope := ""
		if ix.PerScope {
			scope = r.Scope.String()
		}
		seen := map[string]bool{}
		for _, v := range values {
			if seen[v] {
				return nil, i18n.Invalid(i18n.Field("spec."+ix.Path, i18n.ErrBadValue, "reason", fmt.Sprintf("%q appears more than once", v)))
			}
			seen[v] = true
			keys = append(keys, uniqueKey{kind: info.Name, scope: scope, index: ix.Name, value: v})
		}
	}
	return keys, nil
}

// uniqueConflict is the error for a unique value another resource holds.
func uniqueConflict(info *KindInfo, k uniqueKey) error {
	if ix := findIndex(info, k.index); ix != nil && ix.Conflict != nil {
		return ix.Conflict.New("value", k.value)
	}
	return ErrExists.New("what", fmt.Sprintf("%s with %s %s", info.Title, k.index, k.value))
}

// indexValues extracts the values of an indexed field from a spec.
func indexValues(spec json.RawMessage, ix *Index) ([]string, error) {
	var doc any
	if len(spec) > 0 {
		if err := json.Unmarshal(spec, &doc); err != nil {
			return nil, fmt.Errorf("decoding spec: %w", err)
		}
	}
	nodes := []any{doc}
	for _, part := range strings.Split(ix.Path, ".") {
		var next []any
		for _, n := range flatten(nodes) {
			if m, ok := n.(map[string]any); ok {
				if v, ok := m[part]; ok {
					next = append(next, v)
				}
			}
		}
		nodes = next
	}
	var out []string
	for _, n := range flatten(nodes) {
		var s string
		switch v := n.(type) {
		case string:
			s = v
		case float64:
			s = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			s = strconv.FormatBool(v)
		default:
			continue
		}
		if s == "" {
			continue
		}
		if ix.Fold {
			s = strings.ToLower(s)
		}
		out = append(out, s)
	}
	return out, nil
}

func flatten(nodes []any) []any {
	var out []any
	for _, n := range nodes {
		if arr, ok := n.([]any); ok {
			out = append(out, flatten(arr)...)
			continue
		}
		out = append(out, n)
	}
	return out
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func indexMatch(info *KindInfo, r *Resource, want map[string]string) bool {
	for name, value := range want {
		ix := findIndex(info, name)
		values, err := indexValues(r.Spec, ix)
		if err != nil {
			return false
		}
		if ix.Fold {
			value = strings.ToLower(value)
		}
		if !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// checkMeta checks the metadata of a resource being written.
func checkMeta(info *KindInfo, r *Resource) error {
	var fields []i18n.FieldError
	if r.Scope.Level != info.Scope {
		fields = append(fields, i18n.Field("scope.level", i18n.ErrBadValue, "reason", fmt.Sprintf("%s resources are owned at %s level", info.Title, info.Scope)))
	} else if err := r.Scope.Validate(); err != nil {
		fields = append(fields, i18n.Field("scope", i18n.ErrBadValue, "reason", err.Error()))
	}
	if err := ValidateLabels(r.Labels); err != nil {
		fields = append(fields, i18n.Field("labels", i18n.ErrBadValue, "reason", err.Error()))
	}
	if r.Server != "" && !r.Server.Valid() {
		fields = append(fields, i18n.Field("server", i18n.ErrBadValue, "reason", "not a valid id"))
	}
	if len(r.Spec) > 0 && !json.Valid(r.Spec) {
		fields = append(fields, i18n.Field("spec", i18n.ErrBadValue, "reason", "it isn't valid JSON"))
	}
	if len(r.Status) > 0 && !json.Valid(r.Status) {
		fields = append(fields, i18n.Field("status", i18n.ErrBadValue, "reason", "it isn't valid JSON"))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// checkNewID checks or assigns the ID of a resource being created.
func checkNewID(info *KindInfo, r *Resource) error {
	if r.ID == "" {
		r.ID = NewID(info.Prefix)
		return nil
	}
	if !r.ID.Valid() || r.ID.Prefix() != info.Prefix {
		return i18n.Invalid(i18n.Field("id", i18n.ErrBadValue, "reason", fmt.Sprintf("a %s id starts with %q", info.Title, info.Prefix+"_")))
	}
	return nil
}
