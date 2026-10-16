package auth

import (
	"context"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Role is a named set of permissions.
type Role string

// Roles.
const (
	RoleAdmin        Role = "admin"
	RoleReseller     Role = "reseller"
	RoleOwner        Role = "owner"
	RoleCollaborator Role = "collaborator"
	RoleReadOnly     Role = "readonly"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleReseller, RoleOwner, RoleCollaborator, RoleReadOnly:
		return true
	}
	return false
}

// Permission names something an actor may do: "<kind>.<operation>" such as
// "web.site.create", "web.site.read" or "web.site.deploy", or a special
// permission.
type Permission string

// Special permissions. Wildcards in roles never cover them; they must be
// granted by name.
const (
	// PermOverride lets hooks loosen invariants such as sandbox minimums.
	PermOverride Permission = "core.override"
	// PermSecretsRead lets an add-on or token read secret values.
	PermSecretsRead Permission = "secrets.read"
	// PermImpersonate lets an admin or reseller view the panel as a customer.
	PermImpersonate Permission = "core.impersonate"
)

var special = []Permission{PermOverride, PermSecretsRead, PermImpersonate}

// KindPermission returns the permission for an operation on a kind.
func KindPermission(kind, operation string) Permission {
	return Permission(kind + "." + operation)
}

// Grant gives a role on a scope. Object narrows the grant to one resource,
// for per-object grants to collaborators. Permissions narrows a
// collaborator's grant, or adds special permissions to any grant.
type Grant struct {
	Role        Role         `json:"role"`
	Scope       store.Scope  `json:"scope"`
	Object      store.ID     `json:"object,omitempty"`
	Permissions []Permission `json:"permissions,omitempty"`
}

// ActorType says what kind of thing is acting.
type ActorType string

// Actor types.
const (
	ActorUser       ActorType = "user"
	ActorToken      ActorType = "token"
	ActorAutomation ActorType = "automation"
	ActorAddon      ActorType = "addon"
	ActorAssistant  ActorType = "assistant"
	ActorSystem     ActorType = "system"
)

// Source is the surface a request came through.
type Source string

// Sources.
const (
	SourceUI         Source = "ui"
	SourceCLI        Source = "cli"
	SourceAPI        Source = "api"
	SourceAutomation Source = "automation"
	SourceAddon      Source = "addon"
	SourceAssistant  Source = "assistant"
	SourceSystem     Source = "system"
)

// Actor is who is acting, and for whom. Audit records keep both, so an
// assistant action shows the person it acted for.
type Actor struct {
	Type ActorType `json:"type"`
	// ID is the user, token, automation or add-on; empty for the system.
	ID   store.ID `json:"id,omitempty"`
	Name string   `json:"name"`
	// User is the person responsible when something acts for them, such as
	// the owner of a token or the user talking to the assistant.
	User   store.ID `json:"user,omitempty"`
	Source Source   `json:"source"`
	// Impersonator is set while an admin or reseller views as a customer.
	Impersonator store.ID `json:"impersonator,omitempty"`
	IP           string   `json:"ip,omitempty"`
	// Grants are what the actor may do. They are never serialized.
	Grants []Grant `json:"-"`
	// SessionID identifies the session for revocation; never serialized.
	SessionID string `json:"-"`
	// SteppedUpAt is when the actor last confirmed their identity again for a
	// sensitive action.
	SteppedUpAt time.Time `json:"-"`
}

// String describes the actor for logs and audit records, such as
// "user:user_01... via assistant".
func (a *Actor) String() string {
	if a == nil {
		return "anonymous"
	}
	s := string(a.Type)
	if a.ID != "" {
		s += ":" + string(a.ID)
	} else if a.Name != "" {
		s += ":" + a.Name
	}
	if a.User != "" && a.User != a.ID {
		s += " for user:" + string(a.User)
	}
	if a.Source != "" && a.Source != Source(a.Type) {
		s += " via " + string(a.Source)
	}
	return s
}

// Clone returns a copy of the actor whose grants can be changed without
// affecting a, for code that hands the actor to something it doesn't trust
// to leave it alone, such as hooks.
func (a *Actor) Clone() *Actor {
	if a == nil {
		return nil
	}
	c := *a
	if a.Grants != nil {
		c.Grants = make([]Grant, len(a.Grants))
		for i, g := range a.Grants {
			g.Permissions = slices.Clone(g.Permissions)
			c.Grants[i] = g
		}
	}
	return &c
}

// SteppedUpWithin reports whether the actor confirmed their identity again
// within d, as sensitive actions require.
func (a *Actor) SteppedUpWithin(d time.Duration, now time.Time) bool {
	return a != nil && !a.SteppedUpAt.IsZero() && now.Sub(a.SteppedUpAt) <= d
}

// System returns the actor for work Racksail does on its own, such as
// periodic reconciles. name says which part acted, for example
// "reconciler".
func System(name string) *Actor {
	return &Actor{Type: ActorSystem, Name: name, Source: SourceSystem, Grants: []Grant{{Role: RoleAdmin, Scope: store.ServerScope()}}}
}

type actorKey struct{}

// WithActor returns a context carrying the actor.
func WithActor(ctx context.Context, a *Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor carried by ctx.
func ActorFrom(ctx context.Context) (*Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(*Actor)
	return a, ok && a != nil
}

// Authenticator identifies the caller of an HTTP request.
type Authenticator interface {
	// Authenticate returns (nil, nil) when the request carries no
	// credentials, and an error when it carries credentials that aren't
	// valid.
	Authenticate(ctx context.Context, r *http.Request) (*Actor, error)
}

// Target is what a permission is checked against.
type Target struct {
	Scope store.Scope
	// Object is the resource, when there is one, for per-object grants.
	Object store.ID
}

// Authorizer decides whether an actor may use a permission on a target.
type Authorizer interface {
	// Authorize returns nil when allowed and a core.forbidden error otherwise.
	Authorize(ctx context.Context, actor *Actor, perm Permission, target Target) error
}

// ObjectAuthorizer is implemented by authorizers that grant permissions on
// single resources. A list uses it when no grant covers the scope it lists,
// to find the resources the actor may still see there.
type ObjectAuthorizer interface {
	// Objects returns the resources the actor holds perm on through grants
	// on single resources, within scope, or anywhere when scope is nil.
	Objects(ctx context.Context, actor *Actor, perm Permission, scope *store.Scope) ([]store.ID, error)
}

// ScopeAuthorizer is implemented by authorizers that can say which scopes
// an actor's grants on whole scopes cover, so a list of every scope can be
// narrowed to those instead of filtering what it reads.
type ScopeAuthorizer interface {
	// Scopes returns the scopes the actor's grants on whole scopes cover
	// for perm, or for any permission when perm is empty, sorted: a
	// reseller's grant covers the reseller and, when the ownership can list
	// them, its customers' accounts. all says a grant covers every scope.
	Scopes(ctx context.Context, actor *Actor, perm Permission) (scopes []store.Scope, all bool, err error)
}

// Ownership answers questions about tenancy the authorizer needs.
type Ownership interface {
	// ResellerOf returns the reseller an account belongs to, or "" when the
	// account belongs directly to the server.
	ResellerOf(ctx context.Context, account store.ID) (store.ID, error)
}

// AccountLister is implemented by an Ownership that can also list the
// accounts that belong to a reseller, which lists, the timeline and the
// audit log need to show a reseller their customers' records.
type AccountLister interface {
	AccountsOf(ctx context.Context, reseller store.ID) ([]store.ID, error)
}

// RolePermissions lists the permission patterns of each role. Patterns are
// globs over permission names: "*" matches everything (except special
// permissions) and "*.read" matches every read.
var RolePermissions = map[Role][]string{
	RoleAdmin:        {"*"},
	RoleReseller:     {"*"},
	RoleOwner:        {"*"},
	RoleCollaborator: {"*.read"},
	RoleReadOnly:     {"*.read"},
}

// serverOnly are permissions that only server-wide grants carry, even when a
// reseller or owner pattern would match.
var serverOnly = []string{"core.server.*", "core.module.*", "network.*", "storage.*", "cluster.*", "core.reseller.*"}

// RoleAuthorizer is the default Authorizer: roles map to permission
// patterns, and grants map roles to scopes.
type RoleAuthorizer struct {
	mu     sync.RWMutex
	owners Ownership
}

var (
	_ Authorizer       = (*RoleAuthorizer)(nil)
	_ ObjectAuthorizer = (*RoleAuthorizer)(nil)
	_ ScopeAuthorizer  = (*RoleAuthorizer)(nil)
)

// NewRoleAuthorizer returns an authorizer that asks owners about tenancy.
// owners may be nil until the module that keeps resellers sets it with
// SetOwnership; until then reseller grants cover only the reseller's own
// scope, not their customers' accounts.
func NewRoleAuthorizer(owners Ownership) *RoleAuthorizer {
	return &RoleAuthorizer{owners: owners}
}

// SetOwnership tells the authorizer which reseller each account belongs to.
// The panel calls it at startup with what the module that keeps resellers
// provides.
func (r *RoleAuthorizer) SetOwnership(owners Ownership) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners = owners
}

func (r *RoleAuthorizer) ownership() Ownership {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.owners
}

// Authorize implements Authorizer.
func (r *RoleAuthorizer) Authorize(ctx context.Context, actor *Actor, perm Permission, target Target) error {
	if actor == nil {
		return i18n.ErrUnauthenticated.New()
	}
	for _, g := range actor.Grants {
		if !g.Role.Valid() || !permits(g, perm) {
			continue
		}
		if g.Object != "" && g.Object != target.Object {
			continue
		}
		covers, err := r.covers(ctx, g.Scope, target.Scope)
		if err != nil {
			return err
		}
		if covers {
			return nil
		}
	}
	return i18n.ErrForbidden.New("action", string(perm))
}

// Objects implements ObjectAuthorizer: the objects of the actor's grants on
// single resources that carry perm, sorted. Grants on whole scopes don't
// count; Authorize covers those.
func (r *RoleAuthorizer) Objects(ctx context.Context, actor *Actor, perm Permission, scope *store.Scope) ([]store.ID, error) {
	if actor == nil {
		return nil, i18n.ErrUnauthenticated.New()
	}
	var out []store.ID
	for _, g := range actor.Grants {
		if g.Object == "" || !g.Role.Valid() || !permits(g, perm) {
			continue
		}
		if scope != nil {
			covers, err := r.covers(ctx, g.Scope, *scope)
			if err != nil {
				return nil, err
			}
			if !covers {
				continue
			}
		}
		out = append(out, g.Object)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Scopes implements ScopeAuthorizer.
func (r *RoleAuthorizer) Scopes(ctx context.Context, actor *Actor, perm Permission) ([]store.Scope, bool, error) {
	if actor == nil {
		return nil, false, i18n.ErrUnauthenticated.New()
	}
	var out []store.Scope
	add := func(sc store.Scope) {
		if !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	for _, g := range actor.Grants {
		if g.Object != "" || !g.Role.Valid() || (perm != "" && !permits(g, perm)) {
			continue
		}
		switch g.Scope.Level {
		case store.ScopeServer:
			return nil, true, nil
		case store.ScopeReseller:
			add(g.Scope)
			if lister, ok := r.ownership().(AccountLister); ok {
				accounts, err := lister.AccountsOf(ctx, g.Scope.ID)
				if err != nil {
					return nil, false, err
				}
				for _, a := range accounts {
					add(store.AccountScope(a))
				}
			}
		case store.ScopeAccount:
			add(g.Scope)
		}
	}
	slices.SortFunc(out, func(a, b store.Scope) int {
		return strings.Compare(a.String(), b.String())
	})
	return out, false, nil
}

func permits(g Grant, perm Permission) bool {
	if slices.Contains(g.Permissions, perm) {
		return true
	}
	if slices.Contains(special, perm) {
		return false
	}
	patterns := RolePermissions[g.Role]
	if g.Role == RoleCollaborator && len(g.Permissions) > 0 {
		patterns = nil
		for _, p := range g.Permissions {
			patterns = append(patterns, string(p))
		}
	}
	if g.Scope.Level != store.ScopeServer && matchAny(serverOnly, string(perm)) {
		return false
	}
	return matchAny(patterns, string(perm))
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
		// "*" in a pattern also spans dots, so "*.read" covers
		// "web.site.read".
		if strings.HasPrefix(p, "*.") && strings.HasSuffix(name, p[1:]) {
			return true
		}
	}
	return false
}

func (r *RoleAuthorizer) covers(ctx context.Context, grant, target store.Scope) (bool, error) {
	switch grant.Level {
	case store.ScopeServer:
		return true, nil
	case store.ScopeReseller:
		if target.Level == store.ScopeReseller {
			return target.ID == grant.ID, nil
		}
		if owners := r.ownership(); target.Level == store.ScopeAccount && owners != nil {
			reseller, err := owners.ResellerOf(ctx, target.ID)
			if err != nil {
				return false, err
			}
			return reseller == grant.ID, nil
		}
	case store.ScopeAccount:
		return target.Level == store.ScopeAccount && target.ID == grant.ID, nil
	}
	return false, nil
}

// DevAuthenticator treats every request as the server admin. It exists for
// `racksail dev`, which only listens on the loopback address of a
// developer's machine; the panel role never uses it.
type DevAuthenticator struct{}

// Authenticate implements Authenticator.
func (DevAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*Actor, error) {
	return &Actor{
		Type: ActorUser, Name: "developer", Source: SourceUI, IP: r.RemoteAddr,
		Grants: []Grant{{Role: RoleAdmin, Scope: store.ServerScope()}},
	}, nil
}

// NoAuthenticator finds no credentials in any request. The panel uses it
// until accounts exist, so every protected operation answers 401.
type NoAuthenticator struct{}

// Authenticate implements Authenticator.
func (NoAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*Actor, error) {
	return nil, nil
}
