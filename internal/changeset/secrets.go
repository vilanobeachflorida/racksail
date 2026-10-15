package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Secret values never enter specs. A sensitive field holds a reference to
// one of the resource's own secrets, and values that arrive with a change
// are stored under a new name for every change set,
// "<field>.<change set id>", so undoing the change puts the previous
// reference back and the previous value is still there. Versions nothing
// refers to any more are deleted when the change sets that used them are
// no longer kept.

// secretName names the secret holding a field's value as set by a change
// set, for example "smtp.auth-password.chg_01j9...".
func secretName(path string, cs store.ID) string {
	var b strings.Builder
	for _, r := range path {
		if unicode.IsUpper(r) {
			b.WriteByte('-')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String() + "." + string(cs)
}

// pipelineSecret reports whether a reference names a value the pipeline
// stored, as opposed to a secret a module manages itself.
func pipelineSecret(ref secrets.Ref) bool {
	_, name, err := ref.Parse()
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return false
	}
	id := store.ID(name[i+1:])
	return id.Valid() && id.Prefix() == store.PrefixChangeSet
}

// fieldValues returns the values at a dotted path in a JSON document,
// stepping into arrays along the way.
func fieldValues(doc json.RawMessage, path string) ([]any, error) {
	if len(doc) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, fmt.Errorf("decoding spec: %w", err)
	}
	nodes := []any{v}
	for _, part := range strings.Split(path, ".") {
		var next []any
		for _, n := range flatten(nodes) {
			if m, ok := n.(map[string]any); ok {
				if child, ok := m[part]; ok {
					next = append(next, child)
				}
			}
		}
		nodes = next
	}
	return flatten(nodes), nil
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

// setField sets a dotted path in a decoded JSON object, creating the
// objects on the way.
func setField(doc map[string]any, path string, v any) error {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		child, ok := doc[part]
		if !ok || child == nil {
			m := map[string]any{}
			doc[part] = m
			doc = m
			continue
		}
		m, ok := child.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not an object", part)
		}
		doc = m
	}
	doc[parts[len(parts)-1]] = v
	return nil
}

// checkSecretRefs makes sure every sensitive field of r holds nothing but
// references to r's own secrets. A plain value would end up in the store,
// in history, in previews and in hooks; a reference to another resource's
// secret could reach across accounts.
func checkSecretRefs(info *store.KindInfo, r *store.Resource) error {
	var fields []i18n.FieldError
	for _, path := range store.SensitiveFields(info) {
		values, err := fieldValues(r.Spec, path)
		if err != nil {
			return err
		}
		for _, v := range values {
			if v == nil || v == "" {
				continue
			}
			s, ok := v.(string)
			owner, _, err := secrets.Ref(s).Parse()
			if !ok || err != nil || owner != r.ID {
				fields = append(fields, i18n.Field("spec."+path, i18n.ErrBadValue, "reason", "a secret field holds a reference to one of the resource's own secrets, and its value is sent separately"))
				break
			}
		}
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// secretRefs lists the references held by the sensitive fields of a spec.
func secretRefs(info *store.KindInfo, spec json.RawMessage) []secrets.Ref {
	var out []secrets.Ref
	for _, path := range store.SensitiveFields(info) {
		values, err := fieldValues(spec, path)
		if err != nil {
			continue
		}
		for _, v := range values {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, secrets.Ref(s))
			}
		}
	}
	return out
}

// takeSecrets moves the values a change carries into the plan: each
// sensitive field gets a reference to a new secret, and the value waits in
// p until the change set is applied.
func (e *Engine) takeSecrets(p *draft, info *store.KindInfo, r *store.Resource, values map[string]string) error {
	if e.opts.Secrets == nil {
		return errors.New("changeset: this engine has no secrets store, so it can't take secret values")
	}
	sensitive := store.SensitiveFields(info)
	var doc map[string]any
	if err := json.Unmarshal(r.Spec, &doc); err != nil || doc == nil {
		return i18n.Invalid(i18n.Field("spec", i18n.ErrBadValue, "reason", "a spec with secret fields must be an object"))
	}
	paths := make([]string, 0, len(values))
	for path := range values {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if !slices.Contains(sensitive, path) {
			return i18n.Invalid(i18n.Field("secrets."+path, i18n.ErrBadValue, "reason", path+" isn't a secret field of "+info.Title))
		}
		var field any = ""
		if values[path] != "" {
			ref := secrets.NewRef(r.ID, secretName(path, p.cs.ID))
			p.secrets[ref] = []byte(values[path])
			field = string(ref)
		}
		if err := setField(doc, path, field); err != nil {
			return i18n.Invalid(i18n.Field("spec."+path, i18n.ErrBadValue, "reason", err.Error()))
		}
	}
	spec, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	r.Spec = spec
	return nil
}

// pending serves the values of a plan's new secrets to reconcilers while
// it is previewed, before anything is stored.
type pending struct {
	values map[secrets.Ref][]byte
	base   secrets.Reader
}

// Get implements secrets.Reader.
func (p pending) Get(ctx context.Context, ref secrets.Ref) ([]byte, error) {
	if v, ok := p.values[ref]; ok {
		return slices.Clone(v), nil
	}
	if p.base == nil {
		return nil, secrets.ErrNotFound.New("ref", string(ref))
	}
	return p.base.Get(ctx, ref)
}

// putSecrets stores the values of a change set that is being applied and
// returns the references it stored.
func (e *Engine) putSecrets(ctx context.Context, values map[secrets.Ref][]byte) ([]secrets.Ref, error) {
	stored := make([]secrets.Ref, 0, len(values))
	for ref, v := range values {
		if err := e.opts.Secrets.Put(ctx, ref, v); err != nil {
			e.dropSecrets(ctx, stored)
			return nil, err
		}
		stored = append(stored, ref)
	}
	return stored, nil
}

// dropSecrets removes values stored for a change set that couldn't be
// written after all.
func (e *Engine) dropSecrets(ctx context.Context, refs []secrets.Ref) {
	for _, ref := range refs {
		if err := e.opts.Secrets.Delete(ctx, ref); err != nil && !secrets.ErrNotFound.Is(err) {
			e.opts.Log.Warn("removing a secret of a change that failed", "ref", ref, "error", err)
		}
	}
}

// secretsOf lists the pipeline's secrets that the changes of the given
// change sets refer to.
func (e *Engine) secretsOf(sets []*ChangeSet, onlyPipeline bool) map[secrets.Ref]bool {
	out := map[secrets.Ref]bool{}
	for _, cs := range sets {
		if cs.DryRun {
			continue
		}
		for _, c := range cs.Changes {
			k, ok := e.opts.Kinds.Get(c.Kind)
			if !ok {
				continue
			}
			for _, ref := range append(secretRefs(k.Info(), c.Before), secretRefs(k.Info(), c.After)...) {
				if !onlyPipeline || pipelineSecret(ref) {
					out[ref] = true
				}
			}
		}
	}
	return out
}

// collectSecrets deletes the secret versions that evicted change sets
// stored, unless a kept change set or the owner's current spec still
// refers to them.
func (e *Engine) collectSecrets(ctx context.Context, candidates, kept map[secrets.Ref]bool) {
	for ref := range candidates {
		if kept[ref] {
			continue
		}
		owner, _, err := ref.Parse()
		if err != nil {
			continue
		}
		r, err := e.opts.Store.Get(ctx, owner)
		switch {
		case err == nil:
			if k, ok := e.opts.Kinds.Get(r.Kind); ok && slices.Contains(secretRefs(k.Info(), r.Spec), ref) {
				continue
			}
		case !store.ErrNotFound.Is(err):
			e.opts.Log.Warn("checking whether a secret is still used", "ref", ref, "error", err)
			continue
		}
		if err := e.opts.Secrets.Delete(ctx, ref); err != nil && !secrets.ErrNotFound.Is(err) {
			e.opts.Log.Warn("removing a secret nothing uses", "ref", ref, "error", err)
		}
	}
}
