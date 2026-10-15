package api

import (
	"context"

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

// Readable keeps the resources of a list the signed-in actor may read, in
// order. A list operation first authorizes the scope it lists, with
// Authorize and ReadPermission, and queries the store for that scope only,
// so pages stay full; Readable then covers grants on single resources.
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
