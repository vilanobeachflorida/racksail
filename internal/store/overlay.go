package store

import (
	"context"
)

// Overlay is a Reader that shows changes which are planned but not written
// yet on top of another Reader. The change pipeline plans the changes of a
// change set against one, so each change sees the resources the earlier
// ones create, update or delete, and nothing is written until the whole
// change set is applied.
type Overlay struct {
	base  Reader
	kinds *KindRegistry
	items map[ID]*Resource
}

// NewOverlay returns an overlay with nothing planned on top of base. kinds
// resolves the indexes that list queries filter on.
func NewOverlay(base Reader, kinds *KindRegistry) *Overlay {
	return &Overlay{base: base, kinds: kinds, items: map[ID]*Resource{}}
}

// Put records the planned state of a resource, replacing what the base or
// an earlier Put holds for its ID. A resource marked as deleted stays
// readable with Get and drops out of lists, as it does in a Store.
func (o *Overlay) Put(r *Resource) {
	o.items[r.ID] = r.Clone()
}

// Planned reports whether a resource has a planned state in the overlay.
func (o *Overlay) Planned(id ID) bool {
	_, ok := o.items[id]
	return ok
}

// Get implements Reader.
func (o *Overlay) Get(ctx context.Context, id ID) (*Resource, error) {
	if r, ok := o.items[id]; ok {
		return r.Clone(), nil
	}
	return o.base.Get(ctx, id)
}

// List implements Reader. Planned resources replace their stored versions
// and new ones are merged in where the query's order puts them, so pages and
// cursors work as they do on the base.
func (o *Overlay) List(ctx context.Context, q Query) (*Page, error) {
	page, err := o.base.List(ctx, q)
	if err != nil || len(o.items) == 0 {
		return page, err
	}
	info, s, err := checkQuery(o.kinds, q)
	if err != nil {
		return nil, err
	}
	var after *position
	if q.Cursor != "" {
		p, err := s.decode(q.Cursor)
		if err != nil {
			return nil, err
		}
		after = &p
	}
	// When the base has more pages, planned resources sorting after its last
	// item belong to those pages.
	var last *position
	if page.Next != "" && len(page.Items) > 0 {
		p := s.positionOf(info, page.Items[len(page.Items)-1])
		last = &p
	}
	merged := make([]*Resource, 0, len(page.Items)+len(o.items))
	for _, r := range page.Items {
		if _, planned := o.items[r.ID]; !planned {
			merged = append(merged, r)
		}
	}
	for _, r := range o.items {
		p := s.positionOf(info, r)
		if (after != nil && !s.before(*after, p)) || (last != nil && s.before(*last, p)) || !queryMatches(info, r, q) {
			continue
		}
		merged = append(merged, r)
	}
	// The cursor has been applied already.
	q.Cursor = ""
	out, err := pageOf(info, s, merged, q)
	if err != nil {
		return nil, err
	}
	if out.Next == "" {
		out.Next = page.Next
	}
	return out, nil
}
