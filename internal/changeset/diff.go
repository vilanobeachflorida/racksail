package changeset

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
)

var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Diff compares two JSON documents field by field. Paths use dots for
// object keys and brackets for array indexes, such as domains[0] or
// labels["racksail.dev/owner"]. Arrays are compared by position.
func Diff(before, after json.RawMessage) ([]FieldDiff, error) {
	var a, b any
	if len(before) > 0 {
		if err := json.Unmarshal(before, &a); err != nil {
			return nil, fmt.Errorf("decoding the old value: %w", err)
		}
	}
	if len(after) > 0 {
		if err := json.Unmarshal(after, &b); err != nil {
			return nil, fmt.Errorf("decoding the new value: %w", err)
		}
	}
	// A new or removed document is compared against an empty one, so every
	// field shows up on its own.
	if _, ok := b.(map[string]any); ok && a == nil {
		a = map[string]any{}
	}
	if _, ok := a.(map[string]any); ok && b == nil {
		b = map[string]any{}
	}
	var out []FieldDiff
	diffValue("", a, b, &out)
	return out, nil
}

func diffValue(path string, a, b any, out *[]FieldDiff) {
	switch {
	case a == nil && b == nil:
		return
	case a == nil:
		*out = append(*out, FieldDiff{Path: path, Op: DiffAdd, After: raw(b)})
		return
	case b == nil:
		*out = append(*out, FieldDiff{Path: path, Op: DiffRemove, Before: raw(a)})
		return
	}
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		keys := make([]string, 0, len(am)+len(bm))
		for k := range am {
			keys = append(keys, k)
		}
		for k := range bm {
			if _, ok := am[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			diffValue(join(path, k), am[k], bm[k], out)
		}
		return
	}
	as, aIsSlice := a.([]any)
	bs, bIsSlice := b.([]any)
	if aIsSlice && bIsSlice {
		for i := 0; i < max(len(as), len(bs)); i++ {
			var x, y any
			if i < len(as) {
				x = as[i]
			}
			if i < len(bs) {
				y = bs[i]
			}
			diffValue(path+"["+strconv.Itoa(i)+"]", x, y, out)
		}
		return
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, FieldDiff{Path: path, Op: DiffReplace, Before: raw(a), After: raw(b)})
	}
}

func join(path, key string) string {
	if !identPattern.MatchString(key) {
		return path + "[" + strconv.Quote(key) + "]"
	}
	if path == "" {
		return key
	}
	return path + "." + key
}

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// Redact clears the values of diffs whose path is one of the sensitive
// fields, or lies below one.
func Redact(diffs []FieldDiff, sensitive []string) []FieldDiff {
	for i := range diffs {
		for _, s := range sensitive {
			p := diffs[i].Path
			if p == s || (len(p) > len(s) && p[:len(s)] == s && slices.Contains([]byte{'.', '['}, p[len(s)])) {
				diffs[i].Before, diffs[i].After, diffs[i].Sensitive = nil, nil, true
			}
		}
	}
	return diffs
}
