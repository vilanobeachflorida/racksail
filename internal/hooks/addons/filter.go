package addons

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Grants are what an add-on was allowed, read from its approved
// permissions, for filtering what it is sent and checking what it sends
// back.
type Grants struct {
	perms hooks.Permissions
	// Scope is the account an account add-on belongs to; server add-ons
	// have the server's scope.
	Scope store.Scope
}

// NewGrants returns the grants of approved permissions for an add-on owned
// at scope.
func NewGrants(approved hooks.Permissions, scope store.Scope) Grants {
	return Grants{perms: approved, Scope: scope}
}

// fieldsOf returns whether a kind is in scopes and the fields they allow,
// nil meaning all of them.
func fieldsOf(scopes []hooks.DataScope, kind string) ([]string, bool) {
	var fields []string
	found := false
	for _, s := range scopes {
		if s.Kind != kind {
			continue
		}
		if len(s.Fields) == 0 {
			return nil, true
		}
		found = true
		fields = append(fields, s.Fields...)
	}
	return fields, found
}

// CanRead and CanWrite report whether the add-on may see, or change,
// resources of a kind, and which of their fields: nil means every one.
func (g Grants) CanRead(kind string) ([]string, bool) {
	if fields, ok := fieldsOf(g.perms.Write, kind); ok && fields == nil {
		return nil, true
	}
	read, okR := fieldsOf(g.perms.Read, kind)
	write, okW := fieldsOf(g.perms.Write, kind)
	if okR && read == nil {
		return nil, true
	}
	return append(read, write...), okR || okW
}

func (g Grants) CanWrite(kind string) ([]string, bool) { return fieldsOf(g.perms.Write, kind) }

// InScope reports whether a resource of scope belongs to what the add-on
// may see: anything for server add-ons, its own account for account ones.
func (g Grants) InScope(scope store.Scope) bool {
	return !g.Account() || scope == g.Scope
}

// Account reports whether the add-on is an account's, which only ever
// sees and changes that account.
func (g Grants) Account() bool { return g.Scope.Level == store.ScopeAccount }

// Actor is who the add-on acts as when it calls the panel's API: an
// add-on with grants on exactly the kinds and operations its permissions
// allow, at its own scope.
func (g Grants) Actor(id store.ID, name string) *auth.Actor {
	var perms []auth.Permission
	add := func(p auth.Permission) {
		if !slices.Contains(perms, p) {
			perms = append(perms, p)
		}
	}
	for _, s := range g.perms.Read {
		add(auth.KindPermission(s.Kind, "read"))
	}
	for _, s := range g.perms.Write {
		add(auth.KindPermission(s.Kind, "read"))
		for _, op := range []string{"create", "update", "delete"} {
			add(auth.KindPermission(s.Kind, op))
		}
		if k, ok := store.Kinds().Get(s.Kind); ok {
			for _, a := range k.Info().Actions {
				add(auth.KindPermission(s.Kind, a.Name))
			}
		}
	}
	if g.perms.Secrets {
		add(auth.PermSecretsRead)
	}
	if g.perms.Override && g.Scope.Level == store.ScopeServer {
		add(auth.PermOverride)
	}
	actor := &auth.Actor{Type: auth.ActorAddon, ID: id, Name: name, Source: auth.SourceAddon}
	if len(perms) > 0 {
		actor.Grants = []auth.Grant{{Role: auth.RoleCollaborator, Scope: g.Scope, Permissions: perms}}
	}
	return actor
}

// project keeps the fields of a JSON document that fields name, by their
// dotted paths; nil fields keeps everything.
func project(doc json.RawMessage, fields []string) json.RawMessage {
	if fields == nil || len(doc) == 0 {
		return doc
	}
	var m map[string]any
	if json.Unmarshal(doc, &m) != nil {
		return nil
	}
	out := map[string]any{}
	for _, f := range fields {
		parts := strings.Split(f, ".")
		var cur any = m
		for _, p := range parts {
			obj, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = obj[p]
		}
		if cur == nil {
			continue
		}
		dst := out
		for _, p := range parts[:len(parts)-1] {
			next, ok := dst[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				dst[p] = next
			}
			dst = next
		}
		dst[parts[len(parts)-1]] = cur
	}
	raw, _ := json.Marshal(out)
	return raw
}

// redact leaves a change only what says what happens to which resource.
func redact(c *changeset.ResourceChange) {
	c.Before, c.After, c.Input, c.Output, c.Diff, c.Steps, c.Checks = nil, nil, nil, nil, nil, nil, nil
	c.Labels, c.LabelsBefore = nil, nil
}

// pathAllowed reports whether a diff path lies within the allowed fields.
func pathAllowed(path string, fields []string) bool {
	if fields == nil {
		return true
	}
	for _, f := range fields {
		if path == f || strings.HasPrefix(path, f+".") || strings.HasPrefix(path, f+"[") {
			return true
		}
	}
	return false
}

// FilterChangeSet returns the copy of a change set an add-on is sent:
// changes of kinds it may read keep the fields it may read, the rest say
// only what happens to which resource, and changes outside an account
// add-on's account say nothing at all but their position. When a change set
// reaches beyond the account, what it says as a whole, its title, effects,
// estimate and snapshots, could be about other accounts, so an account's
// add-on gets only as many empty effects as there are, and the conflicts and
// contributions of its own changes.
func (g Grants) FilterChangeSet(cs *changeset.ChangeSet) *changeset.ChangeSet {
	if cs == nil {
		return nil
	}
	c := cs.Clone()
	if c.Actor != nil {
		c.Actor = redactActor(c.Actor)
	}
	if g.Account() && slices.ContainsFunc(c.Changes, func(ch changeset.ResourceChange) bool { return !g.InScope(ch.Scope) }) {
		g.keepOwn(c)
	}
	for i := range c.Changes {
		ch := &c.Changes[i]
		fields, ok := g.CanRead(ch.Kind)
		switch {
		case !g.InScope(ch.Scope):
			*ch = changeset.ResourceChange{Op: ch.Op, Source: ch.Source}
		case !ok:
			redact(ch)
		default:
			ch.Before, ch.After = project(ch.Before, fields), project(ch.After, fields)
			ch.Input, ch.Output = nil, nil
			if fields != nil {
				var kept []changeset.FieldDiff
				for _, d := range ch.Diff {
					if pathAllowed(d.Path, fields) {
						kept = append(kept, d)
					}
				}
				ch.Diff = kept
			}
			ch.Steps, ch.Checks = nil, nil
		}
	}
	return c
}

// keepOwn leaves a change set that reaches beyond an account add-on's
// account only what is about the account. Effects say nothing of which
// change they belong to, so each becomes an empty one; keeping their number
// lets a modify hook's answer still add effects after them.
func (g Grants) keepOwn(c *changeset.ChangeSet) {
	own := map[int]bool{}
	ids := map[store.ID]bool{}
	for i, ch := range c.Changes {
		if g.InScope(ch.Scope) {
			own[i], ids[ch.ID] = true, true
		}
	}
	c.Title = ""
	c.Effects = make([]system.Effect, len(c.Effects))
	c.Estimate = changeset.Estimate{}
	c.Snapshots = nil
	c.Conflicts = slices.DeleteFunc(c.Conflicts, func(con changeset.Conflict) bool { return !ids[con.Resource] })
	c.Contributions = slices.DeleteFunc(c.Contributions, func(con changeset.Contribution) bool {
		return len(con.Changes) == 0 || slices.ContainsFunc(con.Changes, func(i int) bool { return !own[i] })
	})
	for i := range c.Contributions {
		c.Contributions[i].Effects = nil
	}
}

// redactActor keeps who acted and through what, leaving out the address
// they came from.
func redactActor(a *auth.Actor) *auth.Actor {
	if a == nil {
		return nil
	}
	return &auth.Actor{Type: a.Type, ID: a.ID, Name: a.Name, User: a.User, Source: a.Source}
}

// Merge takes an add-on's answer to a modify hook into the hook's copy of
// the change set: new specs for the changes the point is about, in the
// fields the add-on may change, and the changes and effects it added, of
// kinds it may change. The add-on only saw part of each spec, so only the
// fields it may write are taken from what it returns.
func (g Grants) Merge(work *changeset.ChangeSet, reply *changeset.ChangeSet, mine []int) error {
	if reply == nil {
		return nil
	}
	if len(reply.Changes) < len(work.Changes) {
		return fmt.Errorf("the add-on's answer has %d changes, fewer than the %d it was sent", len(reply.Changes), len(work.Changes))
	}
	for _, i := range mine {
		ch := &work.Changes[i]
		fields, ok := g.CanWrite(ch.Kind)
		if !ok || !g.InScope(ch.Scope) {
			continue
		}
		readable, _ := g.CanRead(ch.Kind)
		sent := project(ch.After, readable)
		returned := reply.Changes[i].After
		if bytes.Equal(normalizeJSON(sent), normalizeJSON(returned)) {
			continue
		}
		diffs, err := changeset.Diff(sent, returned)
		if err != nil {
			return err
		}
		merged, err := applyDiffs(ch.After, returned, diffs, fields)
		if err != nil {
			return err
		}
		ch.After = merged
	}
	for _, added := range reply.Changes[len(work.Changes):] {
		if _, ok := g.CanWrite(added.Kind); !ok {
			return fmt.Errorf("the add-on added a change to a %s, which it may not change", added.Kind)
		}
		if !g.InScope(added.Scope) {
			return fmt.Errorf("the add-on added a change outside its account")
		}
		fields, _ := g.CanWrite(added.Kind)
		if fields != nil && added.After != nil {
			diffs, err := changeset.Diff(nil, added.After)
			if err != nil {
				return err
			}
			for _, d := range diffs {
				if !pathAllowed(d.Path, fields) {
					return fmt.Errorf("the add-on set %s of a new %s, which it may not change", d.Path, added.Kind)
				}
			}
		}
		work.Changes = append(work.Changes, added)
	}
	if len(reply.Effects) > len(work.Effects) {
		work.Effects = append(work.Effects, reply.Effects[len(work.Effects):]...)
	}
	return nil
}

func normalizeJSON(raw json.RawMessage) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, _ := json.Marshal(v)
	return out
}

// applyDiffs takes the fields an add-on changed from its answer into the
// full spec. Each changed path must lie within a field the add-on may
// write, and that whole field is taken over, so a list it changed is
// replaced as a whole. Anything outside those fields is refused.
func applyDiffs(full, returned json.RawMessage, diffs []changeset.FieldDiff, fields []string) (json.RawMessage, error) {
	var doc, answer map[string]any
	if len(full) > 0 {
		if err := json.Unmarshal(full, &doc); err != nil {
			return nil, err
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}
	if err := json.Unmarshal(returned, &answer); err != nil {
		return nil, fmt.Errorf("the add-on's spec isn't a JSON object: %w", err)
	}
	roots := map[string]bool{}
	for _, d := range diffs {
		root := ""
		if fields == nil {
			root = d.Path
			if i := strings.IndexAny(root, ".["); i >= 0 {
				root = root[:i]
			}
		} else {
			for _, f := range fields {
				if d.Path == f || strings.HasPrefix(d.Path, f+".") || strings.HasPrefix(d.Path, f+"[") {
					root = f
					break
				}
			}
		}
		if root == "" {
			return nil, fmt.Errorf("the add-on changed %s, which it may not change", d.Path)
		}
		roots[root] = true
	}
	for root := range roots {
		value, present := lookupPath(answer, root)
		if err := writePath(doc, root, value, present); err != nil {
			return nil, err
		}
	}
	return json.Marshal(doc)
}

func lookupPath(doc map[string]any, path string) (any, bool) {
	var cur any = doc
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func writePath(doc map[string]any, path string, value any, present bool) error {
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			if !present {
				return nil
			}
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	last := parts[len(parts)-1]
	if !present {
		delete(cur, last)
		return nil
	}
	cur[last] = value
	return nil
}

// CheckWrite reports the first field of a change an add-on may not
// change, or "" when it may make the change.
func (g Grants) CheckWrite(c changeset.ResourceChange) string {
	fields, ok := g.CanWrite(c.Kind)
	if !ok {
		return "anything"
	}
	if fields == nil {
		return ""
	}
	for _, d := range c.Diff {
		if !pathAllowed(d.Path, fields) {
			return d.Path
		}
	}
	return ""
}
