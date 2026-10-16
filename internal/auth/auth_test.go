package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type owners map[store.ID]store.ID

func (o owners) ResellerOf(ctx context.Context, account store.ID) (store.ID, error) {
	return o[account], nil
}

func TestAuthorize(t *testing.T) {
	ctx := context.Background()
	reseller, acctA, acctB, site := store.NewID("rsl"), store.NewID("acct"), store.NewID("acct"), store.NewID("site")
	az := NewRoleAuthorizer(owners{acctA: reseller})

	admin := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleAdmin, Scope: store.ServerScope()}}}
	res := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleReseller, Scope: store.ResellerScope(reseller)}}}
	owner := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleOwner, Scope: store.AccountScope(acctA)}}}
	viewer := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleReadOnly, Scope: store.AccountScope(acctA)}}}
	collab := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleCollaborator, Scope: store.AccountScope(acctA), Object: site, Permissions: []Permission{"web.site.deploy", "web.site.read"}}}}

	tests := []struct {
		name   string
		actor  *Actor
		perm   Permission
		target Target
		allow  bool
	}{
		{"admin anything", admin, "web.site.delete", Target{Scope: store.AccountScope(acctB)}, true},
		{"admin lacks override", admin, PermOverride, Target{Scope: store.ServerScope()}, false},
		{"reseller own account", res, "web.site.create", Target{Scope: store.AccountScope(acctA)}, true},
		{"reseller other account", res, "web.site.create", Target{Scope: store.AccountScope(acctB)}, false},
		{"reseller server kinds", res, "storage.pool.create", Target{Scope: store.AccountScope(acctA)}, false},
		{"owner own account", owner, "mail.mailbox.create", Target{Scope: store.AccountScope(acctA)}, true},
		{"owner other account", owner, "mail.mailbox.read", Target{Scope: store.AccountScope(acctB)}, false},
		{"owner server scope", owner, "web.site.read", Target{Scope: store.ServerScope()}, false},
		{"viewer reads", viewer, "web.site.read", Target{Scope: store.AccountScope(acctA)}, true},
		{"viewer writes", viewer, "web.site.update", Target{Scope: store.AccountScope(acctA)}, false},
		{"collaborator granted object", collab, "web.site.deploy", Target{Scope: store.AccountScope(acctA), Object: site}, true},
		{"collaborator other object", collab, "web.site.deploy", Target{Scope: store.AccountScope(acctA), Object: store.NewID("site")}, false},
		{"collaborator ungranted perm", collab, "web.site.delete", Target{Scope: store.AccountScope(acctA), Object: site}, false},
		{"nobody", nil, "web.site.read", Target{Scope: store.ServerScope()}, false},
	}
	for _, tt := range tests {
		err := az.Authorize(ctx, tt.actor, tt.perm, tt.target)
		if (err == nil) != tt.allow {
			t.Errorf("%s: err = %v, want allowed %v", tt.name, err, tt.allow)
		}
		if err != nil && tt.actor != nil && !errors.Is(err, i18n.ErrForbidden) {
			t.Errorf("%s: denial should be core.forbidden, got %v", tt.name, err)
		}
	}
	overrider := &Actor{Grants: []Grant{{Role: RoleAdmin, Scope: store.ServerScope(), Permissions: []Permission{PermOverride}}}}
	if err := az.Authorize(ctx, overrider, PermOverride, Target{Scope: store.ServerScope()}); err != nil {
		t.Errorf("an explicit grant of core.override should allow it: %v", err)
	}
}

// What only grants on the whole server carry, a grant on a reseller or an
// account doesn't carry by naming it, whatever it is checked against.
func TestServerOnlyPermissionsStayWithTheServer(t *testing.T) {
	ctx := context.Background()
	reseller, acct := store.NewID("rsl"), store.NewID("acct")
	az := NewRoleAuthorizer(owners{acct: reseller})
	for _, scope := range []store.Scope{store.ResellerScope(reseller), store.AccountScope(acct)} {
		named := &Actor{Grants: []Grant{{Role: RoleCollaborator, Scope: scope, Permissions: []Permission{"core.signin_policy.update", "storage.*"}}}}
		for _, target := range []store.Scope{scope, store.ServerScope()} {
			for _, perm := range []Permission{"core.signin_policy.update", "storage.pool.create"} {
				if err := az.Authorize(ctx, named, perm, Target{Scope: target}); err == nil {
					t.Errorf("a grant on %s naming %s carries it on %s", scope, perm, target)
				}
			}
		}
		if scopes, all, _ := az.Scopes(ctx, named, "core.signin_policy.update"); all || len(scopes) != 0 {
			t.Errorf("scopes of a grant on %s naming a server permission = %v, %v", scope, scopes, all)
		}
	}
	admin := &Actor{Grants: []Grant{{Role: RoleCollaborator, Scope: store.ServerScope(), Permissions: []Permission{"core.signin_policy.update"}}}}
	if err := az.Authorize(ctx, admin, "core.signin_policy.update", Target{Scope: store.ServerScope()}); err != nil {
		t.Errorf("a grant on the server naming it = %v", err)
	}
}

// The module that keeps resellers sets the ownership after the authorizer
// is built; from then on a reseller's grant covers their customers.
func TestSetOwnership(t *testing.T) {
	ctx := context.Background()
	reseller, customer := store.NewID("rsl"), store.NewID("acct")
	az := NewRoleAuthorizer(nil)
	res := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleReseller, Scope: store.ResellerScope(reseller)}}}
	target := Target{Scope: store.AccountScope(customer)}
	if err := az.Authorize(ctx, res, "web.site.read", target); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("without ownership nothing says the account is the reseller's: %v", err)
	}
	az.SetOwnership(owners{customer: reseller})
	if err := az.Authorize(ctx, res, "web.site.read", target); err != nil {
		t.Errorf("with ownership the reseller reaches their customer: %v", err)
	}
	if err := az.Authorize(ctx, res, "web.site.read", Target{Scope: store.AccountScope(store.NewID("acct"))}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("but not other accounts: %v", err)
	}
}

// listedOwners can also list each reseller's accounts.
type listedOwners struct{ owners }

func (o listedOwners) AccountsOf(ctx context.Context, reseller store.ID) ([]store.ID, error) {
	var out []store.ID
	for account, r := range o.owners {
		if r == reseller {
			out = append(out, account)
		}
	}
	return out, nil
}

func TestScopes(t *testing.T) {
	ctx := context.Background()
	reseller, customer, own, other := store.NewID("rsl"), store.NewID("acct"), store.NewID("acct"), store.NewID("acct")
	az := NewRoleAuthorizer(owners{customer: reseller})
	actor := &Actor{Type: ActorUser, Grants: []Grant{
		{Role: RoleReseller, Scope: store.ResellerScope(reseller)},
		{Role: RoleReadOnly, Scope: store.AccountScope(own)},
		{Role: RoleCollaborator, Scope: store.AccountScope(other), Object: store.NewID("site")},
	}}
	scopes, all, err := az.Scopes(ctx, actor, "web.site.read")
	if err != nil || all || len(scopes) != 2 || !slices.Contains(scopes, store.ResellerScope(reseller)) || !slices.Contains(scopes, store.AccountScope(own)) {
		t.Errorf("without a list of accounts = %v, %v, %v", scopes, all, err)
	}
	az.SetOwnership(listedOwners{owners{customer: reseller}})
	scopes, _, _ = az.Scopes(ctx, actor, "web.site.read")
	if len(scopes) != 3 || !slices.Contains(scopes, store.AccountScope(customer)) {
		t.Errorf("a reseller's grant reaches their customers = %v", scopes)
	}
	if scopes, _, _ := az.Scopes(ctx, actor, "web.site.delete"); len(scopes) != 2 || slices.Contains(scopes, store.AccountScope(own)) {
		t.Errorf("only grants that carry the permission count = %v", scopes)
	}
	if scopes, _, _ := az.Scopes(ctx, actor, ""); len(scopes) != 3 {
		t.Errorf("any permission = %v", scopes)
	}
	admin := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleAdmin, Scope: store.ServerScope()}}}
	if _, all, _ := az.Scopes(ctx, admin, "web.site.read"); !all {
		t.Error("a grant on the server covers everything")
	}
	if _, _, err := az.Scopes(ctx, nil, "web.site.read"); !errors.Is(err, i18n.ErrUnauthenticated) {
		t.Errorf("nobody: %v", err)
	}
}

func TestObjects(t *testing.T) {
	ctx := context.Background()
	acctA, acctB := store.NewID("acct"), store.NewID("acct")
	siteA, siteB, other := store.NewID("site"), store.NewID("site"), store.NewID("site")
	az := NewRoleAuthorizer(nil)
	collab := &Actor{Type: ActorUser, Grants: []Grant{
		{Role: RoleCollaborator, Scope: store.AccountScope(acctA), Object: siteA, Permissions: []Permission{"web.site.read", "web.site.deploy"}},
		{Role: RoleCollaborator, Scope: store.AccountScope(acctB), Object: siteB, Permissions: []Permission{"web.site.read"}},
		{Role: RoleCollaborator, Scope: store.AccountScope(acctA), Object: other, Permissions: []Permission{"web.site.deploy"}},
		{Role: RoleOwner, Scope: store.AccountScope(store.NewID("acct"))},
	}}
	scopeA := store.AccountScope(acctA)
	if got, err := az.Objects(ctx, collab, "web.site.read", &scopeA); err != nil || len(got) != 1 || got[0] != siteA {
		t.Errorf("objects in one account = %v, %v", got, err)
	}
	all, err := az.Objects(ctx, collab, "web.site.read", nil)
	if err != nil || len(all) != 2 {
		t.Errorf("objects anywhere = %v, %v", all, err)
	}
	if got, _ := az.Objects(ctx, collab, "web.site.delete", nil); len(got) != 0 {
		t.Errorf("grants without the permission don't count: %v", got)
	}
	if _, err := az.Objects(ctx, nil, "web.site.read", nil); !errors.Is(err, i18n.ErrUnauthenticated) {
		t.Errorf("nobody: %v", err)
	}
}

func TestActor(t *testing.T) {
	user, token := store.NewID("user"), store.NewID("tok")
	a := &Actor{Type: ActorToken, ID: token, User: user, Source: SourceCLI}
	if got := a.String(); got != "token:"+string(token)+" for user:"+string(user)+" via cli" {
		t.Errorf("String() = %q", got)
	}
	ctx := WithActor(context.Background(), a)
	if got, ok := ActorFrom(ctx); !ok || got != a {
		t.Error("the actor should travel in the context")
	}
	if _, ok := ActorFrom(context.Background()); ok {
		t.Error("an empty context has no actor")
	}
	now := time.Now()
	a.SteppedUpAt = now.Add(-2 * time.Minute)
	if !a.SteppedUpWithin(5*time.Minute, now) || a.SteppedUpWithin(time.Minute, now) {
		t.Error("step-up window is wrong")
	}
	if System("reconciler").String() != "system:reconciler" {
		t.Errorf("system actor = %q", System("reconciler"))
	}
	a.Grants = []Grant{{Role: RoleCollaborator, Scope: store.ServerScope(), Permissions: []Permission{"web.site.read"}}}
	c := a.Clone()
	c.Grants[0].Permissions[0] = "web.site.delete"
	c.Grants = append(c.Grants, Grant{Role: RoleAdmin, Scope: store.ServerScope()})
	if len(a.Grants) != 1 || a.Grants[0].Permissions[0] != "web.site.read" || c.ID != a.ID {
		t.Errorf("changing a copy changed the actor: %+v", a.Grants)
	}
}

func TestAuthenticators(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:63049"
	a, err := DevAuthenticator{}.Authenticate(context.Background(), req)
	if err != nil || a == nil || a.Grants[0].Role != RoleAdmin {
		t.Errorf("dev = %+v, %v", a, err)
	}
	// Records of what the developer does say where from without the port,
	// and through what.
	if a.IP != "127.0.0.1" || a.Source != SourceAPI {
		t.Errorf("dev without the API's client = %s via %s", a.IP, a.Source)
	}
	req.Header.Set("User-Agent", "racksail/1.4.0 (darwin/arm64)")
	if a, _ := (DevAuthenticator{}).Authenticate(WithClient(context.Background(), Client{IP: "::1"}), req); a.IP != "::1" || a.Source != SourceCLI {
		t.Errorf("dev behind the API = %s via %s", a.IP, a.Source)
	}
	if a, err := (NoAuthenticator{}).Authenticate(context.Background(), req); a != nil || err != nil {
		t.Error("NoAuthenticator finds nobody")
	}
}

func TestSourceOf(t *testing.T) {
	for _, tc := range []struct {
		header map[string]string
		want   Source
	}{
		{map[string]string{"User-Agent": "racksail/0.0.0-dev (linux/amd64)"}, SourceCLI},
		{map[string]string{"User-Agent": "Mozilla/5.0", "Sec-Fetch-Site": "same-origin"}, SourceUI},
		{map[string]string{"User-Agent": "Mozilla/5.0", "Sec-Fetch-Site": "none"}, SourceUI},
		{map[string]string{"User-Agent": "curl/8.14.1"}, SourceAPI},
		{map[string]string{"User-Agent": "Mozilla/5.0"}, SourceAPI},
		{nil, SourceAPI},
	} {
		req := httptest.NewRequest("GET", "/api/v1/sites", nil)
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		if got := SourceOf(req); got != tc.want {
			t.Errorf("%v = %s, want %s", tc.header, got, tc.want)
		}
	}
	if HostOf("[::1]:7407") != "::1" || HostOf("203.0.113.7") != "203.0.113.7" {
		t.Error("HostOf keeps the port or loses the address")
	}
}
