package modules

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Export is a module's resources as they are, offered for keeping before
// the module is removed with its data: every resource of its kinds, with
// its settings and labels. Fields that refer to secrets are left out, since
// secret values never leave the panel this way.
type Export struct {
	Module    string             `json:"module" doc:"The module's id"`
	Version   string             `json:"version" doc:"The module's version"`
	Exported  time.Time          `json:"exported" doc:"When the export was made"`
	Resources []ExportedResource `json:"resources" doc:"Every resource of the module's kinds that isn't deleted"`
}

// ExportedResource is one resource of an export.
type ExportedResource struct {
	Kind      string            `json:"kind" doc:"The resource's kind"`
	ID        store.ID          `json:"id" doc:"The resource's ID"`
	Scope     store.Scope       `json:"scope" doc:"Who the resource belongs to"`
	Labels    map[string]string `json:"labels,omitempty" doc:"The resource's labels"`
	Spec      json.RawMessage   `json:"spec" doc:"The resource's settings, without the fields that refer to secrets"`
	CreatedAt time.Time         `json:"createdAt" doc:"When the resource was created"`
	// Withheld are the fields left out because they refer to secrets.
	Withheld []string `json:"withheld,omitempty" doc:"Fields that refer to secrets, which the export leaves out"`
}

// Export returns the resources of a module's kinds that aren't deleted.
func (m *Manager) Export(ctx context.Context, id string) (*Export, error) {
	man, err := m.manifest(id)
	if err != nil {
		return nil, err
	}
	out := &Export{Module: man.ID, Version: man.Version, Exported: m.opts.Now().UTC(), Resources: []ExportedResource{}}
	kinds := m.opts.Services.Kinds
	if kinds == nil {
		kinds = store.Kinds()
	}
	for _, kind := range m.KindsOf(id) {
		var sensitive []string
		if k, ok := kinds.Get(kind); ok {
			sensitive = store.SensitiveFields(k.Info())
		}
		var failed error
		err := m.each(ctx, m.opts.Services.Store, kind, func(r *store.Resource) {
			if failed != nil {
				return
			}
			spec, withheld, err := withhold(r.Spec, sensitive)
			if err != nil {
				failed = err
				return
			}
			out.Resources = append(out.Resources, ExportedResource{
				Kind: r.Kind, ID: r.ID, Scope: r.Scope, Labels: r.Labels, Spec: spec, CreatedAt: r.CreatedAt, Withheld: withheld,
			})
		})
		if err == nil {
			err = failed
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// withhold takes the fields at the given dotted paths out of a spec, and
// says which of them it held.
func withhold(spec json.RawMessage, paths []string) (json.RawMessage, []string, error) {
	if len(paths) == 0 || len(spec) == 0 {
		return spec, nil, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(spec, &doc); err != nil {
		return nil, nil, err
	}
	var withheld []string
	for _, p := range paths {
		parts := strings.Split(p, ".")
		cur := doc
		for _, part := range parts[:len(parts)-1] {
			next, ok := cur[part].(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = next
		}
		if cur == nil {
			continue
		}
		if _, ok := cur[parts[len(parts)-1]]; ok {
			delete(cur, parts[len(parts)-1])
			withheld = append(withheld, p)
		}
	}
	slices.Sort(withheld)
	raw, err := json.Marshal(doc)
	return raw, withheld, err
}
