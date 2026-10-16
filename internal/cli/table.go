package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// maxCell is how wide a table cell gets before it's cut short.
const maxCell = 48

// column is one column of a list: where its values are in each item, its
// heading and how its values are written.
type column struct {
	path   []string
	header string
	format string
}

// printTable writes an answer for people: lists as tables, one object as
// its fields, with the lists inside it as tables of their own.
func printTable(w io.Writer, v any, kind *store.KindInfo) error {
	switch t := v.(type) {
	case *object:
		if items, next, ok := pageOf(t); ok {
			listTable(w, items, kind)
			if next != "" {
				fmt.Fprintf(w, "\nThere's more: add --cursor %s for the next page, or --all for every page.\n", next)
			}
			return nil
		}
		detailTable(w, t, kind)
	case []any:
		if len(t) > 0 && allObjects(t) {
			listTable(w, t, kind)
			return nil
		}
		for _, item := range t {
			fmt.Fprintln(w, cell(item, ""))
		}
	case nil:
	default:
		fmt.Fprintln(w, cell(t, ""))
	}
	return nil
}

// pageOf recognises a page of a list: one list of objects, and the cursor
// of the next page.
func pageOf(o *object) ([]any, string, bool) {
	var items []any
	found := false
	next := ""
	for _, k := range o.keys {
		switch v := o.values[k].(type) {
		case []any:
			if found || !allObjects(v) {
				return nil, "", false
			}
			items, found = v, true
		case nil:
			if k != "next" {
				// A list the panel sent as null.
				if found {
					return nil, "", false
				}
				found = true
			}
		case string:
			if k != "next" {
				return nil, "", false
			}
			next = v
		default:
			return nil, "", false
		}
	}
	return items, next, found
}

// isLog recognises the lines of a job's log, which read best as lines.
func isLog(items []any) bool {
	first, ok := items[0].(*object)
	return ok && first.get("seq") != nil && first.get("level") != nil && first.get("message") != nil
}

func allObjects(list []any) bool {
	for _, item := range list {
		if _, ok := item.(*object); !ok {
			return false
		}
	}
	return true
}

// listTable writes a list of objects as a table, and a log as its lines.
func listTable(w io.Writer, items []any, kind *store.KindInfo) {
	if len(items) == 0 {
		fmt.Fprintln(w, "Nothing to show.")
		return
	}
	if isLog(items) {
		for _, item := range items {
			raw, _ := json.Marshal(item)
			var l logLine
			if json.Unmarshal(raw, &l) == nil {
				fmt.Fprintln(w, l)
			}
		}
		return
	}
	cols := kindColumns(items, kind)
	if cols == nil {
		cols = columnsOf(items)
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.header
	}
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, item := range items {
		o, _ := item.(*object)
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = clip(cell(lookup(o, c.path), c.format))
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	tw.Flush()
}

// kindColumns are the columns of a list of a kind's resources: the ID,
// the title and the columns the kind's list screen shows.
func kindColumns(items []any, kind *store.KindInfo) []column {
	first, _ := items[0].(*object)
	if kind == nil || first == nil || first.get("spec") == nil {
		return nil
	}
	cols := []column{{path: []string{"id"}, header: "ID"}}
	seen := map[string]bool{"id": true}
	add := func(path, format string) {
		if seen[path] {
			return
		}
		seen[path] = true
		parts := strings.Split(path, ".")
		cols = append(cols, column{path: parts, header: heading(parts[len(parts)-1]), format: format})
	}
	if kind.UI.TitleField != "" {
		add("spec."+kind.UI.TitleField, "")
	}
	for _, c := range kind.UI.Columns {
		add(c.Field, c.Format)
	}
	if len(cols) == 1 || !seen["status.phase"] {
		add("status.phase", "status")
	}
	return cols
}

// preferred columns come first, in this order; stamps go last.
var (
	preferred = []string{"id", "name", "username", "title", "slug", "type", "kind", "state", "status", "phase"}
	stamps    = []string{"time", "createdAt", "updatedAt", "lastUsedAt", "expiresAt", "finishedAt"}
)

// columnsOf picks the columns of a list of objects other than resources:
// the fields with short values, the most telling first.
func columnsOf(items []any) []column {
	var order []string
	seen := map[string]bool{}
	expand := map[string][]string{}
	sample := items[:min(len(items), 50)]
	for _, item := range sample {
		o := item.(*object)
		for _, k := range o.keys {
			if seen[k] {
				continue
			}
			seen[k] = true
			order = append(order, k)
			if sub, ok := o.values[k].(*object); ok && flatMap(sub) {
				expand[k] = sub.keys
			}
		}
	}
	var picked []column
	var tail []column
	for _, k := range order {
		if !useful(sample, k) {
			continue
		}
		if subs, ok := expand[k]; ok {
			for _, s := range subs {
				picked = append(picked, column{path: []string{k, s}, header: heading(s), format: formatFor(s)})
			}
			continue
		}
		c := column{path: []string{k}, header: heading(k), format: formatFor(k)}
		if slices.Contains(stamps, k) {
			tail = append(tail, c)
			continue
		}
		picked = append(picked, c)
	}
	slices.SortStableFunc(picked, func(a, b column) int { return rank(a.path[0]) - rank(b.path[0]) })
	if len(tail) > 2 {
		tail = tail[:2]
	}
	limit := max(8-len(tail), 3)
	if len(picked) > limit {
		picked = picked[:limit]
	}
	return append(picked, tail...)
}

func rank(key string) int {
	if i := slices.Index(preferred, key); i >= 0 {
		return i
	}
	return len(preferred)
}

// useful reports whether a field has short values worth a column: not
// lists of objects, and not empty in every item.
func useful(items []any, key string) bool {
	filled := false
	for _, item := range items {
		switch v := item.(*object).get(key).(type) {
		case nil:
		case []any:
			if len(v) > 0 && allObjects(v) {
				return false
			}
			filled = filled || len(v) > 0
		case string:
			if utf8.RuneCountInString(v) > 120 {
				return false
			}
			filled = filled || v != ""
		default:
			filled = true
		}
	}
	return filled
}

// flatMap recognises objects worth a column per field: maps of a few short
// values, like the reach of each role, rather than things a cell names.
func flatMap(o *object) bool {
	if len(o.keys) < 2 || len(o.keys) > 8 || special(o) {
		return false
	}
	for _, k := range o.keys {
		switch o.values[k].(type) {
		case *object, []any:
			return false
		}
	}
	return true
}

// special recognises objects a single cell can say: owners, progress,
// failures and things with names.
func special(o *object) bool {
	for _, k := range []string{"level", "done", "message", "name", "username", "title"} {
		if o.get(k) != nil {
			return true
		}
	}
	return false
}

// whole recognises objects one cell says all of: owners and progress.
func whole(o *object) bool {
	allowed := map[string]bool{"level": true, "id": true}
	if o.get("level") == nil {
		allowed = map[string]bool{"done": true, "total": true, "message": true}
		if o.get("done") == nil {
			return false
		}
	}
	for _, k := range o.keys {
		if !allowed[k] {
			return false
		}
	}
	return true
}

// detailTable writes one object: its short fields as name and value, then
// each list of objects inside it as a table of its own.
func detailTable(w io.Writer, o *object, kind *store.KindInfo) {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	var lists []string
	var rows func(prefix string, o *object, depth int)
	rows = func(prefix string, o *object, depth int) {
		for _, k := range o.keys {
			v := o.values[k]
			name := prefix + k
			switch t := v.(type) {
			case nil:
			case *object:
				if depth < 3 && !whole(t) && len(t.keys) > 0 {
					rows(name+".", t, depth+1)
					continue
				}
				if text := cell(t, ""); text != "" {
					fmt.Fprintf(tw, "%s:\t%s\n", name, text)
				}
			case []any:
				if len(t) > 0 && allObjects(t) {
					if depth == 0 {
						lists = append(lists, k)
						continue
					}
					fmt.Fprintf(tw, "%s:\t%d items\n", name, len(t))
					continue
				}
				if text := cell(t, ""); text != "" {
					fmt.Fprintf(tw, "%s:\t%s\n", name, text)
				}
			default:
				if text := cell(t, formatFor(k)); text != "" {
					fmt.Fprintf(tw, "%s:\t%s\n", name, strings.ReplaceAll(text, "\n", " "))
				}
			}
		}
	}
	rows("", o, 0)
	tw.Flush()
	for _, k := range lists {
		fmt.Fprintf(w, "\n%s:\n", heading(k))
		listTable(w, o.values[k].([]any), kind)
	}
}

func formatFor(key string) string {
	if strings.HasSuffix(key, "At") || key == "time" {
		return "date"
	}
	if strings.HasSuffix(key, "Bytes") {
		return "bytes"
	}
	return ""
}

func lookup(o *object, path []string) any {
	var v any = o
	for _, p := range path {
		obj, ok := v.(*object)
		if !ok {
			return nil
		}
		v = obj.get(p)
	}
	return v
}

// cell writes one value in a few words.
func cell(v any, format string) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		if format == "date" || looksLikeTime(t) {
			if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
				return ts.Local().Format("2006-01-02 15:04")
			}
		}
		return t
	case json.Number:
		if format == "bytes" {
			if n, err := t.Int64(); err == nil {
				return humanBytes(n)
			}
		}
		return t.String()
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, cell(item, ""))
		}
		return strings.Join(parts, ", ")
	case *object:
		return objectCell(t)
	}
	return fmt.Sprint(v)
}

// objectCell says what an object is in a few words: an owner as
// account:ID, progress as 3/7, a failure by its message, anything else by
// its name, or as JSON.
func objectCell(o *object) string {
	if level, ok := o.get("level").(string); ok {
		if id, _ := o.get("id").(string); id != "" {
			return level + ":" + id
		}
		return level
	}
	if done, ok := o.get("done").(json.Number); ok {
		if total, _ := o.get("total").(json.Number); total != "" && total.String() != "0" {
			return done.String() + "/" + total.String()
		}
		return done.String()
	}
	for _, k := range []string{"message", "name", "username", "title", "id"} {
		if text, ok := o.get(k).(string); ok && text != "" {
			return text
		}
	}
	raw, _ := json.Marshal(o)
	return string(raw)
}

func looksLikeTime(s string) bool {
	return len(s) >= 20 && len(s) <= 40 && s[4] == '-' && s[7] == '-' && s[10] == 'T'
}

func clip(s string) string {
	s = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
	if utf8.RuneCountInString(s) <= maxCell {
		return s
	}
	r := []rune(s)
	return string(r[:maxCell-1]) + "…"
}

// heading turns a field's name into a column heading: createdAt becomes
// CREATED AT.
func heading(key string) string {
	return strings.ToUpper(strings.ReplaceAll(kebab(key), "-", " "))
}

// humanBytes writes a number of bytes the way people read sizes.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	text := strconv.FormatFloat(value, 'f', 1, 64)
	text = strings.TrimSuffix(text, ".0")
	return text + " " + string("KMGTP"[exp]) + "iB"
}
