package auth

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func TestCanGrant(t *testing.T) {
	ctx := context.Background()
	reseller, customer, other := store.NewID("rsl"), store.NewID("acct"), store.NewID("acct")
	site := store.NewID("site")
	az := NewRoleAuthorizer(owners{customer: reseller})

	admin := &Actor{Grants: []Grant{{Role: RoleAdmin, Scope: store.ServerScope(), Permissions: []Permission{PermImpersonate}}}}
	res := &Actor{Grants: []Grant{{Role: RoleReseller, Scope: store.ResellerScope(reseller)}}}
	owner := &Actor{Grants: []Grant{{Role: RoleOwner, Scope: store.AccountScope(customer)}}}
	helper := &Actor{Grants: []Grant{{Role: RoleCollaborator, Scope: store.AccountScope(customer), Permissions: []Permission{"web.*", "core.user.create"}}}}
	siteHelper := &Actor{Grants: []Grant{{Role: RoleCollaborator, Scope: store.AccountScope(customer), Object: site, Permissions: []Permission{"web.site.read", "web.site.deploy"}}}}
	viewer := &Actor{Grants: []Grant{{Role: RoleReadOnly, Scope: store.ServerScope()}}}
	limited := &Actor{Grants: []Grant{{Role: RoleOwner, Scope: store.AccountScope(customer), Limit: []string{"*.read"}}}}

	ownerOf := func(acct store.ID) Grant { return Grant{Role: RoleOwner, Scope: store.AccountScope(acct)} }
	tests := []struct {
		name  string
		actor *Actor
		grant Grant
		allow bool
	}{
		{"admin gives admin", admin, Grant{Role: RoleAdmin, Scope: store.ServerScope()}, true},
		{"admin passes on impersonation", admin, Grant{Role: RoleAdmin, Scope: store.ServerScope(), Permissions: []Permission{PermImpersonate}}, true},
		{"admin can't pass on override", admin, Grant{Role: RoleAdmin, Scope: store.ServerScope(), Permissions: []Permission{PermOverride}}, false},
		{"reseller gives owner of customer", res, ownerOf(customer), true},
		{"reseller gives reseller of own", res, Grant{Role: RoleReseller, Scope: store.ResellerScope(reseller)}, true},
		{"reseller can't give owner elsewhere", res, ownerOf(other), false},
		{"reseller can't give admin", res, Grant{Role: RoleAdmin, Scope: store.ServerScope()}, false},
		{"reseller can't pass on impersonation", res, Grant{Role: RoleReseller, Scope: store.ResellerScope(reseller), Permissions: []Permission{PermImpersonate}}, false},
		{"owner gives owner", owner, ownerOf(customer), true},
		{"owner gives collaborator on a site", owner, Grant{Role: RoleCollaborator, Scope: store.AccountScope(customer), Object: site, Permissions: []Permission{"web.site.deploy"}}, true},
		{"owner can't reach another account", owner, ownerOf(other), false},
		{"collaborator can't give owner", helper, ownerOf(customer), false},
		{"collaborator gives within its permissions", helper, Grant{Role: RoleCollaborator, Scope: store.AccountScope(customer), Permissions: []Permission{"web.site.read"}}, true},
		{"collaborator can't widen", helper, Grant{Role: RoleCollaborator, Scope: store.AccountScope(customer), Permissions: []Permission{"mail.*"}}, false},
		{"collaborator can't give read-only on everything", helper, Grant{Role: RoleReadOnly, Scope: store.AccountScope(customer)}, false},
		{"site helper passes on the same site", siteHelper, Grant{Role: RoleCollaborator, Scope: store.AccountScope(customer), Object: site, Permissions: []Permission{"web.site.read"}}, true},
		{"site helper can't pass on the account", siteHelper, Grant{Role: RoleCollaborator, Scope: store.AccountScope(customer), Permissions: []Permission{"web.site.read"}}, false},
		{"viewer gives read-only", viewer, Grant{Role: RoleReadOnly, Scope: store.AccountScope(other)}, true},
		{"viewer can't give owner", viewer, ownerOf(other), false},
		{"limited owner gives read-only", limited, Grant{Role: RoleReadOnly, Scope: store.AccountScope(customer)}, true},
		{"limited owner can't give owner", limited, ownerOf(customer), false},
		{"limited owner gives a limited owner", limited, Grant{Role: RoleOwner, Scope: store.AccountScope(customer), Limit: []string{"web.site.read"}}, true},
		{"a role off its level", admin, Grant{Role: RoleOwner, Scope: store.ServerScope()}, false},
	}
	for _, tt := range tests {
		err := az.CanGrant(ctx, tt.actor, tt.grant)
		if (err == nil) != tt.allow {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if err := az.CanGrant(ctx, nil, ownerOf(customer)); !errors.Is(err, i18n.ErrUnauthenticated) {
		t.Errorf("nobody: %v", err)
	}
}

func TestLimitsAndAccountLevel(t *testing.T) {
	ctx := context.Background()
	acct := store.NewID("acct")
	az := NewRoleAuthorizer(nil)
	target := Target{Scope: store.AccountScope(acct), Object: acct}
	owner := &Actor{Grants: []Grant{{Role: RoleOwner, Scope: store.AccountScope(acct)}}}
	if err := az.Authorize(ctx, owner, "core.account.read", target); err != nil {
		t.Errorf("owners see their account: %v", err)
	}
	for _, perm := range []Permission{"core.account.update", "core.account.delete"} {
		if err := az.Authorize(ctx, owner, perm, target); err == nil {
			t.Errorf("owners can't %s", perm)
		}
	}
	named := &Actor{Grants: []Grant{{Role: RoleCollaborator, Scope: store.AccountScope(acct), Permissions: []Permission{"core.account.update"}}}}
	if err := az.Authorize(ctx, named, "core.account.update", target); err == nil {
		t.Error("naming an account permission doesn't carry it on an account")
	}
	token := &Actor{Grants: []Grant{{Role: RoleOwner, Scope: store.AccountScope(acct), Limit: []string{"web.*", "dns.zone.read"}}}}
	for perm, want := range map[Permission]bool{"web.site.deploy": true, "dns.zone.read": true, "dns.zone.update": false, "mail.mailbox.read": false} {
		if err := az.Authorize(ctx, token, perm, Target{Scope: store.AccountScope(acct)}); (err == nil) != want {
			t.Errorf("limited %s: %v", perm, err)
		}
	}
}

func TestGrantValidation(t *testing.T) {
	acct := store.NewID("acct")
	for _, tc := range []struct {
		grant Grant
		ok    bool
	}{
		{Grant{Role: RoleAdmin, Scope: store.ServerScope()}, true},
		{Grant{Role: RoleAdmin, Scope: store.AccountScope(acct)}, false},
		{Grant{Role: RoleReseller, Scope: store.AccountScope(acct)}, false},
		{Grant{Role: RoleOwner, Scope: store.AccountScope(acct)}, true},
		{Grant{Role: RoleReadOnly, Scope: store.ServerScope()}, true},
		{Grant{Role: "root", Scope: store.ServerScope()}, false},
		{Grant{Role: RoleOwner, Scope: store.Scope{Level: store.ScopeAccount}}, false},
		{Grant{Role: RoleOwner, Scope: store.AccountScope(acct), Object: store.NewID("site")}, false},
		{Grant{Role: RoleCollaborator, Scope: store.AccountScope(acct), Object: store.NewID("site"), Permissions: []Permission{"web.site.deploy"}}, true},
		{Grant{Role: RoleCollaborator, Scope: store.AccountScope(acct), Permissions: []Permission{"Web Site"}}, false},
		{Grant{Role: RoleOwner, Scope: store.AccountScope(acct), Permissions: []Permission{PermOverride}}, false},
		{Grant{Role: RoleAdmin, Scope: store.ServerScope(), Permissions: []Permission{PermOverride}}, true},
		{Grant{Role: RoleOwner, Scope: store.AccountScope(acct), Limit: []string{"secrets.read"}}, false},
	} {
		err := tc.grant.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc.grant, err)
		}
	}
}

func TestPatternsAndMatrix(t *testing.T) {
	acct := store.NewID("acct")
	actor := &Actor{Grants: []Grant{
		{Role: RoleReadOnly, Scope: store.ServerScope()},
		{Role: RoleCollaborator, Scope: store.AccountScope(acct), Permissions: []Permission{"web.site.deploy", PermImpersonate}},
		{Role: RoleOwner, Scope: store.AccountScope(acct), Limit: []string{"dns.*"}},
	}}
	got := Patterns(actor)
	want := []string{"*.read", "core.impersonate", "dns.*", "web.site.deploy"}
	if !slices.Equal(got, want) {
		t.Errorf("patterns = %v", got)
	}
	if got := Patterns(nil); got == nil || len(got) != 0 {
		t.Errorf("nobody = %v", got)
	}

	rows := MatrixFor([]Permission{"web.site.read", "web.site.delete", "network.ip.update", "core.account.update", PermOverride, PermImpersonate, "web.site.read"})
	if len(rows) != 6 {
		t.Fatalf("rows = %+v", rows)
	}
	byName := map[Permission]map[Role]Access{}
	for _, r := range rows {
		byName[r.Permission] = r.Roles
	}
	for _, c := range []struct {
		perm Permission
		role Role
		want Access
	}{
		{"web.site.read", RoleAdmin, AccessEverywhere},
		{"web.site.read", RoleReadOnly, AccessOwnScope},
		{"web.site.delete", RoleOwner, AccessOwnScope},
		{"web.site.delete", RoleCollaborator, AccessNone},
		{"network.ip.update", RoleReseller, AccessNone},
		{"network.ip.update", RoleAdmin, AccessEverywhere},
		{"core.account.update", RoleOwner, AccessNone},
		{"core.account.update", RoleReseller, AccessOwnScope},
		{PermOverride, RoleAdmin, AccessByName},
		{PermOverride, RoleReseller, AccessNone},
		{PermImpersonate, RoleReseller, AccessByName},
	} {
		if got := byName[c.perm][c.role]; got != c.want {
			t.Errorf("%s for %s = %s, want %s", c.perm, c.role, got, c.want)
		}
	}
	if len(Roles()) != 5 || Roles()[0].Role != RoleAdmin {
		t.Errorf("roles = %+v", Roles())
	}
	if !Known(PermUserRead, nil) || Known("web.site.read", nil) {
		t.Error("registered permissions are known, kinds' only with their kinds")
	}
}
