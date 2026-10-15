package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Fields lists can be sorted by, besides the kinds' indexes.
const (
	SortID         = "id"
	SortCreatedAt  = "createdAt"
	SortUpdatedAt  = "updatedAt"
	SortGeneration = "generation"
	SortPhase      = "phase"
)

// Search entries for a resource's title and subtitle live next to its index
// values under these names, which no index can have.
const (
	titleEntry    = "@title"
	subtitleEntry = "@subtitle"
)

// sortSpec is a parsed Query.Sort.
type sortSpec struct {
	field string
	desc  bool
	index *Index
}

// parseSort reads a sort expression for a kind.
func parseSort(info *KindInfo, s string) (sortSpec, error) {
	spec := sortSpec{field: SortID}
	name, desc := strings.CutPrefix(s, "-")
	spec.desc = desc
	switch name {
	case "", SortID:
	case SortCreatedAt, SortUpdatedAt, SortGeneration, SortPhase:
		spec.field = name
	default:
		ix := findIndex(info, name)
		if ix == nil {
			return spec, i18n.ErrBadRequest.New("reason", fmt.Sprintf("%s lists can't be sorted by %q; use id, createdAt, updatedAt, generation, phase or an indexed field", info.Title, name))
		}
		spec.field, spec.index = name, ix
	}
	return spec, nil
}

// String returns the sort expression in its canonical form.
func (s sortSpec) String() string {
	if s.desc {
		return "-" + s.field
	}
	return s.field
}

// plainIDs reports whether the list is in creation order, whose cursors are
// the last ID alone.
func (s sortSpec) plainIDs() bool { return s.field == SortID && !s.desc }

// position is where a resource sits in a sorted list.
type position struct {
	value string
	id    ID
}

// before reports whether a comes before b in the list's order.
func (s sortSpec) before(a, b position) bool {
	switch {
	case a.value != b.value:
		return (a.value < b.value) != s.desc
	case a.id == b.id:
		return false
	}
	return (a.id < b.id) != s.desc
}

// positionOf returns where r sits in a list sorted by s.
func (s sortSpec) positionOf(info *KindInfo, r *Resource) position {
	return position{value: sortValue(info, r, s), id: r.ID}
}

// encode turns the last position of a page into a cursor.
func (s sortSpec) encode(p position) string {
	if s.plainIDs() {
		return EncodeCursor(p.id)
	}
	raw, _ := json.Marshal([]string{s.String(), p.value, string(p.id)})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decode reads a cursor made by encode for the same sort.
func (s sortSpec) decode(cursor string) (position, error) {
	if s.plainIDs() {
		id, err := DecodeCursor(cursor)
		return position{value: string(id), id: id}, err
	}
	malformed := i18n.ErrBadRequest.New("reason", "the cursor is malformed")
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return position{}, malformed
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) != 3 {
		return position{}, malformed
	}
	if parts[0] != s.String() {
		return position{}, i18n.ErrBadRequest.New("reason", "the cursor belongs to a list sorted another way")
	}
	id, err := ParseID(parts[2])
	if err != nil {
		return position{}, malformed
	}
	return position{value: parts[1], id: id}, nil
}

// timeKey formats a time so that comparing the strings compares the times.
func timeKey(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// generationKey formats a generation so that comparing the strings compares
// the numbers.
func generationKey(g int64) string { return fmt.Sprintf("%020d", g) }

// PhaseOf returns the phase a resource's status reports; a resource no
// reconciler has looked at yet is pending.
func PhaseOf(r *Resource) Phase {
	st, err := r.CommonStatus()
	if err != nil || st.Phase == "" {
		return PhasePending
	}
	return st.Phase
}

// observedGeneration returns the generation a reconciler last acted on.
func observedGeneration(r *Resource) int64 {
	st, err := r.CommonStatus()
	if err != nil {
		return 0
	}
	return st.ObservedGeneration
}

// sortValue returns the value r is sorted by.
func sortValue(info *KindInfo, r *Resource, s sortSpec) string {
	switch s.field {
	case SortCreatedAt:
		return timeKey(r.CreatedAt)
	case SortUpdatedAt:
		return timeKey(r.UpdatedAt)
	case SortGeneration:
		return generationKey(r.Generation)
	case SortPhase:
		return string(PhaseOf(r))
	case SortID:
		return string(r.ID)
	}
	values, err := indexValues(r.Spec, s.index)
	if err != nil || len(values) == 0 {
		return ""
	}
	return slices.Min(values)
}

// asciiLower lowercases ASCII letters only, as SQLite's lower() does, so
// searches match the same way in every store.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// searchValues lists what a search looks at besides the ID: the values of
// every index and of the title and subtitle fields.
func searchValues(info *KindInfo, spec json.RawMessage) []entry {
	var out []entry
	for i := range info.Indexes {
		ix := &info.Indexes[i]
		values, err := indexValues(spec, ix)
		if err != nil {
			continue
		}
		for _, v := range values {
			out = append(out, entry{name: ix.Name, value: v})
		}
	}
	for name, path := range map[string]string{titleEntry: info.UI.TitleField, subtitleEntry: info.UI.SubtitleField} {
		if path == "" {
			continue
		}
		values, err := indexValues(spec, &Index{Path: path})
		if err != nil {
			continue
		}
		for _, v := range values {
			out = append(out, entry{name: name, value: v})
		}
	}
	return out
}

// entry is one indexed or searchable value of a resource.
type entry struct {
	name, value string
}

// queryMatches reports whether r is one of the resources q selects, apart
// from the cursor.
func queryMatches(info *KindInfo, r *Resource, q Query) bool {
	switch {
	case r.Kind != q.Kind:
		return false
	case r.Deleted() && !q.IncludeDeleted && !q.Unreconciled:
		return false
	case q.Unreconciled && !r.Deleted() && observedGeneration(r) >= r.Generation:
		return false
	case q.Scope != nil && r.Scope != *q.Scope:
		return false
	case len(q.Scopes) > 0 && !slices.Contains(q.Scopes, r.Scope):
		return false
	case q.Server != nil && r.Server != *q.Server:
		return false
	case len(q.IDs) > 0 && !slices.Contains(q.IDs, r.ID):
		return false
	case len(q.Phases) > 0 && !slices.Contains(q.Phases, PhaseOf(r)):
		return false
	}
	if q.Search != "" && !searchMatch(info, r, q.Search) {
		return false
	}
	return labelsMatch(r.Labels, q.Labels) && indexMatch(info, r, q.Index)
}

func searchMatch(info *KindInfo, r *Resource, text string) bool {
	text = asciiLower(text)
	if strings.Contains(asciiLower(string(r.ID)), text) {
		return true
	}
	for _, e := range searchValues(info, r.Spec) {
		if strings.Contains(asciiLower(e.value), text) {
			return true
		}
	}
	return false
}

// checkQuery validates the parts of a query that refer to the kind.
func checkQuery(reg *KindRegistry, q Query) (*KindInfo, sortSpec, error) {
	k, ok := reg.Get(q.Kind)
	if !ok {
		return nil, sortSpec{}, i18n.ErrUnknownName.New("what", "kind", "name", q.Kind)
	}
	info := k.Info()
	for name := range q.Index {
		if findIndex(info, name) == nil {
			return nil, sortSpec{}, i18n.ErrUnknownName.New("what", "index", "name", name)
		}
	}
	for _, p := range q.Phases {
		if !p.Valid() {
			return nil, sortSpec{}, i18n.ErrBadRequest.New("reason", fmt.Sprintf("%q isn't a phase", p))
		}
	}
	for k := range q.Labels {
		if !labelKeyPattern.MatchString(k) {
			return nil, sortSpec{}, i18n.ErrBadRequest.New("reason", fmt.Sprintf("%q isn't a label key", k))
		}
	}
	s, err := parseSort(info, q.Sort)
	return info, s, err
}

// pageOf sorts the matching resources, skips those up to the cursor and
// cuts a page. Every store and the overlay page the same way through it.
func pageOf(info *KindInfo, s sortSpec, matched []*Resource, q Query) (*Page, error) {
	positions := make(map[ID]position, len(matched))
	for _, r := range matched {
		positions[r.ID] = s.positionOf(info, r)
	}
	slices.SortFunc(matched, func(a, b *Resource) int {
		pa, pb := positions[a.ID], positions[b.ID]
		switch {
		case s.before(pa, pb):
			return -1
		case s.before(pb, pa):
			return 1
		}
		return 0
	})
	if q.Cursor != "" {
		after, err := s.decode(q.Cursor)
		if err != nil {
			return nil, err
		}
		i := 0
		for i < len(matched) && !s.before(after, positions[matched[i].ID]) {
			i++
		}
		matched = matched[i:]
	}
	page := &Page{}
	if limit := q.EffectiveLimit(); len(matched) > limit {
		matched = matched[:limit]
		page.Next = s.encode(positions[matched[limit-1].ID])
	}
	page.Items = make([]*Resource, len(matched))
	for i, r := range matched {
		page.Items[i] = r.Clone()
	}
	return page, nil
}

// Valid reports whether p is a known phase.
func (p Phase) Valid() bool {
	switch p {
	case PhasePending, PhaseApplying, PhaseReady, PhaseDegraded, PhaseFailed, PhaseDeleting:
		return true
	}
	return false
}
