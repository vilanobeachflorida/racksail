package api

import (
	"context"
	"slices"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Reads never go through the change pipeline, so nothing checks them on a
// handler's behalf: every operation that returns data asks the central
// authorizer itself, through the helpers below, before it returns anything.
// Changes are checked again, change by change, in the pipeline.

// Actor returns the signed-in actor of a request, or core.unauthenticated.
func Actor(ctx context.Context) (*auth.Actor, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return nil, i18n.ErrUnauthenticated.New()
	}
	return a, nil
}

// Authorize asks the central authorizer whether the signed-in actor may use
// perm on target. Without an authorizer nothing is allowed.
func Authorize(ctx context.Context, authz auth.Authorizer, perm auth.Permission, target auth.Target) error {
	actor, err := Actor(ctx)
	if err != nil {
		return err
	}
	if authz == nil {
		return i18n.ErrForbidden.New("action", string(perm))
	}
	return authz.Authorize(ctx, actor, perm, target)
}

// ReadPermission is the permission to read resources of a kind, such as
// web.site.read.
func ReadPermission(kind string) auth.Permission {
	return auth.KindPermission(kind, "read")
}

// AuthorizeRead checks that the signed-in actor may read a resource, given
// its metadata (r.Meta of a store.Resource, or of a typed store.Object). A
// resource the actor may not read is reported as not found, so resources of
// other accounts don't reveal that they exist.
func AuthorizeRead(ctx context.Context, authz auth.Authorizer, m store.Meta) error {
	err := Authorize(ctx, authz, ReadPermission(m.Kind), auth.Target{Scope: m.Scope, Object: m.ID})
	if i18n.ErrForbidden.Is(err) {
		return store.ErrNotFound.New("what", string(m.ID))
	}
	return err
}

// List returns one page of the resources q selects that the signed-in actor
// may read. It is how every list operation reads the store.
//
// When a grant covers the whole scope listed (q.Scope, or every scope when
// it is nil) the store's page is used as it is. A list of every scope by an
// actor whose grants cover only some, such as an account's owner, lists the
// scopes those grants cover. Otherwise the list falls back to the resources
// the actor holds grants on single resources for, so a collaborator granted
// one site sees that site; with none of these, the list is refused with
// core.forbidden. The query names those scopes or resources, so pages stay
// full. Either way each resource is authorized on its own before it is
// returned.
func List(ctx context.Context, authz auth.Authorizer, st store.Reader, q store.Query) (*store.Page, error) {
	actor, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	scope := store.ServerScope()
	if q.Scope != nil {
		scope = *q.Scope
	}
	perm := ReadPermission(q.Kind)
	err = Authorize(ctx, authz, perm, auth.Target{Scope: scope})
	var scopes []store.Scope
	if err != nil && i18n.ErrForbidden.Is(err) && q.Scope == nil {
		var scopeErr error
		if scopes, scopeErr = grantScopes(ctx, authz, actor, perm); scopeErr != nil {
			return nil, scopeErr
		}
	}
	switch {
	case err == nil:
	case len(scopes) > 0:
		q.Scopes = scopes
	case i18n.ErrForbidden.Is(err):
		objects, ok := authz.(auth.ObjectAuthorizer)
		if !ok {
			return nil, err
		}
		ids, objErr := objects.Objects(ctx, actor, perm, q.Scope)
		if objErr != nil {
			return nil, objErr
		}
		if len(ids) == 0 {
			return nil, err
		}
		if len(q.IDs) > 0 {
			// The caller asked for some resources; keep the ones granted.
			ids = slices.DeleteFunc(ids, func(id store.ID) bool { return !slices.Contains(q.IDs, id) })
			if len(ids) == 0 {
				return &store.Page{Items: []*store.Resource{}}, nil
			}
		}
		q.IDs = ids
	default:
		return nil, err
	}
	page, err := st.List(ctx, q)
	if err != nil {
		return nil, err
	}
	items, err := Readable(ctx, authz, page.Items)
	if err != nil {
		return nil, err
	}
	return &store.Page{Items: items, Next: page.Next}, nil
}

// grantScopes returns the scopes the actor's grants on whole scopes reach
// with perm, other than the server's. Authorizers that can list them
// (auth.ScopeAuthorizer) include the accounts of a reseller the actor holds
// a grant on; with others, a grant reaches its own scope.
func grantScopes(ctx context.Context, authz auth.Authorizer, actor *auth.Actor, perm auth.Permission) ([]store.Scope, error) {
	if sa, ok := authz.(auth.ScopeAuthorizer); ok {
		scopes, _, err := sa.Scopes(ctx, actor, perm)
		return slices.DeleteFunc(scopes, func(sc store.Scope) bool { return sc.Level == store.ScopeServer }), err
	}
	var out []store.Scope
	for _, g := range actor.Grants {
		if g.Object != "" || g.Scope.Level == store.ScopeServer || slices.Contains(out, g.Scope) {
			continue
		}
		if perm == "" || authz.Authorize(ctx, actor, perm, auth.Target{Scope: g.Scope}) == nil {
			out = append(out, g.Scope)
		}
	}
	return out, nil
}

// narrowable reports whether grantScopes names every scope the actor's
// grants reach, so a query can be narrowed to them: always with an
// authorizer that lists scopes, and otherwise unless a reseller's grant
// reaches accounts nothing can name.
func narrowable(authz auth.Authorizer, actor *auth.Actor) bool {
	if _, ok := authz.(auth.ScopeAuthorizer); ok {
		return true
	}
	return !slices.ContainsFunc(actor.Grants, func(g auth.Grant) bool { return g.Scope.Level == store.ScopeReseller })
}

// fill reads pages from list until it has limit items keep accepts, or
// there are none left, and returns them with the cursor of the next page.
// Lists whose storage can only narrow down roughly what someone may see use
// it, so their pages stay full and a short page means the last.
func fill[T any](cursor string, limit int, list func(cursor string, limit int) ([]T, string, error), keep func(T) bool, id func(T) store.ID) ([]T, string, error) {
	if limit <= 0 || limit > store.MaxLimit {
		limit = store.DefaultLimit
	}
	out := []T{}
	for {
		batch, next, err := list(cursor, limit)
		if err != nil {
			return nil, "", err
		}
		for i, item := range batch {
			if !keep(item) {
				continue
			}
			out = append(out, item)
			if len(out) == limit {
				if i == len(batch)-1 && next == "" {
					return out, "", nil
				}
				return out, store.EncodeCursor(id(item)), nil
			}
		}
		if next == "" {
			return out, "", nil
		}
		cursor = next
	}
}

// Readable keeps the resources the signed-in actor may read, in order,
// authorizing each with AuthorizeRead. List uses it on every page; use it on
// its own for resources found some other way, such as the ones a search
// returns.
func Readable(ctx context.Context, authz auth.Authorizer, items []*store.Resource) ([]*store.Resource, error) {
	out := make([]*store.Resource, 0, len(items))
	for _, r := range items {
		err := AuthorizeRead(ctx, authz, r.Meta)
		switch {
		case err == nil:
			out = append(out, r)
		case !store.ErrNotFound.Is(err):
			return nil, err
		}
	}
	return out, nil
}
