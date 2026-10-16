package modules

import (
	"context"
	"reflect"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// usageDelay gathers the writes of a busy moment, such as a recipe creating
// several resources, into one count.
const usageDelay = 250 * time.Millisecond

// Run keeps each module's usage and its on or active state current as
// resources of its kinds come and go, until ctx ends. A module becomes
// active when the first resource uses it, which is when its reconcilers
// install its software, and on again when the last one goes.
func (m *Manager) Run(ctx context.Context) {
	svc := m.opts.Services
	if svc.Store == nil || len(m.kinds) == 0 {
		<-ctx.Done()
		return
	}
	kinds := make([]string, 0, len(m.kinds))
	for k := range m.kinds {
		kinds = append(kinds, k)
	}
	w := svc.Store.Watch(store.WatchFilter{Kinds: kinds}, 256)
	defer w.Close()
	pending := map[string]bool{}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-w.C:
			if !ok {
				return
			}
			switch n.Op {
			case store.NoticeCreated, store.NoticeDeleted, store.NoticePurged:
				if id, ok := m.ModuleOf(n.Kind); ok {
					pending[id] = true
					timer.Reset(usageDelay)
				}
			}
		case id := <-m.refresh:
			pending[id] = true
			timer.Reset(usageDelay)
		case <-timer.C:
			for id := range pending {
				if err := m.RefreshUsage(ctx, id); err != nil && ctx.Err() == nil {
					m.opts.Log.Warn("counting what uses a module", "module", id, "error", err)
				}
			}
			clear(pending)
		}
	}
}

// RefreshUsage counts what uses a module now and records it in the
// module's status, with the state that follows from it: active while
// something uses a module that is on.
func (m *Manager) RefreshUsage(ctx context.Context, id string) error {
	svc := m.opts.Services
	rid, ok := m.Resource(id)
	if !ok || svc.Store == nil {
		return nil
	}
	usage, err := m.usage(ctx, svc.Store, id)
	if err != nil {
		return err
	}
	obj, err := Kind.Get(ctx, svc.Store, rid)
	if err != nil {
		return err
	}
	st := obj.Status
	state := st.State
	if state.IsOn() {
		state = StateOn
		if usage.Resources > 0 {
			state = StateActive
		}
	}
	if state == st.State && reflect.DeepEqual(usage, st.Usage) {
		return nil
	}
	if state != st.State {
		now := m.opts.Now().UTC()
		st.Since = &now
	}
	st.State, st.Usage = state, usage
	m.setState(id, state)
	return store.SetStatus(ctx, svc.Store, rid, st)
}
