package api

import (
	"context"
	"log/slog"
	"slices"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Reads never go through the change pipeline, so nothing checks them on a
// handler's behalf: every operation that returns data asks the central
// authorizer itself, through the helpers below, before it returns anything.
// Changes are checked again, change by change, in the pipeline.
//
// Authorize and AuthorizeRead are for an operation's own decision, and
// record in the audit log every request they refuse. Lists and streams that
// leave out what someone may not see filter with Readable instead, which
// records nothing: leaving something out of a list refuses nothing.

// Actor returns the signed-in actor of a request, or core.unauthenticated.
func Actor(ctx context.Context) (*auth.Actor, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return nil, i18n.ErrUnauthenticated.New()
	}
	return a, nil
}

// Authorize asks the central authorizer whether the signed-in actor may use
// perm on target. Without an authorizer nothing is allowed. A refusal is
// recorded in the audit log.
func Authorize(ctx context.Context, authz auth.Authorizer, perm auth.Permission, target auth.Target) error {
	err := allowed(ctx, authz, perm, target)
	if i18n.ErrForbidden.Is(err) {
		refused(ctx, perm, "", target.Object, target.Scope, err)
	}
	return err
}

// allowed is Authorize without the audit record, for checks that only find
// out what someone may do.
func allowed(ctx context.Context, authz auth.Authorizer, perm auth.Permission, target auth.Target) error {
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
// other accounts don't reveal that they exist; the audit log records the
// refusal.
func AuthorizeRead(ctx context.Context, authz auth.Authorizer, m store.Meta) error {
	perm := ReadPermission(m.Kind)
	err := allowed(ctx, authz, perm, auth.Target{Scope: m.Scope, Object: m.ID})
	if i18n.ErrForbidden.Is(err) {
		refused(ctx, perm, m.Kind, m.ID, m.Scope, err)
		return store.ErrNotFound.New("what", string(m.ID))
	}
	return err
}

// canRead reports whether the signed-in actor may read a resource, for
// filters, which record nothing.
func canRead(ctx context.Context, authz auth.Authorizer, m store.Meta) bool {
	return allowed(ctx, authz, ReadPermission(m.Kind), auth.Target{Scope: m.Scope, Object: m.ID}) == nil
}

// auditKey carries where an API request records what it refuses.
type auditKey struct{}

// auditSink is the audit log of a request, and the log for when writing to
// it fails.
type auditSink struct {
	audit events.AuditLog
	log   *slog.Logger
}

// withAudit returns a context whose refusals are recorded in audit.
func withAudit(ctx context.Context, audit events.AuditLog, log *slog.Logger) context.Context {
	if audit == nil {
		return ctx
	}
	return context.WithValue(ctx, auditKey{}, &auditSink{audit: audit, log: log})
}

// refused records that the signed-in actor was refused perm, when ctx
// belongs to an API request that has an audit log.
func refused(ctx context.Context, perm auth.Permission, kind string, subject store.ID, scope store.Scope, err error) {
	sink, ok := ctx.Value(auditKey{}).(*auditSink)
	actor, signedIn := auth.ActorFrom(ctx)
	if !ok || !signedIn {
		return
	}
	rec := events.AuditRecord{
		Actor: *actor, Action: string(perm), Kind: kind, Subject: subject, Scope: scope,
		Outcome: events.OutcomeDenied, Reason: i18n.From(err).Message,
	}
	if recErr := sink.audit.Record(ctx, rec); recErr != nil {
		sink.log.Error("writing an audit record", "action", perm, "error", recErr)
	}
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
// core.forbidden, which the audit log records. The query names those scopes
// or resources, so pages stay full. Either way each resource is authorized
// on its own before it is returned.
func List(ctx context.Context, authz auth.Authorizer, st store.Reader, q store.Query) (*store.Page, error) {
	return list(ctx, authz, st, q, true)
}

// list is List. With record false a list the actor may not see at all
// records nothing, for searches that look through every kind and leave out
// the ones the actor can't read.
func list(ctx context.Context, authz auth.Authorizer, st store.Reader, q store.Query, record bool) (*store.Page, error) {
	actor, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	scope := store.ServerScope()
	if q.Scope != nil {
		scope = *q.Scope
	}
	perm := ReadPermission(q.Kind)
	err = allowed(ctx, authz, perm, auth.Target{Scope: scope})
	var r reach
	if err != nil && i18n.ErrForbidden.Is(err) && q.Scope == nil {
		var scopeErr error
		if r, scopeErr = grantScopes(ctx, authz, actor, perm); scopeErr != nil {
			return nil, scopeErr
		}
	}
	switch {
	case err == nil || r.all:
	case len(r.scopes) > 0:
		q.Scopes = r.scopes
	case i18n.ErrForbidden.Is(err):
		objects, ok := authz.(auth.ObjectAuthorizer)
		if !ok {
			if record {
				refused(ctx, perm, q.Kind, "", scope, err)
			}
			return nil, err
		}
		ids, objErr := objects.Objects(ctx, actor, perm, q.Scope)
		if objErr != nil {
			return nil, objErr
		}
		if len(ids) == 0 {
			if record {
				refused(ctx, perm, q.Kind, "", scope, err)
			}
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

// reach says what records the signed-in actor may read with a permission:
// all of them, or those of some owners.
type reach struct {
	all    bool
	scopes []store.Scope
}

// grantScopes returns what the actor's grants on whole scopes reach with
// perm, or with any permission when perm is empty: every scope, when a grant
// on the server's carries it, or the scopes other than the server's that
// grants reach. Authorizers that can list them (auth.ScopeAuthorizer)
// include the accounts of a reseller the actor holds a grant on; with
// others, a grant reaches its own scope.
func grantScopes(ctx context.Context, authz auth.Authorizer, actor *auth.Actor, perm auth.Permission) (reach, error) {
	if sa, ok := authz.(auth.ScopeAuthorizer); ok {
		scopes, all, err := sa.Scopes(ctx, actor, perm)
		if err != nil || all {
			return reach{all: all}, err
		}
		return reach{scopes: slices.DeleteFunc(scopes, func(sc store.Scope) bool { return sc.Level == store.ScopeServer })}, nil
	}
	var r reach
	for _, g := range actor.Grants {
		if g.Object != "" || slices.Contains(r.scopes, g.Scope) {
			continue
		}
		if perm != "" && authz.Authorize(ctx, actor, perm, auth.Target{Scope: g.Scope}) != nil {
			continue
		}
		if g.Scope.Level == store.ScopeServer {
			return reach{all: true}, nil
		}
		r.scopes = append(r.scopes, g.Scope)
	}
	return r, nil
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

// Readable keeps the resources the signed-in actor may read, in order. List
// uses it on every page; use it on its own for resources found some other
// way, such as the ones a search returns. What it leaves out isn't recorded
// as refused.
func Readable(ctx context.Context, authz auth.Authorizer, items []*store.Resource) ([]*store.Resource, error) {
	out := make([]*store.Resource, 0, len(items))
	for _, r := range items {
		err := allowed(ctx, authz, ReadPermission(r.Kind), auth.Target{Scope: r.Scope, Object: r.ID})
		switch {
		case err == nil:
			out = append(out, r)
		case !i18n.ErrForbidden.Is(err):
			return nil, err
		}
	}
	return out, nil
}
