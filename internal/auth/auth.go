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

// Special reports whether perm is a special permission, which roles never
// carry through their patterns.
func (p Permission) Special() bool { return slices.Contains(special, p) }

// KindPermission returns the permission for an operation on a kind.
func KindPermission(kind, operation string) Permission {
	return Permission(kind + "." + operation)
}

// Grant gives a role on a scope. Object narrows the grant to one resource,
// for per-object grants to collaborators. Permissions narrows a
// collaborator's grant, or adds special permissions to any grant. Limit
// narrows any grant to the permissions its patterns match, as an API token
// scoped to some permissions narrows its owner's grants.
type Grant struct {
	Role        Role         `json:"role" enum:"admin,reseller,owner,collaborator,readonly" doc:"What the grant lets its holder do"`
	Scope       store.Scope  `json:"scope" doc:"Where the grant applies: the whole server, a reseller and their customers, or one account"`
	Object      store.ID     `json:"object,omitempty" doc:"A single resource the grant is limited to, for collaborators"`
	Permissions []Permission `json:"permissions,omitempty" doc:"For collaborators, the permissions they get instead of read-only access; for other roles, special permissions added by name"`
	Limit       []string     `json:"limit,omitempty" doc:"Patterns the grant's permissions are narrowed to"`
}

// Clone returns a copy of g whose lists can be changed without affecting g.
func (g Grant) Clone() Grant {
	g.Permissions = slices.Clone(g.Permissions)
	g.Limit = slices.Clone(g.Limit)
	return g
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
	// UserName and ImpersonatorName are the names of User and Impersonator,
	// so the records of what the actor does say who without looking them
	// up, as they were called then.
	UserName         string `json:"userName,omitempty"`
	ImpersonatorName string `json:"impersonatorName,omitempty"`
	IP               string `json:"ip,omitempty"`
	// Grants are what the actor may do. They are never serialized.
	Grants []Grant `json:"-"`
	// SessionID identifies the session for revocation; never serialized.
	SessionID string `json:"-"`
	// SteppedUpAt is when the actor last confirmed their identity again for a
	// sensitive action.
	SteppedUpAt time.Time `json:"-"`
	// Pending is what a signed-in person must do before anything else, such
	// as choosing a new password; the API refuses everything else until
	// they do.
	Pending string `json:"-"`
	// Limit and Accounts are what an API token was narrowed to: patterns of
	// permissions and the accounts it may act on. Its grants carry them
	// already; they keep what everyone may do to their own user to them
	// too. Never serialized.
	Limit    []string   `json:"-"`
	Accounts []store.ID `json:"-"`
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
	if a.Impersonator != "" {
		s += " impersonated by user:" + string(a.Impersonator)
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
			c.Grants[i] = g.Clone()
		}
	}
	c.Limit, c.Accounts = slices.Clone(a.Limit), slices.Clone(a.Accounts)
	return &c
}

// SteppedUpWithin reports whether the actor confirmed their identity again
// within d, as sensitive actions require.
func (a *Actor) SteppedUpWithin(d time.Duration, now time.Time) bool {
	return a != nil && !a.SteppedUpAt.IsZero() && now.Sub(a.SteppedUpAt) <= d
}

// Person returns the user the actor acts as or for: the user itself, or the
// owner of a token, an automation or the assistant's conversation.
func (a *Actor) Person() store.ID {
	if a == nil {
		return ""
	}
	if a.Type == ActorUser {
		return a.ID
	}
	return a.User
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

type authorizerKey struct{}

// WithAuthorizer returns a context carrying the authorizer that decides the
// request, so validation that depends on what the actor may do, such as
// which roles they may hand out, asks the same authorizer.
func WithAuthorizer(ctx context.Context, a Authorizer) context.Context {
	if a == nil {
		return ctx
	}
	return context.WithValue(ctx, authorizerKey{}, a)
}

// AuthorizerFrom returns the authorizer carried by ctx.
func AuthorizerFrom(ctx context.Context) (Authorizer, bool) {
	a, ok := ctx.Value(authorizerKey{}).(Authorizer)
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

// GrantChecker is implemented by authorizers that can tell whether an actor
// may hand a grant to someone else: only what the actor may do themselves,
// on scopes their own grants cover.
type GrantChecker interface {
	CanGrant(ctx context.Context, actor *Actor, g Grant) error
	// Covers reports whether a grant on outer reaches inner: the server
	// reaches everything, a reseller reaches itself and its customers'
	// accounts, and an account reaches itself.
	Covers(ctx context.Context, outer, inner store.Scope) (bool, error)
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
var serverOnly = []string{"core.server.*", "core.module.*", "network.*", "storage.*", "cluster.*", "core.reseller.*", "core.signin_policy.*", "core.signin_provider.*"}

// ServerOnly reports whether only grants on the whole server carry perm,
// even by name.
func ServerOnly(perm Permission) bool { return matchAny(serverOnly, string(perm)) }

// AboveAccount reports whether grants on an account never carry perm even by
// name: changing the account itself, such as its limits, which its own
// people may only read. Resellers and admins change accounts.
func AboveAccount(perm Permission) bool {
	name := string(perm)
	return strings.HasPrefix(name, "core.account.") && name != "core.account.read"
}

// carriedAt reports whether a grant on a scope of the given level can carry
// a permission, or the permissions a pattern matches, at all, whatever its
// role or the permissions it names.
func carriedAt(level store.ScopeLevel, perm Permission) bool {
	switch {
	case level != store.ScopeServer && ServerOnly(perm):
		return false
	case level == store.ScopeAccount && AboveAccount(perm):
		return false
	}
	return true
}

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
	_ GrantChecker     = (*RoleAuthorizer)(nil)
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

// What everyone may do to their own user, whatever their roles.
const (
	PermUserUpdateProfile Permission = "core.user.update_profile"
	PermUserAddSSHKey     Permission = "core.user.add_ssh_key"
	PermUserRemoveSSHKey  Permission = "core.user.remove_ssh_key"
)

// SelfPermissions are what everyone may do to their own user, whatever
// their roles: see it, change their name, email address and language, and
// add and remove their SSH keys. Only people signed in to the panel change
// their user this way, not API tokens and not someone viewing the panel as
// them; changing roles, turning a user off and the rest take a grant.
var SelfPermissions = []Permission{PermUserRead, PermUserUpdateProfile, PermUserAddSSHKey, PermUserRemoveSSHKey}

// SelfAllowed reports whether the actor may use perm on their own user
// because it's theirs, whatever their grants: one of SelfPermissions, and
// beyond seeing it, only as a person signed in to the panel who isn't
// viewing it as someone else. An API token narrowed to some permissions or
// accounts gets no more of its user than its narrowing allows: its grants
// decide.
func SelfAllowed(actor *Actor, perm Permission) bool {
	if actor == nil || !slices.Contains(SelfPermissions, perm) {
		return false
	}
	if len(actor.Accounts) > 0 || (len(actor.Limit) > 0 && !matchAny(actor.Limit, string(perm))) {
		return false
	}
	return perm == PermUserRead || (actor.Type == ActorUser && actor.Impersonator == "")
}

// ownUser reports whether the actor may use perm on target because target
// is their own user.
func ownUser(actor *Actor, perm Permission, target Target) bool {
	return target.Object != "" && target.Object == actor.Person() && SelfAllowed(actor, perm)
}

// Authorize implements Authorizer.
func (r *RoleAuthorizer) Authorize(ctx context.Context, actor *Actor, perm Permission, target Target) error {
	if actor == nil {
		return i18n.ErrUnauthenticated.New()
	}
	if ownUser(actor, perm, target) {
		return nil
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

// Covers implements GrantChecker.
func (r *RoleAuthorizer) Covers(ctx context.Context, outer, inner store.Scope) (bool, error) {
	return r.covers(ctx, outer, inner)
}

// CanGrant implements GrantChecker. One of the actor's own grants has to
// reach the grant's scope and let the actor do everything the grant would
// let its holder do there; special permissions pass on only from grants
// that carry them by name. A grant on one resource only passes on rights to
// that resource.
func (r *RoleAuthorizer) CanGrant(ctx context.Context, actor *Actor, g Grant) error {
	if actor == nil {
		return i18n.ErrUnauthenticated.New()
	}
	if err := g.Validate(); err != nil {
		return err
	}
	for _, a := range actor.Grants {
		if !a.Role.Valid() || (a.Object != "" && a.Object != g.Object) {
			continue
		}
		covers, err := r.covers(ctx, a.Scope, g.Scope)
		if err != nil {
			return err
		}
		if covers && grantable(a, g) {
			return nil
		}
	}
	return ErrGrantNotAllowed.New("role", string(g.Role), "scope", g.Scope.String())
}

// patternsOf returns what a grant lets its holder do, as permission
// patterns, before Limit: a collaborator's permissions when it has any, and
// otherwise its role's patterns.
func patternsOf(g Grant) []string {
	if g.Role == RoleCollaborator && len(g.Permissions) > 0 {
		out := make([]string, 0, len(g.Permissions))
		for _, p := range g.Permissions {
			if !p.Special() {
				out = append(out, string(p))
			}
		}
		return out
	}
	return RolePermissions[g.Role]
}

// grantable reports whether everything g lets its holder do, a lets its
// holder do too. What a's scope never carries, such as what belongs to the
// whole server on a reseller's grant, a's patterns don't pass on.
func grantable(a, g Grant) bool {
	for _, p := range g.Permissions {
		if p.Special() && !slices.Contains(a.Permissions, p) {
			return false
		}
	}
	held := patternsOf(a)
	covered := func(patterns []string) bool {
		for _, p := range patterns {
			if !coveredBy(held, p) || (len(a.Limit) > 0 && !coveredBy(a.Limit, p)) || !carriedAt(a.Scope.Level, Permission(p)) {
				return false
			}
		}
		return true
	}
	// A grant narrowed by Limit gives no more than its limit, so either
	// list being covered is enough.
	return covered(patternsOf(g)) || (len(g.Limit) > 0 && covered(g.Limit))
}

// coveredBy reports whether every permission p matches is matched by one of
// held. It is exact for the forms roles and tokens use: names, "*", "*.<op>"
// and "<prefix>.*"; anything it can't decide counts as not covered.
func coveredBy(held []string, p string) bool {
	for _, h := range held {
		switch {
		case h == p, h == "*":
			return true
		case strings.HasPrefix(h, "*.") && !strings.ContainsAny(h[2:], "*?"):
			suffix := h[1:]
			if p == h || (!strings.ContainsAny(p, "*?") && strings.HasSuffix(p, suffix)) {
				return true
			}
		case strings.HasSuffix(h, ".*") && !strings.ContainsAny(h[:len(h)-2], "*?"):
			if strings.HasPrefix(p, h[:len(h)-1]) {
				return true
			}
		case !strings.ContainsAny(p, "*?"):
			if ok, _ := path.Match(h, p); ok {
				return true
			}
		}
	}
	return false
}

// Validate checks that a grant makes sense: a known role on a scope of the
// level the role belongs to, permissions that are names or patterns, and an
// object only where single resources can be granted.
func (g Grant) Validate() error {
	var fields []i18n.FieldError
	if !g.Role.Valid() {
		fields = append(fields, i18n.Field("role", i18n.ErrBadValue, "reason", "use admin, reseller, owner, collaborator or readonly"))
	}
	if err := g.Scope.Validate(); err != nil {
		fields = append(fields, i18n.Field("scope", i18n.ErrBadValue, "reason", err.Error()))
	} else if g.Role.Valid() && !slices.Contains(RoleLevels(g.Role), g.Scope.Level) {
		fields = append(fields, i18n.Field("scope", i18n.ErrBadValue, "reason", "the "+string(g.Role)+" role applies to "+levelsText(RoleLevels(g.Role))))
	}
	if g.Object != "" {
		if !g.Object.Valid() {
			fields = append(fields, i18n.Field("object", i18n.ErrBadValue, "reason", "it isn't a resource ID"))
		} else if g.Role != RoleCollaborator && g.Role != RoleReadOnly {
			fields = append(fields, i18n.Field("object", i18n.ErrBadValue, "reason", "only collaborators and read-only users are given single resources"))
		}
	}
	for i, p := range g.Permissions {
		switch {
		case !validPattern(string(p)):
			fields = append(fields, i18n.Field("permissions["+itoa(i)+"]", i18n.ErrBadValue, "reason", "use a permission name such as web.site.deploy, or a pattern such as web.*"))
		case p == PermOverride && (g.Role != RoleAdmin || g.Scope.Level != store.ScopeServer):
			// Loosening what protects every account is for server admins.
			fields = append(fields, i18n.Field("permissions["+itoa(i)+"]", i18n.ErrBadValue, "reason", "only an admin's grant on the whole server can carry core.override"))
		case g.Scope.Level.Valid() && g.Scope.Level != store.ScopeServer && ServerOnly(p):
			// Named or not, it would never let its holder do anything there.
			fields = append(fields, i18n.Field("permissions["+itoa(i)+"]", i18n.ErrBadValue, "reason", "only grants on the whole server carry "+string(p)))
		case g.Scope.Level == store.ScopeAccount && AboveAccount(p):
			fields = append(fields, i18n.Field("permissions["+itoa(i)+"]", i18n.ErrBadValue, "reason", "grants on an account never carry "+string(p)+"; resellers and admins change accounts"))
		}
	}
	for i, p := range g.Limit {
		if !validPattern(p) || Permission(p).Special() {
			fields = append(fields, i18n.Field("limit["+itoa(i)+"]", i18n.ErrBadValue, "reason", "use a permission name or a pattern such as *.read"))
		}
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// RoleLevels returns the scope levels a role can be granted on.
func RoleLevels(r Role) []store.ScopeLevel {
	switch r {
	case RoleAdmin:
		return []store.ScopeLevel{store.ScopeServer}
	case RoleReseller:
		return []store.ScopeLevel{store.ScopeReseller}
	case RoleOwner:
		return []store.ScopeLevel{store.ScopeAccount}
	case RoleCollaborator, RoleReadOnly:
		return []store.ScopeLevel{store.ScopeServer, store.ScopeReseller, store.ScopeAccount}
	}
	return nil
}

func levelsText(levels []store.ScopeLevel) string {
	words := map[store.ScopeLevel]string{store.ScopeServer: "the whole server", store.ScopeReseller: "a reseller", store.ScopeAccount: "an account"}
	parts := make([]string, len(levels))
	for i, l := range levels {
		parts[i] = words[l]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

// ValidPattern reports whether p is a permission name or a pattern of
// them, such as web.* or *.read.
func ValidPattern(p string) bool { return validPattern(p) }

func validPattern(p string) bool {
	if p == "*" {
		return true
	}
	if len(p) == 0 || len(p) > 128 {
		return false
	}
	for _, part := range strings.Split(p, ".") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' && c != '*' && c != '?' && c != '-' {
				return false
			}
		}
	}
	return true
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

// permits reports whether a grant carries a permission. What a grant's scope
// never carries it doesn't, even when it names it, so the guard doesn't rest
// on such permissions only ever being checked against scopes the grant
// can't reach.
func permits(g Grant, perm Permission) bool {
	if !carriedAt(g.Scope.Level, perm) {
		return false
	}
	if len(g.Limit) > 0 && !matchAny(g.Limit, string(perm)) {
		return false
	}
	if slices.Contains(g.Permissions, perm) {
		return true
	}
	if perm.Special() {
		return false
	}
	patterns := RolePermissions[g.Role]
	if g.Role == RoleCollaborator && len(g.Permissions) > 0 {
		patterns = nil
		for _, p := range g.Permissions {
			patterns = append(patterns, string(p))
		}
	}
	return matchAny(patterns, string(perm))
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if p == name {
			return true
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
		// "*" in a pattern also spans dots, so "*.read" covers
		// "web.site.read" and "web.*" covers "web.site.read".
		if strings.HasPrefix(p, "*.") && strings.HasSuffix(name, p[1:]) {
			return true
		}
		if strings.HasSuffix(p, ".*") && strings.HasPrefix(name, p[:len(p)-1]) {
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

// NarrowLimit returns the patterns a grant already limited to limit is
// narrowed to by more: those of more that limit covers. An empty limit is
// no limit. ok is false when nothing is left.
func NarrowLimit(limit, more []string) (out []string, ok bool) {
	if len(more) == 0 {
		return slices.Clone(limit), true
	}
	if len(limit) == 0 {
		return slices.Clone(more), true
	}
	for _, p := range more {
		if coveredBy(limit, p) {
			out = append(out, p)
		}
	}
	for _, p := range limit {
		if coveredBy(more, p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, len(out) > 0
}

// Patterns returns what an actor may do somewhere, as permission names and
// patterns, for the UI to decide what to show. The server still checks
// every request against the scope it is about.
func Patterns(actor *Actor) []string {
	if actor == nil {
		return []string{}
	}
	var out []string
	add := func(p string) {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, g := range actor.Grants {
		if !g.Role.Valid() {
			continue
		}
		patterns := patternsOf(g)
		if len(g.Limit) > 0 {
			// The limit's own patterns are what the grant is narrowed to,
			// as long as the role's patterns reach them.
			patterns = slices.DeleteFunc(slices.Clone(g.Limit), func(p string) bool { return !coveredBy(patternsOf(g), p) })
		}
		for _, p := range patterns {
			add(p)
		}
		for _, p := range g.Permissions {
			if p.Special() {
				add(string(p))
			}
		}
	}
	slices.Sort(out)
	if out == nil {
		return []string{}
	}
	return out
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

// NoAuthenticator finds no credentials in any request, so every protected
// operation answers 401. The API server uses it when it is built without a
// way to sign anyone in, as when the API document is exported; the panel
// signs people in through the identity service.
type NoAuthenticator struct{}

// Authenticate implements Authenticator.
func (NoAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*Actor, error) {
	return nil, nil
}

// Client is where a request comes from, as the API worked it out: the
// address of the person's browser or program, behind the edge as well, and
// what it says it is.
type Client struct {
	IP        string
	UserAgent string
	// Secure says the request reached the panel over HTTPS.
	Secure bool
}

type clientKey struct{}

// WithClient returns a context carrying where the request comes from.
func WithClient(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

// ClientFrom returns where the request carried by ctx comes from.
func ClientFrom(ctx context.Context) Client {
	c, _ := ctx.Value(clientKey{}).(Client)
	return c
}
